package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// resendLogConfig writes a config with a defer_dir and [logging] enabled
// at the given level ("" = default information).
func resendLogConfig(t *testing.T, logfile, level string) (cfgPath, deferDir string) {
	t.Helper()
	extra := "\n[logging]\nlogfile = '" + logfile + "'\n"
	if level != "" {
		extra += "loglevel = \"" + level + "\"\n"
	}
	return deferConfig(t, extra)
}

// Every resend attempt is a sending attempt and is logged (FR-3) with
// the send field set; skipped (cooldown) messages are not attempts.
func TestResendLogsAttempts(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	logfile := filepath.Join(t.TempDir(), "mailfire.log")
	cfg, deferDir := resendLogConfig(t, logfile, "")

	sent := seedDeferred(t, deferDir, "a@example.com", "aged", time.Hour)
	seedDeferred(t, deferDir, "b@example.com", "fresh", 0)

	code, _, stderr := runMock(t, m, "", "resend", "-c", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	entries := readLog(t, logfile)
	if len(entries) != 1 {
		t.Fatalf("log has %d entries, want 1 (skipped messages are not logged): %v", len(entries), entries)
	}
	e := entries[0]
	wantKeys := []string{"timestamp", "message_id", "to", "subject", "status"}
	if len(e) != len(wantKeys) {
		t.Errorf("entry keys = %v, want exactly %v", e, wantKeys)
	}
	for _, k := range wantKeys {
		if _, ok := e[k]; !ok {
			t.Errorf("entry missing %q: %v", k, e)
		}
	}
	if e["message_id"] != "<abc123@mg.example.com>" || e["subject"] != sent.Subject || e["status"] != float64(200) {
		t.Errorf("entry = %v, want the resent message's outcome", e)
	}
	if to, _ := e["to"].([]any); len(to) != 1 || to[0] != "a@example.com" {
		t.Errorf("to = %v, want [a@example.com]", e["to"])
	}
}

// At debug level a resend logs from (read back from the stored body),
// size (the stored body's size), and the response — and never "test".
func TestResendLogsDebugFields(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	logfile := filepath.Join(t.TempDir(), "mailfire.log")
	cfg, deferDir := resendLogConfig(t, logfile, "debug")
	meta := seedDeferred(t, deferDir, "a@example.com", "dbg", time.Hour)

	if code, _, stderr := runMock(t, m, "", "resend", "-c", cfg); code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	entries := readLog(t, logfile)
	if len(entries) != 1 {
		t.Fatalf("log has %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e["from"] != "ops@example.com" {
		t.Errorf("from = %v, want ops@example.com", e["from"])
	}
	if e["size"] != float64(meta.Size) {
		t.Errorf("size = %v, want %d", e["size"], meta.Size)
	}
	if r, _ := e["response"].(string); !strings.Contains(r, "Queued") {
		t.Errorf("response = %v, want the Mailgun response body", e["response"])
	}
	if _, ok := e["test"]; ok {
		t.Errorf("entry has a test field; resends are never test mode: %v", e)
	}
}

// Failures are logged at the error level with their status code;
// successes are not. The API key never reaches the logfile.
func TestResendLogsFailuresAtErrorLevel(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.Unauthorized()
	logfile := filepath.Join(t.TempDir(), "mailfire.log")
	cfg, deferDir := resendLogConfig(t, logfile, "error")
	seedDeferred(t, deferDir, "a@example.com", "fails", time.Hour)

	if code, _, _ := runMock(t, m, "", "resend", "-c", cfg); code != ExitAuth {
		t.Fatalf("exit = %d, want %d", code, ExitAuth)
	}
	entries := readLog(t, logfile)
	if len(entries) != 1 {
		t.Fatalf("log has %d entries, want 1", len(entries))
	}
	if e := entries[0]; e["status"] != float64(401) || e["message_id"] != "" {
		t.Errorf("entry = %v, want status 401 and empty message_id", e)
	}
	data, _ := os.ReadFile(logfile)
	if strings.Contains(string(data), "test-key-not-real") {
		t.Errorf("logfile leaks the API key:\n%s", data)
	}

	// A success at the error level adds nothing.
	m.SendResponse = testutil.Queued("<ok@mg.example.com>")
	if code, _, _ := runMock(t, m, "", "resend", "-c", cfg); code != ExitOK {
		t.Fatalf("second run: exit = %d, want %d", code, ExitOK)
	}
	if n := len(readLog(t, logfile)); n != 1 {
		t.Errorf("log has %d entries after a success at error level, want 1", n)
	}
}

// An unusable logfile is a configuration error, reported before any
// resend attempt: exit 2, nothing sent, nothing removed.
func TestResendUnwritableLogfileExits2(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, deferDir := resendLogConfig(t, filepath.Join(blocker, "mailfire.log"), "")
	meta := seedDeferred(t, deferDir, "a@example.com", "kept", time.Hour)

	code, _, stderr := runMock(t, m, "", "resend", "-c", cfg)
	if code != ExitConfig {
		t.Errorf("exit = %d, want %d; stderr:\n%s", code, ExitConfig, stderr)
	}
	if n := len(m.Requests()); n != 0 {
		t.Errorf("mock received %d requests, want 0", n)
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 1 || ids[0] != meta.ID {
		t.Errorf("defer_dir = %v, want the message kept", ids)
	}
}
