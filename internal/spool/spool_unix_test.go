//go:build unix

package spool

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestCleanupOnSignalRemovesLiveSpools(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	got := make(chan os.Signal, 1)
	afterCleanup = func(sig os.Signal) { got <- sig }
	t.Cleanup(func() { afterCleanup = reraise })

	s, err := Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(); err != nil {
		t.Fatal(err)
	}
	stop := CleanupOnSignal()
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-got:
		if sig != syscall.SIGTERM {
			t.Errorf("signal = %v", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not run")
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatalf("spool survived the signal: %v", err)
	}
}
