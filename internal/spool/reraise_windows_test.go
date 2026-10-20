package spool

import (
	"os/exec"
	"testing"
)

// assertInterruptExit: STATUS_CONTROL_C_EXIT, as Windows' default Ctrl+C
// handling would have ended the process.
func assertInterruptExit(t *testing.T, err *exec.ExitError) {
	t.Helper()
	if got := uint32(err.ExitCode()); got != exitControlC {
		t.Fatalf("child exit status = %#x, want %#x (STATUS_CONTROL_C_EXIT)", got, exitControlC)
	}
}
