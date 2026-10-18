package cli

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/dedup"
	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// dedupConfig writes a config with a sender, a defer_dir, and a dedup
// state_dir in temp directories, plus extra lines appended (inside
// [dedup] unless extra opens another section).
func dedupConfig(t *testing.T, extra string) (cfgPath, deferDir, stateDir string) {
	t.Helper()
	base := t.TempDir()
	deferDir = filepath.Join(base, "deferred")
	stateDir = filepath.Join(base, "dedup-state")
	cfg := "from = \"ops@example.com\"\n\n[defer]\ndefer_dir = '" + deferDir + "'\n\n[dedup]\nstate_dir = '" + stateDir + "'\n" + extra
	return writeConfig(t, cfg), deferDir, stateDir
}

// statusMock returns a mock whose send status can change between runs.
func statusMock(t *testing.T) (*testutil.MockMailgun, *atomic.Int32) {
	t.Helper()
	m := testutil.NewMockMailgun(t)
	var status atomic.Int32
	status.Store(http.StatusOK)
	m.SendHandler = statusHandler(t, &status)
	return m, &status
}

// notify sends one keyed notification at the clock's time.
func notify(t *testing.T, m *testutil.MockMailgun, c *testClock, cfg string, extra ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"-c", cfg, "--to", "ops@example.com", "--subject", "disk full", "--message", "98%",
		"--dedup-key", "disk-full"}, extra...)
	return runAt(t, m, c, args...)
}

func sends(m *testutil.MockMailgun) int {
	n := 0
	for _, r := range m.Requests() {
		if r.Method == http.MethodPost {
			n++
		}
	}
	return n
}

// Repeats within the cooldown are skipped (exit 0, reported with the
// time they become eligible); after it, they are sent. Skips do not
// extend the cooldown.
func TestDedupCooldown(t *testing.T) {
	m, _ := statusMock(t)
	cfg, _, _ := dedupConfig(t, "")
	start := time.Now().UTC()
	c := &testClock{now: start}

	if code, stdout, stderr := notify(t, m, c, cfg); code != ExitOK || stdout != "<ok@mg.example.com>\n" {
		t.Fatalf("first send: exit %d, stdout %q, stderr:\n%s", code, stdout, stderr)
	}
	for _, after := range []time.Duration{time.Minute, 30 * time.Minute, 59 * time.Minute} {
		c.Set(start.Add(after))
		code, stdout, stderr := notify(t, m, c, cfg)
		if code != ExitOK {
			t.Fatalf("repeat after %v: exit %d; stderr:\n%s", after, code, stderr)
		}
		eligible := formatUTC(start.Add(time.Hour))
		if !strings.HasPrefix(stdout, `skipped: duplicate notification (--dedup-key "disk-full")`) ||
			!strings.Contains(stdout, "eligible again at "+eligible) {
			t.Errorf("repeat after %v: stdout = %q, want skipped until %s", after, stdout, eligible)
		}
		if stderr != "" {
			t.Errorf("repeat after %v: stderr = %q", after, stderr)
		}
	}
	if n := sends(m); n != 1 {
		t.Fatalf("%d sends within the cooldown, want 1", n)
	}
	c.Set(start.Add(time.Hour))
	if code, stdout, _ := notify(t, m, c, cfg); code != ExitOK || strings.HasPrefix(stdout, "skipped") {
		t.Errorf("after the cooldown: exit %d, stdout %q, want sent", code, stdout)
	}
	if n := sends(m); n != 2 {
		t.Errorf("%d sends, want 2", n)
	}
}

// --cooldown overrides [dedup].cooldown, which overrides the default;
// the delivered send's cooldown applies.
func TestDedupCooldownOverrides(t *testing.T) {
	m, _ := statusMock(t)
	cfg, _, _ := dedupConfig(t, "cooldown = \"10m\"\n")
	start := time.Now().UTC()
	c := &testClock{now: start}

	notify(t, m, c, cfg)
	c.Set(start.Add(9 * time.Minute))
	if _, stdout, _ := notify(t, m, c, cfg); !strings.HasPrefix(stdout, "skipped") {
		t.Errorf("config cooldown: stdout = %q, want skipped", stdout)
	}
	c.Set(start.Add(10 * time.Minute))
	notify(t, m, c, cfg, "--cooldown", "2m")
	c.Set(start.Add(11 * time.Minute))
	if _, stdout, _ := notify(t, m, c, cfg); !strings.HasPrefix(stdout, "skipped") {
		t.Errorf("--cooldown 2m: stdout = %q, want skipped", stdout)
	}
	c.Set(start.Add(12 * time.Minute))
	if _, stdout, _ := notify(t, m, c, cfg); strings.HasPrefix(stdout, "skipped") {
		t.Errorf("after --cooldown 2m: stdout = %q, want sent", stdout)
	}
	if n := sends(m); n != 3 {
		t.Errorf("%d sends, want 3", n)
	}
}

// Different keys, recipients, and sending domains never interfere;
// recipient case and order do not matter.
func TestDedupScopes(t *testing.T) {
	m, _ := statusMock(t)
	cfg, deferDir, stateDir := dedupConfig(t, "")
	c := &testClock{now: time.Now().UTC()}
	base := []string{"-c", cfg, "--subject", "s", "--message", "m"}
	run := func(args ...string) string {
		t.Helper()
		code, stdout, stderr := runAt(t, m, c, append(append([]string{}, base...), args...)...)
		if code != ExitOK {
			t.Fatalf("Run(%v): exit %d; stderr:\n%s", args, code, stderr)
		}
		return stdout
	}
	run("--to", "a@example.com", "--cc", "b@example.com", "--dedup-key", "k")
	for _, args := range [][]string{
		{"--to", "a@example.com", "--cc", "b@example.com", "--dedup-key", "other"},
		{"--to", "a@example.com", "--dedup-key", "k"},
		{"--to", "a@example.com", "--cc", "b@example.com", "--bcc", "c@example.com", "--dedup-key", "k"},
	} {
		if out := run(args...); strings.HasPrefix(out, "skipped") {
			t.Errorf("%v: skipped by an unrelated notification", args)
		}
	}
	if out := run("--to", "B@Example.com", "--bcc", "A <a@example.com>", "--dedup-key", "k"); !strings.HasPrefix(out, "skipped") {
		t.Errorf("same recipients in another case, order, and field: stdout = %q, want skipped", out)
	}

	// Another sending domain, same state directory.
	other := writeConfig(t, "")
	data, err := os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	otherCfg := strings.Replace(string(data), "mg.example.com", "alerts.example.com", 1) +
		"from = \"ops@example.com\"\n[defer]\ndefer_dir = '" + deferDir + "'\n[dedup]\nstate_dir = '" + stateDir + "'\n"
	if err := os.WriteFile(other, []byte(otherCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runAt(t, m, c, "-c", other, "--to", "a@example.com", "--cc", "b@example.com",
		"--subject", "s", "--message", "m", "--dedup-key", "k")
	if code != ExitOK || strings.HasPrefix(stdout, "skipped") {
		t.Errorf("other domain: exit %d, stdout %q, stderr %s", code, stdout, stderr)
	}
}

// Failed sends start no cooldown.
func TestDedupFailureStartsNoCooldown(t *testing.T) {
	m, status := statusMock(t)
	cfg, _, _ := dedupConfig(t, "")
	c := &testClock{now: time.Now().UTC()}

	status.Store(http.StatusBadRequest)
	if code, _, _ := notify(t, m, c, cfg); code != ExitSendFail {
		t.Fatalf("permanent failure: exit %d, want %d", code, ExitSendFail)
	}
	status.Store(http.StatusInternalServerError)
	if code, _, _ := notify(t, m, c, cfg, "--no-defer"); code != ExitSendFail {
		t.Fatalf("not deferred: exit %d, want %d", code, ExitSendFail)
	}
	status.Store(http.StatusOK)
	if code, stdout, _ := notify(t, m, c, cfg); code != ExitOK || strings.HasPrefix(stdout, "skipped") {
		t.Errorf("after failures: exit %d, stdout %q, want sent", code, stdout)
	}
	if n := sends(m); n != 3 {
		t.Errorf("%d sends, want 3", n)
	}
}

// A deferred notification blocks its duplicates while queued: no request
// is made and no second message is queued. A successful resend starts
// the cooldown recorded with the message.
func TestDedupQueued(t *testing.T) {
	m, status := statusMock(t)
	cfg, deferDir, _ := dedupConfig(t, "")
	start := time.Now().UTC()
	c := &testClock{now: start}

	status.Store(http.StatusServiceUnavailable)
	if code, _, stderr := notify(t, m, c, cfg, "--cooldown", "20m"); code != ExitDeferred {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitDeferred, stderr)
	}
	ids := deferredIDs(t, deferDir)
	if len(ids) != 1 {
		t.Fatalf("deferred %v, want one message", ids)
	}
	meta, _ := readDeferred(t, deferDir, ids[0])
	if len(meta.DedupScope) != 64 || meta.DedupCooldown != 20*time.Minute {
		t.Errorf("meta = %+v, want dedup scope and 20m cooldown", meta)
	}

	status.Store(http.StatusOK)
	c.Set(start.Add(2 * time.Hour))
	code, stdout, _ := notify(t, m, c, cfg)
	if code != ExitOK || stdout != `skipped: duplicate notification (--dedup-key "disk-full"): already deferred as `+ids[0]+"; mailfire resend delivers it\n" {
		t.Errorf("queued duplicate: exit %d, stdout %q", code, stdout)
	}
	if got := deferredIDs(t, deferDir); len(got) != 1 {
		t.Errorf("queue = %v, want only the first message", got)
	}
	if n := sends(m); n != 1 {
		t.Errorf("%d sends, want 1 (the duplicate was not attempted)", n)
	}

	if code, stdout, stderr := runAt(t, m, c, "resend", "-c", cfg); code != ExitOK || !strings.Contains(stdout, "1 sent") {
		t.Fatalf("resend: exit %d, stdout %q, stderr %s", code, stdout, stderr)
	}
	c.Set(start.Add(2*time.Hour + 19*time.Minute))
	if _, stdout, _ := notify(t, m, c, cfg); !strings.Contains(stdout, "skipped") || !strings.Contains(stdout, "sent 19m0s ago") {
		t.Errorf("after resend: stdout = %q, want skipped by the resend's cooldown", stdout)
	}
	c.Set(start.Add(2*time.Hour + 20*time.Minute))
	if _, stdout, _ := notify(t, m, c, cfg); strings.HasPrefix(stdout, "skipped") {
		t.Errorf("after the resend's cooldown: stdout = %q, want sent", stdout)
	}
}

// A queued message blocks duplicates for as long as it stays queued, even
// when marked permanently rejected (it can still be retried with
// --force); once deleted, the notification is sent again.
func TestDedupFailedQueuedBlocks(t *testing.T) {
	m, status := statusMock(t)
	cfg, deferDir, _ := dedupConfig(t, "")
	c := c0()

	status.Store(http.StatusServiceUnavailable)
	notify(t, m, c, cfg)
	id := deferredIDs(t, deferDir)[0]
	meta, _ := readDeferred(t, deferDir, id)
	meta.Failed = true
	writeMetaFile(t, deferDir, meta)

	status.Store(http.StatusOK)
	code, stdout, _ := notify(t, m, c, cfg)
	if code != ExitOK || !strings.Contains(stdout, "already deferred as "+id) {
		t.Errorf("failed queued message: exit %d, stdout %q, want skipped as queued", code, stdout)
	}
	if n := sends(m); n != 1 {
		t.Errorf("%d sends, want 1", n)
	}
	if got := deferredIDs(t, deferDir); len(got) != 1 {
		t.Errorf("queue = %v, want only the first message", got)
	}
	if code, _, stderr := runAt(t, m, c, "queue", "delete", "-c", cfg, id); code != ExitOK {
		t.Fatalf("queue delete: %d %s", code, stderr)
	}
	if code, stdout, _ := notify(t, m, c, cfg); code != ExitOK || strings.HasPrefix(stdout, "skipped") {
		t.Errorf("deleted queued message: exit %d, stdout %q, want sent", code, stdout)
	}
}

// Test sends neither check nor start the cooldown.
func TestDedupTestModeDoesNotConsume(t *testing.T) {
	m, _ := statusMock(t)
	cfg, _, stateDir := dedupConfig(t, "")
	c := &testClock{now: time.Now().UTC()}

	code, stdout, stderr := notify(t, m, c, cfg, "--test")
	if code != ExitOK || !strings.Contains(stdout, "test mode") || !strings.Contains(stderr, "deduplication not applied") {
		t.Fatalf("test send: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Errorf("test send touched the state directory: %v", err)
	}
	if _, stdout, _ := notify(t, m, c, cfg); strings.HasPrefix(stdout, "skipped") {
		t.Errorf("real send after a test send: stdout %q, want sent", stdout)
	}
	if code, stdout, _ := notify(t, m, c, cfg, "--test"); code != ExitOK || strings.HasPrefix(stdout, "skipped") {
		t.Errorf("test send during cooldown: exit %d, stdout %q, want validated", code, stdout)
	}
	if n := sends(m); n != 3 {
		t.Errorf("%d requests, want 3", n)
	}
}

// Deduplication works with deferral disabled.
func TestDedupWithoutDeferral(t *testing.T) {
	m, _ := statusMock(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg := writeConfig(t, "from = \"ops@example.com\"\n[defer]\ndefer = false\n[dedup]\nstate_dir = '"+stateDir+"'\n")
	c := &testClock{now: time.Now().UTC()}
	notify(t, m, c, cfg)
	if _, stdout, _ := notify(t, m, c, cfg); !strings.HasPrefix(stdout, "skipped") {
		t.Errorf("stdout = %q, want skipped", stdout)
	}
}

// An attempt interrupted before its outcome was recorded suppresses
// duplicates for one minute only.
func TestDedupInterruptedAttempt(t *testing.T) {
	m, _ := statusMock(t)
	cfg, _, stateDir := dedupConfig(t, "")
	start := time.Now().UTC()
	c := &testClock{now: start}

	store := &dedup.Store{Dir: stateDir, Now: c.Now}
	sc := dedup.NewScope("mg.example.com", []string{"ops@example.com"}, "disk-full")
	g, err := store.Acquire(sc.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Begin(&sc); err != nil {
		t.Fatal(err)
	}
	if err := g.Release(); err != nil { // crash: outcome never recorded
		t.Fatal(err)
	}

	c.Set(start.Add(30 * time.Second))
	code, stdout, _ := notify(t, m, c, cfg)
	if code != ExitOK || !strings.Contains(stdout, "interrupted and may have been delivered") || !strings.Contains(stdout, "in 30s") {
		t.Errorf("in doubt: exit %d, stdout %q", code, stdout)
	}
	c.Set(start.Add(time.Minute))
	if _, stdout, _ := notify(t, m, c, cfg); strings.HasPrefix(stdout, "skipped") {
		t.Errorf("after the recovery delay: stdout %q, want sent", stdout)
	}
}

// A clock that moved backward ends the cooldown instead of prolonging it.
func TestDedupClockBackward(t *testing.T) {
	m, _ := statusMock(t)
	cfg, _, _ := dedupConfig(t, "")
	start := time.Now().UTC()
	c := &testClock{now: start}
	notify(t, m, c, cfg)
	c.Set(start.Add(-time.Minute))
	if _, stdout, _ := notify(t, m, c, cfg); strings.HasPrefix(stdout, "skipped") {
		t.Errorf("stdout %q, want sent", stdout)
	}
}

// Corrupt state is a clear error (exit 2) and nothing is sent.
func TestDedupCorruptState(t *testing.T) {
	m, _ := statusMock(t)
	cfg, _, stateDir := dedupConfig(t, "")
	sc := dedup.NewScope("mg.example.com", []string{"ops@example.com"}, "disk-full")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, sc.ID()+".json")
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := notify(t, m, c0(), cfg)
	if code != ExitConfig || stdout != "" || !strings.Contains(stderr, path) || !strings.Contains(stderr, "nothing was sent") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if n := sends(m); n != 0 {
		t.Errorf("%d sends, want 0", n)
	}
}

func c0() *testClock { return &testClock{now: time.Now().UTC()} }

func TestDedupStateDirInsideDeferDir(t *testing.T) {
	m, _ := statusMock(t)
	deferDir := filepath.Join(t.TempDir(), "q")
	cfg := writeConfig(t, "from = \"ops@example.com\"\n[defer]\ndefer_dir = '"+deferDir+"'\n[dedup]\nstate_dir = '"+filepath.Join(deferDir, "state")+"'\n")
	code, _, stderr := notify(t, m, c0(), cfg)
	if code != ExitConfig || !strings.Contains(stderr, "inside defer.defer_dir") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	// Without --dedup-key the setting is irrelevant.
	if code, _, stderr := runAt(t, m, c0(), "-c", cfg, "--to", "a@example.com", "--subject", "s", "--message", "m"); code != ExitOK {
		t.Errorf("without --dedup-key: exit %d, stderr %q", code, stderr)
	}
}

// With the defaults, the state directory is the "dedup" subdirectory of
// the defer_dir; the queue ignores it.
func TestDedupDefaultStateDirBesideQueue(t *testing.T) {
	envOnly(t)
	t.Setenv("MAILFIRE_DEFER_DEFER_DIR", "")
	t.Setenv("MAILFIRE_SENDING_FROM", "ops@example.com")
	home := os.Getenv("HOME")
	m, status := statusMock(t)
	c := c0()
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := Run(args, &stdout, &stderr, testInfo, WithStdin(strings.NewReader("")), WithBaseURL(m.URL()), WithClock(c.Now))
		return code, stdout.String(), stderr.String()
	}
	status.Store(http.StatusServiceUnavailable)
	if code, _, stderr := run("--to", "a@example.com", "--subject", "s", "--message", "m", "--dedup-key", "k"); code != ExitDeferred {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	status.Store(http.StatusOK)
	if code, _, stderr := run("--to", "b@example.com", "--subject", "s", "--message", "m", "--dedup-key", "k"); code != ExitOK {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	deferDir := filepath.Join(home, ".local", "share", "mailfire")
	if os.PathSeparator == '\\' {
		deferDir = filepath.Join(home, "local", "mailfire")
	}
	if fi, err := os.Stat(filepath.Join(deferDir, deferral.ReservedDir)); err != nil || !fi.IsDir() {
		t.Fatalf("default state directory missing: %v", err)
	}
	code, stdout, _ := run("queue", "stats")
	if code != ExitOK || !strings.Contains(stdout, "1") {
		t.Errorf("queue stats: exit %d, stdout %q", code, stdout)
	}
	code, stdout, stderr := run("resend", "--force")
	if code != ExitOK || !strings.Contains(stdout, "1 sent, 0 skipped, 0 failed") {
		t.Errorf("resend: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if fi, err := os.Stat(filepath.Join(deferDir, deferral.ReservedDir)); err != nil || !fi.IsDir() {
		t.Errorf("state directory removed by the queue: %v", err)
	}
}

func TestDedupUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--cooldown", "5m"}, "--cooldown requires --dedup-key"},
		{[]string{"--dedup-key", ""}, "--dedup-key: empty key"},
		{[]string{"--dedup-key", "  "}, "--dedup-key: empty key"},
		{[]string{"--dedup-key", "k", "--cooldown", "0"}, "invalid --cooldown"},
		{[]string{"--dedup-key", "k", "--cooldown", "soon"}, "invalid --cooldown"},
		{[]string{"--dedup-key", "k", "--cooldown", ""}, "invalid --cooldown"},
	} {
		args := append([]string{"--to", "a@example.com", "--subject", "s"}, tc.args...)
		code, _, stderr := run(t, args...)
		if code != ExitUsage || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: exit %d, stderr %q, want %q", tc.args, code, stderr, tc.want)
		}
	}
}

// --cooldown overrides an invalid environment value without validating it.
func TestDedupCooldownFlagOverridesInvalidEnv(t *testing.T) {
	m, _ := statusMock(t)
	cfg, _, _ := dedupConfig(t, "")
	t.Setenv("MAILFIRE_DEDUP_COOLDOWN", "soon")
	if code, _, stderr := notify(t, m, c0(), cfg); code != ExitConfig || !strings.Contains(stderr, "MAILFIRE_DEDUP_COOLDOWN") {
		t.Errorf("without --cooldown: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := notify(t, m, c0(), cfg, "--cooldown", "5m"); code != ExitOK {
		t.Errorf("with --cooldown: exit %d, stderr %q", code, stderr)
	}
}

// Concurrent sends of the same notification: exactly one is delivered,
// even while the first request is still in flight.
func TestDedupConcurrentSends(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse.Delay = 50 * time.Millisecond
	cfg, _, _ := dedupConfig(t, "")
	var wg sync.WaitGroup
	var skipped atomic.Int32
	for range 8 {
		wg.Go(func() {
			var stdout, stderr bytes.Buffer
			code := Run([]string{"-c", cfg, "--to", "ops@example.com", "--subject", "s", "--message", "m", "--dedup-key", "k"},
				&stdout, &stderr, testInfo, WithStdin(strings.NewReader("")), WithBaseURL(m.URL()))
			if code != ExitOK {
				t.Errorf("exit %d; stderr:\n%s", code, stderr.String())
			}
			if strings.HasPrefix(stdout.String(), "skipped") {
				skipped.Add(1)
			}
		})
	}
	wg.Wait()
	if n := sends(m); n != 1 || skipped.Load() != 7 {
		t.Errorf("%d sends, %d skipped; want 1 and 7", n, skipped.Load())
	}
}

// Concurrent resend and duplicate send: the duplicate never slips in
// between the delivery and the cooldown being recorded.
func TestDedupResendAndSendRace(t *testing.T) {
	for range 5 {
		m, status := statusMock(t)
		cfg, _, _ := dedupConfig(t, "")
		status.Store(http.StatusServiceUnavailable)
		notify(t, m, c0(), cfg)
		status.Store(http.StatusOK)

		var wg sync.WaitGroup
		wg.Go(func() {
			if code, _, stderr := runAt(t, m, c0(), "resend", "--force", "-c", cfg); code != ExitOK {
				t.Errorf("resend: %d %s", code, stderr)
			}
		})
		wg.Go(func() {
			if code, _, stderr := notify(t, m, c0(), cfg); code != ExitOK {
				t.Errorf("send: %d %s", code, stderr)
			}
		})
		wg.Wait()
		if n := sends(m); n != 2 { // the deferred attempt plus one delivery
			t.Errorf("%d requests, want 2", n)
		}
	}
}

// stateFile reads the dedup state of the default test notification ("" if
// absent).
func stateFile(t *testing.T, stateDir string) string {
	t.Helper()
	sc := dedup.NewScope("mg.example.com", []string{"ops@example.com"}, "disk-full")
	data, err := os.ReadFile(filepath.Join(stateDir, sc.ID()+".json"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Resend records the attempt as pending before the request (a crash
// mid-delivery leaves it in doubt), commits the cooldown on success,
// clears the marker on failure, and leaves the state alone when it skips
// a message without attempting it.
func TestDedupResendRecordsAttempt(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	cfg, deferDir, stateDir := dedupConfig(t, "")
	var mu sync.Mutex
	during := "" // the state file as seen by the latest request
	seen := func() string {
		mu.Lock()
		defer mu.Unlock()
		return during
	}
	ok := statusHandler(t, &status)
	sc := dedup.NewScope("mg.example.com", []string{"ops@example.com"}, "disk-full")
	m.SendHandler = func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(filepath.Join(stateDir, sc.ID()+".json"))
		if err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
		mu.Lock()
		during = string(data)
		mu.Unlock()
		ok(w, r)
	}
	c := c0()
	notify(t, m, c, cfg)
	if got := stateFile(t, stateDir); got != "" {
		t.Fatalf("state after a deferred send = %s, want none", got)
	}

	// Skipped (not yet eligible): nothing is recorded.
	if code, stdout, _ := runAt(t, m, c, "resend", "-c", cfg); code != ExitOK || !strings.Contains(stdout, "1 skipped") {
		t.Fatalf("resend: exit %d, stdout %q", code, stdout)
	}
	if got := stateFile(t, stateDir); got != "" {
		t.Errorf("state after a skipped resend = %s, want none", got)
	}

	// Failed: pending during the request, cleared afterwards.
	if code, _, _ := runAt(t, m, c, "resend", "--force", "-c", cfg); code != ExitDeferred {
		t.Fatalf("failing resend: exit %d", code)
	}
	if !strings.Contains(seen(), `"sending"`) {
		t.Errorf("state during the failing resend = %s, want a pending attempt", seen())
	}
	if got := stateFile(t, stateDir); got != "" {
		t.Errorf("state after a failed resend = %s, want none", got)
	}
	if _, stdout, _ := notify(t, m, c, cfg); !strings.Contains(stdout, "already deferred") {
		t.Errorf("after a failed resend: stdout %q, want skipped as queued", stdout)
	}

	// Delivered: pending during the request, then the cooldown.
	status.Store(http.StatusOK)
	if code, _, _ := runAt(t, m, c, "resend", "--force", "-c", cfg); code != ExitOK {
		t.Fatalf("resend: exit %d", code)
	}
	if !strings.Contains(seen(), `"sending"`) {
		t.Errorf("state during the resend = %s, want a pending attempt", seen())
	}
	got := stateFile(t, stateDir)
	if strings.Contains(got, `"sending"`) || !strings.Contains(got, `"sent_at"`) {
		t.Errorf("state after delivery = %s, want the cooldown only", got)
	}
	if len(deferredIDs(t, deferDir)) != 0 {
		t.Error("delivered message still queued")
	}
}

// failClosed asserts a resend that could not use the deduplication state
// of a scoped message: exit 5, no request, the message still queued.
func failClosed(t *testing.T, m *testutil.MockMailgun, deferDir string, before int, code int, stdout, stderr, want string) {
	t.Helper()
	if code != ExitSendFail || !strings.Contains(stdout, "failed (cannot use the notification deduplication state; message kept, nothing sent)") ||
		!strings.Contains(stdout, "0 sent, 0 skipped, 1 failed") || !strings.Contains(stderr, want) {
		t.Errorf("exit %d, stdout %q, stderr %q; want exit %d naming %q", code, stdout, stderr, ExitSendFail, want)
	}
	if n := sends(m); n != before {
		t.Errorf("%d requests, want %d (nothing sent)", n, before)
	}
	msgs, err := (&deferral.Store{Dir: deferDir}).List()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("queue = %+v (%v), want the message kept", msgs, err)
	}
	if msgs[0].Attempts != 0 {
		t.Errorf("attempts = %d, want 0 (no delivery was attempted)", msgs[0].Attempts)
	}
}

// deferNotification queues one keyed notification and returns the
// request count afterwards.
func deferNotification(t *testing.T, m *testutil.MockMailgun, status *atomic.Int32, cfg string) int {
	t.Helper()
	status.Store(http.StatusServiceUnavailable)
	if code, _, stderr := notify(t, m, c0(), cfg); code != ExitDeferred {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	status.Store(http.StatusOK)
	return sends(m)
}

// Resend applies the same state_dir safety check as send and fails
// closed: a state directory aliased into the defer_dir is neither written
// nor bypassed; the message stays queued and nothing is sent.
func TestDedupResendUnsafeStateDir(t *testing.T) {
	m, status := statusMock(t)
	cfg, deferDir, _ := dedupConfig(t, "")
	before := deferNotification(t, m, status, cfg)

	inner := filepath.Join(deferDir, "inner")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(inner, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	unsafe := writeConfig(t, "from = \"ops@example.com\"\n[defer]\ndefer_dir = '"+deferDir+"'\n[dedup]\nstate_dir = '"+alias+"'\n")
	code, stdout, stderr := runAt(t, m, c0(), "resend", "--force", "-c", unsafe)
	failClosed(t, m, deferDir, before, code, stdout, stderr, "inside defer.defer_dir")
	entries, err := os.ReadDir(inner)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("state written into the defer_dir: %v", entries)
	}
	if code, _, stderr := notify(t, m, c0(), unsafe); code != ExitConfig || !strings.Contains(stderr, "inside defer.defer_dir") {
		t.Errorf("send with aliased state_dir: exit %d, stderr %q", code, stderr)
	}
}

// Corrupt state of a queued notification: resend fails closed. Messages
// without a scope are unaffected.
func TestDedupResendCorruptState(t *testing.T) {
	m, status := statusMock(t)
	cfg, deferDir, stateDir := dedupConfig(t, "")
	before := deferNotification(t, m, status, cfg)
	sc := dedup.NewScope("mg.example.com", []string{"ops@example.com"}, "disk-full")
	path := filepath.Join(stateDir, sc.ID()+".json")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runAt(t, m, c0(), "resend", "--force", "-c", cfg)
	failClosed(t, m, deferDir, before, code, stdout, stderr, path)

	// An unscoped message in the same run is still delivered.
	seedDeferred(t, deferDir, "other@example.com", "plain", 0)
	code, stdout, _ = runAt(t, m, c0(), "resend", "--force", "-c", cfg)
	if code != ExitSendFail || !strings.Contains(stdout, "1 sent, 0 skipped, 1 failed") {
		t.Errorf("mixed run: exit %d, stdout %q", code, stdout)
	}
	if n := sends(m); n != before+1 {
		t.Errorf("%d requests, want %d (only the unscoped message)", n, before+1)
	}
}

// An unusable state_dir or lock file fails closed too.
func TestDedupResendLockFailure(t *testing.T) {
	m, status := statusMock(t)
	cfg, deferDir, stateDir := dedupConfig(t, "")
	before := deferNotification(t, m, status, cfg)
	if err := os.RemoveAll(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runAt(t, m, c0(), "resend", "--force", "-c", cfg)
	failClosed(t, m, deferDir, before, code, stdout, stderr, stateDir)

	// The scope's lock file cannot be opened.
	if err := os.Remove(stateDir); err != nil {
		t.Fatal(err)
	}
	sc := dedup.NewScope("mg.example.com", []string{"ops@example.com"}, "disk-full")
	lockPath := filepath.Join(stateDir, sc.ID()[:2]+".lock")
	if err := os.MkdirAll(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runAt(t, m, c0(), "resend", "--force", "-c", cfg)
	failClosed(t, m, deferDir, before, code, stdout, stderr, lockPath)
}

// If the pending attempt cannot be recorded, nothing is sent.
func TestDedupResendBeginFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions enforced")
	}
	m, status := statusMock(t)
	cfg, deferDir, stateDir := dedupConfig(t, "")
	before := deferNotification(t, m, status, cfg)
	// Create the lock file, then make the directory read-only: the lock
	// still works, writing the state does not.
	sc := dedup.NewScope("mg.example.com", []string{"ops@example.com"}, "disk-full")
	g, err := (&dedup.Store{Dir: stateDir}).Acquire(sc.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(stateDir, 0o700); err != nil {
			t.Error(err)
		}
	})
	code, stdout, stderr := runAt(t, m, c0(), "resend", "--force", "-c", cfg)
	failClosed(t, m, deferDir, before, code, stdout, stderr, "recording the attempt")
}

// gateClock pauses the first Now call until release is closed, returning
// first for it and later for every other call.
type gateClock struct {
	mu           sync.Mutex
	calls        int
	first, later time.Time
	entered      chan struct{}
	release      chan struct{}
}

func (c *gateClock) Now() time.Time {
	c.mu.Lock()
	n := c.calls
	c.calls++
	c.mu.Unlock()
	if n == 0 {
		close(c.entered)
		<-c.release
		return c.first
	}
	return c.later
}

// A queued notification that is unexpired at the start of a resend run
// but exceeds max_age before its turn is no longer queued for new sends;
// once a new send has delivered the notification, the resend must remove
// the expired message instead of delivering it a second time.
func TestDedupResendExpiredDuringRun(t *testing.T) {
	m, status := statusMock(t)
	deferDir := filepath.Join(t.TempDir(), "deferred")
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg := writeConfig(t, "from = \"ops@example.com\"\n[defer]\ndefer_dir = '"+deferDir+"'\nmax_age = \"1h\"\n[dedup]\nstate_dir = '"+stateDir+"'\n")
	t0 := time.Now().UTC()
	deferNotification(t, m, status, cfg)
	id := deferredIDs(t, deferDir)[0]
	meta, _ := readDeferred(t, deferDir, id)
	meta.Timestamp = t0
	writeMetaFile(t, deferDir, meta)

	gc := &gateClock{first: t0.Add(30 * time.Minute), later: t0.Add(2 * time.Hour),
		entered: make(chan struct{}), release: make(chan struct{})}
	type result struct {
		code           int
		stdout, stderr string
	}
	resendDone := make(chan result)
	go func() {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"resend", "-c", cfg}, &stdout, &stderr, testInfo,
			WithStdin(strings.NewReader("")), WithBaseURL(m.URL()), WithClock(gc.Now))
		resendDone <- result{code, stdout.String(), stderr.String()}
	}()
	select {
	case <-gc.entered: // the run has listed the message; its expiry sweep is paused
	case <-time.After(10 * time.Second):
		t.Fatal("resend never reached its expiry sweep")
	}

	// Meanwhile the message has expired: a new send no longer counts it
	// as queued and delivers the notification.
	before := sends(m)
	code, stdout, stderr := notify(t, m, &testClock{now: t0.Add(2 * time.Hour)}, cfg)
	if code != ExitOK || strings.HasPrefix(stdout, "skipped") {
		t.Fatalf("new send: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	close(gc.release) // the sweep sees the message unexpired (30m old)
	r := <-resendDone
	if r.code != ExitOK || !strings.Contains(r.stderr, "expired "+id) || !strings.Contains(r.stdout, "0 sent") {
		t.Errorf("resend: exit %d, stdout %q, stderr %q; want the expired message removed, not sent", r.code, r.stdout, r.stderr)
	}
	if n := sends(m); n != before+1 {
		t.Errorf("%d requests, want %d (only the new send)", n, before+1)
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 0 {
		t.Errorf("queue = %v, want the expired message removed", ids)
	}
}

// pauseWriter blocks the first write after its output contains match
// until resume is closed, signaling reached.
type pauseWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	match   string
	once    sync.Once
	reached chan struct{}
	resume  chan struct{}
}

func (w *pauseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf.Write(p)
	hit := strings.Contains(w.buf.String(), w.match)
	w.mu.Unlock()
	if hit {
		w.once.Do(func() {
			close(w.reached)
			<-w.resume
		})
	}
	return len(p), nil
}

// A duplicate sent while a resend has removed the delivered message from
// the queue but not yet recorded the cooldown stays blocked: it waits for
// the scope lock and is then skipped. Fails if resend does not hold the
// scope lock through recording the outcome.
func TestDedupResendPauseBeforeCooldown(t *testing.T) {
	m, status := statusMock(t)
	cfg, deferDir, _ := dedupConfig(t, "")
	status.Store(http.StatusServiceUnavailable)
	notify(t, m, c0(), cfg)
	status.Store(http.StatusOK)

	pw := &pauseWriter{match: "sent, message id", reached: make(chan struct{}), resume: make(chan struct{})}
	resendDone := make(chan int)
	go func() {
		var stderr bytes.Buffer
		resendDone <- Run([]string{"resend", "--force", "-c", cfg}, pw, &stderr, testInfo,
			WithStdin(strings.NewReader("")), WithBaseURL(m.URL()))
	}()
	select {
	case <-pw.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("resend never reported the delivery")
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 0 {
		t.Fatalf("queue = %v, want the delivered message removed", ids)
	}

	type result struct {
		code   int
		stdout string
	}
	sendDone := make(chan result)
	go func() {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"-c", cfg, "--to", "ops@example.com", "--subject", "disk full", "--message", "98%",
			"--dedup-key", "disk-full"}, &stdout, &stderr, testInfo, WithStdin(strings.NewReader("")), WithBaseURL(m.URL()))
		sendDone <- result{code, stdout.String()}
	}()
	select {
	case r := <-sendDone:
		close(pw.resume)
		<-resendDone
		t.Fatalf("duplicate finished while the resend held the delivery uncommitted: exit %d, stdout %q", r.code, r.stdout)
	case <-time.After(300 * time.Millisecond):
	}
	close(pw.resume)
	if code := <-resendDone; code != ExitOK {
		t.Errorf("resend: exit %d", code)
	}
	r := <-sendDone
	if r.code != ExitOK || !strings.HasPrefix(r.stdout, "skipped") || !strings.Contains(r.stdout, "sent ") {
		t.Errorf("duplicate: exit %d, stdout %q, want skipped by the resend's cooldown", r.code, r.stdout)
	}
	if n := sends(m); n != 2 {
		t.Errorf("%d requests, want 2 (deferred attempt and resend)", n)
	}
}
