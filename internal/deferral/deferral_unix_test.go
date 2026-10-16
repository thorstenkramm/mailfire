//go:build unix

package deferral

import (
	"os"
	"path/filepath"
	"testing"
)

// The defer_dir, the per-message directories and both files are
// owner-only: they contain full message content (FR-2 security posture,
// same as the config file and the logfile).
func TestSavePermissions(t *testing.T) {
	s := newStore(t)
	meta, err := s.Save([]string{"a@example.com"}, "s", []byte("body"), testContentType)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	check := func(path string, want os.FileMode) {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s: perm = %04o, want %04o", path, got, want)
		}
	}
	check(s.Dir, 0o700)
	mdir := filepath.Join(s.Dir, meta.ID)
	check(mdir, 0o700)
	check(filepath.Join(mdir, MetaFile), 0o600)
	check(filepath.Join(mdir, BodyFile), 0o600)
}

// A read-only defer_dir lets MkdirAll succeed (the directory exists) but
// fails creating the message directory: Save reports that nothing was
// stored.
func TestSaveReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	s := newStore(t)
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(s.Dir, 0o700); err != nil {
			t.Error(err)
		}
	})

	meta, err := s.Save([]string{"a@example.com"}, "s", plantBody, testContentType)
	if err == nil {
		t.Fatal("Save succeeded in a read-only defer_dir")
	}
	if meta.ID != "" {
		t.Errorf("meta.ID = %q, want empty: nothing was stored", meta.ID)
	}
	// No half-created message directory is left behind.
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("defer_dir contains %v, want empty", entries)
	}
}

// When the cleanup cannot remove a message, Save still reports the new
// message as stored: it returns the metadata AND the cleanup error, so
// the caller can warn without claiming the message was lost. Removal of
// the old message fails because its directory is read-only (unlinking
// body.bin needs write permission on it), while the defer_dir stays
// writable so the new message is created fine.
func TestEvictionFailureKeepsMessage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	s := newStore(t)
	old := plantMessage(t, s.Dir, "id-old", plantBase, len(plantBody))
	s.MaxSize = 1 // force eviction of the old message
	var drops []Metadata
	s.OnDrop = func(meta Metadata) { drops = append(drops, meta) }

	oldDir := filepath.Join(s.Dir, old.ID)
	if err := os.Chmod(oldDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(oldDir, 0o700); err != nil {
			t.Error(err)
		}
	})

	meta, err := s.Save([]string{"a@example.com"}, "s", plantBody, testContentType)
	if err == nil {
		t.Fatal("Save succeeded despite the failed eviction")
	}
	if meta.ID == "" {
		t.Fatal("meta.ID empty: the new message must be reported as stored")
	}
	if len(drops) != 0 {
		t.Errorf("drops = %+v, want none (removal failed before OnDrop)", drops)
	}
	// Both messages are still on disk.
	for _, id := range []string{old.ID, meta.ID} {
		if _, err := os.Stat(filepath.Join(s.Dir, id)); err != nil {
			t.Errorf("message %s missing: %v", id, err)
		}
	}
}
