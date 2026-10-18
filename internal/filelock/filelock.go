// Package filelock provides exclusive, OS-level advisory locks on lock
// files, shared by the deferral queue and notification deduplication.
// The OS releases a lock when its holder exits, so a crash never leaves
// a lock held.
package filelock

import (
	"errors"
	"fmt"
	"os"
)

// filePerm makes lock files owner-only, like every other mailfire file.
const filePerm = 0o600

// Lock blocks until this process holds the exclusive lock on path,
// creating the file if needed, and returns the function that releases
// it. The parent directory must exist.
func Lock(path string) (unlock func() error, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, filePerm)
	if err != nil {
		return nil, fmt.Errorf("opening lock file %s: %w", path, err)
	}
	if err := lockFile(f, true); err != nil {
		return nil, errors.Join(fmt.Errorf("locking %s: %w", path, err), f.Close())
	}
	return f.Close, nil
}

// TryLock is Lock without waiting: ok is false (and err nil) when
// another holder has the lock.
func TryLock(path string) (unlock func() error, ok bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, filePerm)
	if err != nil {
		return nil, false, fmt.Errorf("opening lock file %s: %w", path, err)
	}
	if err := lockFile(f, false); err != nil {
		cerr := f.Close()
		if errors.Is(err, errBusy) {
			return nil, false, cerr
		}
		return nil, false, errors.Join(fmt.Errorf("locking %s: %w", path, err), cerr)
	}
	return f.Close, true, nil
}

// errBusy reports a lock held elsewhere in non-blocking mode.
var errBusy = errors.New("lock held by another process")
