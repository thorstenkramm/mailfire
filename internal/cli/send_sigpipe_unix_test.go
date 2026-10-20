//go:build unix

package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// Not MAILFIRE_*: TestMain clears those variables before the tests run.
const sigpipeChildEnv = "CLI_TEST_SIGPIPE_CHILD"

// A deferrable failure prints its error before the spool is adopted. With
// stderr a pipe nobody reads any more (a closed log pipeline), that write
// must not end mailfire with SIGPIPE: the message would be lost and the
// spool left behind. The send runs in a child whose fd 2 is such a pipe.
func TestDeferralSurvivesClosedStderr(t *testing.T) {
	if os.Getenv(sigpipeChildEnv) == "1" {
		sigpipeChild(t)
		return
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil { // nobody reads: writes to w fail with EPIPE
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestDeferralSurvivesClosedStderr$")
	cmd.Env = append(os.Environ(), sigpipeChildEnv+"=1")
	cmd.Stdout, cmd.Stderr = &out, w
	runErr := cmd.Run()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		t.Fatalf("child killed by %v; output:\n%s", ws.Signal(), out.String())
	}
	if !strings.Contains(out.String(), "RESULT exit=4 spools=0 deferred=1") {
		t.Fatalf("child: %v; want the message deferred and no spool left; output:\n%s", runErr, out.String())
	}
}

// sigpipeChild sends through a mock answering 500 (deferrable) with
// deferral enabled, then reports the outcome on stdout.
func sigpipeChild(t *testing.T) {
	tmp := isolateTemp(t)
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.InternalError()
	cfg, deferDir := deferConfig(t, "")
	code := Run([]string{"-c", cfg, "--to", "a@example.com", "--subject", "s"}, os.Stdout, os.Stderr, testInfo,
		WithStdin(strings.NewReader("body")), WithBaseURL(m.URL()))
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	spools := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "mailfire-message-") {
			spools++
		}
	}
	deferred := 0
	if _, err := os.Stat(deferDir); err == nil {
		deferred = len(deferredIDs(t, deferDir))
	}
	fmt.Printf("RESULT exit=%d spools=%d deferred=%d\n", code, spools, deferred)
}
