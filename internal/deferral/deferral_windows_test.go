//go:build windows

package deferral

import (
	"os"
	"path/filepath"
	"testing"
)

// Counterpart to deferral_unix_test.go's TestSavePermissions: the
// owner-only permission bits (dirPerm/filePerm) are a no-op on Windows,
// where access is governed by ACLs, not POSIX modes. What must hold on
// Windows is that Save works with those constants and the stored message
// round-trips through List. The read-only-directory scenarios of the
// Unix tests have no Windows equivalent: os.Chmod there can only toggle
// the read-only attribute, which does not reproduce POSIX semantics (a
// read-only attribute on a directory does not make it unwritable), so
// those tests stay Unix-only.
func TestSavePermissionsNoOp(t *testing.T) {
	s := newStore(t)
	meta, err := s.Save([]string{"a@example.com"}, "s", []byte("body"), testContentType)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, name := range []string{MetaFile, BodyFile} {
		fi, err := os.Stat(filepath.Join(s.Dir, meta.ID, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if !fi.Mode().IsRegular() {
			t.Errorf("%s: not a regular file", name)
		}
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != meta.ID {
		t.Errorf("List = %+v, want exactly the saved message %s", list, meta.ID)
	}
}
