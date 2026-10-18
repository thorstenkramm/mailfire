// Package deferral persists failed sends for later resend (PRD FR-4).
//
// When a send fails with a deferrable error (network error, timeout,
// HTTP 429 or 5xx, or an unparseable success response — see
// internal/mailgun) and deferral is enabled, the fully encoded multipart
// request is stored in the defer_dir, one subdirectory per message:
//
//	defer_dir/
//	  20261015T123045.123456789-9f3ab2c17e4d88aa/
//	    meta.json   ID, timestamp, to, subject, size, content type
//	    body.bin    the exact multipart request body to replay
//
// Storing the encoded bytes rather than the mailgun.Message is
// deliberate: attachment and inline-image readers are consumed by
// encoding and cannot be re-read (review 09). Resend (task 15) replays
// body.bin verbatim with the stored content type, without re-composing
// the message; meta.json holds everything `resend --list` shows, so
// listing never parses the request body.
//
// meta.json is written last and acts as the commit marker: body.bin is
// synced to disk first, then meta.json is written to a temporary file,
// synced and renamed into place. A directory without a parseable
// meta.json, or whose body.bin is missing or does not match the recorded
// size, is an incomplete write (crash mid-save): it is skipped by List
// and evicted first by the max_size cleanup — once it is older than
// incompleteGrace, so a save in progress in a concurrent mailfire
// process is never mistaken for a crash leftover.
//
// Retry metadata (task 25) lives in meta.json as optional fields —
// attempts, last_attempt, last_error, failed — and is rewritten
// atomically (temporary file plus rename) after each failed resend;
// body.bin is never modified. Messages stored by older versions lack the
// fields and read as never retried. Older versions ignore them. The
// optional dedup_scope and dedup_cooldown fields (task 24) link a message
// to its notification-deduplication scope in the same compatible way.
//
// Concurrency: every write — Save (including its cleanups), retry
// bookkeeping, deletion — runs under an exclusive lock on the defer_dir's
// lock file (LockFile, see Lock). Reads take no lock: meta.json only ever
// appears or changes via rename, so List always sees a consistent state.
//
// This layout is read by resend and may sit on disk across upgrades;
// change it only with backwards compatibility in mind.
package deferral

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/thorstenkramm/mailfire/internal/fsutil"
)

const (
	// MetaFile is the metadata file inside a message directory.
	MetaFile = "meta.json"
	// BodyFile is the encoded request body inside a message directory.
	BodyFile = "body.bin"

	// Deferred messages are private: both the directory tree and the
	// files are owner-only, like the config file and the logfile.
	dirPerm  = 0o700
	filePerm = 0o600

	// metaTmpFile is meta.json while being written; renamed on success.
	metaTmpFile = MetaFile + ".tmp"

	// LockFile is the lock file in the defer_dir (see Lock). It is a
	// plain file, so it is never mistaken for a message directory.
	LockFile = ".lock"

	// ReservedDir is a subdirectory of the defer_dir that never holds a
	// message: by default the notification-deduplication state lives in
	// <default defer_dir>/dedup (task 24). The store ignores it — it is
	// neither listed nor evicted as an incomplete write.
	ReservedDir = "dedup"
)

// incompleteGrace is how long an incomplete message directory is left
// alone by the max_size cleanup (neither counted nor dropped): a younger
// one may be a save in progress in another mailfire process. A save
// takes milliseconds even at Mailgun's 25 MB limit.
const incompleteGrace = 10 * time.Minute

// Metadata describes one deferred message (stored as meta.json).
type Metadata struct {
	// ID uniquely identifies the message and names its directory. It
	// starts with a sortable UTC timestamp and ends with random hex.
	ID string `json:"id"`
	// Timestamp is when the failed send was deferred.
	Timestamp time.Time `json:"timestamp"`
	To        []string  `json:"to"`
	Subject   string    `json:"subject"`
	// Size is the length of body.bin in bytes.
	Size int64 `json:"size"`
	// ContentType is the multipart Content-Type of body.bin, boundary
	// included; resend replays the body with exactly this header.
	ContentType string `json:"content_type"`
	// Provider identifies the transport that encoded body.bin. Empty means
	// Mailgun for compatibility with queues written before task 31.
	Provider string `json:"provider,omitempty"`
	// Account identifies the selected non-secret account or transport. Resend
	// refuses a mismatch rather than silently using changed configuration.
	// Empty remains compatible with queues written by earlier versions.
	Account string `json:"account,omitempty"`
	// EnvelopeFrom and EnvelopeRecipients are the SMTP envelope. They are
	// deliberately separate from headers: recipients includes Bcc, while
	// the stored RFC 5322 body never does. Empty for Mailgun entries.
	EnvelopeFrom       string   `json:"envelope_from,omitempty"`
	EnvelopeRecipients []string `json:"envelope_recipients,omitempty"`
	// MessageID is generated while building SMTP MIME and stays stable on
	// retries. Mailgun entries get their ID from the server response.
	MessageID string `json:"message_id,omitempty"`

	// Attempts counts failed resend attempts (the original send is not
	// counted). Zero for messages never resent and for messages stored
	// by versions without retry metadata.
	Attempts int `json:"attempts,omitempty"`
	// LastAttempt is when the latest failed resend was attempted.
	LastAttempt time.Time `json:"last_attempt,omitzero"`
	// LastError is the error of the latest failed attempt: the original
	// send's when the message was never resent. Never contains
	// credentials (Mailgun errors carry no API key).
	LastError string `json:"last_error,omitempty"`
	// Failed marks a message Mailgun rejected permanently. It is kept
	// for inspection but excluded from automatic retries.
	Failed bool `json:"failed,omitempty"`

	// DedupScope is the notification-deduplication scope (task 24) of a
	// send with --dedup-key: while the message is queued, duplicates of
	// it are skipped, and a successful resend starts its cooldown of
	// DedupCooldown. Empty for sends without a key and for messages
	// stored by older versions.
	DedupScope    string        `json:"dedup_scope,omitempty"`
	DedupCooldown time.Duration `json:"dedup_cooldown,omitempty"`
}

// Store is a defer_dir holding deferred messages.
type Store struct {
	// Dir is the defer_dir; Save creates it on the fly (0700).
	Dir string
	// MaxSize bounds the total size of the stored messages in bytes
	// ([defer].max_size). After each Save the oldest messages are dropped
	// until the store fits. Non-positive disables the cleanup.
	MaxSize int64
	// OnDrop, when non-nil, is called once per message dropped by the
	// max_size cleanup, in the order they are dropped (oldest first). The
	// CLI wires it to stderr and the send log (PRD FR-4).
	OnDrop func(meta Metadata)
	// MaxAge, when positive, is [defer].max_age: Save and Expire remove
	// messages deferred longer ago than MaxAge. Zero disables expiry.
	MaxAge time.Duration
	// OnExpire, when non-nil, is called once per message removed because
	// it exceeded MaxAge.
	OnExpire func(meta Metadata)
	// Now, when non-nil, replaces time.Now (tests use a controlled
	// clock).
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Save persists body with fresh metadata under a newly generated ID and
// enforces MaxSize. The just-saved message is never dropped by the
// cleanup: when it alone exceeds MaxSize it stays — losing the message
// that was just deferred would defeat the purpose (the logfile's
// "newest entry wins" rule, applied to messages).
//
// Save returns a non-empty Metadata together with an error when the
// message was stored but the max_size cleanup failed: the send counts as
// deferred, but the cleanup problem should be reported.
//
// Save holds the store lock throughout, and expires messages older than
// MaxAge before the max_size cleanup.
func (s *Store) Save(to []string, subject string, body []byte, contentType string) (Metadata, error) {
	return s.SaveWithError(to, subject, body, contentType, "")
}

// SaveWithError is Save recording sendErr — the error of the failed send
// — as the message's LastError.
func (s *Store) SaveWithError(to []string, subject string, body []byte, contentType, sendErr string) (Metadata, error) {
	return s.SaveMessage(Metadata{To: to, Subject: subject, ContentType: contentType, LastError: sendErr}, body)
}

// SaveMessage is Save for a message described by m: its To, Subject,
// ContentType, LastError, DedupScope, and DedupCooldown are stored; ID,
// Timestamp, Size, and the retry state are assigned fresh.
func (s *Store) SaveMessage(m Metadata, body []byte) (meta Metadata, err error) {
	if s.Dir == "" {
		return Metadata{}, errors.New("deferral: no defer_dir configured")
	}
	unlock, err := s.Lock()
	if err != nil {
		return Metadata{}, err
	}
	defer func() {
		if uerr := unlock(); uerr != nil && err == nil {
			err = uerr
		}
	}()

	now := s.now().UTC()
	dir, id, err := s.createMessageDir(now)
	if err != nil {
		return Metadata{}, err
	}
	meta = Metadata{
		ID:                 id,
		Timestamp:          now,
		To:                 append([]string(nil), m.To...),
		Subject:            m.Subject,
		Size:               int64(len(body)),
		ContentType:        m.ContentType,
		Provider:           m.Provider,
		Account:            m.Account,
		EnvelopeFrom:       m.EnvelopeFrom,
		EnvelopeRecipients: append([]string(nil), m.EnvelopeRecipients...),
		MessageID:          m.MessageID,
		LastError:          truncateError(m.LastError),
		DedupScope:         m.DedupScope,
		DedupCooldown:      m.DedupCooldown,
	}

	// body.bin first, meta.json last: meta.json is the commit marker, so
	// a crash mid-save leaves a directory that List skips and the cleanup
	// evicts first. body.bin is synced before meta.json appears, so even
	// a power loss cannot persist the marker without the body; meta.json
	// itself appears atomically via rename. A failed write removes the
	// incomplete directory.
	if err := writeFileSync(filepath.Join(dir, BodyFile), body); err != nil {
		return Metadata{}, errors.Join(fmt.Errorf("deferral: writing %s: %w", filepath.Join(dir, BodyFile), err), os.RemoveAll(dir))
	}
	if err := writeMeta(dir, meta); err != nil {
		return Metadata{}, errors.Join(err, os.RemoveAll(dir))
	}

	if _, err := s.expireLocked(); err != nil {
		return meta, err
	}
	if err := s.enforceMaxSize(meta.ID); err != nil {
		return meta, err
	}
	return meta, nil
}

// writeMeta writes meta.json into the message directory dir atomically:
// a synced temporary file renamed into place, replacing any previous
// meta.json. Callers hold the store lock, so a leftover temporary file
// (crash mid-write) is stale and removed first.
func writeMeta(dir string, meta Metadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("deferral: encoding metadata: %w", err)
	}
	tmp := filepath.Join(dir, metaTmpFile)
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("deferral: removing stale %s: %w", tmp, err)
	}
	if err := writeFileSync(tmp, append(data, '\n')); err != nil {
		return fmt.Errorf("deferral: writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, MetaFile)); err != nil {
		return errors.Join(fmt.Errorf("deferral: writing %s: %w", filepath.Join(dir, MetaFile), err), os.Remove(tmp))
	}
	return nil
}

// List returns the metadata of all properly stored messages, oldest
// first. A missing Dir yields an empty list. Directories without a
// parseable meta.json (incomplete writes, foreign files) are skipped.
func (s *Store) List() ([]Metadata, error) {
	entries, err := s.scan()
	if err != nil {
		return nil, err
	}
	var out []Metadata
	for _, e := range entries {
		if e.valid {
			out = append(out, e.meta)
		}
	}
	return out, nil
}

// createMessageDir creates a fresh, uniquely named message directory for
// a message deferred at the given time. The ID doubles as the directory
// name; a (practically impossible) collision is retried.
func (s *Store) createMessageDir(t time.Time) (dir, id string, err error) {
	for range 3 {
		id = newID(t)
		dir = filepath.Join(s.Dir, id)
		if err = os.Mkdir(dir, dirPerm); err == nil {
			return dir, id, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", "", fmt.Errorf("deferral: creating message directory in %s: %w", s.Dir, err)
		}
	}
	return "", "", fmt.Errorf("deferral: creating message directory in %s: ID collision persists", s.Dir)
}

// writeFileSync creates path exclusively (owner-only), writes data and
// syncs it to disk before closing.
func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return err
	}
	return fsutil.WriteSyncClose(f, data)
}

// idTimeFormat makes the ID's timestamp prefix sortable as plain text.
const idTimeFormat = "20060102T150405.000000000"

// newID builds a message ID: a sortable UTC timestamp plus 16 random hex
// characters (collision-safe for messages deferred within the same
// nanosecond and across processes).
func newID(t time.Time) string {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		// crypto/rand failures are practically impossible; fall back to
		// the nanosecond clock rather than losing the message.
		binary.BigEndian.PutUint64(rnd[:], uint64(t.UnixNano()))
	}
	return t.UTC().Format(idTimeFormat) + "-" + hex.EncodeToString(rnd[:])
}

// entry is one subdirectory of the defer_dir. Entries without a parseable
// meta.json, whose meta.json ID differs from the directory name, or whose
// body.bin is missing or not of the recorded size, are invalid (incomplete writes or foreign directories); they can never
// be resent and are the first eviction candidates.
type entry struct {
	meta    Metadata
	path    string
	size    int64 // total size of the directory's regular files
	valid   bool
	modTime time.Time // of the directory; ages incomplete writes
}

// scan reads the defer_dir and returns its message directories in
// eviction order: invalid entries first (by name), then valid messages
// oldest first (by timestamp, ID as tiebreak). A missing directory scans
// as empty.
func (s *Store) scan() (result []entry, err error) {
	dir, err := os.Open(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("deferral: reading %s: %w", s.Dir, err)
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	fi, err := dir.Stat()
	if err != nil {
		return nil, fmt.Errorf("deferral: stat %s: %w", s.Dir, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("deferral: reading %s: not a directory", s.Dir)
	}
	dirs, err := dir.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("deferral: reading %s: %w", s.Dir, err)
	}
	var entries []entry
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == ReservedDir {
			continue // stray files and the dedup state are not ours to touch
		}
		e := entry{path: filepath.Join(s.Dir, d.Name()), size: dirSize(filepath.Join(s.Dir, d.Name()))}
		if fi, err := d.Info(); err == nil {
			e.modTime = fi.ModTime()
		}
		if data, err := os.ReadFile(filepath.Join(e.path, MetaFile)); err == nil {
			err = json.Unmarshal(data, &e.meta)
			// The ID must name this very directory: every operation
			// addresses a message by its ID, so a corrupt or malicious
			// meta.json (e.g. id = "../elsewhere") must never redirect
			// reads, rewrites, or removals outside it.
			e.valid = err == nil && e.meta.ID == d.Name() && validID(e.meta.ID) && bodyIntact(e.path, e.meta.Size)
		}
		if !e.valid {
			e.meta = Metadata{ID: d.Name(), Size: e.size}
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.valid != b.valid {
			return !a.valid // incomplete writes are dropped first
		}
		if !a.meta.Timestamp.Equal(b.meta.Timestamp) {
			return a.meta.Timestamp.Before(b.meta.Timestamp)
		}
		return a.meta.ID < b.meta.ID
	})
	return entries, nil
}

// bodyIntact reports whether the message directory holds a regular
// body.bin of exactly size bytes: a torn write must never be replayed.
func bodyIntact(path string, size int64) bool {
	fi, err := os.Lstat(filepath.Join(path, BodyFile))
	return err == nil && fi.Mode().IsRegular() && fi.Size() == size
}

// dirSize sums the sizes of the regular files directly inside path.
func dirSize(path string) int64 {
	files, err := os.ReadDir(path)
	if err != nil {
		return 0
	}
	var total int64
	for _, f := range files {
		if fi, err := f.Info(); err == nil && fi.Mode().IsRegular() {
			total += fi.Size()
		}
	}
	return total
}

// enforceMaxSize drops the oldest messages (incomplete writes first, then
// oldest valid) until the store fits MaxSize, reporting each drop via
// OnDrop. The message with protectID — the one just saved — is never
// dropped, and neither is anything ordered after it: those are messages
// saved concurrently by other mailfire processes, each protected by its
// own Save. Without that rule two concurrent cleanups could drop each
// other's just-saved message, and both sends would exit 4 with nothing
// stored. Hence the store may stay above MaxSize (e.g. when the newest
// message alone exceeds it); the next Save evicts it as the oldest.
//
// Incomplete writes younger than incompleteGrace are neither counted nor
// dropped: they may be saves in progress elsewhere.
//
// Sizes are those of the files in each message directory (body.bin plus
// meta.json); filesystem overhead of the directories is not counted.
func (s *Store) enforceMaxSize(protectID string) error {
	if s.MaxSize <= 0 {
		return nil
	}
	all, err := s.scan()
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-incompleteGrace)
	var candidates []entry // eviction order, up to the protected message
	var total int64
	protected := false
	for _, e := range all {
		if !e.valid && e.modTime.After(cutoff) {
			continue
		}
		total += e.size
		if e.valid && e.meta.ID == protectID {
			protected = true
		}
		if !protected {
			candidates = append(candidates, e)
		}
	}
	if !protected {
		// The just-saved message is gone already (dropped by a newer
		// concurrent Save, whose cleanup is responsible): drop nothing.
		return nil
	}
	for _, e := range candidates {
		if total <= s.MaxSize {
			break
		}
		if err := os.RemoveAll(e.path); err != nil {
			return fmt.Errorf("deferral: dropping %s: %w", e.path, err)
		}
		if s.OnDrop != nil {
			s.OnDrop(e.meta)
		}
		total -= e.size
	}
	return nil
}
