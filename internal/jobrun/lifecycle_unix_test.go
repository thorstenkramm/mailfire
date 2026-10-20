//go:build !windows

package jobrun

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func watchDescendant(t *testing.T, pid int) func() {
	t.Helper()
	t.Cleanup(func() { killDescendant(t, pid) })
	return func() {
		t.Helper()
		for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
			if syscall.Kill(pid, 0) == syscall.ESRCH {
				return
			}
			// Container init processes do not always reap orphans promptly.
			if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil && strings.Contains(string(stat), ") Z ") {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("descendant %d survived termination", pid)
	}
}

func killDescendant(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Error(err)
	}
}
