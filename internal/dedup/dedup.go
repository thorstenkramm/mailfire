// Package dedup implements notification deduplication (task 24): a send
// with an explicit deduplication key is skipped while an earlier
// notification with the same scope is in its cooldown, still deferred,
// or possibly delivered by an interrupted attempt.
//
// A scope is the sending domain, the normalized set of all recipients,
// and the key; its ID is a SHA-256 hash of the three. Each scope with
// live state has one file in the state directory:
//
//	state_dir/
//	  <id>.json   key, domain, recipients, and the scope's state:
//	              sent_at/until (cooldown of the latest delivery) or
//	              sending (an attempt whose outcome is not recorded)
//	  <xx>.lock   lock files; xx is the first byte of the scope ID
//
// Concurrency: a sender holds the lock of its scope's stripe (one of 256
// lock files, chosen by the ID's first byte) from checking the state
// until its outcome is recorded, network request included. A concurrent
// send of the same notification therefore waits and then sees the
// outcome; unrelated scopes sharing a stripe merely wait their turn.
// The OS releases the lock when a process dies, so a "sending" marker
// found by a lock holder is always abandoned, never a live sender.
//
// Lock order: a stripe lock is always taken before the deferral store
// lock, never the other way round, and a process holds at most one
// stripe lock at a time (Cleanup only tries locks, it never waits).
//
// State files are replaced atomically (temporary file plus rename) and
// removed once they no longer suppress anything (Cleanup), so the
// directory holds at most one file per recently active scope.
package dedup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/thorstenkramm/mailfire/internal/filelock"
	"github.com/thorstenkramm/mailfire/internal/fsutil"
)

const (
	// DefaultCooldown is the suppression window when neither --cooldown
	// nor [dedup].cooldown is set.
	DefaultCooldown = time.Hour
	// RecoveryDelay is how long an interrupted attempt (crash before its
	// outcome was recorded) keeps suppressing duplicates: it may have
	// been delivered, but an alert must not be lost for a whole cooldown.
	RecoveryDelay = time.Minute

	dirPerm   = 0o700
	filePerm  = 0o600
	stateExt  = ".json"
	tmpSuffix = ".tmp"
	lockExt   = ".lock"
)

// Scope identifies one notification stream.
type Scope struct {
	Domain     string
	Recipients []string // lowercased, sorted, without duplicates
	Key        string
}

// NewScope builds a scope from the sending domain, the recipient
// addresses (to, cc, and bcc; bare addr-specs), and the key. Domain and
// addresses are compared case-insensitively; recipient order and
// duplicates do not matter. The key is used verbatim.
func NewScope(domain string, recipients []string, key string) Scope {
	rs := make([]string, 0, len(recipients))
	for _, r := range recipients {
		rs = append(rs, strings.ToLower(strings.TrimSpace(r)))
	}
	slices.Sort(rs)
	return Scope{Domain: strings.ToLower(strings.TrimSpace(domain)), Recipients: slices.Compact(rs), Key: key}
}

// ID returns the scope's stable identifier: 64 lowercase hex characters.
func (sc Scope) ID() string {
	// JSON keeps the encoding unambiguous for any key or address.
	data, err := json.Marshal([]any{"mailfire-dedup-v1", sc.Domain, sc.Recipients, sc.Key})
	if err != nil {
		panic(err) // strings only: cannot fail
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var reID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// state is the content of one scope's state file.
type state struct {
	Key        string   `json:"key,omitempty"`
	Domain     string   `json:"domain,omitempty"`
	Recipients []string `json:"recipients,omitempty"`
	// SentAt is when the latest notification was delivered; duplicates
	// are suppressed until Until (SentAt plus that send's cooldown).
	SentAt time.Time `json:"sent_at,omitzero"`
	Until  time.Time `json:"until,omitzero"`
	// Sending is when an attempt started whose outcome is not recorded
	// yet. Seen by a lock holder, the attempt was interrupted.
	Sending time.Time `json:"sending,omitzero"`
}

// Reason says why a notification is skipped.
type Reason int

const (
	// Cooldown: the same notification was delivered within its cooldown.
	Cooldown Reason = iota + 1
	// Queued: the same notification is deferred and awaits resend.
	Queued
	// InDoubt: an attempt was interrupted before its outcome was
	// recorded and may have been delivered.
	InDoubt
)

// Skip describes a suppressed notification.
type Skip struct {
	Reason Reason
	// QueuedID is the deferred message's ID (Queued).
	QueuedID string
	// Since is when the earlier notification was sent (Cooldown) or its
	// attempt started (InDoubt).
	Since time.Time
	// Eligible is when the notification may be sent again (Cooldown,
	// InDoubt); zero for Queued, which lasts while the message is queued.
	Eligible time.Time
}

// Store is a deduplication state directory.
type Store struct {
	// Dir is created on the fly (0700).
	Dir string
	// Now, when non-nil, replaces time.Now (tests use a controlled clock).
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Guard is a held scope lock together with the scope's state.
type Guard struct {
	store  *Store
	id     string
	path   string
	st     state
	unlock func() error
	begun  bool // Begin recorded an attempt whose outcome is pending
}

// Acquire blocks until this process holds the lock for scope id and
// loads its state. Release the guard when done. A corrupt or unreadable
// state file is an error: suppression state is never silently dropped.
func (s *Store) Acquire(id string) (*Guard, error) {
	if s.Dir == "" {
		return nil, errors.New("dedup: no state_dir configured")
	}
	if !reID.MatchString(id) {
		return nil, fmt.Errorf("dedup: invalid scope ID %q", id)
	}
	if err := os.MkdirAll(s.Dir, dirPerm); err != nil {
		return nil, fmt.Errorf("dedup: cannot create %s: %w", s.Dir, err)
	}
	unlock, err := filelock.Lock(s.lockPath(id))
	if err != nil {
		return nil, fmt.Errorf("dedup: %w", err)
	}
	g := &Guard{store: s, id: id, path: filepath.Join(s.Dir, id+stateExt), unlock: unlock}
	st, err := readState(g.path)
	if err != nil {
		return nil, errors.Join(err, unlock())
	}
	g.st = st
	return g, nil
}

func (s *Store) lockPath(id string) string {
	return filepath.Join(s.Dir, id[:2]+lockExt)
}

// readState reads a state file; a missing file is the empty state.
func readState(path string) (state, error) {
	var st state
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("dedup: reading %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("dedup: state file %s is corrupt (%w); remove it to reset this notification's deduplication", path, err)
	}
	return st, nil
}

// Check decides whether the notification is a duplicate, from the state
// loaded by Acquire and queuedID — a deferred, still retryable message of
// the same scope, or "" — which the caller looks up while holding the
// guard. It returns nil when the notification may be sent.
//
// A clock that moved backward (sent_at or sending in the future) never
// prolongs suppression: the state is ignored as expired.
func (g *Guard) Check(queuedID string) *Skip {
	if queuedID != "" {
		return &Skip{Reason: Queued, QueuedID: queuedID}
	}
	return check(g.st, g.store.now())
}

func check(st state, now time.Time) *Skip {
	// The guard holds the scope lock, so a sending marker is never a
	// live sender: that attempt was interrupted.
	if !st.Sending.IsZero() && !now.Before(st.Sending) && now.Before(st.Sending.Add(RecoveryDelay)) {
		return &Skip{Reason: InDoubt, Since: st.Sending, Eligible: st.Sending.Add(RecoveryDelay)}
	}
	if !st.SentAt.IsZero() && !now.Before(st.SentAt) && now.Before(st.Until) {
		return &Skip{Reason: Cooldown, Since: st.SentAt, Eligible: st.Until}
	}
	return nil
}

// Begin records that a delivery attempt starts now, before the request
// is made. Until Sent or Abort records its outcome, a crash leaves the
// attempt in doubt for RecoveryDelay. sc, when non-nil, records the
// scope details (resend does not know them and keeps those on file). A
// cooldown still running is kept, so a failed attempt cannot end it; an
// expired one, or one from a clock that moved backward, is dropped.
func (g *Guard) Begin(sc *Scope) error {
	st := g.st
	now := g.store.now()
	if st.SentAt.IsZero() || now.Before(st.SentAt) || !now.Before(st.Until) {
		st.SentAt, st.Until = time.Time{}, time.Time{}
	}
	if sc != nil {
		st.Key, st.Domain, st.Recipients = sc.Key, sc.Domain, sc.Recipients
	}
	st.Sending = now.UTC()
	if err := g.write(st); err != nil {
		return err
	}
	g.begun = true
	return nil
}

// Begun reports whether Begin recorded an attempt whose outcome has not
// been recorded yet.
func (g *Guard) Begun() bool { return g.begun }

// Sent records a delivery at the given time, starting a cooldown of the
// given length and ending the pending attempt. It keeps the scope
// details already recorded.
func (g *Guard) Sent(at time.Time, cooldown time.Duration) error {
	st := g.st
	st.Sending = time.Time{}
	st.SentAt = at.UTC()
	st.Until = st.SentAt.Add(cooldown)
	if err := g.write(st); err != nil {
		return err
	}
	g.begun = false
	return nil
}

// Abort records that the attempt started by Begin did not deliver the
// notification (it failed or was deferred): the pending marker is
// cleared and no cooldown starts; a cooldown kept by Begin stays. A
// state without anything left to suppress is removed.
func (g *Guard) Abort() error {
	st := g.st
	st.Sending = time.Time{}
	if st.SentAt.IsZero() {
		if err := os.Remove(g.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("dedup: removing %s: %w", g.path, err)
		}
		g.st, g.begun = state{}, false
		return nil
	}
	if err := g.write(st); err != nil {
		return err
	}
	g.begun = false
	return nil
}

// Release unlocks the scope.
func (g *Guard) Release() error {
	return g.unlock()
}

// write replaces the state file atomically.
func (g *Guard) write(st state) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("dedup: encoding state: %w", err)
	}
	tmp := g.path + tmpSuffix
	if err := writeFileSync(tmp, append(data, '\n')); err != nil {
		return errors.Join(fmt.Errorf("dedup: writing %s: %w", tmp, err), removeIfExists(tmp))
	}
	if err := os.Rename(tmp, g.path); err != nil {
		return errors.Join(fmt.Errorf("dedup: writing %s: %w", g.path, err), removeIfExists(tmp))
	}
	g.st = st
	return nil
}

// writeFileSync writes data to path (owner-only, replacing a stale file
// left by a crash) and syncs it before closing.
func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, filePerm)
	if err != nil {
		return err
	}
	return fsutil.WriteSyncClose(f, data)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Cleanup removes state files that no longer suppress anything (expired
// cooldowns, recovery delays that have passed, state from a clock that
// moved backward) and leftover temporary files. It only tries each lock:
// stripes in use by other processes are left for a later cleanup.
// Corrupt state files are kept (Acquire reports them). A missing state
// directory is clean.
func (s *Store) Cleanup() error {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("dedup: reading %s: %w", s.Dir, err)
	}
	stripes := map[string][]string{}
	for _, e := range entries {
		id := strings.TrimSuffix(strings.TrimSuffix(e.Name(), tmpSuffix), stateExt)
		if !e.Type().IsRegular() || !reID.MatchString(id) {
			continue
		}
		stripes[id[:2]] = append(stripes[id[:2]], e.Name())
	}
	var errs []error
	for stripe, names := range stripes {
		unlock, ok, err := filelock.TryLock(filepath.Join(s.Dir, stripe+lockExt))
		if err != nil {
			errs = append(errs, fmt.Errorf("dedup: %w", err))
			continue
		}
		if !ok {
			continue
		}
		now := s.now()
		for _, name := range names {
			path := filepath.Join(s.Dir, name)
			if !strings.HasSuffix(name, tmpSuffix) {
				st, err := readState(path)
				if err != nil || check(st, now) != nil {
					continue
				}
			}
			if err := removeIfExists(path); err != nil {
				errs = append(errs, fmt.Errorf("dedup: removing %s: %w", path, err))
			}
		}
		if err := unlock(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
