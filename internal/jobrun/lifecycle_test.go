package jobrun

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

type childReady struct {
	bytes.Buffer
	pid  chan int
	sent bool
}

func (w *childReady) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if !w.sent && strings.Contains(w.String(), "\n") {
		var pid int
		if _, scanErr := fmt.Sscanf(w.String(), "child %d\n", &pid); scanErr == nil {
			w.sent = true
			w.pid <- pid
		}
	}
	return n, err
}

// Inspect the descendant itself: elapsed runtime and pipe EOF cannot prove
// termination when a child has closed its output or its parent has exited.
func TestRunStopsDescendants(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("interrupt=%t", interrupt), func(t *testing.T) {
			t.Setenv("HELPER_MODE", "tree")
			ready := &childReady{pid: make(chan int, 1)}
			signals := make(chan os.Signal, 1)
			spec := &Spec{Path: os.Args[0], Args: []string{"-test.run=TestHelperProcess", "--"},
				TeeOut: ready, Signals: signals, MaxRuntime: 2 * time.Second, KillGrace: 100 * time.Millisecond}
			if interrupt {
				spec.MaxRuntime = 10 * time.Second
			}
			done := make(chan *Result, 1)
			go func() {
				result, err := Run(spec)
				if err != nil {
					t.Error(err)
				}
				done <- result
			}()
			var pid int
			select {
			case pid = <-ready.pid:
			case result := <-done:
				defer closeTest(t, result)
				t.Fatalf("job ended before child readiness: %+v", result)
			case <-time.After(15 * time.Second):
				t.Fatal("child did not become ready")
			}
			assertStopped := watchDescendant(t, pid)
			if interrupt {
				signals <- os.Interrupt
			}
			select {
			case result := <-done:
				defer closeTest(t, result)
				if interrupt && !result.Interrupted || !interrupt && !result.TimedOut {
					t.Fatalf("unexpected termination: %+v", result)
				}
				// Assert before Result.Close: closing a Windows job object must
				// not mask a failure to terminate the tree during Run.
				assertStopped()
			case <-time.After(15 * time.Second):
				t.Fatal("job did not finish after termination")
			}
		})
	}
}
