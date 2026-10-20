package jobrun

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHelperProcess is the job run by the tests. It is not a test: it
// only acts when HELPER_MODE is set and exits immediately otherwise.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("HELPER_MODE")
	if mode == "" {
		return
	}
	// The test arguments follow the "--" separator.
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	switch mode {
	case "echo":
		fmt.Println(strings.Join(args, " "))
	case "stderr":
		fmt.Fprintln(os.Stderr, strings.Join(args, " "))
	case "exit":
		n, err := strconv.Atoi(args[0])
		if err != nil {
			t.Fatal(err)
		}
		os.Exit(n)
	case "sleep":
		fmt.Println("ready")
		d, err := time.ParseDuration(args[0])
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(d)
	case "tree":
		child := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--")
		child.Env = append(os.Environ(), "HELPER_MODE=tree-child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Run(); err != nil {
			os.Exit(1)
		}
	case "tree-child":
		ignoreTerm()
		fmt.Printf("child %d\n", os.Getpid())
		closeTest(t, os.Stdout)
		closeTest(t, os.Stderr)
		time.Sleep(30 * time.Second)
	case "big":
		// Big output: n lines of numbered text, to exercise spill and cap.
		n, err := strconv.Atoi(args[0])
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			fmt.Printf("line %06d of the job's standard output\n", i)
		}
	case "cr":
		fmt.Print("progress 10%\rprogress 60%\rprogress 100%\r\n")
	case "stubborn": // ignore SIGTERM, so only SIGKILL ends the job
		ignoreTerm()
		fmt.Println("ready")
		d, err := time.ParseDuration(args[0])
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(d)
	}
	os.Exit(0)
}

// runHelper starts the test binary as a job in the given mode. The mode
// is passed through the environment: the job inherits mailfire's
// environment, so HELPER_MODE is set for the duration of the run.
func runHelper(t *testing.T, spec *Spec, mode string, args ...string) *Result {
	t.Helper()
	t.Setenv("HELPER_MODE", mode)
	spec.Path = os.Args[0]
	spec.Args = append([]string{"-test.run=TestHelperProcess", "--"}, args...)
	res, err := Run(spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() {
		if err := res.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return res
}

func TestRunSuccess(t *testing.T) {
	res := runHelper(t, &Spec{}, "echo", "hello job")
	if res.ExitCode != 0 || res.StartErr != nil || res.Signal != "" {
		t.Errorf("unexpected result: %+v", res)
	}
	if got := string(res.Stdout().Content()); got != "hello job\n" {
		t.Errorf("stdout = %q, want %q", got, "hello job\n")
	}
	if res.Stderr().Total() != 0 {
		t.Errorf("stderr total = %d, want 0", res.Stderr().Total())
	}
	if res.Duration() <= 0 {
		t.Errorf("duration = %v, want > 0", res.Duration())
	}
	if res.Start.After(res.End) {
		t.Errorf("start %v after end %v", res.Start, res.End)
	}
}

func TestRunExitCode(t *testing.T) {
	res := runHelper(t, &Spec{}, "exit", "3")
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}
}

func TestRunNotFound(t *testing.T) {
	res, err := Run(&Spec{Path: "mailfire-test-no-such-command-xyz"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer closeTest(t, res)
	if res.StartErr == nil {
		t.Fatal("StartErr = nil, want an error")
	}
	if res.ExitCode != 127 {
		t.Errorf("exit code = %d, want 127", res.ExitCode)
	}
}

func TestRunNotExecutable(t *testing.T) {
	if isWindows() {
		t.Skip("no executable-bit semantics on Windows")
	}
	f, err := os.CreateTemp(t.TempDir(), "job-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	res, err := Run(&Spec{Path: f.Name()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer closeTest(t, res)
	if res.StartErr == nil {
		t.Fatal("StartErr = nil, want an error")
	}
	if res.ExitCode != 126 {
		t.Errorf("exit code = %d, want 126", res.ExitCode)
	}
}

func TestRunStderr(t *testing.T) {
	res := runHelper(t, &Spec{}, "stderr", "oops")
	if got := string(res.Stderr().Content()); got != "oops\n" {
		t.Errorf("stderr = %q, want %q", got, "oops\n")
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.ExitCode)
	}
}

func TestRunTee(t *testing.T) {
	var out, errW bytes.Buffer
	res := runHelper(t, &Spec{TeeOut: &out, TeeErr: &errW}, "echo", "copied")
	if got := out.String(); got != "copied\n" {
		t.Errorf("tee stdout = %q, want %q", got, "copied\n")
	}
	if errW.Len() != 0 {
		t.Errorf("tee stderr = %q, want empty", errW.String())
	}
	if got := string(res.Stdout().Content()); got != "copied\n" {
		t.Errorf("captured stdout = %q, want %q", got, "copied\n")
	}
}

type slowWriter struct{ bytes.Buffer }

func (w *slowWriter) Write(p []byte) (int, error) {
	time.Sleep(10 * time.Millisecond)
	return w.Buffer.Write(p)
}

func TestRunDrainsOutputAfterExit(t *testing.T) {
	var tee slowWriter
	res := runHelper(t, &Spec{TeeOut: &tee}, "big", "5000")
	var want strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&want, "line %06d of the job's standard output\n", i)
	}
	if got := string(res.Stdout().Content()); got != want.String() {
		t.Errorf("captured %d bytes, want %d", len(got), want.Len())
	}
	if tee.String() != want.String() {
		t.Errorf("tee copied %d bytes, want %d", tee.Len(), want.Len())
	}
}

func TestCaptureHugeTailLineIsBounded(t *testing.T) {
	c := newCapture("stdout", DefaultLogCap)
	defer closeTest(t, c)
	if _, err := c.Write(bytes.Repeat([]byte("x"), DefaultLogCap)); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	lines := c.tail(10)
	runtime.ReadMemStats(&after)
	if len(lines) != 1 || len(lines[0]) > tailLineBytes {
		t.Fatalf("long tail line was not bounded: %d lines", len(lines))
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 256*1024 {
		t.Errorf("tail allocated %d bytes for one huge line", allocated)
	}
	if _, err := c.Write([]byte("\rfinal progress\r\n")); err != nil {
		t.Fatal(err)
	}
	if got := c.tail(1); len(got) != 1 || string(got[0]) != "final progress" {
		t.Errorf("progress tail = %q", got)
	}
}

// failingWriter rejects every write, like a closed stdout.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

// A tee that stops accepting output must not stop the capture (which
// would leave the job blocked on a full pipe).
func TestRunBrokenTee(t *testing.T) {
	res := runHelper(t, &Spec{TeeOut: failingWriter{}}, "big", "3000") // ~110 KB, more than a pipe buffer
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.ExitCode)
	}
	if res.Stdout().Total() < 100*1024 {
		t.Errorf("captured total = %d, want the whole output despite the broken tee", res.Stdout().Total())
	}
}

func TestRunMaxRuntime(t *testing.T) {
	start := time.Now()
	res := runHelper(t, &Spec{MaxRuntime: 300 * time.Millisecond, KillGrace: time.Second}, "sleep", "30s")
	if !res.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	if res.Signal == "" && !isWindows() {
		t.Errorf("Signal = %q, want a killed-by-signal detail", res.Signal)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("the job was not stopped promptly: %v", time.Since(start))
	}
}

// A job ignoring SIGTERM is killed with SIGKILL after the grace period.
func TestRunMaxRuntimeKillsStubbornJob(t *testing.T) {
	if isWindows() {
		t.Skip("termination is immediate on Windows")
	}
	start := time.Now()
	res := runHelper(t, &Spec{MaxRuntime: 500 * time.Millisecond, KillGrace: 300 * time.Millisecond}, "stubborn", "30s")
	if !res.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	if res.ExitCode != 128+int(syscall.SIGKILL) || res.Signal != "killed by signal 9 (KILL)" {
		t.Errorf("exit %d, signal %q; want 137 and killed by signal 9 (KILL)", res.ExitCode, res.Signal)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("the job was not killed promptly: %v", time.Since(start))
	}
}

func TestRunInterrupt(t *testing.T) {
	if isWindows() {
		t.Skip("no signal forwarding on Windows")
	}
	signals := make(chan os.Signal, 1)
	spec := &Spec{Signals: signals, KillGrace: time.Second, MaxRuntime: 10 * time.Second,
		TeeOut: &signalOnReady{signals: signals}}
	res := runHelper(t, spec, "sleep", "30s")
	if !res.Interrupted {
		t.Error("Interrupted = false, want true")
	}
	if res.ExitCode != 128+int(syscall.SIGTERM) {
		t.Errorf("exit code = %d, want %d", res.ExitCode, 128+int(syscall.SIGTERM))
	}
	if !strings.Contains(res.Signal, "TERM") {
		t.Errorf("Signal = %q, want it to name TERM", res.Signal)
	}
}

type signalOnReady struct {
	bytes.Buffer
	signals chan os.Signal
	sent    bool
}

func (w *signalOnReady) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if !w.sent && strings.Contains(w.String(), "ready\n") {
		w.sent = true
		w.signals <- syscall.SIGTERM
	}
	return n, err
}

// TestRunBigOutput checks that a job printing much more than the memory
// limit and the log cap neither deadlocks nor grows the capture without
// bound: the file holds at most the cap, and the omitted count accounts
// for the rest.
func TestRunBigOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("big-output test")
	}
	const cap = 64 * 1024                                   // small cap, so the test stays fast
	res := runHelper(t, &Spec{LogCap: cap}, "big", "20000") // ~760 KB of output
	s := res.Stdout()
	if s.Total() < 700*1024 {
		t.Errorf("total = %d, want about 760 KB", s.Total())
	}
	if s.Captured() > cap {
		t.Errorf("captured = %d, exceeds the cap %d", s.Captured(), cap)
	}
	if s.Omitted() != s.Total()-s.Captured() {
		t.Errorf("omitted = %d, want total-captured = %d", s.Omitted(), s.Total()-s.Captured())
	}
	content := s.Content()
	if !bytes.HasPrefix(content, []byte("line 000000")) {
		t.Errorf("content does not start with the first line: %.40q", content)
	}
	marker := fmt.Sprintf("[... %d bytes omitted ...]", s.Omitted())
	if !bytes.Contains(content, []byte(marker)) {
		t.Errorf("content lacks the omission marker %q", marker)
	}
	if !bytes.Contains(content, []byte("line 019999")) {
		t.Error("content lacks the last line (the ring)")
	}
	// The tail shows the latest lines.
	tail := s.Tail(3)
	if len(tail) != 3 || !strings.Contains(string(tail[2]), "line 019999") {
		t.Errorf("tail = %q, want the last 3 lines", tail)
	}
}

func TestRunNoTempFileBelowMemoryLimit(t *testing.T) {
	res := runHelper(t, &Spec{}, "echo", "small")
	if res.Stdout().cap.Spilled() {
		t.Error("a 6-byte stream must not spill to a temporary file")
	}
	if res.Stdout().cap.HighWater() > MemoryLimit {
		t.Errorf("high-water mark = %d, exceeds %d", res.Stdout().cap.HighWater(), MemoryLimit)
	}
}

// TestRunSpillCleansUp checks that a spilled stream's temporary file does
// not survive the run: it is unlinked at creation (Unix) or on Close.
func TestRunSpillCleansUp(t *testing.T) {
	tmp := os.TempDir()
	before := tempNames(t, tmp)
	res := runHelper(t, &Spec{}, "big", "1000") // ~37 KB > 10 KiB memory limit
	if !res.Stdout().cap.Spilled() {
		t.Fatal("37 KB must spill to a temporary file")
	}
	if res.Stdout().cap.HighWater() > MemoryLimit {
		t.Errorf("high-water mark = %d, exceeds %d", res.Stdout().cap.HighWater(), MemoryLimit)
	}
	if err := res.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for name := range tempNames(t, tmp) {
		if !before[name] {
			t.Errorf("temporary file %s survived the run", name)
		}
	}
}

func tempNames(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "mailfire-run-") {
			names[e.Name()] = true
		}
	}
	return names
}

func TestCaptureRingExactBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cap   int64
		write int
	}{
		{"exactly head", 100, 50},
		{"head plus one", 100, 51},
		{"exactly cap", 100, 100},
		{"cap plus one", 100, 101},
		{"twice the cap", 100, 200},
		{"huge single write", 100, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCapture("stdout", tc.cap)
			c.mem = 8 // force spilling below the 10 KiB default
			data := bytes.Repeat([]byte("x"), tc.write)
			if _, err := c.Write(data); err != nil {
				t.Fatal(err)
			}
			content := c.Content()
			if int64(len(data)) <= tc.cap {
				if !bytes.Equal(content, data) {
					t.Errorf("content = %d bytes, want the full %d", len(content), len(data))
				}
				if c.Omitted() != 0 {
					t.Errorf("omitted = %d, want 0", c.Omitted())
				}
			} else {
				// Head + omission marker + ring.
				head := data[:tc.cap/2]
				if !bytes.HasPrefix(content, head) {
					t.Error("content does not start with the head")
				}
				if !bytes.HasSuffix(content, data[int64(len(data))-(tc.cap-tc.cap/2):]) {
					t.Error("content does not end with the ring")
				}
				if c.Omitted() != int64(tc.write)-tc.cap {
					t.Errorf("omitted = %d, want %d", c.Omitted(), int64(tc.write)-tc.cap)
				}
			}
			if err := c.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
}

func TestCaptureTailInMemory(t *testing.T) {
	c := newCapture("stdout", DefaultLogCap)
	if _, err := c.Write([]byte("one\ntwo\nthree\n")); err != nil {
		t.Fatal(err)
	}
	for n, want := range map[int][]string{
		0: nil,
		1: {"three"},
		2: {"two", "three"},
		5: {"one", "two", "three"},
	} {
		got := c.tail(n)
		if len(got) != len(want) {
			t.Fatalf("tail(%d) = %d lines, want %d", n, len(got), len(want))
		}
		for i := range want {
			if string(got[i]) != want[i] {
				t.Errorf("tail(%d)[%d] = %q, want %q", n, i, got[i], want[i])
			}
		}
	}
	// No trailing newline: the last chunk is a line.
	c2 := newCapture("stdout", DefaultLogCap)
	if _, err := c2.Write([]byte("one\ntwo")); err != nil {
		t.Fatal(err)
	}
	if got := c2.tail(1); len(got) != 1 || string(got[0]) != "two" {
		t.Errorf("tail(1) without trailing newline = %q, want %q", got, "two")
	}
}

func TestCaptureTailSpilled(t *testing.T) {
	c := newCapture("stdout", 64*1024)
	defer closeTest(t, c)
	var lines []string
	for i := 0; i < 2000; i++ {
		lines = append(lines, fmt.Sprintf("line %06d of the job's standard output", i))
	}
	if _, err := c.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatal(err)
	}
	if !c.Spilled() {
		t.Fatal("the stream must spill")
	}
	got := c.tail(4)
	want := lines[len(lines)-4:]
	if len(got) != 4 {
		t.Fatalf("tail(4) = %d lines, want 4", len(got))
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Errorf("tail(4)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func isWindows() bool { return runtime.GOOS == "windows" }

func closeTest(t *testing.T, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Error(err)
	}
}
