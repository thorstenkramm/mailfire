// Package jobrun runs one command for "mailfire run" and captures its
// output (task 33). The command runs directly, without a shell, with an
// empty stdin and the inherited environment and working directory. stdout
// and stderr are captured separately and concurrently, bounded in memory
// (10 KiB per stream) and on disk (the log cap), so a job printing
// gigabytes neither exhausts memory nor fills the disk.
//
// A job exceeding --max-runtime is stopped (Unix: SIGTERM to the process
// group, SIGKILL after a grace period; Windows: the process tree is
// terminated). A signal delivered to mailfire is forwarded to the job's
// process group; mailfire waits for the job and reports status
// "interrupted".
package jobrun

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Defaults of the run command's capture and stop behavior.
const (
	// DefaultLogCap bounds one captured stream: the first half of the
	// cap is kept as is, then the latest half as a ring. With the
	// default the attached log stays well below provider size limits.
	DefaultLogCap = 5 * 1024 * 1024 // 5 MiB
	// MemoryLimit is how much of a stream is held in memory before it
	// spills to a temporary file.
	MemoryLimit = memLimit
	// DefaultKillGrace is how long the job has after SIGTERM before
	// SIGKILL follows. On Windows termination is immediate.
	DefaultKillGrace = 5 * time.Second
)

// Spec describes the job to run.
type Spec struct {
	// Path and Args are the command: exec.Command(Path, Args...).
	Path string
	Args []string
	// MaxRuntime stops the job when exceeded; zero: no limit.
	MaxRuntime time.Duration
	// LogCap bounds one captured stream; zero: DefaultLogCap.
	LogCap int64
	// TeeOut and TeeErr, when set, receive live copies of the job's
	// stdout and stderr.
	TeeOut io.Writer
	TeeErr io.Writer
	// Signals, when set, forwards each received signal to the job and
	// marks the run interrupted.
	Signals <-chan os.Signal
	// KillGrace is the wait between SIGTERM and SIGKILL; zero:
	// DefaultKillGrace.
	KillGrace time.Duration
}

// Stream is one captured output stream of the job.
type Stream struct {
	cap *capture
}

// Total returns the bytes the job wrote; Captured the bytes kept (≤ the
// log cap); Omitted the bytes the cap dropped in between.
func (s *Stream) Total() int64    { return s.cap.Total() }
func (s *Stream) Captured() int64 { return s.cap.Captured() }
func (s *Stream) Omitted() int64  { return s.cap.Omitted() }

// Content returns the captured stream: the whole stream within the log
// cap, else its head, a "[... N bytes omitted ...]" marker, and its
// latest part. Raw bytes: callers sanitize for the report body and
// attachments.
func (s *Stream) Content() []byte { return s.cap.Content() }

// Tail returns the stream's last n lines, raw and without line endings.
func (s *Stream) Tail(n int) [][]byte { return s.cap.tail(n) }

// CaptureErr returns the first capture I/O error, if any.
func (s *Stream) CaptureErr() error { return s.cap.Err() }

// Result is the outcome of one job run.
type Result struct {
	Start    time.Time
	End      time.Time
	ExitCode int
	// StartErr is set when the command could not be started at all
	// (not found, not executable, permission denied); ExitCode is then
	// 127 or 126, as in shells.
	StartErr error
	// Signal describes a job killed by a signal: "killed by signal 9
	// (KILL)". Empty otherwise.
	Signal string
	// TimedOut: stopped for exceeding MaxRuntime. Interrupted: mailfire
	// received a signal and forwarded it.
	TimedOut    bool
	Interrupted bool

	stdout *capture
	stderr *capture
	proc   proc
}

// Duration is the job's wall-clock runtime.
func (r *Result) Duration() time.Duration { return r.End.Sub(r.Start) }

// Stdout and Stderr are the job's captured streams.
func (r *Result) Stdout() *Stream { return &Stream{cap: r.stdout} }
func (r *Result) Stderr() *Stream { return &Stream{cap: r.stderr} }

// Close removes the captures' temporary files (they are already unlinked
// on Unix) and releases the platform handle.
func (r *Result) Close() error {
	r.proc.close()
	return errors.Join(r.stdout.Close(), r.stderr.Close())
}

// Run executes the job and blocks until it exits, it is stopped for
// exceeding MaxRuntime, or a forwarded signal ends it. The returned error
// is nil even when the job fails: the job's outcome is in the Result. A
// non-nil error is an internal failure (the output pipes could not be
// created); the Result is still usable and reports it as StartErr.
func Run(spec *Spec) (*Result, error) {
	cap := spec.LogCap
	if cap <= 0 {
		cap = DefaultLogCap
	}
	grace := spec.KillGrace
	if grace <= 0 {
		grace = DefaultKillGrace
	}

	out, errCap := newCapture("stdout", cap), newCapture("stderr", cap)
	res := &Result{Start: time.Now(), stdout: out, stderr: errCap}
	fail := func(err error) (*Result, error) {
		res.End, res.StartErr, res.ExitCode = time.Now(), err, 126
		return res, err
	}

	path, args := resolveCommand(spec.Path, spec.Args)
	cmd := exec.Command(path, args...)
	// nil Stdin reads from the null device (/dev/null, NUL).
	setPgid(cmd)
	// Own the pipes: Cmd.Wait closes StdoutPipe/StderrPipe before a slow
	// reader has necessarily drained the buffered output.
	outPipe, outWrite, err := os.Pipe()
	if err != nil {
		return fail(fmt.Errorf("capturing stdout: %w", err))
	}
	defer closePipe(outPipe)
	defer closePipe(outWrite)
	errPipe, errWrite, err := os.Pipe()
	if err != nil {
		return fail(fmt.Errorf("capturing stderr: %w", err))
	}
	defer closePipe(errPipe)
	defer closePipe(errWrite)
	cmd.Stdout, cmd.Stderr = outWrite, errWrite
	p, err := startProc(cmd)
	if err != nil {
		res.End = time.Now()
		res.StartErr = err
		res.ExitCode = startErrorCode(err)
		return res, nil
	}
	res.proc = p
	closePipe(outWrite)
	closePipe(errWrite)

	// Capture both streams concurrently: reading only one would
	// deadlock a job that fills the other's pipe buffer. The tee copies
	// share one lock: both may go to the same writer (--tee --json).
	var teeMu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyStream(out, lockedWriter(&teeMu, spec.TeeOut), outPipe) }()
	go func() { defer wg.Done(); copyStream(errCap, lockedWriter(&teeMu, spec.TeeErr), errPipe) }()
	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()

	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	var timeout, graceT *time.Timer
	var timeoutCh, graceCh <-chan time.Time
	if spec.MaxRuntime > 0 {
		timeout = time.NewTimer(spec.MaxRuntime)
		timeoutCh = timeout.C
	}
	var waitErr error
	var exited, captured bool
	for !exited || !captured || graceCh != nil && p.running() {
		select {
		case waitErr = <-wait:
			exited = true
			wait = nil
		case <-drained:
			captured = true
			drained = nil
		case <-timeoutCh:
			timeoutCh = nil
			res.TimedOut = true
			p.terminate()
			if graceCh == nil {
				graceT = time.NewTimer(grace)
				graceCh = graceT.C
			}
		case <-graceCh:
			p.kill()
			graceCh = nil
		case sig, ok := <-spec.Signals:
			if !ok {
				spec.Signals = nil
				continue
			}
			res.Interrupted = true
			p.forward(sig)
			if graceCh == nil {
				graceT = time.NewTimer(grace)
				graceCh = graceT.C
			}
		}
	}
	if timeout != nil {
		timeout.Stop()
	}
	if graceT != nil {
		graceT.Stop()
	}
	res.End = time.Now()
	res.ExitCode, res.Signal = exitCode(waitErr)
	return res, nil
}

// teeWriter serializes writes to a shared tee writer and never fails: a
// tee that stops accepting output (a closed stdout) is dropped, so the
// capture keeps draining the job's pipe instead of blocking the job.
type teeWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	broken bool
}

func (t *teeWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.broken {
		if _, err := t.w.Write(p); err != nil {
			t.broken = true
		}
	}
	return len(p), nil
}

// lockedWriter wraps w with mu; nil stays nil (no tee).
func lockedWriter(mu *sync.Mutex, w io.Writer) io.Writer {
	if w == nil {
		return nil
	}
	return &teeWriter{mu: mu, w: w}
}

// copyStream copies src into cap and, when tee is set, into tee.
func copyStream(cap *capture, tee io.Writer, src io.Reader) {
	var dst io.Writer = cap
	if tee != nil {
		dst = io.MultiWriter(cap, tee)
	}
	if _, err := io.Copy(dst, src); err != nil {
		cap.setErr(fmt.Errorf("reading %s pipe: %w", cap.label, err))
	}
}

func closePipe(f *os.File) {
	if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		log.Printf("mailfire: closing job pipe: %v", err)
	}
}

// exitCode classifies the wait result: the process exit code, or 128+n
// and a "killed by signal n (NAME)" detail when a signal killed the job.
func exitCode(waitErr error) (int, string) {
	if waitErr == nil {
		return 0, ""
	}
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		// A wait failure other than ExitError (broken pipe handling)
		// leaves no meaningful code.
		return 1, ""
	}
	if status, ok := ee.Sys().(syscall.WaitStatus); ok {
		if status.Signaled() {
			sig := status.Signal()
			return 128 + int(sig), fmt.Sprintf("killed by signal %d (%s)", int(sig), signalName(sig))
		}
	}
	return ee.ExitCode(), ""
}

// startErrorCode maps a start failure to the shell's codes: 127 when the
// command does not exist, 126 when it is not executable.
func startErrorCode(err error) int {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return 127
	}
	return 126
}
