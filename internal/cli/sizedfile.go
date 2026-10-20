package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// sizedFile streams an attachment or inline image and fails when the file
// does not keep the size it had when it was opened: a file truncated or
// extended while being read (a log being rotated, a backup still being
// written) would otherwise be sent incomplete or inconsistent without any
// error. The error surfaces while the message is composed, so it is
// permanent: nothing is sent or deferred (task 34).
//
// sizedFile deliberately has no WriteTo or ReadFrom: io.Copy must go
// through Read, or it would bypass the check (*os.File has a WriteTo).
type sizedFile struct {
	f         *os.File
	want, got int64
}

// newSizedFile records f's current size. On error f is left open; the
// caller closes it.
func newSizedFile(f *os.File) (*sizedFile, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Name(), err)
	}
	return &sizedFile{f: f, want: fi.Size()}, nil
}

func (s *sizedFile) Read(p []byte) (int, error) {
	n, err := s.f.Read(p)
	s.got += int64(n)
	switch {
	case s.got > s.want:
		return n, fmt.Errorf("%s: file grew while being read (%d bytes when opened)", s.f.Name(), s.want)
	case errors.Is(err, io.EOF) && s.got < s.want:
		return n, fmt.Errorf("%s: file shrank while being read (%d of %d bytes)", s.f.Name(), s.got, s.want)
	}
	return n, err
}

// Close closes the file; closeFiles finds it through io.Closer.
func (s *sizedFile) Close() error { return s.f.Close() }

// Name is the file's path, for error messages and previews.
func (s *sizedFile) Name() string { return s.f.Name() }

// openSized opens path like openRegular and wraps it in a sizedFile.
func openSized(kind, path string) (*sizedFile, error) {
	f, err := openRegular(kind, path)
	if err != nil {
		return nil, err
	}
	s, err := newSizedFile(f)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%s %w", kind, err), f.Close())
	}
	return s, nil
}
