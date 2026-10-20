//go:build windows

package jobrun

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This file is the Windows half of job execution. Windows has no process
// groups and no signals in the Unix sense: the job is assigned to a Job
// Object, and --max-runtime and interruption terminate the whole process
// tree through it. A capture file cannot be removed while it is open, so
// Close removes it.

// Start suspended so the Job Object can be assigned before user code runs.
func setPgid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED}
}

// resolveCommand runs batch files through cmd /c and PowerShell scripts
// through powershell -File; .exe and .com files run directly. The
// extension is taken from the PATH-resolved file, so "backup" finds
// backup.cmd via PATHEXT.
func resolveCommand(path string, args []string) (string, []string) {
	resolved := path
	if p, err := exec.LookPath(path); err == nil {
		resolved = p
	}
	switch strings.ToLower(filepath.Ext(resolved)) {
	case ".bat", ".cmd":
		return "cmd", append([]string{"/c", resolved}, args...)
	case ".ps1":
		return "powershell", append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", resolved}, args...)
	}
	return path, args
}

// proc is the running job's platform handle: the Job Object the process
// tree is assigned to.
type proc struct {
	cmd *exec.Cmd
	job windows.Handle
}

func startProc(cmd *exec.Cmd) (proc, error) {
	p := proc{cmd: cmd}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return p, fmt.Errorf("creating job object: %w", err)
	}
	p.job = job
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		p.close()
		return proc{}, fmt.Errorf("configuring job object: %w", err)
	}
	if err := cmd.Start(); err != nil {
		p.close()
		return proc{}, err
	}
	fail := func(err error) (proc, error) {
		killProcess(cmd)
		if waitErr := cmd.Wait(); waitErr != nil {
			var exit *exec.ExitError
			if !errors.As(waitErr, &exit) {
				err = errors.Join(err, waitErr)
			}
		}
		p.close()
		return proc{}, err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(fmt.Errorf("opening job process: %w", err))
	}
	defer closeHandle(handle)
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		return fail(fmt.Errorf("assigning job process: %w", err))
	}
	// The primary thread is still suspended, so no child can escape the
	// job object before assignment. os/exec does not expose its thread
	// handle; find it through the documented Toolhelp API.
	if err := resumeProcess(uint32(cmd.Process.Pid)); err != nil {
		return fail(err)
	}
	return p, nil
}

func resumeProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("listing job threads: %w", err)
	}
	defer closeHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return fmt.Errorf("opening job thread: %w", err)
		}
		_, err = windows.ResumeThread(thread)
		return errors.Join(err, windows.CloseHandle(thread))
	}
	return fmt.Errorf("finding suspended job thread: %w", err)
}

// terminate stops the job's process tree. Windows has no graceful
// terminate for console processes, so termination is immediate.
func (p proc) terminate() {
	if p.job != 0 {
		if err := windows.TerminateJobObject(p.job, 1); err != nil {
			log.Printf("mailfire: terminating job object: %v", err)
		}
		return
	}
	killProcess(p.cmd)
}

// kill is terminate: the Job Object already ended the tree.
func (p proc) kill() { p.terminate() }

// TerminateJobObject ends all members immediately; no grace period is needed.
func (p proc) running() bool { return false }

// forward ends the job: there is no signal to forward on Windows.
func (p proc) forward(os.Signal) { p.terminate() }

// close releases the Job Object handle after the job has exited.
func (p *proc) close() {
	if p.job != 0 {
		closeHandle(p.job)
		p.job = 0
	}
}

func closeHandle(handle windows.Handle) {
	if err := windows.CloseHandle(handle); err != nil {
		log.Printf("mailfire: closing job handle: %v", err)
	}
}

func killProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			log.Printf("mailfire: killing job process: %v", err)
		}
	}
}

// signalName never reports a name: Windows jobs do not die from signals.
func signalName(sig syscall.Signal) string { return "" }

// unlinkOnCreate is a no-op: an open file cannot be removed on Windows.
func unlinkOnCreate(f captureFile) {}

// removeTempFile removes the capture file on Close.
func removeTempFile(name string, closeErr error) error {
	if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
