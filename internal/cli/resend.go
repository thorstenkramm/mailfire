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
// Cooldown (Decision Q3): without --force, messages younger than
// [defer].resend_cooldown are skipped and reported with their age and
// the time they become eligible. The cooldown deliberately measures the
// age of the original deferral, and a failed resend keeps the original
// timestamp: updating it would let a persistently failing message hide
// behind the cooldown forever (each failed resend would reset the clock,
// so an old message could look fresh). resend is a manual, deliberate
// command — retrying an old message again on the next run is acceptable
// and keeps the failure visible.
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
// Concurrency: two resend processes running at once may both list the
// same message. If one has already delivered and removed it, the other
// finds it gone and reports it as skipped, not failed. If both read it
// before either removes it, it is delivered twice: preventing that
// needs a cross-process claim/lock on the message directory (with crash
// recovery for stale claims) — a store design change beyond task 15.
// resend is a manual command, so overlapping runs are unlikely.
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
	List   bool     // --list: show deferred messages without sending
	Force  bool     // --force: ignore the resend cooldown
	Config string   // -c / --config
	IDs    []string // positional arguments: specific messages to resend
}

// parseResendFlags parses the resend subcommand's flags. Positional
// arguments are message IDs. --list combined with IDs is a usage error:
// listing never sends, so IDs would be silently ignored.
func parseResendFlags(args []string) (resendOptions, error) {
	var opts resendOptions
	var configShort string

	fs := flag.NewFlagSet("resend", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.List, "list", false, "")
	fs.BoolVar(&opts.Force, "force", false, "")
	fs.StringVar(&configShort, "c", "", "")
	fs.StringVar(&opts.Config, "config", "", "")
	if err := fs.Parse(args); err != nil {
		return resendOptions{}, err
	}

	if configShort != "" {
		if opts.Config != "" && opts.Config != configShort {
			return resendOptions{}, fmt.Errorf("conflicting config paths: use either -c or --config, not both")
		}
		opts.Config = configShort
	}

	opts.IDs = fs.Args()
	if opts.List && len(opts.IDs) > 0 {
		return resendOptions{}, fmt.Errorf("--list cannot be combined with message IDs")
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
	if err != nil {
		printfOutput(stderr, "mailfire: resend: %v\n\n", err)
		printOutput(stderr, resendUsage)
		return ExitUsage
	}
	cfg, err := config.Load(opts.Config, stderr)
	if err != nil {
		printfOutput(stderr, "mailfire: %v\n", err)
		return ExitConfig
	}
	store := &deferral.Store{Dir: cfg.DeferDir}
	if opts.List {
		return resendList(store, stdout, stderr)
	}
	return resendSend(cfg, store, &opts, rc, stdout, stderr)
}

// resendList prints the deferred messages — ID, date, to, subject, size —
// as an aligned table, oldest first, without sending anything.
func resendList(store *deferral.Store, stdout, stderr io.Writer) int {
	msgs, code := listDeferred(store, stderr)
	if code != ExitOK {
		return code
	}
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
			m.Timestamp.UTC().Format("2006-01-02 15:04:05") + " UTC",
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
func resendSend(cfg *config.Config, store *deferral.Store, opts *resendOptions,
	rc *runContext, stdout, stderr io.Writer) int {
	msgs, code := listDeferred(store, stderr)
	if code != ExitOK {
		return code
	}
	selected, err := selectMessages(msgs, opts.IDs)
	if err != nil {
		printfOutput(stderr, "mailfire: resend: %v\n", err)
		return ExitUsage
	}
	if len(selected) == 0 {
		printlnOutput(stdout, "no deferred messages")
		return ExitOK
	}

	client, err := mailgun.New(cfg.APIKey, cfg.Domain, cfg.Region, rc.clientOpts...)
	if err != nil {
		// Cannot happen: config.Load already validated these fields.
		printfOutput(stderr, "mailfire: %v\n", err)
		return ExitConfig
	}

	// As in send, the logfile is opened before any attempt: an unusable
	// path is a configuration error and aborts the run.
	logger, err := openLogger(cfg)
	if err != nil {
		printfOutput(stderr, "mailfire: %v\n", err)
		return ExitConfig
	}
	defer closeLogger(logger, stderr)

	now := time.Now()
	var sent, skipped, failed int
	worst := ExitOK
	for _, meta := range selected {
		if !opts.Force && cooldownActive(meta, cfg.ResendCooldown, now) {
			skipped++
			age := now.Sub(meta.Timestamp).Round(time.Second)
			eligible := meta.Timestamp.Add(cfg.ResendCooldown)
			printfOutput(stdout, "%s: skipped (deferred %s ago; eligible at %s, in %s; use --force to resend now)\n",
				meta.ID, age, eligible.UTC().Format("2006-01-02 15:04:05")+" UTC",
				eligible.Sub(now).Round(time.Second))
			continue
		}
		code, gone := resendOne(client, store, logger, meta, stdout, stderr)
		switch {
		case gone:
			skipped++
		case code == ExitOK:
			sent++
		default:
			failed++
		}
		if resendSeverity(code) > resendSeverity(worst) {
			worst = code
		}
	}
	printfOutput(stdout, "resend: %d sent, %d skipped, %d failed\n", sent, skipped, failed)
	return worst
}

// listDeferred reads the defer_dir. An unreadable defer_dir is reported
// on stderr and maps to exit 5, like a deferral store failure on send:
// the run failed and nothing was (re)sent.
func listDeferred(store *deferral.Store, stderr io.Writer) ([]deferral.Metadata, int) {
	if store.Dir == "" {
		// defer = false with no defer_dir: there is nothing to resend.
		return nil, ExitOK
	}
	msgs, err := store.List()
	if err != nil {
		printfOutput(stderr, "mailfire: resend: %v\n", err)
		return nil, ExitSendFail
	}
	return msgs, ExitOK
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

// cooldownActive reports whether the message is younger than the resend
// cooldown and must be skipped (Decision Q3).
func cooldownActive(meta deferral.Metadata, cooldown time.Duration, now time.Time) bool {
	return now.Sub(meta.Timestamp) < cooldown
}

// resendOne replays one deferred message. On success the message is
// removed from the defer_dir; on any failure it stays. The outcome line
// goes to stdout, the error detail to stderr, and the attempt is logged
// when logger is non-nil. The returned exit code is the send's mapped
// code (PRD §8): 0, 3 (auth), 4 (deferrable — the message remains
// deferred), or 5 (permanent / local read failure). gone reports that
// the message vanished since it was listed — resent or removed by a
// concurrent mailfire process — and nothing was attempted (code 0).
func resendOne(client *mailgun.Client, store *deferral.Store, logger *logging.Logger,
	meta deferral.Metadata, stdout, stderr io.Writer) (code int, gone bool) {
	dir := filepath.Join(store.Dir, meta.ID)
	body, err := os.ReadFile(filepath.Join(dir, deferral.BodyFile))
	if errors.Is(err, fs.ErrNotExist) {
		printfOutput(stdout, "%s: skipped (no longer in defer_dir; resent or removed by another process)\n", meta.ID)
		return ExitOK, true
	}
	if err != nil {
		printfOutput(stderr, "mailfire: resend: %s: reading %s: %v\n", meta.ID, deferral.BodyFile, err)
		printfOutput(stdout, "%s: failed (stored request unreadable; message kept)\n", meta.ID)
		return ExitSendFail, false
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
	return reportResend(store, meta, resp, err, stdout, stderr), false
}

// reportResend prints the outcome of one resend attempt, removes the
// message from the defer_dir after successful delivery, and maps the
// outcome to an exit code (see resendOne).
func reportResend(store *deferral.Store, meta deferral.Metadata, resp *mailgun.SendResponse,
	err error, stdout, stderr io.Writer) int {
	dir := filepath.Join(store.Dir, meta.ID)
	if err == nil {
		if rerr := os.RemoveAll(dir); rerr != nil {
			// Delivered but not removed: the next resend would send a
			// duplicate. The send still counts as sent — removing is
			// housekeeping — but the leftover is reported loudly.
			printfOutput(stderr, "mailfire: resend: %s: delivered, but removing from defer_dir failed: %v\n", meta.ID, rerr)
			printfOutput(stdout, "%s: sent, message id %s (warning: not removed from defer_dir)\n", meta.ID, resp.ID)
			return ExitOK
		}
		printfOutput(stdout, "%s: sent, message id %s\n", meta.ID, resp.ID)
		return ExitOK
	}
	printfOutput(stderr, "mailfire: resend: %s: %v\n", meta.ID, err)
	switch {
	case mailgun.IsAuth(err):
		printfOutput(stdout, "%s: failed (authentication error; message kept)\n", meta.ID)
		return ExitAuth
	case mailgun.IsDeferrable(err):
		printfOutput(stdout, "%s: failed (retryable; message kept for the next resend)\n", meta.ID)
		return ExitDeferred
	default:
		printfOutput(stdout, "%s: failed (permanent; message kept — inspect it in %s and remove it manually)\n",
			meta.ID, store.Dir)
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
