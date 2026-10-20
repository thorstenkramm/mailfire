//go:build unix

package spool

import (
	"os/exec"
	"syscall"
	"testing"
)

// assertInterruptExit: killed by SIGINT itself, as without the handler.
func assertInterruptExit(t *testing.T, err *exec.ExitError) {
	t.Helper()
	ws, ok := err.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Fatalf("child exit = %v, want killed by SIGINT", err)
	}
}
