package spool

import (
	"bytes"
	"errors"
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

// Windows cannot delete an open file, and an interrupt arrives exactly
// while a spool is open: being written (attachment encoding) or read (the
// upload). The cleanup must close those handles before removing. Checked
// on every OS through the handles themselves, which Unix would not need.
func TestSignalCleanupClosesOpenHandles(t *testing.T) {
	isolateTempDir(t)
	got := make(chan os.Signal, 1)
	afterCleanup = func(sig os.Signal) { got <- sig }
	t.Cleanup(func() { afterCleanup = reraise })

	writing, err := Create() // being composed: writer open
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writing.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	sending, err := Compose(func(w io.Writer) error { _, err := io.WriteString(w, "message"); return err })
	if err != nil {
		t.Fatal(err)
	}
	upload, err := sending.Open() // being uploaded: reader open
	if err != nil {
		t.Fatal(err)
	}

	ch := make(chan os.Signal, 1)
	stop := watch(ch, func() {})
	defer stop()
	ch <- os.Interrupt
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not run")
	}
	if _, err := upload.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Errorf("upload handle still open after cleanup: read error %v", err)
	}
	if _, err := writing.Write([]byte("x")); err == nil {
		t.Error("writer still usable after cleanup")
	}
	for _, p := range []string{writing.Path(), sending.Path()} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the cleanup: %v", p, err)
		}
	}
}

// Abort likewise closes handles still open on the spool before removing it.
func TestAbortClosesOpenHandles(t *testing.T) {
	isolateTempDir(t)
	s, err := Compose(func(w io.Writer) error { _, err := io.WriteString(w, "message"); return err })
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Errorf("reader still open after Abort: read error %v", err)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Errorf("spool survived Abort: %v", err)
	}
}

// A panic while composing (an encoder bug) must not leave the spool behind.
func TestComposeRemovesSpoolOnPanic(t *testing.T) {
	dir := isolateTempDir(t)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
		}()
		s, err := Compose(func(w io.Writer) error {
			if _, err := io.WriteString(w, "partial"); err != nil {
				return err
			}
			panic("encoder bug")
		})
		t.Errorf("Compose returned (%v, %v) instead of panicking", s, err)
	}()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("left behind after a panic: %s", e.Name())
	}
}

// isolateTempDir points the system temporary directory (TMPDIR on Unix,
// TMP and TEMP on Windows) at a fresh directory and returns it.
func isolateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, dir)
	}
	if os.TempDir() != dir {
		t.Fatalf("os.TempDir() = %q, want %q", os.TempDir(), dir)
	}
	return dir
}

// stop releases the signal delivery and, with no signal delivered, ends
// the handler without acting.
func TestWatchStopWithoutSignal(t *testing.T) {
	called := make(chan os.Signal, 1)
	afterCleanup = func(sig os.Signal) { called <- sig }
	t.Cleanup(func() { afterCleanup = reraise })
	released := make(chan struct{}, 2)
	watch(make(chan os.Signal), func() { released <- struct{}{} })()
	if len(released) == 0 {
		t.Error("stop did not release the signal delivery")
	}
	select {
	case <-called:
		t.Fatal("cleanup ran without a signal")
	case <-time.After(100 * time.Millisecond):
	}
}

// A signal delivered just before stop (the user pressed Ctrl+C as the send
// finished) must still end the process: the default action is suppressed
// while the handler is armed, so a lost signal would leave mailfire running.
// Repeated, because the race between the two is what is under test.
func TestWatchHonoursSignalRacingStop(t *testing.T) {
	called := make(chan os.Signal, 1)
	afterCleanup = func(sig os.Signal) { called <- sig }
	t.Cleanup(func() { afterCleanup = reraise })
	for i := range 200 {
		ch := make(chan os.Signal, 1)
		stop := watch(ch, func() {})
		ch <- os.Interrupt
		stop()
		select {
		case <-called:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: signal lost when it raced stop", i)
		}
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
	if n := removeStale(dir, now); n != 1 {
		t.Errorf("removeStale removed %d, want 1", n)
	}
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
