package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// readLog decodes every line of the logfile into a map.
func readLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading logfile: %v", err)
	}
	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not valid JSON: %q: %v", line, err)
		}
		entries = append(entries, m)
	}
	return entries
}

// loggingConfig writes a config with [logging] enabled for the given
// logfile path and level ("" = default information).
func loggingConfig(t *testing.T, logfile, level string) string {
	t.Helper()
	extra := "from = \"ops@example.com\"\n\n[logging]\nlogfile = " + strconv.Quote(logfile) + "\n"
	if level != "" {
		extra += "loglevel = \"" + level + "\"\n"
	}
	return writeConfig(t, extra)
}

func TestLoggingDisabledByDefault(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	// No logfile anywhere: nothing to assert beyond the send working,
	// which is covered above. The test documents the default.
}

func TestLoggingSuccessAtDefaultLevel(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	logfile := filepath.Join(t.TempDir(), "mailfire.log")
	cfg := loggingConfig(t, logfile, "")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "the subject")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	entries := readLog(t, logfile)
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want exactly 1 per send", len(entries))
	}
	e := entries[0]
	if e["message_id"] != "<abc123@mg.example.com>" {
		t.Errorf("message_id = %v", e["message_id"])
	}
	if e["subject"] != "the subject" {
		t.Errorf("subject = %v", e["subject"])
	}
	if e["status"] != float64(200) {
		t.Errorf("status = %v, want 200", e["status"])
	}
	to, ok := e["to"].([]any)
	if !ok || len(to) != 1 || to[0] != "a@example.com" {
		t.Errorf("to = %v, want [a@example.com]", e["to"])
	}
	if _, present := e["test"]; present {
		t.Errorf("test field present without --test: %v", e)
	}
}

func TestLoggingErrorLevelSkipsSuccess(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	logfile := filepath.Join(t.TempDir(), "mailfire.log")
	cfg := loggingConfig(t, logfile, "error")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	// A successful send produces no entry at the error level.
	if _, err := os.Stat(logfile); !os.IsNotExist(err) {
		entries := readLog(t, logfile)
		if len(entries) != 0 {
			t.Errorf("got %d entries, want none for a successful send at error level", len(entries))
		}
	}

	// A failing send is logged. The send defers (exit 4) into a pinned
	// defer_dir; the attempt stays a failure for logging (status 500).
	m.SendResponse = testutil.InternalError()
	deferDir := filepath.Join(t.TempDir(), "deferred")
	cfgFail := writeConfig(t, "from = \"ops@example.com\"\n\n[logging]\nlogfile = '"+logfile+"'\nloglevel = \"error\"\n\n[defer]\ndefer_dir = '"+deferDir+"'\n")
	code, _, _ = runMock(t, m, "body",
		"-c", cfgFail, "--to", "a@example.com", "--subject", "s")
	if code != ExitDeferred {
		t.Fatalf("exit = %d, want %d", code, ExitDeferred)
	}
	entries := readLog(t, logfile)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 for the failed send", len(entries))
	}
	e := entries[0]
	if e["status"] != float64(500) {
		t.Errorf("status = %v, want 500", e["status"])
	}
	// No message ID was returned; the field is present and empty.
	if v, present := e["message_id"]; !present || v != "" {
		t.Errorf("message_id = %v (present=%v), want present and empty", v, present)
	}
	if _, present := e["subject"]; present {
		t.Errorf("subject present at error level: %v", e)
	}
}

func TestLoggingDebugLevel(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	logfile := filepath.Join(t.TempDir(), "mailfire.log")
	cfg := loggingConfig(t, logfile, "debug")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	entries := readLog(t, logfile)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e["from"] != "ops@example.com" {
		t.Errorf("from = %v", e["from"])
	}
	if size, ok := e["size"].(float64); !ok || size <= 0 {
		t.Errorf("size = %v, want the encoded message size > 0", e["size"])
	}
	resp, ok := e["response"].(string)
	if !ok {
		t.Fatalf("response is not a string: %v", e["response"])
	}
	if !strings.Contains(resp, "Queued. Thank you.") || !strings.Contains(resp, "abc123") {
		t.Errorf("response = %q, want the API response body", resp)
	}
}

func TestLoggingTestSendMarked(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	logfile := filepath.Join(t.TempDir(), "mailfire.log")
	cfg := loggingConfig(t, logfile, "")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--test")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	entries := readLog(t, logfile)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0]["test"] != true {
		t.Errorf("test = %v, want true for a --test send", entries[0]["test"])
	}
	// Required fields stay intact on a test send.
	for _, k := range []string{"timestamp", "message_id", "to", "subject", "status"} {
		if _, present := entries[0][k]; !present {
			t.Errorf("field %q missing on a test send: %v", k, entries[0])
		}
	}
}

func TestLoggingUnwritableLogfileExits2(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	// A regular file where the log directory is expected fails on every
	// platform (permission bits are unreliable under root).
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := loggingConfig(t, filepath.Join(blocker, "mailfire.log"), "")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitConfig {
		t.Errorf("exit = %d, want %d; stderr:\n%s", code, ExitConfig, stderr)
	}
	if !strings.Contains(stderr, "logging:") {
		t.Errorf("stderr missing logging error:\n%s", stderr)
	}
	if len(m.Requests()) != 0 {
		t.Errorf("mock received %d requests, want 0 (fail before any send attempt)", len(m.Requests()))
	}
}

func TestLoggingLogfileTildeExpansion(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := loggingConfig(t, "~/mailfire.log", "")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	entries := readLog(t, filepath.Join(home, "mailfire.log"))
	if len(entries) != 1 {
		t.Errorf("got %d entries in ~/mailfire.log, want 1", len(entries))
	}
}

// The API key must never reach the logfile, even when a (malicious or
// broken) server echoes it in the response body — the client redacts it.
func TestLoggingNeverLogsAPIKey(t *testing.T) {
	const key = "test-key-not-real"

	t.Run("success body echoes the key", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		m.SendResponse = testutil.Response{
			Status: 200,
			Body:   `{"id":"<k1@mg.example.com>","message":"Queued; key=` + key + `"}`,
		}
		logfile := filepath.Join(t.TempDir(), "mailfire.log")
		cfg := loggingConfig(t, logfile, "debug")

		code, _, stderr := runMock(t, m, "body",
			"-c", cfg, "--to", "a@example.com", "--subject", "s")
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
		}
		data, err := os.ReadFile(logfile)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), key) {
			t.Errorf("logfile leaks the API key:\n%s", data)
		}
	})

	t.Run("error body echoes the key", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		m.SendResponse = testutil.ErrorJSON(400, "rejected; key="+key)
		logfile := filepath.Join(t.TempDir(), "mailfire.log")
		cfg := loggingConfig(t, logfile, "debug")

		code, _, _ := runMock(t, m, "body",
			"-c", cfg, "--to", "a@example.com", "--subject", "s")
		if code != ExitSendFail {
			t.Fatalf("exit = %d, want %d", code, ExitSendFail)
		}
		data, err := os.ReadFile(logfile)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), key) {
			t.Errorf("logfile leaks the API key:\n%s", data)
		}
	})
}
