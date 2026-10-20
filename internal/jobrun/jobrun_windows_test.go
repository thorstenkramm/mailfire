//go:build windows

package jobrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeScript writes a job script into dir and returns its path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runScript(t *testing.T, spec *Spec) *Result {
	t.Helper()
	res, err := Run(spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() {
		if err := res.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return res
}

func TestRunBatchFile(t *testing.T) {
	dir := t.TempDir()
	for _, ext := range []string{".bat", ".cmd"} {
		path := writeScript(t, dir, "job"+ext, "@echo off\r\necho hello from %~nx0 %1\r\necho oops 1>&2\r\nexit /b 3\r\n")
		res := runScript(t, &Spec{Path: path, Args: []string{"arg1"}})
		if res.StartErr != nil || res.ExitCode != 3 {
			t.Errorf("%s: start error %v, exit %d; want exit 3", ext, res.StartErr, res.ExitCode)
		}
		if got := string(res.Stdout().Content()); got != "hello from job"+ext+" arg1\r\n" {
			t.Errorf("%s: stdout = %q", ext, got)
		}
		if got := string(res.Stderr().Content()); !strings.HasPrefix(got, "oops") {
			t.Errorf("%s: stderr = %q", ext, got)
		}
	}
}

func TestRunPowerShellScript(t *testing.T) {
	path := writeScript(t, t.TempDir(), "job.ps1", "Write-Output \"ps says $($args[0])\"\r\nexit 4\r\n")
	res := runScript(t, &Spec{Path: path, Args: []string{"hi"}})
	if res.StartErr != nil || res.ExitCode != 4 {
		t.Errorf("start error %v, exit %d; want exit 4", res.StartErr, res.ExitCode)
	}
	if got := string(res.Stdout().Content()); got != "ps says hi\r\n" {
		t.Errorf("stdout = %q", got)
	}
}

// A script found through PATH and PATHEXT ("backup" for backup.cmd) is
// run through cmd /c too.
func TestRunBatchFileViaPath(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "mailfire-test-backup.cmd", "@echo off\r\necho found via PATH\r\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	res := runScript(t, &Spec{Path: "mailfire-test-backup"})
	if res.StartErr != nil || res.ExitCode != 0 {
		t.Fatalf("start error %v, exit %d", res.StartErr, res.ExitCode)
	}
	if got := string(res.Stdout().Content()); got != "found via PATH\r\n" {
		t.Errorf("stdout = %q", got)
	}
}

// --max-runtime terminates the whole process tree: a grandchild holding
// the output pipe would otherwise keep the run open until it exits.
func TestRunTerminatesProcessTree(t *testing.T) {
	path := writeScript(t, t.TempDir(), "tree.bat", "@echo off\r\necho started\r\nping -n 30 127.0.0.1 >nul\r\n")
	start := time.Now()
	res := runScript(t, &Spec{Path: path, MaxRuntime: time.Second})
	if !res.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("run took %v: the grandchild (ping) was not terminated", elapsed)
	}
	if got := string(res.Stdout().Content()); !strings.HasPrefix(got, "started") {
		t.Errorf("stdout = %q", got)
	}
}
