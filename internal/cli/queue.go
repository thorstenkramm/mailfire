package cli

import (
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/deferral"
)

// queue.go implements "mailfire queue list|show|delete|stats" (task 25):
// inspection and explicit deletion of deferred messages. Delivery stays
// with resend.
//
// list, show, and stats are read-only: they never send, modify, expire,
// or remove messages, take no lock (meta.json changes atomically), and do
// not create a missing defer_dir. Expired messages (older than
// [defer].max_age) are shown as expired; they are removed by the next
// deferral or resend run.
//
// delete validates every requested ID under the store lock before
// removing anything: one unknown ID rejects the whole request.
//
// show prints metadata only; the stored request (message content) is
// printed only with --raw. The stored request never contains credentials:
// the API key travels in the HTTP Authorization header, not the body.
//
// Exit codes: 0 on success, 1 for usage errors and unknown IDs, 2 for
// configuration errors, 5 when the defer_dir cannot be read or a message
// cannot be deleted.

// queueOptions holds the parsed flags of a queue subcommand.
type queueOptions struct {
	Raw    bool   // show --raw
	Config string // -c / --config
	JSON   bool   // --json: print the result as one JSON document
	IDs    []string
}

// runQueue dispatches the queue subcommands.
func runQueue(args []string, rc *runContext, stdout, stderr io.Writer) int {
	if len(args) == 0 || isJSONFlag(args[0]) {
		printfOutput(stderr, "mailfire: queue: missing subcommand\n\n")
		printOutput(stderr, queueUsage)
		return jsonFail(stdout, queueJSON(args), "queue", errUsage, errors.New("missing subcommand"), ExitUsage)
	}
	sub := args[0]
	if sub == "-h" || sub == "--help" || sub == "help" {
		printOutput(stdout, queueUsage)
		return ExitOK
	}
	switch sub {
	case "list", "show", "delete", "stats":
	default:
		printfOutput(stderr, "mailfire: queue: unknown subcommand %q\n\n", sub)
		printOutput(stderr, queueUsage)
		return jsonFail(stdout, queueJSON(args[1:]), "queue", errUsage,
			fmt.Errorf("unknown subcommand %q", sub), ExitUsage)
	}
	if wantsHelp(args[1:]) {
		printOutput(stdout, queueUsage)
		return ExitOK
	}
	command := "queue " + sub
	opts, err := parseQueueFlags(sub, args[1:])
	if errors.Is(err, flag.ErrHelp) {
		printOutput(stdout, queueUsage)
		return ExitOK
	}
	if err != nil {
		printfOutput(stderr, "mailfire: queue %s: %v\n\n", sub, err)
		printOutput(stderr, queueUsage)
		return jsonFail(stdout, opts.JSON, command, errUsage, err, ExitUsage)
	}
	cfg, err := config.Load(opts.Config, stderr)
	if err != nil {
		printfOutput(stderr, "mailfire: %v\n", err)
		return jsonFail(stdout, opts.JSON, command, errConfig, err, ExitConfig)
	}
	store := &deferral.Store{Dir: cfg.DeferDir, MaxAge: cfg.DeferMaxAge}
	now := rc.now()
	var msgs []deferral.Metadata
	if sub != "delete" {
		// Read without creating a missing defer_dir.
		if store.Dir != "" {
			msgs, err = store.List()
		}
		if err != nil {
			printfOutput(stderr, "mailfire: queue: %v\n", err)
			return jsonFail(stdout, opts.JSON, command, errQueue, err, ExitSendFail)
		}
	}
	switch sub {
	case "list":
		if opts.JSON {
			return emitList(stdout, command, store, msgs, cfg.ResendCooldown, now)
		}
		return queueList(store, msgs, cfg.ResendCooldown, now, stdout)
	case "show":
		return queueShow(store, msgs, cfg.ResendCooldown, opts, now, stdout, stderr)
	case "delete":
		return queueDelete(store, opts, stdout, stderr)
	default:
		return queueStats(store, msgs, cfg.ResendCooldown, now, opts.JSON, stdout)
	}
}

// queueFlagSet registers the flags of queue subcommand sub.
func queueFlagSet(sub string, opts *queueOptions, configShort *string) *flag.FlagSet {
	fs := flag.NewFlagSet("queue "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if sub == "show" {
		fs.BoolVar(&opts.Raw, "raw", false, "")
	}
	fs.StringVar(configShort, "c", "", "")
	fs.StringVar(&opts.Config, "config", "", "")
	fs.BoolVar(&opts.JSON, "json", false, "")
	return fs
}

// queueJSON reports whether args of an unknown or missing queue
// subcommand request JSON output, read with the flags of queue show (a
// superset of all queue flags).
func queueJSON(args []string) bool {
	var opts queueOptions
	var configShort string
	return scanJSON(queueFlagSet("show", &opts, &configShort), args, true)
}

// parseQueueFlags parses the flags of queue subcommand sub. show takes
// exactly one ID, delete at least one, list and stats none. On error,
// only the returned JSON field is meaningful: the output mode.
func parseQueueFlags(sub string, args []string) (queueOptions, error) {
	var opts queueOptions
	var configShort string
	fs := queueFlagSet(sub, &opts, &configShort)
	ids, err := parseInterspersed(fs, args)
	if err != nil {
		return queueOptions{JSON: scanJSON(fs, args, true)}, err
	}
	fail := func(err error) (queueOptions, error) { return queueOptions{JSON: opts.JSON}, err }
	if configShort != "" {
		if opts.Config != "" && opts.Config != configShort {
			return fail(errors.New("conflicting config paths: use either -c or --config, not both"))
		}
		opts.Config = configShort
	}
	opts.IDs = ids
	switch sub {
	case "show":
		if len(opts.IDs) != 1 {
			return fail(errors.New("expected exactly one message ID"))
		}
	case "delete":
		if len(opts.IDs) == 0 {
			return fail(errors.New("expected at least one message ID"))
		}
	default:
		if len(opts.IDs) > 0 {
			return fail(fmt.Errorf("unexpected arguments: %s", strings.Join(opts.IDs, " ")))
		}
	}
	return opts, nil
}

// parseInterspersed parses flags anywhere among the positional
// arguments (as in "queue delete <id> -c <path>"), which it returns in
// order. Everything after a "--" terminator is positional; a "--" that
// is the value of a flag (--config --) is not a terminator.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	flags, positional := splitArgs(fs, args, true)
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return positional, nil
}

// splitArgs reads args the way fs.Parse does and separates the flag
// arguments (each flag with its value) from the positional arguments.
// fs tells which flags take a value: a value flag without "=" consumes
// the next argument, even one that looks like a flag or is "--".
// Everything after a "--" terminator is positional. With interspersed,
// flags may follow positional arguments; otherwise the first positional
// argument ends the flags, as in fs.Parse. Unknown and malformed flags
// are kept as flag arguments (fs.Parse rejects them) and are assumed to
// take no value.
func splitArgs(fs *flag.FlagSet, args []string, interspersed bool) (flags, positional []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return flags, append(positional, args[i+1:]...)
		}
		if len(a) < 2 || a[0] != '-' {
			if !interspersed {
				return flags, append(positional, args[i:]...)
			}
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		name, _, hasValue := strings.Cut(strings.TrimPrefix(a[1:], "-"), "=")
		if f := fs.Lookup(name); f != nil && !isBoolFlag(f) && !hasValue && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, positional
}

// scanJSON reports whether args, read like fs.Parse reads them (see
// splitArgs), enable the --json flag of fs. It decides the output mode
// when parsing fails: after an unknown flag, which fs.Parse stops at,
// later flags are still recognized.
func scanJSON(fs *flag.FlagSet, args []string, interspersed bool) bool {
	flags, _ := splitArgs(fs, args, interspersed)
	on := false
	for i := 0; i < len(flags); i++ {
		name, value, hasValue := strings.Cut(strings.TrimPrefix(flags[i][1:], "-"), "=")
		f := fs.Lookup(name)
		switch {
		case f == nil:
		case !isBoolFlag(f):
			if !hasValue {
				i++ // skip the value
			}
		case name == "json":
			b, err := strconv.ParseBool(value)
			on = !hasValue || (err == nil && b)
		}
	}
	return on
}

// isBoolFlag reports whether f takes no value.
func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// retryState describes when a message will be retried: "failed"
// (permanently rejected), "expired" (older than max_age), "now", or the
// time it becomes eligible.
func retryState(store *deferral.Store, m deferral.Metadata, cooldown time.Duration, now time.Time) string {
	switch {
	case m.Failed:
		return "failed"
	case store.Expired(m, now):
		return "expired"
	}
	if next := m.NextRetry(cooldown); next.After(now) {
		return formatUTC(next)
	}
	return "now"
}

// queueList prints one row per deferred message, oldest first.
func queueList(store *deferral.Store, msgs []deferral.Metadata, cooldown time.Duration, now time.Time, stdout io.Writer) int {
	if len(msgs) == 0 {
		printlnOutput(stdout, "no deferred messages")
		return ExitOK
	}
	header := []string{"ID", "DATE", "ATTEMPTS", "NEXT RETRY", "TO", "SUBJECT", "SIZE"}
	rows := [][]string{header}
	for _, m := range msgs {
		rows = append(rows, []string{
			listCell(m.ID, 0),
			formatUTC(m.Timestamp),
			fmt.Sprintf("%d", m.Attempts),
			retryState(store, m, cooldown, now),
			listCell(strings.Join(m.To, ", "), maxListTo),
			listCell(m.Subject, maxListSubject),
			fmt.Sprintf("%d", m.Size),
		})
	}
	widths := make([]int, len(header))
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	for _, row := range rows {
		var b strings.Builder
		for i, cell := range row {
			switch {
			case i == len(row)-1: // SIZE, right-aligned
				fmt.Fprintf(&b, "%*s", widths[i], cell)
			case i == 2: // ATTEMPTS, right-aligned
				fmt.Fprintf(&b, "%*s  ", widths[i], cell)
			default:
				fmt.Fprintf(&b, "%-*s  ", widths[i], cell)
			}
		}
		printlnOutput(stdout, b.String())
	}
	return ExitOK
}

// showResult is the JSON document of queue show. Raw is present only
// with --raw: the stored request, base64-encoded (it may be binary).
type showResult struct {
	envelope
	Message queuedMessage `json:"message"`
	Raw     *rawRequest   `json:"raw,omitempty"`
}

type rawRequest struct {
	ContentType string `json:"content_type"`
	BodyBase64  string `json:"body_base64"`
}

// queueShow prints one message's metadata or, with --raw, its stored
// request.
func queueShow(store *deferral.Store, msgs []deferral.Metadata, cooldown time.Duration, opts queueOptions,
	now time.Time, stdout, stderr io.Writer) int {
	const command = "queue show"
	id := opts.IDs[0]
	var meta deferral.Metadata
	found := false
	for _, m := range msgs {
		if m.ID == id {
			meta, found = m, true
			break
		}
	}
	if !found {
		err := fmt.Errorf("unknown deferred message ID %q (run 'mailfire queue list' to see all)", id)
		printfOutput(stderr, "mailfire: queue show: %v\n", err)
		return jsonFail(stdout, opts.JSON, command, errUnknownID, err, ExitUsage)
	}
	res := &showResult{envelope: envelope{Command: command}, Message: newQueuedMessage(store, meta, cooldown, now)}
	if opts.Raw {
		dir, err := store.MessageDir(meta.ID)
		if err != nil {
			printfOutput(stderr, "mailfire: queue show: %v\n", err)
			return jsonFail(stdout, opts.JSON, command, errQueue, err, ExitSendFail)
		}
		body, err := os.ReadFile(filepath.Join(dir, deferral.BodyFile))
		if err != nil {
			printfOutput(stderr, "mailfire: queue show: %s: reading %s: %v\n", meta.ID, deferral.BodyFile, err)
			return jsonFail(stdout, opts.JSON, command, errQueue, fmt.Errorf("%s: reading %s: %w", meta.ID, deferral.BodyFile, err), ExitSendFail)
		}
		if opts.JSON {
			res.Raw = &rawRequest{ContentType: meta.ContentType, BodyBase64: base64.StdEncoding.EncodeToString(body)}
			return emitJSON(stdout, res, ExitOK)
		}
		printfOutput(stdout, "Content-Type: %s\r\n\r\n", meta.ContentType)
		if _, err := stdout.Write(body); err != nil {
			return ExitSendFail
		}
		return ExitOK
	}
	if opts.JSON {
		return emitJSON(stdout, res, ExitOK)
	}

	field := func(name, value string) { printfOutput(stdout, "%-14s%s\n", name+":", value) }
	field("ID", meta.ID)
	field("Deferred", fmt.Sprintf("%s (%s ago)", formatUTC(meta.Timestamp), now.Sub(meta.Timestamp).Round(time.Second)))
	field("To", listCell(strings.Join(meta.To, ", "), 0))
	field("Subject", listCell(meta.Subject, 0))
	field("Provider", storedProvider(meta))
	field("Size", fmt.Sprintf("%d bytes", meta.Size))
	field("Attempts", fmt.Sprintf("%d", meta.Attempts))
	if meta.LastAttempt.IsZero() {
		field("Last attempt", "none since deferral")
	} else {
		field("Last attempt", fmt.Sprintf("%s (%s ago)", formatUTC(meta.LastAttempt), now.Sub(meta.LastAttempt).Round(time.Second)))
	}
	lastErr := "none recorded"
	if meta.LastError != "" {
		lastErr = listCell(meta.LastError, 0)
	}
	field("Last error", lastErr)
	switch state := retryState(store, meta, cooldown, now); state {
	case "failed":
		field("Status", "failed (permanently rejected; excluded from automatic retries)")
		field("Next retry", fmt.Sprintf("none; retry with 'mailfire resend --force %s'", meta.ID))
	case "expired":
		field("Status", "expired (older than max_age; removed by the next deferral or resend)")
		field("Next retry", "none")
	case "now":
		field("Status", "pending")
		field("Next retry", "now")
	default:
		field("Status", "pending")
		next := meta.NextRetry(cooldown)
		field("Next retry", fmt.Sprintf("%s (in %s)", state, next.Sub(now).Round(time.Second)))
	}
	return ExitOK
}

// deleteResult is the JSON document of queue delete. UnknownIDs is set
// when unknown IDs rejected the request: then nothing was deleted.
type deleteResult struct {
	envelope
	Deleted    []string `json:"deleted"`
	NotDeleted []string `json:"not_deleted"`
	UnknownIDs []string `json:"unknown_ids,omitempty"`
}

// queueDelete deletes exactly the given messages, or nothing when any ID
// is unknown.
func queueDelete(store *deferral.Store, opts queueOptions, stdout, stderr io.Writer) int {
	ids := opts.IDs
	res := &deleteResult{envelope: envelope{Command: "queue delete"}, Deleted: []string{}, NotDeleted: []string{}}
	out := humanOut(stdout, opts.JSON)
	code := ExitOK
	deleted, err := store.Delete(ids)
	var unknown *deferral.UnknownIDsError
	switch {
	case errors.As(err, &unknown):
		err = fmt.Errorf("unknown deferred message %s %s; nothing was deleted (run 'mailfire queue list' to see all)",
			plural(len(unknown.IDs), "ID", "IDs"), quoteAll(unknown.IDs))
		printfOutput(stderr, "mailfire: queue delete: %v\n", err)
		res.setError(errUnknownID, err)
		res.UnknownIDs = unknown.IDs
		code = ExitUsage
	case err != nil:
		printfOutput(stderr, "mailfire: queue delete: %v\n", err)
		res.setError(errQueue, err)
		code = ExitSendFail
	}
	done := map[string]bool{}
	for _, m := range deleted {
		printfOutput(out, "%s: deleted\n", m.ID)
		res.Deleted = append(res.Deleted, m.ID)
		done[m.ID] = true
	}
	for _, id := range ids {
		if !done[id] {
			res.NotDeleted = append(res.NotDeleted, id)
		}
	}
	switch code {
	case ExitSendFail:
		printfOutput(out, "queue delete: %d deleted, %d not deleted\n", len(deleted), len(ids)-len(deleted))
		if len(deleted) > 0 {
			res.Outcome = "partial"
		}
	case ExitOK:
		printfOutput(out, "queue delete: %d deleted\n", len(deleted))
	}
	if opts.JSON {
		return emitJSON(stdout, res, code)
	}
	return code
}

// quoteAll quotes and comma-separates ids.
func quoteAll(ids []string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = fmt.Sprintf("%q", id)
	}
	return strings.Join(q, ", ")
}

// statsResult is the JSON document of queue stats.
type statsResult struct {
	envelope
	Stats queueStatsJSON `json:"stats"`
}

type queueStatsJSON struct {
	Messages         int    `json:"messages"`
	Failed           int    `json:"failed"`
	Expired          int    `json:"expired"`
	Eligible         int    `json:"eligible"`
	TotalSize        int64  `json:"total_size"`
	Attempts         int    `json:"attempts"`
	OldestID         string `json:"oldest_id,omitempty"`
	OldestDeferredAt string `json:"oldest_deferred_at,omitempty"`
	LastAttempt      string `json:"last_attempt,omitempty"`
	NextRetry        string `json:"next_retry,omitempty"`
}

// queueStats prints queue totals.
func queueStats(store *deferral.Store, msgs []deferral.Metadata, cooldown time.Duration, now time.Time,
	jsonMode bool, stdout io.Writer) int {
	var total int64
	var attempts, failed, expired, eligible int
	var lastAttempt, nextRetry time.Time
	for _, m := range msgs {
		total += m.Size
		attempts += m.Attempts
		if m.LastAttempt.After(lastAttempt) {
			lastAttempt = m.LastAttempt
		}
		switch {
		case m.Failed:
			failed++
			continue
		case store.Expired(m, now):
			expired++
			continue
		}
		next := m.NextRetry(cooldown)
		if !next.After(now) {
			eligible++
		}
		if nextRetry.IsZero() || next.Before(nextRetry) {
			nextRetry = next
		}
	}
	if jsonMode {
		st := queueStatsJSON{Messages: len(msgs), Failed: failed, Expired: expired, Eligible: eligible,
			TotalSize: total, Attempts: attempts, LastAttempt: rfc3339(lastAttempt), NextRetry: rfc3339(nextRetry)}
		if len(msgs) > 0 {
			st.OldestID, st.OldestDeferredAt = msgs[0].ID, rfc3339(msgs[0].Timestamp)
		}
		return emitJSON(stdout, &statsResult{envelope: envelope{Command: "queue stats"}, Stats: st}, ExitOK)
	}
	field := func(name, value string) { printfOutput(stdout, "%-14s%s\n", name+":", value) }
	field("Messages", fmt.Sprintf("%d (%d failed, %d expired)", len(msgs), failed, expired))
	field("Total size", fmt.Sprintf("%d bytes", total))
	if len(msgs) == 0 {
		field("Oldest", "none")
	} else {
		oldest := msgs[0] // List order: oldest first
		field("Oldest", fmt.Sprintf("%s, deferred %s (%s ago)", oldest.ID, formatUTC(oldest.Timestamp),
			now.Sub(oldest.Timestamp).Round(time.Second)))
	}
	field("Attempts", fmt.Sprintf("%d failed resend %s", attempts, plural(attempts, "attempt", "attempts")))
	if lastAttempt.IsZero() {
		field("Last attempt", "none")
	} else {
		field("Last attempt", fmt.Sprintf("%s (%s ago)", formatUTC(lastAttempt), now.Sub(lastAttempt).Round(time.Second)))
	}
	switch {
	case nextRetry.IsZero():
		field("Next retry", "none")
	case !nextRetry.After(now):
		field("Next retry", fmt.Sprintf("now (%d eligible)", eligible))
	default:
		field("Next retry", fmt.Sprintf("%s (in %s)", formatUTC(nextRetry), nextRetry.Sub(now).Round(time.Second)))
	}
	return ExitOK
}
