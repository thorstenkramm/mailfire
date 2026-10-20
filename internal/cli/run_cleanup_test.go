package cli

import (
	"bytes"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// isolateTemp points the system temporary directory (TMPDIR on Unix,
// TMP and TEMP on Windows) at a fresh directory and returns it, so a test
// can see every capture file mailfire creates.
func isolateTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, dir)
	}
	if os.TempDir() != dir {
		t.Fatalf("os.TempDir() = %q, want %q", os.TempDir(), dir)
	}
	return dir
}

// captureFiles lists mailfire's capture files in dir.
func captureFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "mailfire-run-") {
			names = append(names, e.Name())
		}
	}
	return names
}

// interruptAfter sends SIGTERM on the returned channel after d, standing
// in for a signal delivered to mailfire.
func interruptAfter(d time.Duration) chan os.Signal {
	signals := make(chan os.Signal, 1)
	go func() {
		time.Sleep(d)
		signals <- syscall.SIGTERM
	}()
	return signals
}

// No capture file survives a run, however it ends: a successful run, a
// failed send, a --max-runtime stop, or an interruption. Each job spills
// both streams to temporary files first.
func TestRunLeavesNoTemporaryFiles(t *testing.T) {
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	for _, tc := range []struct {
		name   string
		args   []string
		sleep  string
		fail   bool // the mock rejects the report
		signal bool // interrupt mailfire after a second
		code   func(int) bool
	}{
		{name: "success", sleep: "0s", code: func(c int) bool { return c == ExitOK }},
		{name: "failed send", sleep: "0s", fail: true, code: func(c int) bool { return c == ExitSendFail }},
		{name: "max-runtime stop", args: []string{"--max-runtime", "1s"}, sleep: "30s", code: func(c int) bool { return c != ExitOK }},
		{name: "interruption", sleep: "30s", signal: true, code: func(c int) bool { return c != ExitOK }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := isolateTemp(t)
			m := testutil.NewMockMailgun(t)
			if tc.fail {
				m.SendResponse = testutil.BadRequest()
			}
			args := append([]string{"run"}, append(runArgs(cfg, tc.args...), helperJob(t, "spill-sleep", tc.sleep)...)...)
			opts := []Option{WithBaseURL(m.URL())}
			if tc.signal {
				opts = append(opts, WithSignals(interruptAfter(time.Second)))
			}
			var stdout, stderr bytes.Buffer
			code := Run(args, &stdout, &stderr, testInfo, opts...)
			if !tc.code(code) {
				t.Errorf("exit = %d; stderr:\n%s", code, stderr.String())
			}
			req := m.LastRequest(t)
			if len(req.Files) != 2 {
				t.Fatalf("attachments = %d, want both spilled streams", len(req.Files))
			}
			if !bytes.Contains(req.Files[1].Content, []byte("stderr line 1999")) {
				t.Error("the stderr attachment lacks the spilled output")
			}
			if left := captureFiles(t, tmp); len(left) != 0 {
				t.Errorf("capture files left behind: %v", left)
			}
		})
	}
}

// An interrupted job reports status "interrupted" and still sends, also
// with --on failure. On Windows, interruption terminates the process tree.
func TestRunInterrupted(t *testing.T) {
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	for _, on := range []string{"always", "failure"} {
		t.Run(on, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			args := append([]string{"run"}, append(runArgs(cfg, "--on", on), helperJob(t, "sleep", "30s")...)...)
			var stdout, stderr bytes.Buffer
			start := time.Now()
			code := Run(args, &stdout, &stderr, testInfo, WithBaseURL(m.URL()), WithSignals(interruptAfter(500*time.Millisecond)))
			if time.Since(start) > 15*time.Second {
				t.Error("the job was not stopped promptly")
			}
			wantCode := 128 + int(syscall.SIGTERM)
			if runtime.GOOS == "windows" {
				wantCode = 1 // TerminateJobObject's exit code
			}
			if code != wantCode {
				t.Errorf("exit = %d, want %d; stderr:\n%s", code, wantCode, stderr.String())
			}
			if len(m.Requests()) != 1 {
				t.Fatalf("requests = %d, want the report", len(m.Requests()))
			}
			req := m.LastRequest(t)
			if !strings.HasPrefix(req.Field("subject"), "⚠️ nightly interrupted") {
				t.Errorf("subject = %q, want an interrupted subject", req.Field("subject"))
			}
			if runtime.GOOS != "windows" && !strings.Contains(req.Field("text"), "killed by signal 15 (TERM)") {
				t.Errorf("text part lacks the signal detail:\n%s", req.Field("text"))
			}
		})
	}
}
