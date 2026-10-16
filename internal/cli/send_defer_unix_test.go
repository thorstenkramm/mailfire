//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// When the max_size cleanup cannot drop the oldest message (its directory
// is read-only), the send still counts as deferred — the new message is
// safely stored — but the cleanup failure is reported on stderr.
func TestDeferEvictionFailureStillExits4(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.InternalError()
	cfg, deferDir := deferConfig(t, "max_size = \"100\"\n")

	seeder := &deferral.Store{Dir: deferDir}
	old, err := seeder.Save([]string{"old@example.com"}, "old subject",
		make([]byte, 1000), "multipart/form-data; boundary=seed")
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	oldDir := filepath.Join(deferDir, old.ID)
	if err := os.Chmod(oldDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(oldDir, 0o700); err != nil {
			t.Error(err)
		}
	})

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitDeferred {
		t.Errorf("exit = %d, want %d (the message was stored; only cleanup failed); stderr:\n%s",
			code, ExitDeferred, stderr)
	}
	if !strings.Contains(stderr, "mailfire: defer:") || !strings.Contains(stderr, "dropping") {
		t.Errorf("stderr missing the cleanup failure:\n%s", stderr)
	}
	// Both messages survive: the new one is stored, the old one could
	// not be removed.
	if ids := deferredIDs(t, deferDir); len(ids) != 2 {
		t.Errorf("defer_dir = %v, want both messages", ids)
	}
}
