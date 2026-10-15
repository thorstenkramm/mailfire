package logging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// openLogger opens a logger on a fresh file in a temp dir.
func openLogger(t *testing.T, level string, maxSize int64) (*Logger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailfire.log")
	l, err := Open(path, level, maxSize)
	if err != nil {
		t.Fatalf("Open(%q, %q, %d): %v", path, level, maxSize, err)
	}
	t.Cleanup(func() { l.Close() })
	return l, path
}

// readEntries reads the logfile and decodes every line into a map.
func readEntries(t *testing.T, path string) []map[string]any {
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

// keys returns the sorted keys of a decoded entry.
func keys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func equalKeys(got []string, want ...string) bool {
	sort.Strings(want)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

var testTime = time.Date(2026, 10, 15, 12, 30, 0, 0, time.UTC)

func successAttempt() Attempt {
	return Attempt{
		Time:      testTime,
		MessageID: "<abc123@mg.example.com>",
		From:      "Ops Bot <ops@example.com>",
		To:        []string{"a@example.com", "b@example.com"},
		Subject:   "Backup report",
		Size:      1234,
		Status:    200,
		Response:  `{"id":"<abc123@mg.example.com>","message":"Queued. Thank you."}`,
		Success:   true,
	}
}

func failureAttempt() Attempt {
	return Attempt{
		Time:     testTime,
		From:     "Ops Bot <ops@example.com>",
		To:       []string{"a@example.com"},
		Subject:  "Backup report",
		Size:     567,
		Status:   500,
		Response: "Internal error",
	}
}

func TestFieldSetsPerLevel(t *testing.T) {
	for _, tc := range []struct {
		level string
		want  []string // exact JSON keys expected in every line
	}{
		{LevelError, []string{"timestamp", "message_id", "to", "status"}},
		{LevelInformation, []string{"timestamp", "message_id", "to", "subject", "status"}},
		{LevelDebug, []string{"timestamp", "message_id", "from", "to", "subject", "size", "status", "response"}},
	} {
		t.Run(tc.level, func(t *testing.T) {
			l, path := openLogger(t, tc.level, 1<<20)
			if err := l.Log(successAttempt()); err != nil {
				t.Fatalf("Log(success): %v", err)
			}
			if err := l.Log(failureAttempt()); err != nil {
				t.Fatalf("Log(failure): %v", err)
			}
			entries := readEntries(t, path)
			for _, e := range entries {
				if !equalKeys(keys(e), tc.want...) {
					t.Errorf("keys = %v, want exactly %v", keys(e), tc.want)
				}
			}
		})
	}
}

func TestFieldValues(t *testing.T) {
	l, path := openLogger(t, LevelDebug, 1<<20)
	if err := l.Log(successAttempt()); err != nil {
		t.Fatalf("Log: %v", err)
	}
	entries := readEntries(t, path)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]

	if e["timestamp"] != "2026-10-15T12:30:00Z" {
		t.Errorf("timestamp = %v, want RFC3339 2026-10-15T12:30:00Z", e["timestamp"])
	}
	// The timestamp must parse as RFC3339.
	if _, err := time.Parse(time.RFC3339, e["timestamp"].(string)); err != nil {
		t.Errorf("timestamp %q does not parse as RFC3339: %v", e["timestamp"], err)
	}
	if e["message_id"] != "<abc123@mg.example.com>" {
		t.Errorf("message_id = %v", e["message_id"])
	}
	if e["from"] != "Ops Bot <ops@example.com>" {
		t.Errorf("from = %v", e["from"])
	}
	to, ok := e["to"].([]any)
	if !ok || len(to) != 2 || to[0] != "a@example.com" || to[1] != "b@example.com" {
		t.Errorf("to = %v, want [a@example.com b@example.com]", e["to"])
	}
	if e["subject"] != "Backup report" {
		t.Errorf("subject = %v", e["subject"])
	}
	if e["size"] != float64(1234) {
		t.Errorf("size = %v, want 1234", e["size"])
	}
	if e["status"] != float64(200) {
		t.Errorf("status = %v, want 200", e["status"])
	}
	if !strings.Contains(e["response"].(string), "Queued") {
		t.Errorf("response = %v, want the API response body", e["response"])
	}
}

func TestErrorLevelLogsFailuresOnly(t *testing.T) {
	l, path := openLogger(t, LevelError, 1<<20)
	for i := 0; i < 3; i++ {
		if err := l.Log(successAttempt()); err != nil {
			t.Fatalf("Log(success): %v", err)
		}
	}
	if err := l.Log(failureAttempt()); err != nil {
		t.Fatalf("Log(failure): %v", err)
	}
	entries := readEntries(t, path)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (failures only)", len(entries))
	}
	if entries[0]["status"] != float64(500) {
		t.Errorf("status = %v, want 500", entries[0]["status"])
	}
}

func TestInformationLevelLogsSuccessAndFailure(t *testing.T) {
	l, path := openLogger(t, LevelInformation, 1<<20)
	if err := l.Log(successAttempt()); err != nil {
		t.Fatalf("Log(success): %v", err)
	}
	if err := l.Log(failureAttempt()); err != nil {
		t.Fatalf("Log(failure): %v", err)
	}
	entries := readEntries(t, path)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (success and failure)", len(entries))
	}
}

// Failures get no message ID from Mailgun; the field stays present and
// empty (documented package behavior).
func TestMessageIDEmptyOnFailure(t *testing.T) {
	l, path := openLogger(t, LevelError, 1<<20)
	if err := l.Log(failureAttempt()); err != nil {
		t.Fatalf("Log: %v", err)
	}
	entries := readEntries(t, path)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	v, present := entries[0]["message_id"]
	if !present {
		t.Fatal("message_id field missing on a failure entry")
	}
	if v != "" {
		t.Errorf("message_id = %v, want empty string", v)
	}
}

func TestTestField(t *testing.T) {
	l, path := openLogger(t, LevelInformation, 1<<20)

	testSend := successAttempt()
	testSend.Test = true
	if err := l.Log(testSend); err != nil {
		t.Fatalf("Log(test): %v", err)
	}
	if err := l.Log(successAttempt()); err != nil {
		t.Fatalf("Log(regular): %v", err)
	}
	entries := readEntries(t, path)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0]["test"] != true {
		t.Errorf("test = %v, want true for a --test send", entries[0]["test"])
	}
	if _, present := entries[1]["test"]; present {
		t.Errorf("test field present on a regular send: %v", entries[1])
	}
	// The required fields stay intact on a test send.
	if !equalKeys(keys(entries[0]), "timestamp", "message_id", "to", "subject", "status", "test") {
		t.Errorf("test send keys = %v, want the information fields plus test", keys(entries[0]))
	}
}

func TestTruncationDropsOldest(t *testing.T) {
	// One information line with the padded subject is ~290 bytes; allow
	// roughly three.
	const maxSize = 900
	l, path := openLogger(t, LevelInformation, maxSize)

	const total = 10
	for i := 0; i < total; i++ {
		a := successAttempt()
		a.Subject = strings.Repeat("x", 100) + " message " + string(rune('0'+i))
		if err := l.Log(a); err != nil {
			t.Fatalf("Log(%d): %v", i, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) > maxSize {
		t.Errorf("logfile size = %d, want <= %d after truncation", len(data), maxSize)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("got %d lines after truncation, want at least 2", len(lines))
	}
	// Every surviving line is a complete, valid JSON object (no partial
	// line at the cut).
	for _, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("corrupt line after truncation: %q: %v", line, err)
		}
	}
	// The newest entry survives; the oldest were dropped.
	last := lines[len(lines)-1]
	if !strings.Contains(last, "message 9") {
		t.Errorf("newest entry missing after truncation:\n%s", last)
	}
	if strings.Contains(string(data), "message 0") || strings.Contains(string(data), "message 5") {
		t.Errorf("old entries survive truncation:\n%s", data)
	}
}

func TestTruncationKeepsOversizedNewestLine(t *testing.T) {
	// A single line larger than maxSize is never dropped: the newest
	// entry wins over the size bound.
	l, path := openLogger(t, LevelDebug, 100)
	a := successAttempt()
	a.Subject = strings.Repeat("y", 500)
	if err := l.Log(a); err != nil {
		t.Fatalf("Log: %v", err)
	}
	entries := readEntries(t, path)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want the single oversized newest entry", len(entries))
	}
}

func TestTruncationOnExistingFile(t *testing.T) {
	// A pre-existing logfile that already exceeds maxSize is truncated on
	// the first append.
	l, path := openLogger(t, LevelInformation, 300)
	filler := strings.Repeat(`{"old":true}`+"\n", 50)
	if err := os.WriteFile(path, []byte(filler), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Log(successAttempt()); err != nil {
		t.Fatalf("Log: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) > 300 {
		t.Errorf("size = %d, want <= 300", len(data))
	}
	if !strings.Contains(string(data), "abc123") {
		t.Errorf("newest entry missing:\n%s", data)
	}
	// The truncation keeps only the newest whole lines that fit, so most
	// of the filler must be gone.
	if n := strings.Count(string(data), `"old":true`); n >= 50 {
		t.Errorf("%d of 50 old entries survive truncation:\n%s", n, data)
	}
}

func TestOpenCreatesParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "deep", "mailfire.log")
	l, err := Open(path, LevelInformation, 1<<20)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	if err := l.Log(successAttempt()); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if entries := readEntries(t, path); len(entries) != 1 {
		t.Errorf("got %d entries, want 1", len(entries))
	}
}

func TestOpenUnwritablePath(t *testing.T) {
	// A regular file where a directory is expected fails on every
	// platform, without depending on permission bits (root ignores them).
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(blocker, "mailfire.log"), LevelInformation, 1<<20); err == nil {
		t.Fatal("Open succeeded on a path whose parent is a regular file")
	} else if !strings.Contains(err.Error(), "logging:") {
		t.Errorf("error = %q, want a logging:-prefixed message", err)
	}
}

func TestOpenInvalidLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailfire.log")
	if _, err := Open(path, "chatty", 1<<20); err == nil {
		t.Fatal("Open succeeded with an invalid level")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("logfile created despite invalid level: %v", err)
	}
}

func TestOpenOnDirectory(t *testing.T) {
	// An existing directory cannot be opened for writing.
	if _, err := Open(t.TempDir(), LevelInformation, 1<<20); err == nil {
		t.Fatal("Open succeeded on a directory")
	} else if !strings.Contains(err.Error(), "cannot open logfile") {
		t.Errorf("error = %q, want a cannot-open message", err)
	}
}

func TestLogWriteError(t *testing.T) {
	// A write to a closed logfile fails; the error is returned to the
	// caller (the CLI reports it on stderr without failing the send).
	l, _ := openLogger(t, LevelInformation, 1<<20)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Log(successAttempt()); err == nil {
		t.Fatal("Log on a closed logger succeeded")
	} else if !strings.Contains(err.Error(), "writing") {
		t.Errorf("error = %q, want a write error", err)
	}
}
