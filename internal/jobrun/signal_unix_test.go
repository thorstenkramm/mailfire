//go:build !windows

package jobrun

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ignoreTerm makes the helper job ignore SIGTERM.
func ignoreTerm() { signal.Ignore(syscall.SIGTERM) }

func TestTimeoutKillsDescendantAfterParentExits(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// The outer shell dies on TERM; its child ignores TERM and closes
	// its output, so neither parent exit nor pipe EOF proves cleanup.
	res, err := Run(&Spec{
		Path: "sh", Args: []string{"-c", `sh -c 'trap "" TERM; echo $$ > "$1"; exec sleep 30' child "$1" >/dev/null 2>&1 & wait`, "parent", pidFile},
		MaxRuntime: 500 * time.Millisecond, KillGrace: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, res)
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer killDescendant(t, pid)
	if !res.TimedOut {
		t.Fatal("expected timeout")
	}
	// KILL is asynchronous. Allow the OS to reap the orphan.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Linux containers need not reap zombies promptly; a zombie is stopped.
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil && strings.Contains(string(stat), ") Z ") {
		return
	}
	t.Fatal("descendant survived the timeout")
}

func TestSignalName(t *testing.T) {
	for sig, want := range map[syscall.Signal]string{
		syscall.SIGHUP: "HUP", syscall.SIGINT: "INT", syscall.SIGQUIT: "QUIT", syscall.SIGILL: "ILL",
		syscall.SIGABRT: "ABRT", syscall.SIGFPE: "FPE", syscall.SIGKILL: "KILL", syscall.SIGSEGV: "SEGV",
		syscall.SIGPIPE: "PIPE", syscall.SIGALRM: "ALRM", syscall.SIGTERM: "TERM", syscall.SIGUSR1: "USR1",
		syscall.SIGUSR2: "USR2",
	} {
		if got := signalName(sig); got != want {
			t.Errorf("signalName(%d) = %q, want %q", sig, got, want)
		}
	}
	if got := signalName(syscall.SIGWINCH); got == "" {
		t.Error("an unlisted signal must still have a name")
	}
}
