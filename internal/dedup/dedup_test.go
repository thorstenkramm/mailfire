package dedup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/filelock"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

// clock is a controllable, goroutine-safe clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func newStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	c := &clock{now: t0}
	return &Store{Dir: filepath.Join(t.TempDir(), "dedup"), Now: c.Now}, c
}

var scope = NewScope("mg.example.com", []string{"ops@example.com"}, "disk-full")

// acquire locks scope, failing the test on error.
func acquire(t *testing.T, s *Store, id string) *Guard {
	t.Helper()
	g, err := s.Acquire(id)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	return g
}

// deliver runs one complete, successful notification at the store's now.
func deliver(t *testing.T, s *Store, sc Scope, cooldown time.Duration) {
	t.Helper()
	g := acquire(t, s, sc.ID())
	defer release(t, g)
	if skip := g.Check(""); skip != nil {
		t.Fatalf("Check = %+v, want send", skip)
	}
	if err := g.Begin(&sc); err != nil {
		t.Fatal(err)
	}
	if err := g.Sent(s.now(), cooldown); err != nil {
		t.Fatal(err)
	}
}

func release(t *testing.T, g *Guard) {
	t.Helper()
	if err := g.Release(); err != nil {
		t.Fatal(err)
	}
}

// check returns the decision for sc at the store's now.
func checkAt(t *testing.T, s *Store, sc Scope, queued string) *Skip {
	t.Helper()
	g := acquire(t, s, sc.ID())
	defer release(t, g)
	return g.Check(queued)
}

func TestScopeNormalization(t *testing.T) {
	a := NewScope("MG.Example.com", []string{"B@example.com", "a@example.com", "b@EXAMPLE.com"}, "k")
	b := NewScope("mg.example.com", []string{"a@example.com", "b@example.com"}, "k")
	if a.ID() != b.ID() {
		t.Errorf("case, order, and duplicates changed the scope: %+v vs %+v", a, b)
	}
	if len(a.ID()) != 64 {
		t.Errorf("ID length = %d", len(a.ID()))
	}
	for _, other := range []Scope{
		NewScope("other.example.com", []string{"a@example.com", "b@example.com"}, "k"),
		NewScope("mg.example.com", []string{"a@example.com"}, "k"),
		NewScope("mg.example.com", []string{"a@example.com", "b@example.com", "c@example.com"}, "k"),
		NewScope("mg.example.com", []string{"a@example.com", "b@example.com"}, "K"),
		NewScope("mg.example.com", []string{"a@example.com", "b@example.com"}, "k "),
	} {
		if other.ID() == b.ID() {
			t.Errorf("%+v shares the scope of %+v", other, b)
		}
	}
	// Field boundaries are unambiguous.
	if NewScope("d", []string{"a,b"}, "k").ID() == NewScope("d", []string{"a", "b"}, "k").ID() {
		t.Error("recipient boundaries are ambiguous")
	}
}

func TestCooldown(t *testing.T) {
	s, c := newStore(t)
	deliver(t, s, scope, time.Hour)

	c.Set(t0.Add(59 * time.Minute))
	skip := checkAt(t, s, scope, "")
	if skip == nil || skip.Reason != Cooldown || !skip.Since.Equal(t0) || !skip.Eligible.Equal(t0.Add(time.Hour)) {
		t.Fatalf("within cooldown: skip = %+v", skip)
	}
	c.Set(t0.Add(time.Hour))
	if skip := checkAt(t, s, scope, ""); skip != nil {
		t.Errorf("after cooldown: skip = %+v, want send", skip)
	}
	// Other scopes are unaffected.
	c.Set(t0.Add(time.Minute))
	for _, other := range []Scope{
		NewScope("mg.example.com", []string{"dev@example.com"}, "disk-full"),
		NewScope("other.example.com", []string{"ops@example.com"}, "disk-full"),
		NewScope("mg.example.com", []string{"ops@example.com"}, "cpu-high"),
	} {
		if skip := checkAt(t, s, other, ""); skip != nil {
			t.Errorf("%+v: skip = %+v, want send", other, skip)
		}
	}
}

// Skips change nothing: checking never extends the cooldown.
func TestSkipDoesNotExtend(t *testing.T) {
	s, c := newStore(t)
	deliver(t, s, scope, time.Hour)
	for _, m := range []time.Duration{10, 30, 59} {
		c.Set(t0.Add(m * time.Minute))
		if skip := checkAt(t, s, scope, ""); skip == nil {
			t.Fatalf("minute %d: not suppressed", m)
		}
	}
	c.Set(t0.Add(time.Hour))
	if skip := checkAt(t, s, scope, ""); skip != nil {
		t.Errorf("skips extended the cooldown: %+v", skip)
	}
}

func TestQueuedSkips(t *testing.T) {
	s, _ := newStore(t)
	skip := checkAt(t, s, scope, "20261017T120000.000000000-0123456789abcdef")
	if skip == nil || skip.Reason != Queued || skip.QueuedID == "" {
		t.Errorf("skip = %+v, want queued", skip)
	}
}

// An attempt that never recorded its outcome (crash: the lock is
// released by the OS) is in doubt for RecoveryDelay, then allowed.
func TestInterruptedAttempt(t *testing.T) {
	s, c := newStore(t)
	g := acquire(t, s, scope.ID())
	if err := g.Begin(&scope); err != nil {
		t.Fatal(err)
	}
	release(t, g) // "crash": no Sent or Abort

	c.Set(t0.Add(59 * time.Second))
	skip := checkAt(t, s, scope, "")
	if skip == nil || skip.Reason != InDoubt || !skip.Eligible.Equal(t0.Add(RecoveryDelay)) {
		t.Fatalf("skip = %+v, want in doubt until %v", skip, t0.Add(RecoveryDelay))
	}
	c.Set(t0.Add(RecoveryDelay))
	if skip := checkAt(t, s, scope, ""); skip != nil {
		t.Errorf("after recovery delay: skip = %+v, want send", skip)
	}
}

// A live sender holds the lock: no elapsed time lets another process
// past it, however old its attempt is.
func TestLiveSenderBlocks(t *testing.T) {
	s, c := newStore(t)
	g := acquire(t, s, scope.ID())
	if err := g.Begin(&scope); err != nil {
		t.Fatal(err)
	}
	c.Set(t0.Add(24 * time.Hour))

	done := make(chan *Skip)
	go func() {
		g2, err := s.Acquire(scope.ID())
		if err != nil {
			t.Error(err)
			close(done)
			return
		}
		defer func() {
			if err := g2.Release(); err != nil {
				t.Error(err)
			}
		}()
		done <- g2.Check("")
	}()
	select {
	case <-done:
		t.Fatal("a second sender got past a live one")
	case <-time.After(150 * time.Millisecond):
	}
	if err := g.Sent(c.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	release(t, g)
	select {
	case skip := <-done:
		if skip == nil || skip.Reason != Cooldown {
			t.Errorf("after the live sender delivered: skip = %+v, want cooldown", skip)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second sender never acquired the lock")
	}
}

// Failed and deferred attempts start no cooldown.
func TestAbortStartsNoCooldown(t *testing.T) {
	s, _ := newStore(t)
	g := acquire(t, s, scope.ID())
	if err := g.Begin(&scope); err != nil {
		t.Fatal(err)
	}
	if err := g.Abort(); err != nil {
		t.Fatal(err)
	}
	release(t, g)
	if skip := checkAt(t, s, scope, ""); skip != nil {
		t.Errorf("skip = %+v after a failed attempt, want send", skip)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, scope.ID()+".json")); !os.IsNotExist(err) {
		t.Errorf("state file kept after Abort: %v", err)
	}
}

// A clock that moved backward resets cooldowns and in-doubt attempts.
func TestClockBackward(t *testing.T) {
	s, c := newStore(t)
	deliver(t, s, scope, time.Hour)
	c.Set(t0.Add(-time.Minute))
	if skip := checkAt(t, s, scope, ""); skip != nil {
		t.Errorf("cooldown after clock moved back: skip = %+v, want send", skip)
	}

	c.Set(t0)
	g := acquire(t, s, scope.ID())
	if err := g.Begin(&scope); err != nil {
		t.Fatal(err)
	}
	release(t, g)
	c.Set(t0.Add(-time.Second))
	if skip := checkAt(t, s, scope, ""); skip != nil {
		t.Errorf("in doubt after clock moved back: skip = %+v, want send", skip)
	}
	// Queued duplicates are still skipped.
	if skip := checkAt(t, s, scope, "id"); skip == nil || skip.Reason != Queued {
		t.Errorf("queued after clock moved back: skip = %+v", skip)
	}
}

func TestCorruptStateIsAnError(t *testing.T) {
	s, _ := newStore(t)
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, scope.ID()+".json")
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Acquire(scope.ID())
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("err = %v, want corrupt state error naming %s", err, path)
	}
	// The lock was released: the scope can be locked again.
	unlock, ok, err := filelock.TryLock(s.lockPath(scope.ID()))
	if err != nil || !ok {
		t.Fatalf("lock still held after the error: ok=%v err=%v", ok, err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireErrors(t *testing.T) {
	if _, err := (&Store{}).Acquire(scope.ID()); err == nil {
		t.Error("no Dir: want error")
	}
	s, _ := newStore(t)
	for _, id := range []string{"", "../x", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if _, err := s.Acquire(id); err == nil {
			t.Errorf("Acquire(%q): want error", id)
		}
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Store{Dir: filepath.Join(file, "dedup")}).Acquire(scope.ID()); err == nil {
		t.Error("state dir below a file: want error")
	}
}

func TestStateFilePermissions(t *testing.T) {
	s, _ := newStore(t)
	deliver(t, s, scope, time.Hour)
	fi, err := os.Stat(filepath.Join(s.Dir, scope.ID()+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("state file mode = %v, want owner-only", fi.Mode().Perm())
	}
}

// Concurrent senders of one notification: exactly one gets through.
func TestConcurrentSendersOneDelivery(t *testing.T) {
	s := &Store{Dir: filepath.Join(t.TempDir(), "dedup")}
	var sent atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			g, err := s.Acquire(scope.ID())
			if err != nil {
				t.Error(err)
				return
			}
			defer func() {
				if err := g.Release(); err != nil {
					t.Error(err)
				}
			}()
			if g.Check("") != nil {
				return
			}
			if err := g.Begin(&scope); err != nil {
				t.Error(err)
				return
			}
			time.Sleep(5 * time.Millisecond) // the network request
			sent.Add(1)
			if err := g.Sent(time.Now(), time.Hour); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := sent.Load(); n != 1 {
		t.Errorf("%d deliveries, want 1", n)
	}
}

func TestCleanup(t *testing.T) {
	s, c := newStore(t)
	expired := NewScope("mg.example.com", []string{"a@example.com"}, "expired")
	active := NewScope("mg.example.com", []string{"a@example.com"}, "active")
	inDoubt := NewScope("mg.example.com", []string{"a@example.com"}, "in-doubt")
	deliver(t, s, expired, time.Minute)
	deliver(t, s, active, time.Hour)
	g := acquire(t, s, inDoubt.ID())
	if err := g.Begin(&inDoubt); err != nil {
		t.Fatal(err)
	}
	release(t, g)

	corruptID := strings.Repeat("c", 64)
	stale := filepath.Join(s.Dir, strings.Repeat("d", 64)+".json.tmp")
	foreign := filepath.Join(s.Dir, "notes.txt")
	for path, data := range map[string]string{
		filepath.Join(s.Dir, corruptID+".json"): "{nope",
		stale:                                   "{}",
		foreign:                                 "keep me",
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	c.Set(t0.Add(30 * time.Second))
	if err := s.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	exists := func(id string) bool {
		_, err := os.Stat(filepath.Join(s.Dir, id+".json"))
		return err == nil
	}
	if !exists(expired.ID()) || !exists(active.ID()) || !exists(inDoubt.ID()) {
		t.Error("Cleanup removed live state")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale temporary file kept")
	}

	c.Set(t0.Add(2 * time.Minute))
	// A stripe in use is skipped, not waited for.
	unlock, err := filelock.Lock(s.lockPath(active.ID()))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if exists(expired.ID()) && expired.ID()[:2] != active.ID()[:2] {
		t.Error("expired state kept")
	}
	if exists(inDoubt.ID()) && inDoubt.ID()[:2] != active.ID()[:2] {
		t.Error("in-doubt state kept after the recovery delay")
	}
	if !exists(active.ID()) || !exists(corruptID) {
		t.Error("Cleanup removed active or corrupt state")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("Cleanup removed a foreign file")
	}

	c.Set(t0.Add(2 * time.Hour))
	if err := s.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if exists(expired.ID()) || exists(active.ID()) || exists(inDoubt.ID()) {
		t.Error("expired state kept")
	}
	if err := (&Store{Dir: filepath.Join(t.TempDir(), "missing")}).Cleanup(); err != nil {
		t.Errorf("Cleanup of a missing dir: %v", err)
	}
}

// Sent keeps the scope details recorded by Begin, and a resend (Sent
// without Begin) clears an abandoned attempt.
func TestSentAfterInterruptedAttempt(t *testing.T) {
	s, c := newStore(t)
	g := acquire(t, s, scope.ID())
	if err := g.Begin(&scope); err != nil {
		t.Fatal(err)
	}
	release(t, g)
	c.Set(t0.Add(10 * time.Second))
	g = acquire(t, s, scope.ID())
	if err := g.Sent(c.Now(), 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	release(t, g)
	data, err := os.ReadFile(filepath.Join(s.Dir, scope.ID()+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"disk-full"`) || strings.Contains(string(data), "sending") {
		t.Errorf("state = %s", data)
	}
	c.Set(t0.Add(5 * time.Minute))
	if skip := checkAt(t, s, scope, ""); skip == nil || skip.Reason != Cooldown {
		t.Errorf("skip = %+v, want cooldown", skip)
	}
}

// TestHelperProcess is one "mailfire send" in a separate process for
// TestConcurrentProcesses; it is a no-op in normal test runs.
func TestHelperProcess(t *testing.T) {
	dir := os.Getenv("MAILFIRE_DEDUP_HELPER_DIR")
	if dir == "" {
		return
	}
	s := &Store{Dir: dir}
	g, err := s.Acquire(scope.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer release(t, g)
	if g.Check("") != nil {
		return
	}
	if err := g.Begin(&scope); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond) // the network request
	f, err := os.OpenFile(filepath.Join(dir, "deliveries"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("sent\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := g.Sent(time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
}

// Concurrent mailfire processes cannot both get past the cooldown.
func TestConcurrentProcesses(t *testing.T) {
	if os.Getenv("MAILFIRE_DEDUP_HELPER_DIR") != "" {
		t.Skip("helper process")
	}
	dir := t.TempDir()
	var cmds []*exec.Cmd
	for range 8 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		cmd.Env = append(os.Environ(), "MAILFIRE_DEDUP_HELPER_DIR="+dir)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Errorf("helper: %v", err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "deliveries"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "sent"); n != 1 {
		t.Errorf("%d deliveries across processes, want 1", n)
	}
}

// An attempt that fails while a cooldown is running (a resend of an older
// queued message) keeps the cooldown; an expired cooldown is dropped.
func TestBeginAbortKeepsActiveCooldown(t *testing.T) {
	s, c := newStore(t)
	deliver(t, s, scope, time.Hour)
	c.Set(t0.Add(10 * time.Minute))
	g := acquire(t, s, scope.ID())
	if err := g.Begin(nil); err != nil {
		t.Fatal(err)
	}
	if !g.Begun() {
		t.Error("Begun = false after Begin")
	}
	if err := g.Abort(); err != nil {
		t.Fatal(err)
	}
	if g.Begun() {
		t.Error("Begun = true after Abort")
	}
	release(t, g)
	if skip := checkAt(t, s, scope, ""); skip == nil || skip.Reason != Cooldown || !skip.Eligible.Equal(t0.Add(time.Hour)) {
		t.Errorf("skip = %+v, want the running cooldown kept", skip)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, scope.ID()+".json"))
	if err != nil || !strings.Contains(string(data), `"disk-full"`) {
		t.Errorf("Begin(nil) lost the scope details: %s %v", data, err)
	}

	c.Set(t0.Add(2 * time.Hour))
	g = acquire(t, s, scope.ID())
	if err := g.Begin(nil); err != nil {
		t.Fatal(err)
	}
	if err := g.Abort(); err != nil {
		t.Fatal(err)
	}
	release(t, g)
	if _, err := os.Stat(filepath.Join(s.Dir, scope.ID()+".json")); !os.IsNotExist(err) {
		t.Errorf("expired state kept after a failed attempt: %v", err)
	}
}
