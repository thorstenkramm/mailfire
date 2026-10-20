// Package spool owns temporary, owner-only files used to compose transport
// payloads before a provider is contacted. A completed File can be opened
// repeatedly for sending and deferral without retaining its contents in memory.
//
// Spools must not outlive the send attempt: the caller removes them (Abort)
// or adopts them into the deferral store. CleanupOnSignal covers the
// signals that end the process without running deferred calls, and Create
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

// sweepOnce limits the stale-spool sweep to the first spool of a process:
// a sweep reads the whole (possibly large, shared) temporary directory.
var sweepOnce sync.Once

// Create creates an owner-only spool in the system temporary directory.
// The file is created and registered under the registry lock, so a signal
// handler cleaning up concurrently either sees it or runs before it exists.
func Create() (*File, error) {
	dir := os.TempDir()
	sweepOnce.Do(func() { removeStale(dir, time.Now()) })
	liveMu.Lock()
	defer liveMu.Unlock()
	f, err := os.CreateTemp(dir, namePrefix+"*"+nameSuffix)
	if err != nil {
		return nil, fmt.Errorf("spool: create: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("spool: permissions: %w", err), f.Close(), os.Remove(f.Name()))
	}
	live[f.Name()] = []*os.File{f}
	return &File{path: f.Name(), f: f}, nil
}

// Compose creates a spool, lets write fill it, and finishes it. When write
// or Finish fails, or write panics, the spool is removed and the error
// returned (joined with a removal failure, if any): nothing is left behind.
func Compose(write func(w io.Writer) error) (*File, error) {
	s, err := Create()
	if err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			if err := s.Abort(); err != nil {
				log.Printf("mailfire: removing message spool: %v", err)
			}
			panic(r)
		}
	}()
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
	if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
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

// Abort closes every handle on the spool — Windows cannot delete an open
// file — and removes it. A missing file is success, which also makes this
// safe after deferral renamed the spool.
func (s *File) Abort() error {
	if s == nil {
		return nil
	}
	s.f = nil // closed with the other handles below
	err := release(s.path)
	if rerr := os.Remove(s.path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		err = errors.Join(err, rerr)
	}
	return err
}

// Open opens a completed spool at its beginning. The handle is registered,
// so Abort and the signal cleanup can close it before removing the file.
func (s *File) Open() (*os.File, error) {
	if s == nil || s.f != nil {
		return nil, errors.New("spool: file is not complete")
	}
	liveMu.Lock()
	defer liveMu.Unlock()
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("spool: open: %w", err)
	}
	if handles, ok := live[s.path]; ok {
		live[s.path] = append(handles, f)
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
	// live maps each spool not yet removed or adopted to the handles
	// opened on it (the writer and every Open). Closed handles stay listed
	// until the spool is released; closing them again is harmless.
	live = map[string][]*os.File{}
)

// release closes every handle on path and forgets the spool. Handles the
// caller already closed report os.ErrClosed, which is not an error here.
func release(path string) error {
	liveMu.Lock()
	defer liveMu.Unlock()
	return releaseLocked(path)
}

func releaseLocked(path string) error {
	var err error
	for _, f := range live[path] {
		if cerr := f.Close(); cerr != nil && !errors.Is(cerr, os.ErrClosed) {
			err = errors.Join(err, cerr)
		}
	}
	delete(live, path)
	return err
}

// removeLive closes and removes every spool not yet removed or adopted.
func removeLive() {
	liveMu.Lock()
	defer liveMu.Unlock()
	for path := range live {
		err := releaseLocked(path)
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
		if err != nil {
			log.Printf("mailfire: removing message spool: %v", err)
		}
	}
}

// removeStale removes spools in dir older than staleAge — left behind by a
// process killed without a chance to clean up (SIGKILL, crash, power loss)
// — and returns how many it removed. It is best effort and silent: in a
// shared temporary directory other users' spools cannot be removed, and on
// Windows a spool held open by a hung process cannot be either; reporting
// that on every send would only be noise.
func removeStale(dir string, now time.Time) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(name, namePrefix) || !strings.HasSuffix(name, nameSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= staleAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err == nil {
			removed++
		}
	}
	return removed
}

// afterCleanup ends the process as sig would have (reraise, per OS) once
// the spools are removed; a variable so tests can observe the cleanup
// instead.
var afterCleanup = reraise

// CleanupOnSignal removes every live spool when one of the signals that
// end the process arrives (handledSignals: SIGINT, SIGTERM, SIGHUP; on
// Windows Ctrl+C, Ctrl+Break, console close), then ends the process as the
// signal would have. Go does not run deferred calls when a signal ends the
// process, so without it an interrupted send would leave a copy of the
// message in the temporary directory.
//
// A signal the process ignores (inherited, as for "mailfire &" in a script
// or under nohup) stays ignored: installing a handler would make it end
// the send. The returned stop restores the default handling; call it once
// no spool exists any more.
func CleanupOnSignal() (stop func()) {
	var sigs []os.Signal
	for _, sig := range handledSignals {
		if !signal.Ignored(sig) {
			sigs = append(sigs, sig)
		}
	}
	if len(sigs) == 0 {
		return func() {}
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sigs...)
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
