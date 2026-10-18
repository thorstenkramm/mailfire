//go:build !windows

package filelock

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on f, waiting for it when
// wait is set and failing with errBusy otherwise. The lock is released
// when f is closed, including when the process dies.
func lockFile(f *os.File, wait bool) error {
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	for {
		err := syscall.Flock(int(f.Fd()), how)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return errBusy
		}
		return err
	}
}
