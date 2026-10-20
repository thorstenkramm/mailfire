//go:build unix

package spool

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/signals"
)

// Real signal delivery: SIGTERM (kill) and SIGHUP (closed terminal,
// dropped ssh session) both remove the live spools.
func TestCleanupOnSignalRemovesLiveSpools(t *testing.T) {
	isolateTempDir(t)
	got := make(chan os.Signal, 1)
	afterCleanup = func(sig os.Signal) { got <- sig }
	t.Cleanup(func() { afterCleanup = reraise })

	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		if signals.IgnoredAtStart(sig) {
			// Under nohup (or a runner ignoring SIGHUP) the cleanup rightly
			// leaves the signal ignored; sending it would test nothing.
			t.Logf("%v inherited as ignored; skipped", sig)
			continue
		}
		s, err := Create()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(); err != nil {
			t.Fatal(err)
		}
		stop := CleanupOnSignal()
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			t.Fatal(err)
		}
		select {
		case received := <-got:
			if received != sig {
				t.Errorf("signal = %v, want %v", received, sig)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%v: cleanup did not run", sig)
		}
		stop()
		if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
			t.Fatalf("%v: spool survived the signal: %v", sig, err)
		}
	}
}

const ignoredChildEnv = "MAILFIRE_SPOOL_IGNORED_CHILD"

// A signal the process inherits as ignored ("mailfire send … &" in a
// script, nohup) must stay ignored: the send goes on, the spool is kept.
// The child runs with SIGINT ignored by the shell, as for a background job,
// and first notifies and stops SIGINT as "mailfire run" does for its job —
// which makes signal.Ignored forget the inherited ignore.
func TestInheritedIgnoredSignalStaysIgnored(t *testing.T) {
	if os.Getenv(ignoredChildEnv) == "1" {
		ignoredChild()
		return
	}
	cmd := exec.Command("/bin/sh", "-c",
		`trap '' INT; exec "$0" -test.run='^TestInheritedIgnoredSignalStaysIgnored$'`, os.Args[0])
	dir := t.TempDir()
	cmd.Env = append(os.Environ(), ignoredChildEnv+"=1", "TMPDIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

// ignoredChild sends itself the ignored SIGINT while the handler is armed
// and exits 0 only if it survived with its spool intact.
func ignoredChild() {
	fail := func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
		os.Exit(1)
	}
	if !signal.Ignored(syscall.SIGINT) {
		fail("SIGINT is not ignored in the child; the test setup is broken")
	}
	jobSignals := make(chan os.Signal, 1) // as "mailfire run" forwards to its job
	signal.Notify(jobSignals, syscall.SIGINT)
	signal.Stop(jobSignals)
	s, err := Create()
	if err != nil {
		fail("Create: %v", err)
	}
	stop := CleanupOnSignal()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		fail("kill: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	stop()
	if _, err := os.Stat(s.Path()); err != nil {
		fail("the ignored SIGINT removed the spool: %v", err)
	}
	if err := s.Abort(); err != nil {
		fail("Abort: %v", err)
	}
	os.Exit(0)
}
