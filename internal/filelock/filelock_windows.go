//go:build windows

package filelock

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var procLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

const (
	lockfileFailImmediately = 0x1 // LOCKFILE_FAIL_IMMEDIATELY
	lockfileExclusiveLock   = 0x2 // LOCKFILE_EXCLUSIVE_LOCK
	errorLockViolation      = syscall.Errno(33)
)

// lockFile takes an exclusive lock on the first byte of f, waiting for
// it when wait is set and failing with errBusy otherwise. The lock is
// released when f is closed, including when the process dies.
func lockFile(f *os.File, wait bool) error {
	flags := uintptr(lockfileExclusiveLock)
	if !wait {
		flags |= lockfileFailImmediately
	}
	var ol syscall.Overlapped
	r, _, err := procLockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r == 0 {
		if errors.Is(err, errorLockViolation) {
			return errBusy
		}
		return err
	}
	return nil
}
