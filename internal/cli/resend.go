package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/dedup"
	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/logging"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
)

// resend.go implements the resend subcommand (PRD FR-4): it replays
// deferred messages from the defer_dir. The stored request body is sent
// verbatim with the stored Content-Type via Client.SendRaw — the message
// is never re-composed. A message is removed from the defer_dir only
// after successful delivery; any failure keeps it in place.
//
// Cooldown and backoff (Decision Q3, task 25): without --force, a message
// is retried only once it is eligible. A message never resent becomes
// eligible [defer].resend_cooldown after it was deferred; after each
// failed resend the delay doubles, measured from the latest attempt and
// capped at one hour (deferral.RetryDelay; a cooldown above one hour is
// used unchanged). The original deferral timestamp is never changed: the
// retry metadata (attempts, last attempt, last error) is recorded
// separately in meta.json, and the payload is never touched. Messages
// stored by older versions have no retry metadata and are treated as
// never resent. --force ignores the cooldown and backoff.
//
// Permanent failures (task 25): a message Mailgun rejects permanently is
// kept, marked failed, and skipped by every later resend — with or
// without --force — unless it is retried explicitly with
// "resend --force <id>". It stays inspectable and deletable via
// "mailfire queue".
//
// Expiry: with [defer].max_age set, messages deferred longer ago are
// removed at the start of every resend run (and on every deferral), each
// reported on stderr and in the send log.
//
// Test mode (Decision Q5): resent messages are never test mode. Task 14
// guarantees stored bodies never carry o:testmode (--test sends are
// never deferred), and resend replays the bytes verbatim, so the
// invariant holds by construction; resend_test.go asserts it.
//
// Logging (FR-3): every resend is a sending attempt and is logged like a
// send, with the same field sets per level (see logOutcome in send.go).
// from is read back from the stored request body (meta.json does not
// record it), size is the stored body's size, and resent messages are
// never test sends. Skipped messages (cooldown, gone) are not attempts
// and are not logged; neither is a stored body that cannot be read.
//
// Concurrency: each message is resent under the deferral store lock,
// from re-reading its metadata to removing it or recording the failure.
// A concurrent resend, queue delete, or deferral waits, so a message is
// never delivered twice by overlapping local runs and never deleted
// mid-send; a message gone by the time its turn comes is reported as
// skipped, not failed. Exactly-once delivery still cannot be guaranteed:
// when a request times out or the connection drops after Mailgun
// accepted it, the outcome is unknown, the message stays deferred, and
// the next resend may deliver it again.
//
// Notification deduplication (task 24): a message deferred by a send
// with --dedup-key carries its scope. It is resent under that scope's
// lock (taken before the store lock). The attempt is recorded as pending
// before the request; a successful resend starts the notification
// cooldown recorded with the message, a failed one clears the pending
// marker, and skipped messages leave the state untouched (see dedup.go).
// It fails closed: if the state cannot be used (unsafe state_dir, corrupt
// or unreadable state, lock or write failure), the message stays queued,
// no request is made, and the run exits 5.
//
// Expiry is re-checked under the store lock right before delivery, so a
// message that exceeded max_age during the run is removed, not sent.
//
// Exit codes: 0 when everything selected was sent (or there was nothing
// to do — skipped messages are not failures). Any failure maps to the
// send's code: auth 3, permanent 5, deferrable 4 (the message stays
// deferred). With mixed outcomes the most severe code wins, ranked
// 3 > 5 > 4 > 0: a rejected API key (3) means every further attempt —
// and plain sends too — will fail; a permanent failure (5) means a
// stored message can never be delivered as-is and needs human attention;
// a deferrable failure (4) is the least severe because the message is
// still safely queued for the next run. Usage errors (unknown message
// ID, --list combined with IDs) exit 1.

// resendOptions holds the parsed flags of the resend subcommand.
type resendOptions struct {
	List    bool          // --list: show deferred messages without sending
	Force   bool          // --force: ignore the resend cooldown
	Timeout time.Duration // --timeout; zero: mailgun.timeout or the client default
	Config  string        // -c / --config
	JSON    bool          // --json: print the result as one JSON document
	IDs     []string      // positional arguments: specific messages to resend
}

// parseResendFlags parses the resend subcommand's flags. Positional
// arguments are message IDs. --list combined with IDs is a usage error:
// listing never sends, so IDs would be silently ignored. On error, only
// the returned JSON field is meaningful: the output mode.
func parseResendFlags(args []string) (resendOptions, error) {
	var opts resendOptions
	var configShort string
	var timeoutFlag string

	fs := flag.NewFlagSet("resend", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.List, "list", false, "")
	fs.BoolVar(&opts.Force, "force", false, "")
	fs.StringVar(&timeoutFlag, "timeout", "", "")
	fs.StringVar(&configShort, "c", "", "")
	fs.StringVar(&opts.Config, "config", "", "")
	fs.BoolVar(&opts.JSON, "json", false, "")
	ids, err := parseInterspersed(fs, args)
	if err != nil {
		return resendOptions{JSON: scanJSON(fs, args, true)}, err
	}
	fail := func(err error) (resendOptions, error) { return resendOptions{JSON: opts.JSON}, err }

	// Presence matters: an explicitly empty --timeout is an error, while an
	// absent flag leaves config/default in charge.
	d, err := parseTimeoutOption(fs, timeoutFlag)
	if err != nil {
		return fail(err)
	}
	opts.Timeout = d

	if configShort != "" {
		if opts.Config != "" && opts.Config != configShort {
			return fail(fmt.Errorf("conflicting config paths: use either -c or --config, not both"))
		}
		opts.Config = configShort
	}

	opts.IDs = ids
	if opts.List && len(opts.IDs) > 0 {
		return fail(fmt.Errorf("--list cannot be combined with message IDs"))
	}
	// Duplicate IDs would resend (or fail on) the same message twice;
	// dedupe, keeping the order given.
	seen := map[string]bool{}
	deduped := opts.IDs[:0]
	for _, id := range opts.IDs {
		if !seen[id] {
			seen[id] = true
			deduped = append(deduped, id)
		}
	}
	opts.IDs = deduped
	return opts, nil
}

// runResend parses the resend flags, loads the configuration (resend
// needs the Mailgun credentials and the defer_dir), and lists or
// resends.
func runResend(args []string, rc *runContext, stdout, stderr io.Writer) int {
	opts, err := parseResendFlags(args)
	if errors.Is(err, flag.ErrHelp) {
		printOutput(stdout, resendUsage)
		return ExitOK
	}
	if err != nil {
		printfOutput(stderr, "mailfire: resend: %v\n\n", err)
		printOutput(stderr, resendUsage)
		return jsonFail(stdout, opts.JSON, "resend", errUsage, err, ExitUsage)
	}
	cfg, err := config.LoadWith(opts.Config, stderr, config.Overrides{Timeout: opts.Timeout > 0})
	if err != nil {
		printfOutput(stderr, "mailfire: %v\n", err)
		return jsonFail(stdout, opts.JSON, "resend", errConfig, err, ExitConfig)
	}
	store := &deferral.Store{Dir: cfg.DeferDir, MaxAge: cfg.DeferMaxAge}
	msgs, err := listDeferred(store)
	if err != nil {
		printfOutput(stderr, "mailfire: resend: %v\n", err)
		return jsonFail(stdout, opts.JSON, "resend", errQueue, err, ExitSendFail)
	}
	if opts.List {
		if opts.JSON {
			return emitList(stdout, "resend", store, msgs, cfg.ResendCooldown, rc.now())
		}
		return resendList(msgs, stdout)
	}
	res := &resendResult{envelope: envelope{Command: "resend"}, Messages: []resendMessage{}}
	code := resendSend(cfg, store, msgs, &opts, rc, res, humanOut(stdout, opts.JSON), stderr)
	if opts.JSON {
		return emitJSON(stdout, res, code)
	}
	return code
}

// resendList prints the deferred messages — ID, date, to, subject, size —
// as an aligned table, oldest first, without sending anything.
func resendList(msgs []deferral.Metadata, stdout io.Writer) int {
	if len(msgs) == 0 {
		printlnOutput(stdout, "no deferred messages")
		return ExitOK
	}
	header := [5]string{"ID", "DATE", "TO", "SUBJECT", "SIZE"}
	rows := make([][5]string, 0, len(msgs))
	var widths [5]int
	for i, h := range header {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, m := range msgs {
		// The ID is never shortened: it is what `resend <id>` takes.
		row := [5]string{
			listCell(m.ID, 0),
			formatUTC(m.Timestamp),
			listCell(strings.Join(m.To, ", "), maxListTo),
			listCell(m.Subject, maxListSubject),
			fmt.Sprintf("%d", m.Size),
		}
		for i, cell := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
		rows = append(rows, row)
	}
	// Padding is computed in runes (fmt's width counts runes, too), so
	// non-ASCII subjects stay aligned.
	printRow := func(row [5]string) {
		printfOutput(stdout, "%-*s  %-*s  %-*s  %-*s  %*s\n",
			widths[0], row[0], widths[1], row[1], widths[2], row[2],
			widths[3], row[3], widths[4], row[4])
	}
	printRow(header)
	for _, row := range rows {
		printRow(row)
	}
	return ExitOK
}

// Column caps for resend --list: long recipient lists and subjects are
// truncated so one message stays one readable table row.
const (
	maxListTo      = 40
	maxListSubject = 50
)

// listCell makes s safe for one table cell: control characters (a
// newline in a subject would break the row) become spaces, and when
// maxRunes > 0 the cell is truncated with an ellipsis.
func listCell(s string, maxRunes int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if maxRunes > 0 && utf8.RuneCountInString(s) > maxRunes {
		s = string([]rune(s)[:maxRunes-1]) + "…"
	}
	return s
}

// resendSend resends the selected messages (all, oldest first, or the
// given IDs in the order given) and prints one outcome line per message
// plus a summary.
func resendSend(cfg *config.Config, store *deferral.Store, msgs []deferral.Metadata, opts *resendOptions,
	rc *runContext, res *resendResult, stdout, stderr io.Writer) int {
	selected, err := selectMessages(msgs, opts.IDs)
	if err != nil {
		printfOutput(stderr, "mailfire: resend: %v\n", err)
		res.setError(errUnknownID, err)
		return ExitUsage
	}
	if len(selected) == 0 {
		printlnOutput(stdout, "no deferred messages")
		return ExitOK
	}

	client, err := mailgun.New(cfg.APIKey, cfg.Domain, cfg.Region, clientOptions(rc, cfg, opts.Timeout)...)
	if err != nil {
		// Cannot happen: config.Load already validated these fields.
		printfOutput(stderr, "mailfire: %v\n", err)
		res.setError(errConfig, err)
		return ExitConfig
	}

	// As in send, the logfile is opened before any attempt: an unusable
	// path is a configuration error and aborts the run.
	logger, err := openLogger(cfg)
	if err != nil {
		printfOutput(stderr, "mailfire: %v\n", err)
		res.setError(errConfig, err)
		return ExitConfig
	}
	defer closeLogger(logger, stderr)

	// The most severe exit code wins (see resendSeverity); its error is
	// the run's error in the JSON result.
	worst := ExitOK
	note := func(code int, e *jsonError) {
		if resendSeverity(code) > resendSeverity(worst) {
			worst = code
			res.Error = e
		}
	}
	store.Now = rc.now
	reportExpired := expireReporter("resend", logger, rc.now, stderr)
	store.OnExpire = func(meta deferral.Metadata) {
		reportExpired(meta)
		res.Messages = append(res.Messages, resendMessage{ID: meta.ID, Outcome: "expired"})
		res.Summary.Expired++
	}
	expired, err := store.Expire()
	if err != nil {
		printfOutput(stderr, "mailfire: resend: %v\n", err)
		note(ExitSendFail, newJSONError(errQueue, err))
	}
	gone := make(map[string]bool, len(expired))
	for _, m := range expired {
		gone[m.ID] = true
	}

	explicit := len(opts.IDs) > 0
	sum := &res.Summary
	for _, listed := range selected {
		if gone[listed.ID] {
			continue // expired: already reported
		}
		rec := resendMessage{ID: listed.ID}
		guard, err := resendGuard(cfg, listed, rc)
		var code, outcome int
		if err != nil {
			// Fail closed: without its deduplication state the message
			// could be delivered next to a duplicate; keep it queued.
			printfOutput(stderr, "mailfire: resend: %s: %v\n", listed.ID, err)
			printfOutput(stdout, "%s: failed (cannot use the notification deduplication state; message kept, nothing sent)\n", listed.ID)
			rec.Error = newJSONError(errDedup, err)
			code, outcome = ExitSendFail, outcomeFailed
		} else {
			code, outcome = resendLocked(client, store, logger, guard, listed.ID, cfg.ResendCooldown,
				opts.Force, explicit, rc, &rec, stdout, stderr)
			finishResendGuard(guard, cfg, listed, outcome == outcomeSent, rc, stderr)
		}
		switch outcome {
		case outcomeSent:
			sum.Sent++
			rec.Outcome = "sent"
		case outcomeSkipped:
			sum.Skipped++
			rec.Outcome = "skipped"
		case outcomeFailed:
			sum.Failed++
			rec.Outcome = "failed"
		}
		if outcome != outcomeExpired { // expired: recorded by OnExpire
			res.Messages = append(res.Messages, rec)
		}
		note(code, rec.Error)
	}
	printfOutput(stdout, "resend: %d sent, %d skipped, %d failed\n", sum.Sent, sum.Skipped, sum.Failed)
	if worst != ExitOK && sum.Sent > 0 {
		res.Outcome = "partial"
	}
	return worst
}

// Outcomes of one message in a resend run.
const (
	outcomeSent = iota
	outcomeSkipped
	outcomeFailed
	outcomeExpired // removed for exceeding max_age; reported like the initial sweep, not counted
)

// resendLocked resends one message under the store lock: it re-reads the
// metadata (another process may have resent, retried, or deleted the
// message since it was listed), applies the permanent-failure and
// backoff rules unless overridden, and attempts delivery. guard, when
// non-nil, is the held deduplication scope of the message.
func resendLocked(client *mailgun.Client, store *deferral.Store, logger *logging.Logger, guard *dedup.Guard, id string,
	cooldown time.Duration, force, explicit bool, rc *runContext, rec *resendMessage, stdout, stderr io.Writer) (int, int) {
	unlock, err := store.Lock()
	if err != nil {
		printfOutput(stderr, "mailfire: resend: %s: %v\n", id, err)
		printfOutput(stdout, "%s: failed (cannot lock defer_dir; message kept)\n", id)
		rec.Error = newJSONError(errQueue, err)
		return ExitSendFail, outcomeFailed
	}
	defer func() {
		if err := unlock(); err != nil {
			printfOutput(stderr, "mailfire: resend: %v\n", err)
		}
	}()

	meta, ok, err := store.Get(id)
	if err != nil {
		printfOutput(stderr, "mailfire: resend: %s: %v\n", id, err)
		printfOutput(stdout, "%s: failed (cannot read defer_dir; message kept)\n", id)
		rec.Error = newJSONError(errQueue, err)
		return ExitSendFail, outcomeFailed
	}
	if !ok {
		printfOutput(stdout, "%s: skipped (no longer in defer_dir; resent or removed by another process)\n", id)
		rec.Reason = "gone"
		return ExitOK, outcomeSkipped
	}
	now := rc.now()
	// Re-check max_age under the lock: the message may have expired
	// since the sweep at the start of the run (e.g. while waiting for
	// the scope lock), and a new send of the same notification no longer
	// counts it as queued. An expired message is removed, never sent.
	if store.Expired(meta, now) {
		if err := store.Remove(meta.ID); err != nil {
			printfOutput(stderr, "mailfire: resend: %s: %v\n", id, err)
			printfOutput(stdout, "%s: failed (expired, but removing it failed; nothing sent)\n", id)
			rec.Error = newJSONError(errQueue, err)
			return ExitSendFail, outcomeFailed
		}
		if store.OnExpire != nil {
			store.OnExpire(meta)
		}
		return ExitOK, outcomeExpired
	}
	if meta.Failed && (!force || !explicit) {
		printfOutput(stdout, "%s: skipped (permanently rejected; inspect with 'mailfire queue show %s', retry with 'mailfire resend --force %s')\n",
			id, id, id)
		rec.Reason = "permanently_failed"
		return ExitOK, outcomeSkipped
	}
	if eligible := meta.NextRetry(cooldown); !force && now.Before(eligible) {
		when := fmt.Sprintf("eligible at %s, in %s; use --force to resend now",
			formatUTC(eligible), eligible.Sub(now).Round(time.Second))
		if meta.Attempts == 0 || meta.LastAttempt.IsZero() {
			printfOutput(stdout, "%s: skipped (deferred %s ago; %s)\n", id, now.Sub(meta.Timestamp).Round(time.Second), when)
		} else {
			printfOutput(stdout, "%s: skipped (%d failed %s, last %s ago; %s)\n", id, meta.Attempts,
				plural(meta.Attempts, "attempt", "attempts"), now.Sub(meta.LastAttempt).Round(time.Second), when)
		}
		rec.Reason, rec.NextRetry = "not_eligible", rfc3339(eligible)
		return ExitOK, outcomeSkipped
	}
	code, gone := resendOne(client, store, logger, guard, meta, now, cooldown, rec, stdout, stderr)
	switch {
	case gone:
		return code, outcomeSkipped
	case code == ExitOK:
		return code, outcomeSent
	default:
		return code, outcomeFailed
	}
}

// formatUTC formats t for outcome lines and queue output.
func formatUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05") + " UTC"
}

// plural picks the singular or plural form for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// listDeferred reads the defer_dir. The caller reports an unreadable
// defer_dir with exit 5, like a deferral store failure on send: the run
// failed and nothing was (re)sent.
func listDeferred(store *deferral.Store) ([]deferral.Metadata, error) {
	if store.Dir == "" {
		// defer = false with no defer_dir: there is nothing to resend.
		return nil, nil
	}
	return store.List()
}

// selectMessages resolves the requested IDs against the stored messages.
// With no IDs, all messages are selected (List order: oldest first).
// With IDs, the selection is in the order given. Any unknown ID is a
// usage error and aborts before anything is sent.
func selectMessages(msgs []deferral.Metadata, ids []string) ([]deferral.Metadata, error) {
	if len(ids) == 0 {
		return msgs, nil
	}
	byID := make(map[string]deferral.Metadata, len(msgs))
	for _, m := range msgs {
		byID[m.ID] = m
	}
	selected := make([]deferral.Metadata, 0, len(ids))
	for _, id := range ids {
		m, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("unknown deferred message ID %q (run 'mailfire resend --list' to see all)", id)
		}
		selected = append(selected, m)
	}
	return selected, nil
}

// resendOne replays one deferred message; the caller holds the store
// lock. On success the message is removed from the defer_dir; on a
// delivery failure it stays and its retry metadata records the attempt
// made at the given time. The outcome line goes to stdout, the error
// detail to stderr, and the attempt is logged when logger is non-nil.
// The returned exit code is the send's mapped code (PRD §8): 0, 3
// (auth), 4 (deferrable — the message remains deferred), or 5
// (permanent / local read failure). gone reports that the message
// vanished since it was listed and nothing was attempted (code 0).
//
// With a deduplication guard, the attempt is recorded as pending before
// the request is made, so a crash between delivery and recording the
// cooldown leaves the notification in doubt rather than unprotected.
func resendOne(client *mailgun.Client, store *deferral.Store, logger *logging.Logger, guard *dedup.Guard,
	meta deferral.Metadata, at time.Time, cooldown time.Duration, rec *resendMessage, stdout, stderr io.Writer) (code int, gone bool) {
	dir, err := store.MessageDir(meta.ID)
	if err != nil {
		printfOutput(stderr, "mailfire: resend: %v\n", err)
		printfOutput(stdout, "%s: failed (invalid message ID; nothing sent)\n", meta.ID)
		rec.Error = newJSONError(errQueue, err)
		return ExitSendFail, false
	}
	body, err := os.ReadFile(filepath.Join(dir, deferral.BodyFile))
	if errors.Is(err, fs.ErrNotExist) {
		printfOutput(stdout, "%s: skipped (no longer in defer_dir; resent or removed by another process)\n", meta.ID)
		rec.Reason = "gone"
		return ExitOK, true
	}
	if err != nil {
		printfOutput(stderr, "mailfire: resend: %s: reading %s: %v\n", meta.ID, deferral.BodyFile, err)
		printfOutput(stdout, "%s: failed (stored request unreadable; message kept)\n", meta.ID)
		rec.Error = newJSONError(errQueue, err)
		return ExitSendFail, false
	}
	if guard != nil {
		if err := guard.Begin(nil); err != nil {
			printfOutput(stderr, "mailfire: resend: %s: recording the attempt for notification deduplication: %v\n", meta.ID, err)
			printfOutput(stdout, "%s: failed (cannot use the notification deduplication state; message kept, nothing sent)\n", meta.ID)
			rec.Error = newJSONError(errDedup, err)
			return ExitSendFail, false
		}
	}
	resp, err := client.SendRaw(context.Background(), meta.ContentType, body)
	if logger != nil {
		logOutcome(logger, logging.Attempt{
			From:    storedFrom(meta.ContentType, body),
			To:      meta.To,
			Subject: meta.Subject,
			Size:    int64(len(body)),
		}, resp, err, stderr)
	}
	return reportResend(store, meta, resp, err, at, cooldown, rec, stdout, stderr), false
}

// reportResend prints the outcome of one resend attempt, removes the
// message from the defer_dir after successful delivery or records the
// failed attempt, and maps the outcome to an exit code (see resendOne).
func reportResend(store *deferral.Store, meta deferral.Metadata, resp *mailgun.SendResponse,
	err error, at time.Time, cooldown time.Duration, rec *resendMessage, stdout, stderr io.Writer) int {
	if err == nil {
		rec.MessageID = resp.ID
		if rerr := store.Remove(meta.ID); rerr != nil {
			// Delivered but not removed: the next resend would send a
			// duplicate. The send still counts as sent — removing is
			// housekeeping — but the leftover is reported loudly.
			printfOutput(stderr, "mailfire: resend: %s: delivered, but removing from defer_dir failed: %v\n", meta.ID, rerr)
			printfOutput(stdout, "%s: sent, message id %s (warning: not removed from defer_dir)\n", meta.ID, resp.ID)
			rec.Warning = "delivered, but not removed from defer_dir: " + rerr.Error()
			return ExitOK
		}
		printfOutput(stdout, "%s: sent, message id %s\n", meta.ID, resp.ID)
		return ExitOK
	}
	printfOutput(stderr, "mailfire: resend: %s: %v\n", meta.ID, err)
	rec.Error = newJSONError(sendErrorCode(err), err)
	permanent := !mailgun.IsAuth(err) && !mailgun.IsDeferrable(err)
	updated, rerr := store.RecordFailure(meta.ID, at, err.Error(), permanent)
	if rerr != nil {
		// The message itself is untouched; only its retry metadata is
		// stale, so the next run may retry it sooner than the backoff.
		printfOutput(stderr, "mailfire: resend: %s: recording the failed attempt: %v\n", meta.ID, rerr)
		updated = meta
	}
	switch {
	case mailgun.IsAuth(err):
		printfOutput(stdout, "%s: failed (authentication error; message kept)\n", meta.ID)
		return ExitAuth
	case mailgun.IsDeferrable(err):
		printfOutput(stdout, "%s: failed (retryable; message kept, next retry at %s)\n",
			meta.ID, formatUTC(updated.NextRetry(cooldown)))
		rec.NextRetry = rfc3339(updated.NextRetry(cooldown))
		return ExitDeferred
	default:
		printfOutput(stdout, "%s: failed (permanent; message kept and marked failed, excluded from automatic retries — "+
			"inspect with 'mailfire queue show %s', retry with 'mailfire resend --force %s', or remove with 'mailfire queue delete %s')\n",
			meta.ID, meta.ID, meta.ID, meta.ID)
		return ExitSendFail
	}
}

// storedFrom extracts the "from" form field of a stored multipart
// request for the debug-level log; meta.json does not record the sender.
// It returns "" when the body cannot be parsed or has no from field —
// the log line is still written.
func storedFrom(contentType string, body []byte) string {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return ""
	}
	r := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := r.NextPart()
		if err != nil {
			return ""
		}
		if part.FormName() == "from" {
			v, err := io.ReadAll(io.LimitReader(part, 4096))
			if err != nil {
				return ""
			}
			return string(v)
		}
	}
}

// resendSeverity ranks resend exit codes so the most severe outcome of a
// run wins: 3 (auth — nothing will work until the key is fixed) > 5
// (permanent — a message can never be delivered as-is) > 4 (deferrable —
// the message is still safely deferred) > 0.
func resendSeverity(code int) int {
	switch code {
	case ExitAuth:
		return 3
	case ExitSendFail:
		return 2
	case ExitDeferred:
		return 1
	default:
		return 0
	}
}
