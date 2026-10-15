package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersionStamped(t *testing.T) {
	var stdout, stderr bytes.Buffer
	info := BuildInfo{Version: "0.3.1", Commit: "a1b2c3d", BuildDate: "2026-10-15"}
	code := Run([]string{"version"}, &stdout, &stderr, info)
	if code != ExitOK {
		t.Errorf("exit = %d, want %d", code, ExitOK)
	}
	want := "mailfire 0.3.1 (commit a1b2c3d, built 2026-10-15)\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunVersionDevDefaults(t *testing.T) {
	// Unstamped development build: main.go's defaults must produce a
	// sane dev line without any stamping.
	var stdout, stderr bytes.Buffer
	info := BuildInfo{Version: "dev", Commit: "none", BuildDate: "unknown"}
	code := Run([]string{"version"}, &stdout, &stderr, info)
	if code != ExitOK {
		t.Errorf("exit = %d, want %d", code, ExitOK)
	}
	want := "mailfire dev (commit none, built unknown)\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunVersionNeedsNoConfig(t *testing.T) {
	// version must work standalone: no config file, no network.
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	code := Run([]string{"version"}, &stdout, &stderr, testInfo)
	if code != ExitOK {
		t.Errorf("exit = %d, want %d", code, ExitOK)
	}
	if !strings.HasPrefix(stdout.String(), "mailfire 1.2.3 (commit abc1234, built 2026-10-15)") {
		t.Errorf("unexpected stdout: %q", stdout.String())
	}
}

func TestRunVersionHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"version", "--help"}, &stdout, &stderr, testInfo)
	if code != ExitOK {
		t.Errorf("exit = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(stdout.String(), "mailfire version") {
		t.Errorf("stdout does not contain usage text:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}
