package logging

import (
	"encoding/json"
	"errors"
	"fmt"
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
	t.Cleanup(func() {
		// Write-error tests deliberately close the underlying file first.
		if err := l.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})
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
	stamp, ok := e["timestamp"].(string)
	if !ok {
		t.Fatalf("timestamp is not a string: %v", e["timestamp"])
	}
	if _, err := time.Parse(time.RFC3339, stamp); err != nil {
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
	if response, ok := e["response"].(string); !ok || !strings.Contains(response, "Queued") {
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

func TestTruncationRejectsChangedPath(t *testing.T) {
	for _, missing := range []bool{false, true} {
		l, original := openLogger(t, LevelInformation, 1)
		other := filepath.Join(t.TempDir(), "other.log")
		const content = "must not be truncated\n"
		if !missing {
			if err := os.WriteFile(other, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// Simulate the pathname no longer referring to the open logfile,
		// without relying on OS-specific rename-sharing permissions.
		l.path = other
		if err := l.Log(successAttempt()); err == nil {
			t.Fatal("truncation accepted a changed logfile path")
		}
		if entries := readEntries(t, original); len(entries) != 1 {
			t.Fatalf("original log lost its appended entry: %v", entries)
		}
		data, err := os.ReadFile(other)
		if missing {
			if !os.IsNotExist(err) {
				t.Fatalf("missing path was created: %v", err)
			}
		} else if err != nil || string(data) != content {
			t.Fatalf("unrelated file changed: %q, %v", data, err)
		}
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
	defer func() {
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	}()
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

// Subjects and response bodies may contain quotes, newlines, control
// characters, line separators, non-ASCII and invalid UTF-8; every entry
// must remain exactly one valid JSON line.
func TestJSONEscapingKeepsOneLinePerEntry(t *testing.T) {
	l, path := openLogger(t, LevelDebug, 1<<20)
	tricky := []string{
		`he said "hi" \ back`,
		"multi\nline\r\nsubject\tTab",
		"pretty JSON body {\n  \"id\": \"<x@y>\"\n}\n",
		"unicode: Grüße 日本語 🚀 \u2028 \u2029 \x00 \x1b[0m",
		"invalid utf8: \xff\xfe",
	}
	for _, s := range tricky {
		a := successAttempt()
		a.Subject = s
		a.Response = s
		if err := l.Log(a); err != nil {
			t.Fatalf("Log(%q): %v", s, err)
		}
	}
	entries := readEntries(t, path) // fails on any invalid line
	if len(entries) != len(tricky) {
		t.Fatalf("got %d lines, want %d (an entry spans lines)", len(entries), len(tricky))
	}
	for i, s := range tricky[:4] {
		if entries[i]["subject"] != s || entries[i]["response"] != s {
			t.Errorf("entry %d does not round-trip: subject=%q response=%q, want %q",
				i, entries[i]["subject"], entries[i]["response"], s)
		}
	}
}

// Message IDs are written verbatim ("<id@domain>"), not HTML-escaped,
// so they can be grepped for with the ID printed on stdout.
func TestMessageIDNotHTMLEscaped(t *testing.T) {
	l, path := openLogger(t, LevelInformation, 1<<20)
	if err := l.Log(successAttempt()); err != nil {
		t.Fatalf("Log: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"<abc123@mg.example.com>"`) {
		t.Errorf("message id not written verbatim:\n%s", data)
	}
}

// A pre-existing logfile ending in a partial line (crash mid-write) must
// not corrupt the next entry.
func TestAppendAfterPartialLine(t *testing.T) {
	l, path := openLogger(t, LevelInformation, 1<<20)
	if err := os.WriteFile(path, []byte(`{"old":true}`+"\n"+`{"partial":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Log(successAttempt()); err != nil {
		t.Fatalf("Log: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), data)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &m); err != nil {
		t.Errorf("new entry corrupted by the partial line: %q: %v", lines[2], err)
	}
}

func TestTruncationExactlyAtMaxSize(t *testing.T) {
	// Measure one line, then set maxSize to exactly two lines: the file
	// at exactly maxSize is not truncated; one more line triggers it.
	probe, probePath := openLogger(t, LevelInformation, 1<<20)
	if err := probe.Log(successAttempt()); err != nil {
		t.Fatal(err)
	}
	one, err := os.ReadFile(probePath)
	if err != nil {
		t.Fatal(err)
	}
	maxSize := int64(2 * len(one))

	l, path := openLogger(t, LevelInformation, maxSize)
	for i := 0; i < 2; i++ {
		if err := l.Log(successAttempt()); err != nil {
			t.Fatal(err)
		}
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi.Size() != maxSize {
		t.Fatalf("size = %d, want exactly %d (no truncation at the limit)", fi.Size(), maxSize)
	}
	if err := l.Log(successAttempt()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi.Size() != maxSize {
		t.Errorf("size = %d, want %d (oldest of three equal lines dropped)", fi.Size(), maxSize)
	}
}

func TestTruncationManyAppends(t *testing.T) {
	// Variable-length lines over many appends: after every append the
	// file is within maxSize, consists of whole valid lines, and ends
	// with the newest entry, preceded by the next-newest in order.
	const maxSize = 700
	l, path := openLogger(t, LevelInformation, maxSize)
	for i := 0; i < 300; i++ {
		a := successAttempt()
		a.Subject = fmt.Sprintf("n=%d %s", i, strings.Repeat("z", (i*37)%150))
		if err := l.Log(a); err != nil {
			t.Fatalf("Log(%d): %v", i, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > maxSize {
			t.Fatalf("append %d: size %d > %d", i, len(data), maxSize)
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			t.Fatalf("append %d: file does not end with a newline", i)
		}
		entries := readEntries(t, path)
		for j, e := range entries {
			want := i - (len(entries) - 1 - j)
			if subject, ok := e["subject"].(string); !ok || !strings.HasPrefix(subject, fmt.Sprintf("n=%d ", want)) {
				t.Fatalf("append %d: entry %d subject %q, want n=%d", i, j, e["subject"], want)
			}
		}
	}
}

// LogDrop appends a "defer_drop" entry (PRD FR-4) at every level,
// including error: a discarded deferred message is a failure-class event.
func TestLogDrop(t *testing.T) {
	d := Drop{
		Time:    testTime,
		ID:      "20261015T123045.000000000-0123456789abcdef",
		To:      []string{"a@example.com"},
		Subject: "dropped subject",
		Size:    4711,
	}
	for _, level := range []string{LevelError, LevelInformation, LevelDebug} {
		t.Run(level, func(t *testing.T) {
			l, path := openLogger(t, level, 1<<20)
			if err := l.LogDrop(d); err != nil {
				t.Fatalf("LogDrop: %v", err)
			}
			entries := readEntries(t, path)
			if len(entries) != 1 {
				t.Fatalf("got %d entries, want 1", len(entries))
			}
			e := entries[0]
			if !equalKeys(keys(e), "timestamp", "event", "message_id", "to", "subject", "size") {
				t.Errorf("keys = %v, want the drop field set", keys(e))
			}
			if e["event"] != "defer_drop" {
				t.Errorf("event = %v, want defer_drop", e["event"])
			}
			if e["message_id"] != d.ID {
				t.Errorf("message_id = %v, want %s", e["message_id"], d.ID)
			}
			if e["subject"] != d.Subject || e["size"] != float64(d.Size) {
				t.Errorf("subject/size = %v/%v", e["subject"], e["size"])
			}
			if e["timestamp"] != "2026-10-15T12:30:00Z" {
				t.Errorf("timestamp = %v, want RFC3339", e["timestamp"])
			}
		})
	}
}

// A drop entry obeys the same append rules as attempts: it lands intact
// after a partial line left by a crash mid-write.
func TestLogDropAfterPartialLine(t *testing.T) {
	l, path := openLogger(t, LevelInformation, 1<<20)
	if err := os.WriteFile(path, []byte(`{"old":true}`+"\n"+`{"partial":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.LogDrop(Drop{Time: testTime, ID: "x", To: []string{"a@example.com"}}); err != nil {
		t.Fatalf("LogDrop: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), data)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &m); err != nil {
		t.Fatalf("drop entry corrupted by the partial line: %q: %v", lines[2], err)
	}
	if m["event"] != "defer_drop" {
		t.Errorf("last entry = %v, want the drop", m)
	}
}

func TestLogDropWriteError(t *testing.T) {
	l, _ := openLogger(t, LevelInformation, 1<<20)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.LogDrop(Drop{Time: testTime, ID: "x"}); err == nil {
		t.Fatal("LogDrop on a closed logger succeeded")
	} else if !strings.Contains(err.Error(), "writing") {
		t.Errorf("error = %q, want a write error", err)
	}
}

func TestKeepNewest(t *testing.T) {
	for _, tc := range []struct {
		data    string
		maxSize int64
		want    string
	}{
		{"", 10, ""},
		{"aa\nbb\ncc\n", 9, "aa\nbb\ncc\n"},       // exactly at limit
		{"aa\nbb\ncc\n", 8, "bb\ncc\n"},           // one byte over
		{"aa\nbb\ncc\n", 6, "bb\ncc\n"},           // cut on a line boundary
		{"aa\nbb\ncc\n", 5, "cc\n"},               // cut straddles a line
		{"aa\nbb\ncc\n", 0, "cc\n"},               // newest always kept
		{"aaaa\nbbbbbbbbbb\n", 3, "bbbbbbbbbb\n"}, // oversized newest kept
		{"aa\nbb\npartial", 10, "bb\npartial"},    // no trailing newline
	} {
		got := string(keepNewest([]byte(tc.data), tc.maxSize))
		if got != tc.want {
			t.Errorf("keepNewest(%q, %d) = %q, want %q", tc.data, tc.maxSize, got, tc.want)
		}
	}
}
