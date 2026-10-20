// Package spool owns temporary, owner-only files used to compose transport
// payloads before a provider is contacted. A completed File can be opened
// repeatedly for sending and deferral without retaining its contents in memory.
package spool

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// File is one temporary transport payload. Create returns it open for writing;
// Finish must succeed before Open or Size is used.
type File struct {
	path string
	f    *os.File
	size int64
}

// Create creates an owner-only spool in the system temporary directory.
func Create() (*File, error) {
	f, err := os.CreateTemp("", "mailfire-message-*.spool")
	if err != nil {
		return nil, fmt.Errorf("spool: create: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("spool: permissions: %w", err), f.Close(), os.Remove(f.Name()))
	}
	return &File{path: f.Name(), f: f}, nil
}

// Write appends to an unfinished spool.
func (s *File) Write(p []byte) (int, error) {
	if s == nil || s.f == nil {
		return 0, errors.New("spool: file is not open for writing")
	}
	return s.f.Write(p)
}

// Finish syncs and closes the spool, then records its size.
func (s *File) Finish() error {
	if s == nil || s.f == nil {
		return errors.New("spool: file is not open for writing")
	}
	f := s.f
	s.f = nil
	if err := f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("spool: sync: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("spool: close: %w", err)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return fmt.Errorf("spool: stat: %w", err)
	}
	s.size = info.Size()
	return nil
}

// Abort closes and removes a partial or completed spool. A missing file is
// success, which also makes this safe after deferral renamed the spool.
func (s *File) Abort() error {
	if s == nil {
		return nil
	}
	var err error
	if s.f != nil {
		err = s.f.Close()
		s.f = nil
	}
	if rerr := os.Remove(s.path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		err = errors.Join(err, rerr)
	}
	return err
}

// Open opens a completed spool at its beginning.
func (s *File) Open() (*os.File, error) {
	if s == nil || s.f != nil {
		return nil, errors.New("spool: file is not complete")
	}
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("spool: open: %w", err)
	}
	return f, nil
}

// Size returns the completed payload size.
func (s *File) Size() int64 { return s.size }

// Path returns the temporary pathname. It is for atomically adopting the
// spool into persistent storage; providers should use Open.
func (s *File) Path() string { return s.path }

// CopyTo copies a completed spool to w from the beginning.
func (s *File) CopyTo(w io.Writer) error {
	f, err := s.Open()
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(w, f)
	return errors.Join(copyErr, f.Close())
}
