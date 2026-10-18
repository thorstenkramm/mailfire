// Package fsutil holds small file helpers shared by the packages that
// persist mailfire state.
package fsutil

import (
	"errors"
	"os"
)

// WriteSyncClose writes data to f, syncs it to disk, and closes f. The
// caller opens f with the flags and permissions it needs; f is closed on
// every path, and the first failure is returned together with any close
// error.
func WriteSyncClose(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}
