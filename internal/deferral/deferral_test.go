package deferral

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testContentType = `multipart/form-data; boundary=abc123`

// newStore returns a Store in a fresh temp dir (not yet created, so Save
// exercises the on-the-fly creation) with cleanup effectively disabled.
func newStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Dir: filepath.Join(t.TempDir(), "deferred")}
}

// readMeta reads and decodes the meta.json of one stored message.
func readMeta(t *testing.T, dir, id string) Metadata {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id, MetaFile))
	if err != nil {
		t.Fatalf("reading meta.json: %v", err)
	}
	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("meta.json is not valid JSON: %v", err)
	}
	return meta
}

func TestSaveAndListRoundTrip(t *testing.T) {
	s := newStore(t)
	before := time.Now().UTC()

	body1 := []byte("first body")
	meta1, err := s.Save([]string{"a@example.com"}, "first subject", body1, testContentType)
	if err != nil {
		t.Fatalf("Save 1: %v", err)
	}
	body2 := []byte("second body, slightly longer")
	meta2, err := s.Save([]string{"a@example.com", "b@example.com"}, "second subject", body2, testContentType)
	if err != nil {
		t.Fatalf("Save 2: %v", err)
	}
	after := time.Now().UTC()

	if meta1.ID == "" || meta2.ID == "" || meta1.ID == meta2.ID {
		t.Errorf("IDs = %q, %q, want non-empty and unique", meta1.ID, meta2.ID)
	}
	for _, meta := range []Metadata{meta1, meta2} {
		if meta.Timestamp.Before(before) || meta.Timestamp.After(after) {
			t.Errorf("timestamp %v outside [%v, %v]", meta.Timestamp, before, after)
		}
		if meta.Timestamp.Location() != time.UTC {
			t.Errorf("timestamp location = %v, want UTC", meta.Timestamp.Location())
		}
	}

	// The directory layout: one subdirectory per message, named by ID,
	// holding body.bin and meta.json.
	dirs, err := os.ReadDir(s.Dir)
	if err != nil {
		t.Fatalf("reading defer_dir: %v", err)
	}
	if len(dirs) != 2 {
		t.Fatalf("got %d entries in defer_dir, want 2", len(dirs))
	}
	for i, meta := range []Metadata{meta1, meta2} {
		if dirs[i].Name() != meta.ID {
			t.Errorf("dir %q, want message ID %q", dirs[i].Name(), meta.ID)
		}
	}

	// body.bin holds the exact bytes passed to Save.
	got, err := os.ReadFile(filepath.Join(s.Dir, meta2.ID, BodyFile))
	if err != nil {
		t.Fatalf("reading body.bin: %v", err)
	}
	if string(got) != string(body2) {
		t.Errorf("body.bin = %q, want %q", got, body2)
	}

	// meta.json on disk round-trips into the returned metadata.
	onDisk := readMeta(t, s.Dir, meta2.ID)
	if onDisk.ID != meta2.ID || onDisk.Subject != "second subject" ||
		onDisk.Size != int64(len(body2)) || onDisk.ContentType != testContentType ||
		len(onDisk.To) != 2 || onDisk.To[1] != "b@example.com" {
		t.Errorf("meta.json round-trip mismatch: %+v", onDisk)
	}

	// List returns everything `resend --list` needs, oldest first,
	// without opening body.bin.
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].ID != meta1.ID || list[1].ID != meta2.ID {
		t.Errorf("List = %+v, want [%s %s]", list, meta1.ID, meta2.ID)
	}
	if list[0].Size != int64(len(body1)) || list[0].Subject != "first subject" {
		t.Errorf("List[0] = %+v", list[0])
	}
}

func TestListEmptyAndMissingDir(t *testing.T) {
	s := newStore(t)
	list, err := s.List()
	if err != nil {
		t.Fatalf("List on missing dir: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List = %+v, want empty", list)
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Errorf("List created the defer_dir: %v", err)
	}
}

func TestSaveNoDir(t *testing.T) {
	s := &Store{}
	if _, err := s.Save([]string{"a@example.com"}, "s", []byte("b"), testContentType); err == nil {
		t.Fatal("Save with empty Dir succeeded")
	}
}

func TestSaveDirIsAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(file, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{Dir: file}
	meta, err := s.Save([]string{"a@example.com"}, "s", []byte("b"), testContentType)
	if err == nil {
		t.Fatal("Save into a regular file succeeded")
	}
	if meta.ID != "" {
		t.Errorf("meta.ID = %q, want empty: nothing was stored", meta.ID)
	}
}

// Directories without a parseable meta.json (crash mid-save, foreign
// files) are invisible to List.
func TestListSkipsIncompleteWrites(t *testing.T) {
	s := newStore(t)
	meta, err := s.Save([]string{"a@example.com"}, "s", []byte("b"), testContentType)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	// A directory without meta.json, one with corrupt JSON, one with
	// valid JSON but no ID, and a stray file directly in the defer_dir.
	for _, d := range []string{"incomplete", "corrupt", "noid"} {
		if err := os.Mkdir(filepath.Join(s.Dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "corrupt", MetaFile), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "noid", MetaFile), []byte(`{"subject":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "stray.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != meta.ID {
		t.Errorf("List = %+v, want only %s", list, meta.ID)
	}
}

// sizeOf returns the total size of the regular files in one message dir.
func sizeOf(t *testing.T, dir string) int64 {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, f := range files {
		fi, err := f.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += fi.Size()
	}
	return total
}

// plantMessage writes a complete deferred message directly to disk with
// an exact body size, so eviction tests do not depend on JSON length
// details of Save's output. Equal arguments give equal directory sizes.
func plantMessage(t *testing.T, dir, id string, ts time.Time, bodySize int) Metadata {
	t.Helper()
	meta := Metadata{
		ID:          id,
		Timestamp:   ts,
		To:          []string{"a@example.com"},
		Subject:     "s",
		Size:        int64(bodySize),
		ContentType: testContentType,
	}
	mdir := filepath.Join(dir, id)
	if err := os.MkdirAll(mdir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdir, MetaFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdir, BodyFile), make([]byte, bodySize), 0o600); err != nil {
		t.Fatal(err)
	}
	return meta
}

var (
	plantBase = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	plantBody = make([]byte, 1000)
)

// TestEvictionOldestFirst: when the store exceeds MaxSize after a Save,
// the oldest messages are dropped first and each drop is reported via
// OnDrop in drop order.
func TestEvictionOldestFirst(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old1 := plantMessage(t, s.Dir, "id-1", plantBase, len(plantBody))
	old2 := plantMessage(t, s.Dir, "id-2", plantBase.Add(time.Hour), len(plantBody))

	// MaxSize fits one old message; saving a new one (its size differs
	// slightly from the planted ones) must evict both old messages, in
	// order, and keep the new one (it is never dropped).
	s.MaxSize = sizeOf(t, filepath.Join(s.Dir, old1.ID))
	var drops []Metadata
	s.OnDrop = func(meta Metadata) { drops = append(drops, meta) }
	meta, err := s.Save([]string{"a@example.com"}, "s", plantBody, testContentType)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	if len(drops) != 2 || drops[0].ID != old1.ID || drops[1].ID != old2.ID {
		t.Errorf("drops = %+v, want %s then %s", drops, old1.ID, old2.ID)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != meta.ID {
		t.Errorf("List = %+v, want only the new message %s", list, meta.ID)
	}
	for _, d := range drops {
		if _, err := os.Stat(filepath.Join(s.Dir, d.ID)); !os.IsNotExist(err) {
			t.Errorf("dropped message %s still on disk", d.ID)
		}
	}
}

// The message just saved is never dropped, even when it alone exceeds
// MaxSize: deferring must not lose the message it was asked to keep.
func TestEvictionKeepsJustSaved(t *testing.T) {
	s := newStore(t)
	s.MaxSize = 1 // one byte: everything exceeds it
	var drops []Metadata
	s.OnDrop = func(meta Metadata) { drops = append(drops, meta) }

	meta, err := s.Save([]string{"a@example.com"}, "s", []byte("a body far longer than one byte"), testContentType)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(drops) != 0 {
		t.Errorf("drops = %+v, want none (only the just-saved message exists)", drops)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != meta.ID {
		t.Errorf("List = %+v, want the oversized new message kept", list)
	}
}

// Incomplete writes (no parseable meta.json) can never be resent, so the
// cleanup drops them before any valid message — even before an older one.
func TestEvictionDropsIncompleteFirst(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := plantMessage(t, s.Dir, "id-old", plantBase, len(plantBody))
	// A stalled write: body.bin but no meta.json, big enough that
	// dropping it alone brings the store back under MaxSize.
	stalled := filepath.Join(s.Dir, "00000000T000000.000000000-stalled")
	if err := os.Mkdir(stalled, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stalled, BodyFile), make([]byte, 5000), 0o600); err != nil {
		t.Fatal(err)
	}
	age(t, stalled)

	dOld := sizeOf(t, filepath.Join(s.Dir, old.ID))
	dStalled := sizeOf(t, stalled)
	s.MaxSize = dOld + dStalled // fits old + stalled, not old + stalled + new
	var drops []Metadata
	s.OnDrop = func(meta Metadata) { drops = append(drops, meta) }
	meta, err := s.Save([]string{"a@example.com"}, "s", plantBody, testContentType)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The stalled write is dropped even though it is "newer" than the
	// valid message: it can never be resent.
	if len(drops) != 1 || drops[0].ID != "00000000T000000.000000000-stalled" {
		t.Errorf("drops = %+v, want only the stalled write", drops)
	}
	if _, err := os.Stat(stalled); !os.IsNotExist(err) {
		t.Error("stalled write still on disk")
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].ID != old.ID || list[1].ID != meta.ID {
		t.Errorf("List = %+v, want [%s %s]", list, old.ID, meta.ID)
	}
}

// TestIDsSortable: the ID's timestamp prefix makes directory names sort
// chronologically as plain text, and the random suffix keeps IDs unique.
func TestIDsSortable(t *testing.T) {
	t1 := time.Date(2026, 10, 15, 12, 30, 45, 123456789, time.UTC)
	t2 := t1.Add(time.Nanosecond)
	id1, id2 := newID(t1), newID(t2)
	if id1 >= id2 {
		t.Errorf("IDs not sortable: %q >= %q", id1, id2)
	}
	if !strings.HasPrefix(id1, "20261015T123045.123456789-") {
		t.Errorf("ID %q does not start with the sortable timestamp", id1)
	}
	// Same timestamp: the random suffix must still make IDs unique.
	a, b := newID(t1), newID(t1)
	if a == b {
		t.Errorf("IDs for the same timestamp collide: %q", a)
	}
	if len(a) != len("20261015T123045.123456789-")+16 {
		t.Errorf("ID %q has unexpected length %d", a, len(a))
	}
}

// age backdates a directory past incompleteGrace, marking an incomplete
// write in it as a crash leftover rather than a save in progress.
func age(t *testing.T, dir string) {
	t.Helper()
	old := time.Now().Add(-2 * incompleteGrace)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
}

// plantIncomplete simulates a crash mid-save: a message directory with
// body.bin but without meta.json (or with only the temporary meta file).
func plantIncomplete(t *testing.T, dir, name string, bodySize int, withTmpMeta bool) string {
	t.Helper()
	mdir := filepath.Join(dir, name)
	if err := os.MkdirAll(mdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdir, BodyFile), make([]byte, bodySize), 0o600); err != nil {
		t.Fatal(err)
	}
	if withTmpMeta {
		meta := Metadata{ID: name, Size: int64(bodySize), ContentType: testContentType}
		data, err := json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mdir, metaTmpFile), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return mdir
}

// Crash between body.bin and meta.json (with or without the temporary
// meta file): the entry is never listed (so never replayed by resend),
// but once older than the grace period the cleanup evicts it.
func TestCrashMidSaveNeverListedButEvicted(t *testing.T) {
	for _, withTmp := range []bool{false, true} {
		t.Run(fmt.Sprintf("tmpmeta=%v", withTmp), func(t *testing.T) {
			s := newStore(t)
			crashed := plantIncomplete(t, s.Dir, "20261015T000000.000000000-crashed", 5000, withTmp)
			age(t, crashed)

			list, err := s.List()
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(list) != 0 {
				t.Fatalf("List = %+v, want the crashed save invisible", list)
			}

			s.MaxSize = 3000
			var drops []Metadata
			s.OnDrop = func(meta Metadata) { drops = append(drops, meta) }
			meta, err := s.Save([]string{"a@example.com"}, "s", []byte("new"), testContentType)
			if err != nil {
				t.Fatalf("Save: %v", err)
			}
			if len(drops) != 1 || drops[0].ID != filepath.Base(crashed) {
				t.Errorf("drops = %+v, want the crashed save", drops)
			}
			if _, err := os.Stat(crashed); !os.IsNotExist(err) {
				t.Error("crashed save still on disk")
			}
			if list, err := s.List(); err != nil || len(list) != 1 || list[0].ID != meta.ID {
				t.Errorf("List = %+v, want only %s", list, meta.ID)
			}
		})
	}
}

// A fresh incomplete directory may be a save in progress in a concurrent
// mailfire process: the cleanup must neither drop it nor count it (which
// would evict valid messages in its place).
func TestEvictionSparesYoungIncomplete(t *testing.T) {
	s := newStore(t)
	inProgress := plantIncomplete(t, s.Dir, "20261015T000000.000000000-inprogress", 5000, false)
	old := plantMessage(t, s.Dir, "id-old", plantBase, 100)

	// Fits old + new comfortably, but not with the in-progress body.
	s.MaxSize = 2000
	var drops []Metadata
	s.OnDrop = func(meta Metadata) { drops = append(drops, meta) }
	if _, err := s.Save([]string{"a@example.com"}, "s", []byte("new"), testContentType); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(drops) != 0 {
		t.Errorf("drops = %+v, want none", drops)
	}
	for _, p := range []string{inProgress, filepath.Join(s.Dir, old.ID)} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}
}

// A meta.json whose body.bin is missing or of a different size (torn
// write, power loss) marks an entry that must never be replayed.
func TestListSkipsTornBody(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	short := plantMessage(t, s.Dir, "id-short", plantBase, 100)
	if err := os.WriteFile(filepath.Join(s.Dir, short.ID, BodyFile), make([]byte, 40), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := plantMessage(t, s.Dir, "id-missing", plantBase, 100)
	if err := os.Remove(filepath.Join(s.Dir, missing.ID, BodyFile)); err != nil {
		t.Fatal(err)
	}
	good := plantMessage(t, s.Dir, "id-good", plantBase, 100)

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != good.ID {
		t.Errorf("List = %+v, want only %s", list, good.ID)
	}
}

// Save leaves no temporary meta file behind.
func TestSaveLeavesNoTempFile(t *testing.T) {
	s := newStore(t)
	meta, err := s.Save([]string{"a@example.com"}, "s", []byte("b"), testContentType)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, meta.ID, metaTmpFile)); !os.IsNotExist(err) {
		t.Errorf("%s left behind: %v", metaTmpFile, err)
	}
}

// Many saves at once (same process, possibly the same clock tick) all
// get distinct directories, the eviction loop terminates even when every
// message exceeds MaxSize alone, and the cleanups never drop each other's
// newest message: the newest saved message always survives.
func TestConcurrentSaves(t *testing.T) {
	s := newStore(t)
	s.MaxSize = 1
	const n = 20
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		saved []Metadata
	)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			meta, err := s.Save([]string{"a@example.com"}, "s", []byte("body"), testContentType)
			if meta.ID == "" {
				t.Errorf("Save lost the message: %v", err)
				return
			}
			mu.Lock()
			saved = append(saved, meta)
			mu.Unlock()
		}()
	}
	wg.Wait()

	ids := map[string]bool{}
	newest := saved[0]
	for _, m := range saved {
		if ids[m.ID] {
			t.Errorf("duplicate ID %s", m.ID)
		}
		ids[m.ID] = true
		if m.Timestamp.After(newest.Timestamp) ||
			(m.Timestamp.Equal(newest.Timestamp) && m.ID > newest.ID) {
			newest = m
		}
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, m := range list {
		found = found || m.ID == newest.ID
	}
	if !found {
		t.Errorf("List = %+v, newest message %s was dropped", list, newest.ID)
	}
}

// When the just-saved message has already been dropped by a concurrent
// (newer) Save, the cleanup drops nothing more.
func TestEvictionProtectedGone(t *testing.T) {
	s := newStore(t)
	old := plantMessage(t, s.Dir, "id-old", plantBase, 1000)
	s.MaxSize = 1
	dropped := false
	s.OnDrop = func(Metadata) { dropped = true }
	if err := s.enforceMaxSize("id-vanished"); err != nil {
		t.Fatalf("enforceMaxSize: %v", err)
	}
	if dropped {
		t.Error("dropped a message although the protected one is gone")
	}
	if _, err := os.Stat(filepath.Join(s.Dir, old.ID)); err != nil {
		t.Errorf("old message removed: %v", err)
	}
}

// Messages ordered after the just-saved one (saved concurrently by other
// processes) are never dropped by its cleanup.
func TestEvictionSparesNewer(t *testing.T) {
	s := newStore(t)
	older := plantMessage(t, s.Dir, "id-1", plantBase, 1000)
	mine := plantMessage(t, s.Dir, "id-2", plantBase.Add(time.Minute), 1000)
	newer := plantMessage(t, s.Dir, "id-3", plantBase.Add(2*time.Minute), 1000)
	s.MaxSize = 1
	var drops []Metadata
	s.OnDrop = func(m Metadata) { drops = append(drops, m) }
	if err := s.enforceMaxSize(mine.ID); err != nil {
		t.Fatalf("enforceMaxSize: %v", err)
	}
	if len(drops) != 1 || drops[0].ID != older.ID {
		t.Errorf("drops = %+v, want only %s", drops, older.ID)
	}
	if list, err := s.List(); err != nil || len(list) != 2 || list[0].ID != mine.ID || list[1].ID != newer.ID {
		t.Errorf("List = %+v, want [%s %s]", list, mine.ID, newer.ID)
	}
}

// writeFileSync never overwrites: message files are created exclusively.
func TestWriteFileSyncExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := writeFileSync(path, []byte("one")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeFileSync(path, []byte("two")); err == nil {
		t.Fatal("second write over an existing file succeeded")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "one" {
		t.Errorf("content = %q, want the original", got)
	}
}

// A defer_dir that is not a directory fails List (and thus the cleanup).
func TestListDirIsAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{Dir: file}
	if _, err := s.List(); err == nil {
		t.Fatal("List on a regular file succeeded")
	}
	s.MaxSize = 1
	if err := s.enforceMaxSize("x"); err == nil {
		t.Fatal("enforceMaxSize on a regular file succeeded")
	}
}
