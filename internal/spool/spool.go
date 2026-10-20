// Package spool owns temporary, owner-only files used to compose transport
// payloads before a provider is contacted. A completed File can be opened
// repeatedly for sending and deferral without retaining its contents in memory.
//
// Spools must not outlive the send attempt: the caller removes them (Abort)
// or adopts them into the deferral store. CleanupOnSignal covers SIGINT and
// SIGTERM, which end the process without running deferred calls, and Create
// sweeps spools left behind by a process that was killed outright.
package spool

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	namePrefix = "mailfire-message-"
	nameSuffix = ".spool"
	// staleAge is how old a spool must be before Create treats it as left
	// behind by a killed process. A spool lives only for one send attempt,
	// which is bounded by the provider timeout.
	staleAge = 24 * time.Hour
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
	dir := os.TempDir()
	removeStale(dir, time.Now())
	f, err := os.CreateTemp(dir, namePrefix+"*"+nameSuffix)
	if err != nil {
		return nil, fmt.Errorf("spool: create: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("spool: permissions: %w", err), f.Close(), os.Remove(f.Name()))
	}
	register(f.Name())
	return &File{path: f.Name(), f: f}, nil
}

// Compose creates a spool, lets write fill it, and finishes it. When write
// or Finish fails, the spool is removed and the error returned (joined
// with a removal failure, if any): nothing is left behind.
func Compose(write func(w io.Writer) error) (*File, error) {
	s, err := Create()
	if err != nil {
		return nil, err
	}
	if err := write(s); err != nil {
		return nil, errors.Join(err, s.Abort())
	}
	if err := s.Finish(); err != nil {
		return nil, errors.Join(err, s.Abort())
	}
	return s, nil
}

// CloseRead closes a file that was only read. Its data is consumed, so a
// close error cannot change any outcome; it is logged, as the providers log
// a failure to close a response body.
func CloseRead(f io.Closer) {
	if err := f.Close(); err != nil {
		log.Printf("mailfire: %v", err)
	}
}

// Write appends to an unfinished spool.
func (s *File) Write(p []byte) (int, error) {
	if s == nil || s.f == nil {
		return 0, errors.New("spool: file is not open for writing")
	}
	return s.f.Write(p)
}

// Finish closes the spool and records its size. It does not sync: only a
// spool adopted by the deferral store must survive a power loss, and the
// store syncs it then.
func (s *File) Finish() error {
	if s == nil || s.f == nil {
		return errors.New("spool: file is not open for writing")
	}
	f := s.f
	s.f = nil
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
	unregister(s.path)
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

// HTTPBody returns an HTTP request body of size bytes read from r and, when
// r is an io.ReaderAt (a spool or body.bin file, a bytes.Reader), a GetBody
// function: with it net/http can rewind the body and transparently retry a
// request that failed on a stale connection, as it does for in-memory bodies.
func HTTPBody(r io.Reader, size int64) (io.ReadCloser, func() (io.ReadCloser, error)) {
	ra, ok := r.(io.ReaderAt)
	if !ok {
		return io.NopCloser(r), nil
	}
	open := func() (io.ReadCloser, error) {
		return io.NopCloser(io.NewSectionReader(ra, 0, size)), nil
	}
	return io.NopCloser(io.NewSectionReader(ra, 0, size)), open
}

var (
	liveMu sync.Mutex
	live   = map[string]struct{}{}
)

func register(path string) {
	liveMu.Lock()
	defer liveMu.Unlock()
	live[path] = struct{}{}
}

func unregister(path string) {
	liveMu.Lock()
	defer liveMu.Unlock()
	delete(live, path)
}

// removeLive removes every spool not yet removed or adopted.
func removeLive() {
	liveMu.Lock()
	defer liveMu.Unlock()
	for path := range live {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("mailfire: removing message spool: %v", err)
		}
		delete(live, path)
	}
}

// removeStale removes spools in dir older than staleAge: left behind by a
// process killed without a chance to clean up (SIGKILL, crash, power loss).
// It is best effort: in a shared temporary directory, other users' spools
// cannot be removed (permission denied), which is expected and not logged.
func removeStale(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(name, namePrefix) || !strings.HasSuffix(name, nameSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= staleAge {
			continue
		}
		err = os.Remove(filepath.Join(dir, name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrPermission) {
			log.Printf("mailfire: removing stale message spool: %v", err)
		}
	}
}

// afterCleanup ends the process as sig would have (reraise, per OS) once
// the spools are removed; a variable so tests can observe the cleanup
// instead.
var afterCleanup = reraise

// CleanupOnSignal removes every live spool when SIGINT or SIGTERM (on
// Windows: Ctrl+C, Ctrl+Break, console close) arrives, then ends the
// process as the signal would have. Go does not run deferred calls when a
// signal ends the process, so without it an interrupted send would leave a
// copy of the message in the temporary directory. The returned stop
// restores the default handling; call it once no spool exists any more.
func CleanupOnSignal() (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return watch(ch, func() { signal.Stop(ch) })
}

// watch is CleanupOnSignal's handler, separate from the OS signal source so
// it can be tested on every platform. release stops the signal delivery;
// it may run twice. stop releases the delivery before it disarms, so a
// signal already delivered is still in ch: the handler drains it rather
// than losing it — with the default action suppressed, a lost Ctrl+C would
// leave mailfire running.
func watch(ch <-chan os.Signal, release func()) (stop func()) {
	done := make(chan struct{})
	handle := func(sig os.Signal) {
		removeLive()
		release()
		afterCleanup(sig)
	}
	go func() {
		select {
		case sig := <-ch:
			handle(sig)
		case <-done:
			select {
			case sig := <-ch:
				handle(sig)
			default:
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			release()
			close(done)
		})
	}
}
