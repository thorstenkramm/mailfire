//go:build windows

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goodsign/monday"
	"github.com/thorstenkramm/mailfire/internal/locale"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// A .bat job runs through cmd /c: its exit code passes through, and its
// CRLF output is normalized in the attachment and the body.
func TestRunWindowsBatchJob(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	job := filepath.Join(t.TempDir(), "backup.bat")
	if err := os.WriteFile(job, []byte("@echo off\r\necho copying files\r\necho done\r\necho disk full 1>&2\r\nexit /b 3\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runRun(t, m, "-c", cfg, "--to", "ops@example.com", "--", job)
	if code != 3 {
		t.Fatalf("exit = %d, want 3; stderr:\n%s", code, stderr)
	}
	req := m.LastRequest(t)
	if !strings.HasPrefix(req.Field("subject"), "🔴 backup.bat failed on ") {
		t.Errorf("subject = %q", req.Field("subject"))
	}
	text := req.Field("text")
	if !strings.Contains(text, "copying files\ndone\n") || strings.Contains(text, "\r") {
		t.Errorf("text part is not CRLF-normalized:\n%q", text)
	}
	if len(req.Files) != 2 || string(req.Files[0].Content) != "copying files\ndone\n" ||
		req.Files[0].Filename != "backup.bat.stdout.log" {
		t.Errorf("attachments = %+v", req.Files)
	}
	if !strings.Contains(text, job) {
		t.Errorf("text part lacks the unquoted command %s:\n%s", job, text)
	}
}

// Without --locale or [run].locale, the report dates follow the Windows
// user locale; LANG is ignored.
func TestRunWindowsUserLocale(t *testing.T) {
	l, ok := locale.FromEnvironment(os.Getenv)
	if !ok {
		t.Skip("the Windows user locale is not supported by monday; en_US applies")
	}
	t.Setenv("LANG", "xx_YY")
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	if code, _, stderr := runRun(t, m, append(runArgs(cfg), helperJob(t, "echo", "x")...)...); code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	text := m.LastRequest(t).Field("text")
	// The start date uses the user locale's month names: one of them
	// must appear in the "Started:" line.
	line := text[strings.Index(text, "Started:"):]
	line = line[:strings.Index(line, "\n")]
	for _, month := range monday.GetShortMonths(l) {
		if strings.Contains(line, month) {
			t.Logf("user locale %s: %s", l, line)
			return
		}
	}
	t.Errorf("%q uses no %s month name", line, l)
}

func TestRunWindowsPowerShellJob(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	job := filepath.Join(t.TempDir(), "report.ps1")
	if err := os.WriteFile(job, []byte("Write-Output \"rows: $($args[0])\"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runRun(t, m, "-c", cfg, "--to", "ops@example.com", "--", job, "42")
	if code != 0 {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	req := m.LastRequest(t)
	if !strings.HasPrefix(req.Field("subject"), "✅ report.ps1 succeeded on ") {
		t.Errorf("subject = %q", req.Field("subject"))
	}
	if len(req.Files) != 1 || string(req.Files[0].Content) != "rows: 42\n" {
		t.Errorf("attachments = %+v", req.Files)
	}
}
