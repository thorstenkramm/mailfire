package spool

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

func TestFileLifecycle(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s, err := Create()
	if err != nil {
		t.Fatal(err)
	}
	path := s.Path()
	t.Cleanup(func() { testutil.Check(t, s.Abort) })
	if _, err := s.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(); err != nil {
		t.Fatal(err)
	}
	if s.Size() != 7 {
		t.Fatalf("Size = %d, want 7", s.Size())
	}
	var got bytes.Buffer
	if err := s.CopyTo(&got); err != nil {
		t.Fatal(err)
	}
	if got.String() != "payload" {
		t.Fatalf("body = %q", got.String())
	}
	if err := s.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat after Abort = %v", err)
	}
}

// The signal handler itself, fed through a channel so it runs on every OS
// (Windows cannot signal its own process; spool_unix_test.go covers the
// real SIGTERM path).
func TestWatchRemovesLiveSpoolsOnSignal(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("TMP", os.Getenv("TMPDIR"))
	t.Setenv("TEMP", os.Getenv("TMPDIR"))
	got := make(chan os.Signal, 1)
	afterCleanup = func(sig os.Signal) { got <- sig }
	t.Cleanup(func() { afterCleanup = reraise })

	s, err := Compose(func(w io.Writer) error { _, err := io.WriteString(w, "message"); return err })
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan os.Signal, 1)
	released := make(chan struct{}, 2)
	stop := watch(ch, func() { released <- struct{}{} })
	defer stop()
	ch <- os.Interrupt
	select {
	case sig := <-got:
		if sig != os.Interrupt {
			t.Errorf("signal = %v", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not run")
	}
	if len(released) == 0 {
		t.Error("signal delivery was not released before ending the process")
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatalf("spool survived the signal: %v", err)
	}
}

// After stop, a signal no longer triggers the cleanup: the spool of the
// next send must not be removed by a stale handler.
func TestWatchStopDisarms(t *testing.T) {
	called := make(chan os.Signal, 1)
	afterCleanup = func(sig os.Signal) { called <- sig }
	t.Cleanup(func() { afterCleanup = reraise })
	ch := make(chan os.Signal, 1)
	watch(ch, func() {})()
	ch <- os.Interrupt
	select {
	case <-called:
		t.Fatal("cleanup ran after stop")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRemoveStaleKeepsRecentAndForeignFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stale := write("mailfire-message-1.spool", 2*staleAge)
	recent := write("mailfire-message-2.spool", time.Minute)
	foreign := write("other-1.spool", 2*staleAge)
	removeStale(dir, now)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale spool kept: %v", err)
	}
	for _, p := range []string{recent, foreign} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}
}

func TestHTTPBodyRewinds(t *testing.T) {
	body, getBody := HTTPBody(bytes.NewReader([]byte("payload")), 7)
	if getBody == nil {
		t.Fatal("no GetBody for an io.ReaderAt")
	}
	for range 2 {
		got, err := io.ReadAll(body)
		if err != nil || string(got) != "payload" {
			t.Fatalf("body = %q, %v", got, err)
		}
		if body, err = getBody(); err != nil {
			t.Fatal(err)
		}
	}
	if _, getBody := HTTPBody(io.MultiReader(), 0); getBody != nil {
		t.Error("GetBody for a reader that cannot rewind")
	}
}
