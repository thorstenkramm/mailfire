//go:build unix || windows

package spool

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

const reraiseChildEnv = "MAILFIRE_SPOOL_RERAISE_CHILD"

// reraise ends the process, so it runs in a child: the test binary itself,
// restricted to this test. assertInterruptExit (per OS) checks the child
// ended exactly as an unhandled interrupt would have ended it.
func TestReraiseEndsProcessLikeAnInterrupt(t *testing.T) {
	if os.Getenv(reraiseChildEnv) == "1" {
		reraise(os.Interrupt)
		t.Fatal("reraise returned") // unreachable when it works
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReraiseEndsProcessLikeAnInterrupt$")
	cmd.Env = append(os.Environ(), reraiseChildEnv+"=1")
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child did not fail: %v", err)
	}
	assertInterruptExit(t, exitErr)
}
