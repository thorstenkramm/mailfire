package filelock

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestTryLockBusyUntilUnlocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := Lock(path)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if _, ok, err := TryLock(path); err != nil || ok {
		t.Fatalf("TryLock while held: ok=%v err=%v, want busy", ok, err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	unlock2, ok, err := TryLock(path)
	if err != nil || !ok {
		t.Fatalf("TryLock after unlock: ok=%v err=%v", ok, err)
	}
	if err := unlock2(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil {
		t.Error(err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("lock file mode = %v, want 0600", fi.Mode().Perm())
	}
}

// Lock waits for the holder to release the lock.
func TestLockWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func() error)
	go func() {
		u, err := Lock(path)
		if err != nil {
			t.Error(err)
			close(acquired)
			return
		}
		acquired <- u
	}()
	select {
	case <-acquired:
		t.Fatal("second Lock acquired while the first was held")
	case <-time.After(100 * time.Millisecond):
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-acquired:
		if u != nil {
			if err := u(); err != nil {
				t.Fatal(err)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second Lock never acquired after unlock")
	}
}

func TestLockMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "x.lock")
	if _, err := Lock(path); err == nil {
		t.Error("Lock in a missing directory succeeded")
	}
	if _, _, err := TryLock(path); err == nil {
		t.Error("TryLock in a missing directory succeeded")
	}
}
