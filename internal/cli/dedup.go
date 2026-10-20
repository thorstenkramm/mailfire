package cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/dedup"
	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/mailbox"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
	"github.com/thorstenkramm/mailfire/internal/smtp"
)

// dedup.go wires notification deduplication (task 24) into send and
// resend. It is opt-in: only sends with --dedup-key are checked or
// recorded, so omitting the flag is the way to bypass it. The scope is
// the sending domain, all recipients (to, cc, bcc; case-insensitive, any
// order), and the key.
//
// A send with a key is skipped (exit 0, "skipped: ..." on stdout) while
//   - the same notification is still in the deferred queue, even if
//     marked permanently rejected (the first queued message is kept, no
//     duplicate is queued),
//   - the same notification was delivered within its cooldown
//     (--cooldown, else [dedup].cooldown, default 1h, of the delivered
//     send; skipped duplicates do not extend it), or
//   - an earlier attempt was interrupted (crash) less than a minute ago
//     and may have been delivered.
//
// Only a delivery starts the cooldown — by send or by a successful
// resend of the deferred message. Failed and deferred sends do not, and
// --test sends neither check nor record anything. The send holds the
// scope's lock from the check until its outcome is recorded, so a
// concurrent duplicate waits and then sees the outcome.

// notification is a send guarded by its deduplication scope.
type notification struct {
	store    *dedup.Store
	guard    *dedup.Guard
	scope    dedup.Scope
	cooldown time.Duration
	now      func() time.Time
	stderr   io.Writer
}

// checkNotificationStorage validates the same state used at send time without
// beginning a notification, suppressing the job, or retaining its scope lock.
func checkNotificationStorage(cfg *config.Config, opts *SendOptions, msg *mailgun.Message, rc *runContext) error {
	n, _, err := prepareNotification(cfg, opts, msg, rc, io.Discard)
	if err != nil {
		return err
	}
	return n.guard.Release()
}

// startNotification checks the scope of msg and, unless it is a
// duplicate, records that an attempt starts. done reports that the send
// must end with code: skipped as a duplicate (0) or unable to use the
// deduplication state (2). Otherwise the caller must report the outcome
// (delivered or notDelivered) and close the notification.
func startNotification(cfg *config.Config, opts *SendOptions, msg *mailgun.Message, rc *runContext,
	res *sendResult, stdout, stderr io.Writer) (n *notification, code int, done bool) {
	n, queued, err := prepareNotification(cfg, opts, msg, rc, stderr)
	if err != nil {
		printfOutput(stderr, "mailfire: send: %v; nothing was sent\n", err)
		res.setError(errDedup, err)
		return nil, ExitConfig, true
	}
	if skip := n.guard.Check(queued); skip != nil {
		reportSkip(stdout, opts.DedupKey, skip, rc.now())
		res.Outcome = "skipped"
		res.Skip = newSkipInfo(opts.DedupKey, skip)
		n.close()
		return nil, ExitOK, true
	}
	if err := n.guard.Begin(&n.scope); err != nil {
		printfOutput(stderr, "mailfire: send: %v; nothing was sent\n", err)
		res.setError(errDedup, err)
		n.close()
		return nil, ExitConfig, true
	}
	return n, ExitOK, false
}

func prepareNotification(cfg *config.Config, opts *SendOptions, msg *mailgun.Message, rc *runContext, stderr io.Writer) (*notification, string, error) {
	if err := cfg.CheckDedupDir(); err != nil {
		return nil, "", err
	}
	cooldown := opts.Cooldown
	if cooldown <= 0 {
		cooldown = cfg.DedupCooldown
	}
	n := &notification{
		store:    &dedup.Store{Dir: cfg.DedupDir, Now: rc.now},
		scope:    dedup.NewScope(cfg.Domain, recipientAddrs(msg), opts.DedupKey),
		cooldown: cooldown,
		now:      rc.now,
		stderr:   stderr,
	}
	guard, err := n.store.Acquire(n.scope.ID())
	if err != nil {
		return nil, "", err
	}
	n.guard = guard
	queued, err := queuedNotification(cfg, n.scope.ID(), rc.now())
	if err != nil {
		return nil, "", errors.Join(fmt.Errorf("checking deferred messages: %w", err), guard.Release())
	}
	return n, queued, nil
}

// recipientAddrs returns the bare addresses of all recipients.
func recipientAddrs(msg *mailgun.Message) []string {
	var out []string
	for _, list := range [][]string{msg.To, msg.Cc, msg.Bcc} {
		for _, r := range list {
			if a, err := mailbox.Parse(r); err == nil {
				r = a.Address
			}
			out = append(out, r)
		}
	}
	return out
}

// queuedNotification returns the ID of a deferred message of the scope
// that is still queued — including one marked permanently rejected,
// which stays until retried with --force or deleted — unless it is past
// max_age (removed, unsent, by the next resend or deferral). Reading
// takes no queue lock (meta.json changes atomically); the scope lock
// keeps resend from delivering it meanwhile.
func queuedNotification(cfg *config.Config, scopeID string, now time.Time) (string, error) {
	if cfg.DeferDir == "" {
		return "", nil
	}
	store := &deferral.Store{Dir: cfg.DeferDir, MaxAge: cfg.DeferMaxAge}
	msgs, err := store.List()
	if err != nil {
		return "", err
	}
	for _, m := range msgs {
		if m.DedupScope == scopeID && !store.Expired(m, now) {
			return m.ID, nil
		}
	}
	return "", nil
}

// reportSkip prints the skipped outcome of a duplicate notification.
func reportSkip(stdout io.Writer, key string, skip *dedup.Skip, now time.Time) {
	prefix := fmt.Sprintf("skipped: duplicate notification (--dedup-key %q)", key)
	eligible := func() string {
		return fmt.Sprintf("eligible again at %s, in %s", formatUTC(skip.Eligible), skip.Eligible.Sub(now).Round(time.Second))
	}
	switch skip.Reason {
	case dedup.Queued:
		printfOutput(stdout, "%s: already deferred as %s; mailfire resend delivers it\n", prefix, skip.QueuedID)
	case dedup.InDoubt:
		printfOutput(stdout, "%s: an attempt started %s ago was interrupted and may have been delivered; %s\n",
			prefix, now.Sub(skip.Since).Round(time.Second), eligible())
	default:
		printfOutput(stdout, "%s: sent %s ago; %s\n", prefix, now.Sub(skip.Since).Round(time.Second), eligible())
	}
}

// newSkipInfo describes a skipped duplicate for --json.
func newSkipInfo(key string, skip *dedup.Skip) *skipInfo {
	s := &skipInfo{DedupKey: key, QueuedID: skip.QueuedID, Since: rfc3339(skip.Since), EligibleAt: rfc3339(skip.Eligible)}
	switch skip.Reason {
	case dedup.Queued:
		s.Reason = "queued"
	case dedup.InDoubt:
		s.Reason = "in_doubt"
	default:
		s.Reason = "cooldown"
	}
	return s
}

// delivered starts the cooldown. A failure is reported but never
// changes the send's outcome: the notification was delivered.
func (n *notification) delivered() {
	if err := n.guard.Sent(n.now(), n.cooldown); err != nil {
		printfOutput(n.stderr, "mailfire: send: delivered, but recording the notification cooldown failed: %v\n", err)
	}
}

// notDelivered records that the attempt failed or was deferred: no
// cooldown starts.
func (n *notification) notDelivered() {
	if err := n.guard.Abort(); err != nil {
		printfOutput(n.stderr, "mailfire: send: %v\n", err)
	}
}

// close releases the scope lock and removes expired state.
func (n *notification) close() {
	if err := n.guard.Release(); err != nil {
		printfOutput(n.stderr, "mailfire: send: dedup: %v\n", err)
	}
	if err := n.store.Cleanup(); err != nil {
		printfOutput(n.stderr, "mailfire: send: dedup cleanup: %v\n", err)
	}
}

// deferMetadata describes msg for the deferral store, linking it to its
// deduplication scope when n is non-nil.
func deferMetadata(cfg *config.Config, msg *mailgun.Message, sendErr error, n *notification) deferral.Metadata {
	m := deferral.Metadata{To: msg.To, Subject: msg.Subject, ContentType: msg.EncodedType, Provider: cfg.Provider, Account: providerAccount(cfg, cfg.Provider)}
	if cfg.Provider == config.ProviderSMTP {
		if env, err := smtp.Envelope(msg); err == nil {
			m.EnvelopeFrom, m.EnvelopeRecipients = env.From, env.Recipients
		}
		m.MessageID = msg.MessageID
	}
	if sendErr != nil {
		m.LastError = sendErr.Error()
	}
	if n != nil {
		m.DedupScope = n.scope.ID()
		m.DedupCooldown = n.cooldown
	}
	return m
}

// resendGuard locks the deduplication scope of a deferred message before
// it is resent (scope lock before queue lock, as in send), so a
// concurrent send of the same notification waits for the outcome. A
// message without a scope yields (nil, nil). For a scoped message any
// failure — unsafe state_dir, corrupt or unreadable state, lock error —
// is returned: the caller must not send it (fail closed).
func resendGuard(cfg *config.Config, meta deferral.Metadata, rc *runContext) (*dedup.Guard, error) {
	if meta.DedupScope == "" {
		return nil, nil
	}
	if err := cfg.CheckDedupDir(); err != nil {
		return nil, err
	}
	store := &dedup.Store{Dir: cfg.DedupDir, Now: rc.now}
	return store.Acquire(meta.DedupScope)
}

// finishResendGuard records the outcome of a resend and releases the
// scope lock: a delivery starts the notification cooldown, a failed
// attempt clears its pending marker, and a skipped message (no attempt)
// changes nothing.
func finishResendGuard(g *dedup.Guard, cfg *config.Config, meta deferral.Metadata, sent bool, rc *runContext, stderr io.Writer) {
	if g == nil {
		return
	}
	if !sent && g.Begun() {
		if err := g.Abort(); err != nil {
			printfOutput(stderr, "mailfire: resend: %s: %v\n", meta.ID, err)
		}
	}
	if sent {
		cooldown := meta.DedupCooldown
		if cooldown <= 0 {
			cooldown = cfg.DedupCooldown
		}
		if err := g.Sent(rc.now(), cooldown); err != nil {
			printfOutput(stderr, "mailfire: resend: %s: delivered, but recording the notification cooldown failed: %v\n", meta.ID, err)
		}
	}
	if err := g.Release(); err != nil {
		printfOutput(stderr, "mailfire: resend: %v\n", err)
	}
}
