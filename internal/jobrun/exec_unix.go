//go:build !windows

package jobrun

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"syscall"
)

// This file is the Unix half of job execution: the job runs in its own
// process group, so --max-runtime and interruption can signal the whole
// tree, and capture files are unlinked right after creation.

// setPgid runs the job in its own process group (its pgid equals its
// pid), so signals reach the job's children too.
func setPgid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// resolveCommand returns the command unchanged: exec looks it up in PATH
// and the kernel handles scripts with a shebang line.
func resolveCommand(path string, args []string) (string, []string) { return path, args }

// proc is the running job's platform handle.
type proc struct{ cmd *exec.Cmd }

func startProc(cmd *exec.Cmd) (proc, error) {
	return proc{cmd}, cmd.Start()
}

// terminate asks the job's process group to stop (SIGTERM).
func (p proc) terminate() { signalGroup(p.cmd, syscall.SIGTERM) }

// kill force-stops the job's process group (SIGKILL) after the grace
// period.
func (p proc) kill() { signalGroup(p.cmd, syscall.SIGKILL) }

// Descendants may outlive the group leader, including after SIGTERM.
func (p proc) running() bool {
	err := syscall.Kill(-p.cmd.Process.Pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// forward passes a signal mailfire received on to the job's process
// group.
func (p proc) forward(sig os.Signal) {
	if s, ok := sig.(syscall.Signal); ok {
		signalGroup(p.cmd, s)
	}
}

// close releases the platform handle after the job has exited. There is
// nothing to release on Unix.
func (p proc) close() {}

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	// The process may already have exited: ESRCH is expected, and the
	// waiter reports the job's outcome either way.
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		log.Printf("mailfire: signaling job process group: %v", err)
	}
}

// signalName maps a signal number to its conventional name for the
// report's "killed by signal 9 (KILL)" detail.
func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGHUP:
		return "HUP"
	case syscall.SIGINT:
		return "INT"
	case syscall.SIGQUIT:
		return "QUIT"
	case syscall.SIGILL:
		return "ILL"
	case syscall.SIGABRT:
		return "ABRT"
	case syscall.SIGFPE:
		return "FPE"
	case syscall.SIGKILL:
		return "KILL"
	case syscall.SIGSEGV:
		return "SEGV"
	case syscall.SIGPIPE:
		return "PIPE"
	case syscall.SIGALRM:
		return "ALRM"
	case syscall.SIGTERM:
		return "TERM"
	case syscall.SIGUSR1:
		return "USR1"
	case syscall.SIGUSR2:
		return "USR2"
	}
	return sig.String()
}

// unlinkOnCreate removes the temporary capture file's directory entry:
// the open handle keeps working, and no crash can leave the file behind.
func unlinkOnCreate(f captureFile) {
	if err := os.Remove(f.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// Not fatal: Close's removal covers the normal exits.
		_ = err
	}
}

// Normally already unlinked; retry if unlinking at creation failed.
func removeTempFile(name string, closeErr error) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
