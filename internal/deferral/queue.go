package deferral

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thorstenkramm/mailfire/internal/filelock"
)

// MaxBackoff caps the resend backoff (task 25): the delay doubles after
// each failed resend but never exceeds one hour — unless the configured
// resend_cooldown itself is longer, which is then used unchanged.
const MaxBackoff = time.Hour

// maxErrorLen bounds the stored LastError, in runes.
const maxErrorLen = 500

// RetryDelay returns how long to wait after the latest attempt before
// retrying a message that has failed attempts resends: cooldown after
// the original deferral, doubled after each failed resend, capped at
// MaxBackoff. A cooldown at or above MaxBackoff is used unchanged: the
// cap never shortens the configured cooldown.
func RetryDelay(cooldown time.Duration, attempts int) time.Duration {
	if cooldown >= MaxBackoff {
		return cooldown
	}
	d := cooldown
	for range attempts {
		d *= 2
		if d >= MaxBackoff {
			return MaxBackoff
		}
	}
	return d
}

// NextRetry returns when the message becomes eligible for an automatic
// retry, measured from the latest attempt: the original deferral when it
// was never resent (including messages stored by older versions), else
// the last failed resend. It ignores Failed; callers check that first.
func (m Metadata) NextRetry(cooldown time.Duration) time.Time {
	if m.Attempts == 0 || m.LastAttempt.IsZero() {
		return m.Timestamp.Add(cooldown)
	}
	return m.LastAttempt.Add(RetryDelay(cooldown, m.Attempts))
}

// Expired reports whether the message is older than MaxAge at now.
func (s *Store) Expired(m Metadata, now time.Time) bool {
	return s.MaxAge > 0 && now.Sub(m.Timestamp) > s.MaxAge
}

// Lock blocks until this process holds the exclusive store lock — an
// OS-level lock on LockFile in the defer_dir, which it creates if
// needed — and returns the function that releases it. Every operation
// that changes the store holds it: Save, Delete, Expire, and a resend
// from re-reading a message to recording its outcome, so a message is
// never sent twice or deleted mid-send by concurrent mailfire processes.
// Other features that must coordinate with the queue (e.g. notification
// deduplication) can take the same lock. The OS releases the lock when
// the process exits, so a crash never leaves it held.
func (s *Store) Lock() (unlock func() error, err error) {
	if s.Dir == "" {
		return nil, errors.New("deferral: no defer_dir configured")
	}
	if err := os.MkdirAll(s.Dir, dirPerm); err != nil {
		return nil, fmt.Errorf("deferral: cannot create %s: %w", s.Dir, err)
	}
	unlock, err = filelock.Lock(filepath.Join(s.Dir, LockFile))
	if err != nil {
		return nil, fmt.Errorf("deferral: %w", err)
	}
	return unlock, nil
}

// Get returns the stored metadata of the message with the given ID.
// ok is false when no such (complete) message exists. Only IDs listed
// by List are found, so an ID can never address a path outside Dir.
func (s *Store) Get(id string) (meta Metadata, ok bool, err error) {
	msgs, err := s.List()
	if err != nil {
		return Metadata{}, false, err
	}
	for _, m := range msgs {
		if m.ID == id {
			return m, true, nil
		}
	}
	return Metadata{}, false, nil
}

// validID reports whether id can name a message directory: a single,
// local path element — no separators, no "." or "..", not the lock file
// or the reserved directory.
func validID(id string) bool {
	return id != "" && id != "." && id != ".." && id != LockFile && id != ReservedDir &&
		!strings.ContainsAny(id, `/\:`) && filepath.IsLocal(id) && filepath.Base(id) == id
}

// MessageDir returns the directory of message id: always a direct child
// of Dir. It rejects IDs that are not a single path element, so no
// caller can address a path outside the store, whatever a meta.json or
// the command line says.
func (s *Store) MessageDir(id string) (string, error) {
	if s.Dir == "" {
		return "", errors.New("deferral: no defer_dir configured")
	}
	if !validID(id) {
		return "", fmt.Errorf("deferral: invalid message ID %q", id)
	}
	return filepath.Join(s.Dir, id), nil
}

// RecordFailure updates the retry metadata of message id after a failed
// resend attempted at the given time: Attempts is incremented and
// LastAttempt and LastError are set; permanent marks it Failed, and a
// non-permanent failure clears Failed (a forced retry of a failed message
// may fail transiently). body.bin is not touched. The caller must hold
// the store lock.
func (s *Store) RecordFailure(id string, at time.Time, sendErr string, permanent bool) (Metadata, error) {
	meta, ok, err := s.Get(id)
	if err != nil {
		return Metadata{}, err
	}
	if !ok {
		return Metadata{}, fmt.Errorf("deferral: message %s not found", id)
	}
	meta.Attempts++
	meta.LastAttempt = at.UTC()
	meta.LastError = truncateError(sendErr)
	meta.Failed = permanent
	dir, err := s.MessageDir(id)
	if err != nil {
		return Metadata{}, err
	}
	if err := writeMeta(dir, meta); err != nil {
		return Metadata{}, err
	}
	return meta, nil
}

// Remove deletes one message directory. The caller must hold the store
// lock and must have obtained id from List or Get.
func (s *Store) Remove(id string) error {
	dir, err := s.MessageDir(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("deferral: removing %s: %w", id, err)
	}
	return nil
}

// UnknownIDsError reports IDs that name no stored message; Delete
// returns it before deleting anything.
type UnknownIDsError struct {
	IDs []string
}

func (e *UnknownIDsError) Error() string {
	return fmt.Sprintf("deferral: unknown message IDs: %v", e.IDs)
}

// Delete removes exactly the messages with the given IDs, under the
// store lock. All IDs are validated first: if any is unknown, nothing is
// deleted and an *UnknownIDsError lists the unknown ones. Duplicate IDs
// are deleted once. It returns the metadata of the deleted messages in
// the order given; on a removal error, those deleted so far.
func (s *Store) Delete(ids []string) (deleted []Metadata, err error) {
	if s.Dir == "" {
		return nil, &UnknownIDsError{IDs: ids}
	}
	if _, serr := os.Stat(s.Dir); errors.Is(serr, os.ErrNotExist) {
		// Do not create the defer_dir just to report that it is empty.
		return nil, &UnknownIDsError{IDs: ids}
	}
	err = s.withLock(func() error {
		msgs, err := s.List()
		if err != nil {
			return err
		}
		byID := make(map[string]Metadata, len(msgs))
		for _, m := range msgs {
			byID[m.ID] = m
		}
		var selected []Metadata
		var unknown []string
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			m, ok := byID[id]
			if !ok {
				unknown = append(unknown, id)
				continue
			}
			selected = append(selected, m)
		}
		if len(unknown) > 0 {
			return &UnknownIDsError{IDs: unknown}
		}
		for _, m := range selected {
			if err := s.Remove(m.ID); err != nil {
				return err
			}
			deleted = append(deleted, m)
		}
		return nil
	})
	return deleted, err
}

// Expire removes, under the store lock, every message older than MaxAge,
// reporting each via OnExpire, and returns them oldest first. With MaxAge
// disabled, or no defer_dir yet, it does nothing.
func (s *Store) Expire() (expired []Metadata, err error) {
	if s.MaxAge <= 0 || s.Dir == "" {
		return nil, nil
	}
	if _, serr := os.Stat(s.Dir); errors.Is(serr, os.ErrNotExist) {
		return nil, nil
	}
	err = s.withLock(func() error {
		expired, err = s.expireLocked()
		return err
	})
	return expired, err
}

// withLock runs fn while holding the store lock. A failure to release the
// lock is returned only when fn succeeded; fn's own error takes precedence.
func (s *Store) withLock(fn func() error) (err error) {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer func() {
		if uerr := unlock(); uerr != nil && err == nil {
			err = uerr
		}
	}()
	return fn()
}

// expireLocked is Expire for a caller holding the store lock.
func (s *Store) expireLocked() ([]Metadata, error) {
	if s.MaxAge <= 0 {
		return nil, nil
	}
	msgs, err := s.List()
	if err != nil {
		return nil, err
	}
	now := s.now()
	var expired []Metadata
	for _, m := range msgs {
		if !s.Expired(m, now) {
			continue
		}
		if err := s.Remove(m.ID); err != nil {
			return expired, err
		}
		expired = append(expired, m)
		if s.OnExpire != nil {
			s.OnExpire(m)
		}
	}
	return expired, nil
}

// truncateError bounds a stored error message to maxErrorLen runes.
func truncateError(s string) string {
	if utf8.RuneCountInString(s) <= maxErrorLen {
		return s
	}
	return string([]rune(s)[:maxErrorLen-1]) + "…"
}
