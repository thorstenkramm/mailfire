package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/deferral"
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
		fmt.Fprintf(stderr, "mailfire: resend: %v\n\n", err)
		fmt.Fprint(stderr, usageText)
		return ExitUsage
	}
	cfg, err := config.Load(opts.Config, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "mailfire: %v\n", err)
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
		fmt.Fprintln(stdout, "no deferred messages")
		return ExitOK
	}
	rows := make([][5]string, 0, len(msgs))
	widths := [5]int{len("ID"), len("DATE"), len("TO"), len("SUBJECT"), len("SIZE")}
	for _, m := range msgs {
		row := [5]string{
			m.ID,
			m.Timestamp.UTC().Format("2006-01-02 15:04:05") + " UTC",
			strings.Join(m.To, ", "),
			m.Subject,
			fmt.Sprintf("%d", m.Size),
		}
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
		rows = append(rows, row)
	}
	printRow := func(row [5]string) {
		fmt.Fprintf(stdout, "%-*s  %-*s  %-*s  %-*s  %*s\n",
			widths[0], row[0], widths[1], row[1], widths[2], row[2],
			widths[3], row[3], widths[4], row[4])
	}
	printRow([5]string{"ID", "DATE", "TO", "SUBJECT", "SIZE"})
	for _, row := range rows {
		printRow(row)
	}
	return ExitOK
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
		fmt.Fprintf(stderr, "mailfire: resend: %v\n", err)
		return ExitUsage
	}
	if len(selected) == 0 {
		fmt.Fprintln(stdout, "no deferred messages")
		return ExitOK
	}

	client, err := mailgun.New(cfg.APIKey, cfg.Domain, cfg.Region, rc.clientOpts...)
	if err != nil {
		// Cannot happen: config.Load already validated these fields.
		fmt.Fprintf(stderr, "mailfire: %v\n", err)
		return ExitConfig
	}

	now := time.Now()
	var sent, skipped, failed int
	worst := ExitOK
	for _, meta := range selected {
		if !opts.Force && cooldownActive(meta, cfg.ResendCooldown, now) {
			skipped++
			age := now.Sub(meta.Timestamp).Round(time.Second)
			eligible := meta.Timestamp.Add(cfg.ResendCooldown)
			fmt.Fprintf(stdout, "%s: skipped (deferred %s ago; eligible at %s, in %s; use --force to resend now)\n",
				meta.ID, age, eligible.UTC().Format("2006-01-02 15:04:05")+" UTC",
				time.Until(eligible).Round(time.Second))
			continue
		}
		code := resendOne(client, store, meta, stdout, stderr)
		if code == ExitOK {
			sent++
		} else {
			failed++
		}
		if resendSeverity(code) > resendSeverity(worst) {
			worst = code
		}
	}
	fmt.Fprintf(stdout, "resend: %d sent, %d skipped, %d failed\n", sent, skipped, failed)
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
		fmt.Fprintf(stderr, "mailfire: resend: %v\n", err)
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
// goes to stdout, the error detail to stderr. The returned exit code is
// the send's mapped code (PRD §8): 0, 3 (auth), 4 (deferrable — the
// message remains deferred), or 5 (permanent / local read failure).
func resendOne(client *mailgun.Client, store *deferral.Store, meta deferral.Metadata,
	stdout, stderr io.Writer) int {
	dir := filepath.Join(store.Dir, meta.ID)
	body, err := os.ReadFile(filepath.Join(dir, deferral.BodyFile))
	if err != nil {
		fmt.Fprintf(stderr, "mailfire: resend: %s: reading %s: %v\n", meta.ID, deferral.BodyFile, err)
		fmt.Fprintf(stdout, "%s: failed (stored request unreadable; message kept)\n", meta.ID)
		return ExitSendFail
	}
	resp, err := client.SendRaw(context.Background(), meta.ContentType, body)
	if err == nil {
		if rerr := os.RemoveAll(dir); rerr != nil {
			// Delivered but not removed: the next resend would send a
			// duplicate. The send still counts as sent — removing is
			// housekeeping — but the leftover is reported loudly.
			fmt.Fprintf(stderr, "mailfire: resend: %s: delivered, but removing from defer_dir failed: %v\n", meta.ID, rerr)
			fmt.Fprintf(stdout, "%s: sent, message id %s (warning: not removed from defer_dir)\n", meta.ID, resp.ID)
			return ExitOK
		}
		fmt.Fprintf(stdout, "%s: sent, message id %s\n", meta.ID, resp.ID)
		return ExitOK
	}
	fmt.Fprintf(stderr, "mailfire: resend: %s: %v\n", meta.ID, err)
	switch {
	case mailgun.IsAuth(err):
		fmt.Fprintf(stdout, "%s: failed (authentication error; message kept)\n", meta.ID)
		return ExitAuth
	case mailgun.IsDeferrable(err):
		fmt.Fprintf(stdout, "%s: failed (retryable; message kept for the next resend)\n", meta.ID)
		return ExitDeferred
	default:
		fmt.Fprintf(stdout, "%s: failed (permanent; message kept — inspect it in %s and remove it manually)\n",
			meta.ID, store.Dir)
		return ExitSendFail
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
