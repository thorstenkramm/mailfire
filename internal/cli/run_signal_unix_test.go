//go:build !windows

package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// A real SIGTERM delivered to the mailfire process (not an injected test
// channel) is forwarded to the job's process group; mailfire waits for
// the job, still sends the report, and exits with the job's code.
func TestRunRealSIGTERM(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	marker := t.TempDir() + "/job-started"

	// The test binary acts as mailfire (RUN_AS_MAILFIRE); its job is the
	// test binary again, in RUN_HELPER mode "touch-sleep".
	mailfireArgs := append([]string{"run"}, append(runArgs(cfg), helperJob(t, "touch-sleep", marker, "30s")...)...)
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestRunHelperProcess", "--"}, mailfireArgs...)...)
	cmd.Env = append(os.Environ(), "RUN_AS_MAILFIRE=1", "RUN_MOCK_URL="+m.URL())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Wait until the job runs, then signal mailfire only (its own pid).
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("the job did not start; stderr:\n%s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("mailfire did not finish after SIGTERM")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 128+int(syscall.SIGTERM) {
		t.Errorf("mailfire: %v, want exit %d; stderr:\n%s", err, 128+int(syscall.SIGTERM), stderr.String())
	}
	req := m.LastRequest(t)
	if !strings.HasPrefix(req.Field("subject"), "⚠️ nightly interrupted") {
		t.Errorf("subject = %q, want an interrupted subject", req.Field("subject"))
	}
	if !strings.Contains(req.Field("text"), "killed by signal 15 (TERM)") {
		t.Errorf("text part lacks the signal detail:\n%s", req.Field("text"))
	}
	if !strings.HasPrefix(stdout.String(), "<abc123@") {
		t.Errorf("stdout = %q, want the report's message ID", stdout.String())
	}
}
