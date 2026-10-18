package deferral

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/filelock"
)

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// clock is a controllable time source for Store.Now.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func TestRetryDelay(t *testing.T) {
	for _, tc := range []struct {
		cooldown time.Duration
		attempts int
		want     time.Duration
	}{
		{5 * time.Minute, 0, 5 * time.Minute},
		{5 * time.Minute, 1, 10 * time.Minute},
		{5 * time.Minute, 2, 20 * time.Minute},
		{5 * time.Minute, 3, 40 * time.Minute},
		{5 * time.Minute, 4, time.Hour}, // 80m capped
		{5 * time.Minute, 1000, time.Hour},
		{45 * time.Minute, 1, time.Hour},
		{time.Hour, 0, time.Hour},
		{time.Hour, 5, time.Hour},
		// A cooldown above the cap is used unchanged: never shortened,
		// never doubled.
		{2 * time.Hour, 0, 2 * time.Hour},
		{2 * time.Hour, 3, 2 * time.Hour},
	} {
		if got := RetryDelay(tc.cooldown, tc.attempts); got != tc.want {
			t.Errorf("RetryDelay(%v, %d) = %v, want %v", tc.cooldown, tc.attempts, got, tc.want)
		}
	}
}

func TestNextRetry(t *testing.T) {
	never := Metadata{Timestamp: t0}
	if got := never.NextRetry(5 * time.Minute); !got.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("never retried: NextRetry = %v, want deferral + cooldown", got)
	}
	retried := Metadata{Timestamp: t0, Attempts: 2, LastAttempt: t0.Add(3 * time.Hour)}
	if got := retried.NextRetry(5 * time.Minute); !got.Equal(t0.Add(3*time.Hour + 20*time.Minute)) {
		t.Errorf("retried twice: NextRetry = %v, want last attempt + 20m", got)
	}
}

// Messages written by versions without retry metadata read as never
// retried, stay retryable, and gain the metadata on their first failure
// without their body or deferral timestamp changing.
func TestLegacyMetadataCompatible(t *testing.T) {
	s := newStore(t)
	id := "20261015T120000.000000000-0123456789abcdef"
	dir := filepath.Join(s.Dir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("legacy body")
	legacy := `{
  "id": "` + id + `",
  "timestamp": "2026-10-15T12:00:00Z",
  "to": ["a@example.com"],
  "subject": "legacy",
  "size": 11,
  "content_type": "` + testContentType + `"
}
`
	if err := os.WriteFile(filepath.Join(dir, MetaFile), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, BodyFile), body, 0o600); err != nil {
		t.Fatal(err)
	}

	meta, ok, err := s.Get(id)
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v; want the legacy message", ok, err)
	}
	if meta.Attempts != 0 || !meta.LastAttempt.IsZero() || meta.Failed || meta.LastError != "" {
		t.Errorf("legacy message has retry metadata: %+v", meta)
	}
	if !meta.NextRetry(5 * time.Minute).Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("legacy NextRetry = %v", meta.NextRetry(5*time.Minute))
	}

	if _, err := s.RecordFailure(id, t0.Add(time.Hour), "status 500", false); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	got := readMeta(t, s.Dir, id)
	if got.Attempts != 1 || !got.LastAttempt.Equal(t0.Add(time.Hour)) || got.LastError != "status 500" {
		t.Errorf("metadata after failure = %+v", got)
	}
	if !got.Timestamp.Equal(t0) || got.Subject != "legacy" || got.ContentType != testContentType {
		t.Errorf("original metadata changed: %+v", got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, BodyFile)); err != nil || !bytes.Equal(b, body) {
		t.Errorf("body changed: %q", b)
	}
}

func TestRecordFailure(t *testing.T) {
	s := newStore(t)
	body := []byte("the payload")
	meta, err := s.SaveWithError([]string{"a@example.com"}, "s", body, testContentType, "status 503")
	if err != nil {
		t.Fatal(err)
	}
	if readMeta(t, s.Dir, meta.ID).LastError != "status 503" {
		t.Error("SaveWithError did not record the send error")
	}

	if _, err := s.RecordFailure(meta.ID, t0, "status 400: bad", true); err != nil {
		t.Fatal(err)
	}
	got := readMeta(t, s.Dir, meta.ID)
	if !got.Failed || got.Attempts != 1 || got.LastError != "status 400: bad" {
		t.Errorf("after permanent failure: %+v", got)
	}
	// A later transient failure (forced retry) clears the failed mark.
	if _, err := s.RecordFailure(meta.ID, t0.Add(time.Minute), "timeout", false); err != nil {
		t.Fatal(err)
	}
	got = readMeta(t, s.Dir, meta.ID)
	if got.Failed || got.Attempts != 2 || !got.LastAttempt.Equal(t0.Add(time.Minute)) {
		t.Errorf("after transient failure: %+v", got)
	}
	if !got.Timestamp.Equal(meta.Timestamp) || got.Size != meta.Size {
		t.Errorf("original metadata changed: %+v", got)
	}
	if b, err := os.ReadFile(filepath.Join(s.Dir, meta.ID, BodyFile)); err != nil || !bytes.Equal(b, body) {
		t.Errorf("body changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, meta.ID, metaTmpFile)); !os.IsNotExist(err) {
		t.Errorf("%s left behind: %v", metaTmpFile, err)
	}
	if _, err := s.RecordFailure("no-such-id", t0, "x", false); err == nil {
		t.Error("RecordFailure on an unknown ID succeeded")
	}
}

func TestTruncateError(t *testing.T) {
	long := string(bytes.Repeat([]byte("é"), 2*maxErrorLen))
	if got := []rune(truncateError(long)); len(got) != maxErrorLen || got[len(got)-1] != '…' {
		t.Errorf("truncated to %d runes, want %d ending in an ellipsis", len(got), maxErrorLen)
	}
	if truncateError("short") != "short" {
		t.Error("short error changed")
	}
}

// Delete validates every ID before removing anything.
func TestDeleteAllOrNothing(t *testing.T) {
	s := newStore(t)
	var ids []string
	for _, subj := range []string{"a", "b", "c"} {
		m, err := s.Save([]string{"x@example.com"}, subj, []byte(subj), testContentType)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}

	_, err := s.Delete([]string{ids[0], "nope-1", ids[2], "nope-2"})
	var unknown *UnknownIDsError
	if !errors.As(err, &unknown) {
		t.Fatalf("Delete with unknown IDs: err = %v, want *UnknownIDsError", err)
	}
	if len(unknown.IDs) != 2 || unknown.IDs[0] != "nope-1" || unknown.IDs[1] != "nope-2" {
		t.Errorf("unknown IDs = %v", unknown.IDs)
	}
	if list, err := s.List(); err != nil || len(list) != 3 {
		t.Fatalf("after rejected delete: %d messages, want all 3", len(list))
	}

	deleted, err := s.Delete([]string{ids[2], ids[0], ids[2]})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(deleted) != 2 || deleted[0].ID != ids[2] || deleted[1].ID != ids[0] {
		t.Errorf("deleted = %+v, want [c a]", deleted)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].ID != ids[1] {
		t.Errorf("remaining = %+v, want only b", list)
	}
	// Path tricks never address anything outside the store.
	if _, err := s.Delete([]string{"..", "../" + filepath.Base(s.Dir)}); !errors.As(err, &unknown) {
		t.Errorf("Delete of path-like IDs: err = %v, want *UnknownIDsError", err)
	}
}

func TestDeleteMissingDirNotCreated(t *testing.T) {
	s := newStore(t)
	var unknown *UnknownIDsError
	if _, err := s.Delete([]string{"x"}); !errors.As(err, &unknown) {
		t.Errorf("err = %v, want *UnknownIDsError", err)
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Errorf("Delete created the defer_dir: %v", err)
	}
}

// Expiry follows the controlled clock: Expire and Save remove messages
// older than MaxAge and report each; with MaxAge unset nothing expires.
func TestExpire(t *testing.T) {
	c := &clock{now: t0}
	s := newStore(t)
	s.Now = c.Now
	var reported []string
	s.OnExpire = func(m Metadata) { reported = append(reported, m.Subject) }

	save := func(subject string) Metadata {
		t.Helper()
		m, err := s.Save([]string{"a@example.com"}, subject, []byte(subject), testContentType)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	save("old")
	c.now = t0.Add(30 * time.Minute)
	save("young")

	c.now = t0.Add(10 * time.Hour)
	if got, err := s.Expire(); err != nil || len(got) != 0 {
		t.Errorf("Expire with MaxAge unset = %v, %v; want nothing", got, err)
	}

	s.MaxAge = time.Hour
	c.now = t0.Add(time.Hour) // exactly max_age old: kept
	if got, err := s.Expire(); err != nil || len(got) != 0 {
		t.Errorf("message exactly max_age old expired: %v", got)
	}
	c.now = t0.Add(time.Hour + time.Second)
	got, err := s.Expire()
	if err != nil || len(got) != 1 || got[0].Subject != "old" {
		t.Fatalf("Expire = %+v, %v; want [old]", got, err)
	}

	// Save expires as well, but never the message it just stored.
	c.now = t0.Add(3 * time.Hour)
	fresh := save("fresh")
	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].ID != fresh.ID {
		t.Errorf("after Save: %+v, want only the fresh message", list)
	}
	if len(reported) != 2 || reported[0] != "old" || reported[1] != "young" {
		t.Errorf("OnExpire reported %v, want [old young]", reported)
	}
}

func TestExpireMissingDirNotCreated(t *testing.T) {
	s := newStore(t)
	s.MaxAge = time.Hour
	if _, err := s.Expire(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Errorf("Expire created the defer_dir: %v", err)
	}
}

// The store lock is exclusive, also between two opens in one process,
// which is how two mailfire processes see it.
func TestLockIsExclusive(t *testing.T) {
	s := newStore(t)
	unlock, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		other := &Store{Dir: s.Dir}
		u, err := other.Lock()
		if err != nil {
			t.Error(err)
			close(acquired)
			return
		}
		close(acquired)
		if err := u(); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-acquired:
		t.Fatal("second Lock succeeded while the first was held")
	case <-time.After(100 * time.Millisecond):
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("second Lock not acquired after release")
	}
}

func TestLockNoDir(t *testing.T) {
	if _, err := (&Store{}).Lock(); err == nil {
		t.Error("Lock with empty Dir succeeded")
	}
}

// plantForeign creates a message directory named dirName whose intact
// meta.json claims the given ID and timestamp, plus a sentinel directory
// next to the defer_dir. It returns the sentinel path.
func plantForeign(t *testing.T, s *Store, dirName, claimedID string, ts time.Time) string {
	t.Helper()
	sentinel := filepath.Join(filepath.Dir(s.Dir), "outside-queue")
	if err := os.MkdirAll(sentinel, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sentinel, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Dir, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("body")
	if err := os.WriteFile(filepath.Join(dir, BodyFile), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeMeta(dir, Metadata{ID: claimedID, Timestamp: ts, Size: int64(len(body)), ContentType: testContentType}); err != nil {
		t.Fatal(err)
	}
	return sentinel
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(path, "keep")); err != nil {
		t.Fatalf("sentinel %s was touched: %v", path, err)
	}
}

// A meta.json whose ID does not name its own directory — corrupt, or a
// path traversal such as "../outside-queue" — is not a message: it is
// never listed, found, deleted, or expired by that ID, and nothing
// outside the defer_dir is ever removed.
func TestTraversalIDNeverEscapes(t *testing.T) {
	c := &clock{now: t0.Add(100 * time.Hour)}
	s := newStore(t)
	s.Now, s.MaxAge = c.Now, time.Hour
	dirName := "20261015T120000.000000000-00000000000000aa"
	sentinel := plantForeign(t, s, dirName, "../outside-queue", t0)

	if list, err := s.List(); err != nil || len(list) != 0 {
		t.Errorf("List = %+v, %v; want the traversal entry skipped", list, err)
	}
	if _, ok, err := s.Get("../outside-queue"); err != nil || ok {
		t.Errorf("Get(traversal) = %v, %v; want not found", ok, err)
	}
	var unknown *UnknownIDsError
	if _, err := s.Delete([]string{"../outside-queue"}); !errors.As(err, &unknown) {
		t.Errorf("Delete: err = %v, want *UnknownIDsError", err)
	}
	if got, err := s.Expire(); err != nil || len(got) != 0 {
		t.Errorf("Expire = %+v, %v; want nothing", got, err)
	}
	// Save runs expiry automatically.
	if _, err := s.Save([]string{"a@example.com"}, "new", []byte("b"), testContentType); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("../outside-queue"); err == nil {
		t.Error("Remove accepted a traversal ID")
	}
	if _, err := s.RecordFailure("../outside-queue", t0, "x", false); err == nil {
		t.Error("RecordFailure accepted a traversal ID")
	}
	assertExists(t, sentinel)
	if _, err := os.Stat(filepath.Join(s.Dir, dirName)); err != nil {
		t.Errorf("corrupt entry removed by an ID-based path: %v", err)
	}

	// The max_size cleanup evicts the corrupt entry by its own directory
	// (once past the grace period), never by its claimed ID.
	old := time.Now().Add(-2 * incompleteGrace)
	if err := os.Chtimes(filepath.Join(s.Dir, dirName), old, old); err != nil {
		t.Fatal(err)
	}
	s.MaxSize = 1
	if _, err := s.Save([]string{"a@example.com"}, "newer", []byte("b"), testContentType); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, dirName)); !os.IsNotExist(err) {
		t.Errorf("corrupt entry not evicted: %v", err)
	}
	assertExists(t, sentinel)
}

// A meta.json claiming another message's ID is not listed under it, and
// deleting that ID removes only the real message.
func TestMismatchedIDIgnored(t *testing.T) {
	s := newStore(t)
	real, err := s.Save([]string{"a@example.com"}, "real", []byte("b"), testContentType)
	if err != nil {
		t.Fatal(err)
	}
	impostor := "20261015T120000.000000000-00000000000000bb"
	plantForeign(t, s, impostor, real.ID, t0)

	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].Subject != "real" {
		t.Fatalf("List = %+v, %v; want only the real message", list, err)
	}
	if _, err := s.Delete([]string{real.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, impostor)); err != nil {
		t.Errorf("impostor directory removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, real.ID)); !os.IsNotExist(err) {
		t.Errorf("real message not removed: %v", err)
	}
}

func TestMessageDir(t *testing.T) {
	s := newStore(t)
	good := "20261015T120000.000000000-0123456789abcdef"
	dir, err := s.MessageDir(good)
	if err != nil || filepath.Dir(dir) != s.Dir || filepath.Base(dir) != good {
		t.Errorf("MessageDir(%q) = %q, %v; want a direct child of Dir", good, dir, err)
	}
	for _, id := range []string{"", ".", "..", "../x", "a/b", `a\b`, "/abs", "c:x", LockFile, "x/.."} {
		if _, err := s.MessageDir(id); err == nil {
			t.Errorf("MessageDir(%q) accepted", id)
		}
	}
	if _, err := (&Store{}).MessageDir(good); err == nil {
		t.Error("MessageDir with empty Dir accepted")
	}
}

// TestWithLock: fn runs holding the store lock, its error is returned,
// and the lock is released afterwards, also on failure.
func TestWithLock(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	lockPath := filepath.Join(s.Dir, LockFile)
	errFn := errors.New("fn failed")
	for _, want := range []error{nil, errFn} {
		err := s.withLock(func() error {
			if _, ok, err := filelock.TryLock(lockPath); ok || err != nil {
				t.Errorf("store lock not held inside fn (ok = %v, err = %v)", ok, err)
			}
			return want
		})
		if !errors.Is(err, want) || (want == nil && err != nil) {
			t.Errorf("withLock = %v, want %v", err, want)
		}
		unlock, ok, err := filelock.TryLock(lockPath)
		if !ok || err != nil {
			t.Fatalf("store lock still held after withLock (ok = %v, err = %v)", ok, err)
		}
		if err := unlock(); err != nil {
			t.Fatal(err)
		}
	}
}
