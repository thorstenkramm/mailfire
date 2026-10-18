package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

var qt0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// testClock is a controllable clock for WithClock.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// runAt runs Run against the mock with the given clock.
func runAt(t *testing.T, m *testutil.MockMailgun, c *testClock, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr, testInfo,
		WithStdin(strings.NewReader("")), WithBaseURL(m.URL()), WithClock(c.Now))
	return code, stdout.String(), stderr.String()
}

// writeMetaFile rewrites a stored message's meta.json.
func writeMetaFile(t *testing.T, deferDir string, meta deferral.Metadata) {
	t.Helper()
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deferDir, meta.ID, deferral.MetaFile), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedAt stores a message deferred at the given time.
func seedAt(t *testing.T, deferDir, to, subject string, at time.Time) deferral.Metadata {
	t.Helper()
	meta := seedDeferred(t, deferDir, to, subject, 0)
	meta.Timestamp = at
	writeMetaFile(t, deferDir, meta)
	return meta
}

// snapshot records every file below dir with its content and mode, to
// prove read-only commands change nothing.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		v := info.Mode().String() + " " + info.ModTime().String()
		if !d.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			v += " " + string(data)
		}
		out[path] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// statusHandler replies with the status currently stored in code (200:
// queued), safe to change between runs.
func statusHandler(t *testing.T, code *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		status := int(code.Load())
		w.WriteHeader(status)
		body := `{"id":"<ok@mg.example.com>","message":"Queued. Thank you."}`
		if status != http.StatusOK {
			body = `{"message":"failure"}`
		}
		if _, err := fmt.Fprint(w, body); err != nil {
			t.Error(err)
		}
	}
}

// Inspection never sends, modifies, expires, or removes anything — not
// even creating a lock file — and never creates a missing defer_dir.
func TestQueueInspectionIsReadOnly(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "max_age = \"1h\"\n")
	c := &testClock{now: qt0.Add(3 * time.Hour)}

	old := seedAt(t, deferDir, "old@example.com", "expired one", qt0)
	failed := seedAt(t, deferDir, "f@example.com", "failed one", qt0.Add(150*time.Minute))
	failed.Failed, failed.Attempts, failed.LastAttempt, failed.LastError = true, 1, qt0.Add(170*time.Minute), "status 400"
	writeMetaFile(t, deferDir, failed)
	before := snapshot(t, deferDir)

	for _, args := range [][]string{
		{"queue", "list"}, {"queue", "stats"},
		{"queue", "show", old.ID}, {"queue", "show", "--raw", failed.ID},
	} {
		code, _, stderr := runAt(t, m, c, append(args, "-c", cfg)...)
		if code != ExitOK {
			t.Errorf("%v: exit = %d; stderr:\n%s", args, code, stderr)
		}
	}
	if n := len(m.Requests()); n != 0 {
		t.Errorf("mock received %d requests, want 0", n)
	}
	after := snapshot(t, deferDir)
	if len(before) != len(after) {
		t.Errorf("entries changed: %d before, %d after", len(before), len(after))
	}
	for path, v := range before {
		if after[path] != v {
			t.Errorf("%s changed by inspection", path)
		}
	}

	missingCfg, missingDir := deferConfig(t, "")
	for _, sub := range []string{"list", "stats"} {
		code, stdout, _ := runAt(t, m, c, "queue", sub, "-c", missingCfg)
		if code != ExitOK {
			t.Errorf("queue %s on a missing defer_dir: exit %d", sub, code)
		}
		if sub == "list" && !strings.Contains(stdout, "no deferred messages") {
			t.Errorf("queue list: stdout = %q", stdout)
		}
	}
	if _, err := os.Stat(missingDir); !os.IsNotExist(err) {
		t.Errorf("inspection created the defer_dir: %v", err)
	}
}

func TestQueueList(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "max_age = \"24h\"\n")
	now := qt0.Add(48 * time.Hour)
	c := &testClock{now: now}

	expired := seedAt(t, deferDir, "e@example.com", "expired", qt0)
	due := seedAt(t, deferDir, "d@example.com", "due", now.Add(-time.Hour))
	waiting := seedAt(t, deferDir, "w@example.com", "waiting", now.Add(-time.Hour))
	waiting.Attempts, waiting.LastAttempt = 2, now.Add(-5*time.Minute) // next: +20m
	writeMetaFile(t, deferDir, waiting)
	failed := seedAt(t, deferDir, "f@example.com", "failed", now.Add(-time.Minute))
	failed.Failed, failed.Attempts = true, 1
	writeMetaFile(t, deferDir, failed)

	code, stdout, stderr := runAt(t, m, c, "queue", "list", "-c", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want header + 4:\n%s", len(lines), stdout)
	}
	for _, col := range []string{"ID", "DATE", "ATTEMPTS", "NEXT RETRY", "TO", "SUBJECT", "SIZE"} {
		if !strings.Contains(lines[0], col) {
			t.Errorf("header missing %q: %q", col, lines[0])
		}
	}
	want := map[string]string{
		expired.ID: "expired",
		due.ID:     "now",
		waiting.ID: formatUTC(now.Add(15 * time.Minute)),
		failed.ID:  "failed",
	}
	for _, line := range lines[1:] {
		id := strings.Fields(line)[0]
		if !strings.Contains(line, want[id]) {
			t.Errorf("row for %s lacks %q:\n%s", id, want[id], line)
		}
	}
	if !strings.Contains(stdout, "waiting") || !strings.Contains(lines[1], expired.ID) {
		t.Errorf("rows wrong or not oldest first:\n%s", stdout)
	}
}

// show prints metadata only; --raw is required for the content, which
// never contains the API key. Unknown IDs exit 1.
func TestQueueShow(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "")
	now := qt0.Add(2 * time.Hour)
	c := &testClock{now: now}

	meta := seedAt(t, deferDir, "a@example.com", "Disk full", qt0)
	meta.Attempts, meta.LastAttempt, meta.LastError = 1, qt0.Add(time.Hour), "mailgun: send: status 503: busy"
	writeMetaFile(t, deferDir, meta)

	code, stdout, stderr := runAt(t, m, c, "queue", "show", meta.ID, "-c", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	for _, want := range []string{
		"ID:", meta.ID, "a@example.com", "Disk full", "(2h0m0s ago)", "Attempts:     1",
		"Last attempt:", "status 503: busy", "Status:", "pending",
		// One failed attempt: 10m after the last attempt, 50m ago: due.
		"Next retry:   now",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show output lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "body of Disk full") {
		t.Errorf("show without --raw printed the message content:\n%s", stdout)
	}

	code, stdout, _ = runAt(t, m, c, "queue", "show", "--raw", meta.ID, "-c", cfg)
	if code != ExitOK {
		t.Fatalf("--raw: exit = %d", code)
	}
	_, body := readDeferred(t, deferDir, meta.ID)
	if want := "Content-Type: " + meta.ContentType + "\r\n\r\n" + string(body); stdout != want {
		t.Errorf("--raw output = %q, want the stored request %q", stdout, want)
	}
	if strings.Contains(stdout, "test-key-not-real") {
		t.Error("--raw output contains the API key")
	}

	code, _, stderr = runAt(t, m, c, "queue", "show", "no-such-id", "-c", cfg)
	if code != ExitUsage || !strings.Contains(stderr, `unknown deferred message ID "no-such-id"`) {
		t.Errorf("unknown ID: exit %d, stderr:\n%s", code, stderr)
	}
}

// delete validates every ID first: one unknown ID deletes nothing.
func TestQueueDelete(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "")
	c := &testClock{now: qt0}
	a := seedAt(t, deferDir, "a@example.com", "a", qt0)
	b := seedAt(t, deferDir, "b@example.com", "b", qt0)
	cc := seedAt(t, deferDir, "c@example.com", "c", qt0)
	before := snapshot(t, deferDir)

	code, stdout, stderr := runAt(t, m, c, "queue", "delete", "-c", cfg, a.ID, "bogus-1", cc.ID, "bogus-2")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, `"bogus-1", "bogus-2"`) || !strings.Contains(stderr, "nothing was deleted") {
		t.Errorf("stderr does not report the unknown IDs:\n%s", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	after := snapshot(t, deferDir)
	for path, v := range before {
		if after[path] != v {
			t.Errorf("%s changed by a rejected delete", path)
		}
	}

	code, stdout, stderr = runAt(t, m, c, "queue", "delete", "-c", cfg, cc.ID, a.ID)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, cc.ID+": deleted") || !strings.Contains(stdout, "queue delete: 2 deleted") {
		t.Errorf("stdout:\n%s", stdout)
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 1 || ids[0] != b.ID {
		t.Errorf("defer_dir = %v, want only %s", ids, b.ID)
	}
	if n := len(m.Requests()); n != 0 {
		t.Errorf("mock received %d requests, want 0", n)
	}
}

func TestQueueStats(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "")
	now := qt0.Add(time.Hour)
	c := &testClock{now: now}

	code, stdout, _ := runAt(t, m, c, "queue", "stats", "-c", cfg)
	if code != ExitOK || !strings.Contains(stdout, "Messages:     0 (0 failed, 0 expired)") || !strings.Contains(stdout, "Next retry:   none") {
		t.Errorf("empty stats: exit %d:\n%s", code, stdout)
	}

	oldest := seedAt(t, deferDir, "a@example.com", "a", qt0)
	oldest.Attempts, oldest.LastAttempt = 3, now.Add(-10*time.Minute) // next: +40m → in 30m
	writeMetaFile(t, deferDir, oldest)
	fresh := seedAt(t, deferDir, "b@example.com", "b", now.Add(-time.Minute)) // next: in 4m
	failed := seedAt(t, deferDir, "c@example.com", "c", now.Add(-30*time.Minute))
	failed.Failed, failed.Attempts, failed.LastAttempt = true, 1, now.Add(-2*time.Minute)
	writeMetaFile(t, deferDir, failed)
	total := oldest.Size + fresh.Size + failed.Size

	code, stdout, stderr := runAt(t, m, c, "queue", "stats", "-c", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	for _, want := range []string{
		"Messages:     3 (1 failed, 0 expired)",
		fmt.Sprintf("Total size:   %d bytes", total),
		"Oldest:       " + oldest.ID, "(1h0m0s ago)",
		"Attempts:     4 failed resend attempts",
		"Last attempt: " + formatUTC(now.Add(-2*time.Minute)),
		"Next retry:   " + formatUTC(now.Add(4*time.Minute)) + " (in 4m0s)",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stats lacks %q:\n%s", want, stdout)
		}
	}
}

func TestQueueUsageErrors(t *testing.T) {
	cfg, _ := deferConfig(t, "")
	for _, args := range [][]string{
		{"queue"},
		{"queue", "purge"},
		{"queue", "show", "-c", cfg},
		{"queue", "show", "-c", cfg, "a", "b"},
		{"queue", "delete", "-c", cfg},
		{"queue", "list", "-c", cfg, "extra"},
		{"queue", "list", "--raw", "-c", cfg},
		{"queue", "stats", "-c", "a.conf", "--config", "b.conf"},
	} {
		code, stdout, stderr := run(t, args...)
		if code != ExitUsage {
			t.Errorf("%v: exit = %d, want %d", args, code, ExitUsage)
		}
		if stdout != "" || !strings.Contains(stderr, "Usage:\n  mailfire queue list") {
			t.Errorf("%v: want queue usage on stderr; stdout %q, stderr:\n%s", args, stdout, stderr)
		}
	}
	code, _, _ := run(t, "queue", "list", "-c", filepath.Join(t.TempDir(), "missing.conf"))
	if code != ExitConfig {
		t.Errorf("missing config: exit %d, want %d", code, ExitConfig)
	}
}

// Backoff with a controlled clock: the first retry waits resend_cooldown
// after deferral, each failure doubles the wait from the latest attempt,
// and --force overrides it. Failures record metadata but never touch the
// payload or the deferral timestamp; success removes the message.
func TestResendBackoff(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	var status atomic.Int32
	status.Store(http.StatusInternalServerError)
	m.SendHandler = statusHandler(t, &status)
	cfg, deferDir := deferConfig(t, "")
	c := &testClock{now: qt0}
	meta := seedAt(t, deferDir, "a@example.com", "backoff", qt0)
	_, body := readDeferred(t, deferDir, meta.ID)

	step := func(at time.Duration, wantRequests int, wantOut string, args ...string) deferral.Metadata {
		t.Helper()
		c.Set(qt0.Add(at))
		_, stdout, _ := runAt(t, m, c, append([]string{"resend", "-c", cfg}, args...)...)
		if n := len(m.Requests()); n != wantRequests {
			t.Fatalf("at +%v: %d requests, want %d; stdout:\n%s", at, n, wantRequests, stdout)
		}
		if !strings.Contains(stdout, wantOut) {
			t.Errorf("at +%v: stdout lacks %q:\n%s", at, wantOut, stdout)
		}
		got, gotBody := readDeferred(t, deferDir, meta.ID)
		if !bytes.Equal(gotBody, body) || !got.Timestamp.Equal(qt0) {
			t.Fatalf("at +%v: payload or deferral timestamp changed", at)
		}
		return got
	}

	step(4*time.Minute+59*time.Second, 0, "skipped (deferred 4m59s ago")
	got := step(5*time.Minute, 1, "failed (retryable; message kept, next retry at "+formatUTC(qt0.Add(15*time.Minute)))
	if got.Attempts != 1 || !got.LastAttempt.Equal(qt0.Add(5*time.Minute)) || !strings.Contains(got.LastError, "500") || got.Failed {
		t.Errorf("after first failure: %+v", got)
	}
	step(14*time.Minute, 1, "skipped (1 failed attempt, last 9m0s ago; eligible at "+formatUTC(qt0.Add(15*time.Minute)))
	got = step(15*time.Minute, 2, "failed (retryable")
	if got.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", got.Attempts)
	}
	step(34*time.Minute, 2, "in 1m0s") // 15m + 20m = 35m
	step(16*time.Minute, 3, "failed (retryable", "--force")

	status.Store(http.StatusOK)
	c.Set(qt0.Add(17 * time.Minute))
	code, stdout, _ := runAt(t, m, c, "resend", "--force", "-c", cfg)
	if code != ExitOK || !strings.Contains(stdout, meta.ID+": sent") {
		t.Errorf("forced success: exit %d:\n%s", code, stdout)
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 0 {
		t.Errorf("defer_dir = %v, want empty after success", ids)
	}
}

// A permanently rejected message is kept, marked failed, and skipped by
// later runs — even with --force — until it is retried explicitly with
// "resend --force <id>".
func TestResendPermanentFailureExcluded(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	var status atomic.Int32
	status.Store(http.StatusBadRequest)
	m.SendHandler = statusHandler(t, &status)
	cfg, deferDir := deferConfig(t, "")
	c := &testClock{now: qt0.Add(time.Hour)}
	meta := seedAt(t, deferDir, "a@example.com", "rejected", qt0)

	code, stdout, _ := runAt(t, m, c, "resend", "-c", cfg)
	if code != ExitSendFail || !strings.Contains(stdout, "failed (permanent; message kept and marked failed") {
		t.Fatalf("exit %d:\n%s", code, stdout)
	}
	got, _ := readDeferred(t, deferDir, meta.ID)
	if !got.Failed || got.Attempts != 1 {
		t.Fatalf("metadata = %+v, want failed after 1 attempt", got)
	}

	status.Store(http.StatusOK)
	c.Set(qt0.Add(10 * time.Hour))
	for _, args := range [][]string{{}, {"--force"}, {meta.ID}} {
		code, stdout, _ := runAt(t, m, c, append([]string{"resend", "-c", cfg}, args...)...)
		if code != ExitOK || !strings.Contains(stdout, meta.ID+": skipped (permanently rejected") {
			t.Errorf("%v: exit %d:\n%s", args, code, stdout)
		}
	}
	if n := len(m.Requests()); n != 1 {
		t.Fatalf("%d requests, want 1 (failed message excluded)", n)
	}

	code, stdout, _ = runAt(t, m, c, "resend", "--force", "-c", cfg, meta.ID)
	if code != ExitOK || !strings.Contains(stdout, meta.ID+": sent") {
		t.Errorf("explicit forced retry: exit %d:\n%s", code, stdout)
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 0 {
		t.Errorf("defer_dir = %v, want empty", ids)
	}
}

// Messages stored by versions without retry metadata are listed,
// shown, and resent normally.
func TestQueueLegacyMessages(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "")
	c := &testClock{now: qt0.Add(time.Hour)}
	meta := seedAt(t, deferDir, "a@example.com", "legacy", qt0)
	legacy := fmt.Sprintf(`{"id":%q,"timestamp":"2026-10-15T12:00:00Z","to":["a@example.com"],"subject":"legacy","size":%d,"content_type":%q}`,
		meta.ID, meta.Size, meta.ContentType)
	if err := os.WriteFile(filepath.Join(deferDir, meta.ID, deferral.MetaFile), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := runAt(t, m, c, "queue", "show", meta.ID, "-c", cfg)
	if code != ExitOK || !strings.Contains(stdout, "Attempts:     0") || !strings.Contains(stdout, "Last attempt: none") ||
		!strings.Contains(stdout, "Next retry:   now") {
		t.Errorf("show legacy: exit %d:\n%s", code, stdout)
	}
	code, stdout, _ = runAt(t, m, c, "resend", "-c", cfg)
	if code != ExitOK || !strings.Contains(stdout, meta.ID+": sent") {
		t.Errorf("resend legacy: exit %d:\n%s", code, stdout)
	}
}

// With max_age set, resend and deferral remove and report expired
// messages (stderr and log); inspection only marks them.
func TestDeferMaxAge(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	logfile := filepath.Join(t.TempDir(), "mailfire.log")
	cfg, deferDir := deferConfig(t, "max_age = \"24h\"\n\n[logging]\nlogfile = '"+logfile+"'\n")
	now := qt0.Add(48 * time.Hour)
	c := &testClock{now: now}
	old := seedAt(t, deferDir, "old@example.com", "old", qt0)
	young := seedAt(t, deferDir, "young@example.com", "young", now.Add(-time.Hour))

	code, stdout, stderr := runAt(t, m, c, "resend", "-c", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "mailfire: resend: expired "+old.ID) || !strings.Contains(stderr, "older than max_age") {
		t.Errorf("stderr missing the expiry report:\n%s", stderr)
	}
	if !strings.Contains(stdout, young.ID+": sent") || !strings.Contains(stdout, "resend: 1 sent, 0 skipped, 0 failed") {
		t.Errorf("stdout:\n%s", stdout)
	}
	if n := len(m.Requests()); n != 1 || m.LastRequest(t).Field("subject") != "young" {
		t.Errorf("requests = %d, want only the young message", n)
	}
	log, err := os.ReadFile(logfile)
	if err != nil || !strings.Contains(string(log), `"event":"defer_expire"`) || !strings.Contains(string(log), old.ID) {
		t.Errorf("log lacks the expiry entry (%v):\n%s", err, log)
	}

	// Deferral (a queue write) expires too, using the real clock.
	stale := seedDeferred(t, deferDir, "stale@example.com", "stale", 25*time.Hour)
	m.SendResponse = testutil.InternalError()
	code, _, stderr = runMock(t, m, "body", "-c", cfg, "--to", "a@example.com", "--subject", "new")
	if code != ExitDeferred {
		t.Fatalf("send: exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "mailfire: defer: expired "+stale.ID) {
		t.Errorf("send stderr missing the expiry report:\n%s", stderr)
	}
	ids := deferredIDs(t, deferDir)
	if len(ids) != 1 {
		t.Fatalf("defer_dir = %v, want only the new message", ids)
	}
	saved, _ := readDeferred(t, deferDir, ids[0])
	if saved.Subject != "new" || !strings.Contains(saved.LastError, "500") {
		t.Errorf("new message = %+v, want subject new with the send error recorded", saved)
	}
}

// Two overlapping resend runs never deliver a message twice.
func TestResendConcurrentRunsNoDuplicates(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendHandler = func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond) // widen the race window
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprint(w, `{"id":"<x@mg.example.com>","message":"Queued. Thank you."}`); err != nil {
			t.Error(err)
		}
	}
	cfg, deferDir := deferConfig(t, "")
	const n = 8
	for i := range n {
		seedDeferred(t, deferDir, "a@example.com", fmt.Sprintf("msg %d", i), time.Hour)
	}

	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			var stdout, stderr bytes.Buffer
			if code := Run([]string{"resend", "-c", cfg}, &stdout, &stderr, testInfo, WithBaseURL(m.URL())); code != ExitOK {
				t.Errorf("resend exit %d; stderr:\n%s", code, stderr.String())
			}
		})
	}
	wg.Wait()

	delivered := map[string]int{}
	for _, req := range m.Requests() {
		delivered[req.Field("subject")]++
	}
	if len(delivered) != n {
		t.Errorf("delivered %d distinct messages, want %d", len(delivered), n)
	}
	for subject, count := range delivered {
		if count != 1 {
			t.Errorf("%s delivered %d times, want once", subject, count)
		}
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 0 {
		t.Errorf("defer_dir = %v, want empty", ids)
	}
}

// A queue delete issued while the message is being resent waits for the
// resend to finish, then finds the message gone: it can neither delete
// a message mid-send nor cause a second delivery.
func TestQueueDeleteWaitsForResend(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	inSend := make(chan struct{})
	release := make(chan struct{})
	m.SendHandler = func(w http.ResponseWriter, r *http.Request) {
		close(inSend)
		<-release
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprint(w, `{"id":"<x@mg.example.com>","message":"Queued. Thank you."}`); err != nil {
			t.Error(err)
		}
	}
	cfg, deferDir := deferConfig(t, "")
	meta := seedDeferred(t, deferDir, "a@example.com", "s", time.Hour)

	resendDone := make(chan int)
	go func() {
		var stdout, stderr bytes.Buffer
		resendDone <- Run([]string{"resend", "-c", cfg}, &stdout, &stderr, testInfo, WithBaseURL(m.URL()))
	}()
	<-inSend

	type result struct {
		code   int
		stderr string
	}
	deleteDone := make(chan result)
	go func() {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"queue", "delete", "-c", cfg, meta.ID}, &stdout, &stderr, testInfo)
		deleteDone <- result{code, stderr.String()}
	}()
	select {
	case r := <-deleteDone:
		t.Fatalf("queue delete finished during the resend (exit %d)", r.code)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if code := <-resendDone; code != ExitOK {
		t.Errorf("resend exit %d", code)
	}
	r := <-deleteDone
	if r.code != ExitUsage || !strings.Contains(r.stderr, "unknown deferred message ID") {
		t.Errorf("delete after resend: exit %d, stderr:\n%s", r.code, r.stderr)
	}
	if n := len(m.Requests()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestParseInterspersed(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		wantIDs []string
		wantCfg string
	}{
		{[]string{"a", "-c", "x.conf", "b"}, []string{"a", "b"}, "x.conf"},
		{[]string{"-c", "x.conf", "a", "b"}, []string{"a", "b"}, "x.conf"},
		{[]string{"a", "--", "-c", "b"}, []string{"a", "-c", "b"}, ""},
		{nil, nil, ""},
	} {
		opts, err := parseQueueFlags("delete", append([]string{}, tc.args...))
		if len(tc.wantIDs) == 0 {
			if err == nil {
				t.Errorf("%v: want an error for delete without IDs", tc.args)
			}
			continue
		}
		if err != nil {
			t.Errorf("%v: %v", tc.args, err)
			continue
		}
		if strings.Join(opts.IDs, " ") != strings.Join(tc.wantIDs, " ") || opts.Config != tc.wantCfg {
			t.Errorf("%v: IDs %v config %q, want %v %q", tc.args, opts.IDs, opts.Config, tc.wantIDs, tc.wantCfg)
		}
	}
}

// A planted meta.json with a traversal ID is invisible to the queue
// commands, and neither queue delete nor resend's automatic expiry
// removes anything outside the defer_dir.
func TestQueueTraversalIDPreservesOutside(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "max_age = \"1h\"\n")
	c := &testClock{now: qt0.Add(100 * time.Hour)}
	real := seedAt(t, deferDir, "a@example.com", "real", qt0.Add(99*time.Hour+30*time.Minute))

	sentinel := filepath.Join(filepath.Dir(deferDir), "outside-queue")
	if err := os.MkdirAll(sentinel, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sentinel, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	evil := seedAt(t, deferDir, "e@example.com", "evil", qt0)
	dirName := evil.ID
	evil.ID = "../outside-queue"
	data, err := json.Marshal(evil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deferDir, dirName, deferral.MetaFile), data, 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := runAt(t, m, c, "queue", "list", "-c", cfg)
	if code != ExitOK || strings.Contains(stdout, "outside-queue") || !strings.Contains(stdout, real.ID) {
		t.Errorf("queue list: exit %d:\n%s", code, stdout)
	}
	code, _, _ = runAt(t, m, c, "queue", "delete", "-c", cfg, "../outside-queue")
	if code != ExitUsage {
		t.Errorf("queue delete of the traversal ID: exit %d, want %d", code, ExitUsage)
	}
	code, stdout, stderr := runAt(t, m, c, "resend", "-c", cfg)
	if code != ExitOK || strings.Contains(stderr, "expired") || !strings.Contains(stdout, real.ID+": sent") {
		t.Errorf("resend: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if n := len(m.Requests()); n != 1 {
		t.Errorf("%d requests, want 1 (only the real message)", n)
	}
	if _, err := os.Stat(filepath.Join(sentinel, "keep")); err != nil {
		t.Fatalf("sentinel outside the defer_dir was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(deferDir, dirName)); err != nil {
		t.Errorf("corrupt entry removed via its claimed ID: %v", err)
	}
}
