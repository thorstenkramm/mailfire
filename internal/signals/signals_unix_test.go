//go:build unix

package signals

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
)

const childEnv = "MAILFIRE_SIGNALS_CHILD"

// Started with SIGINT ignored (as a shell starts a background job), the
// process must keep knowing that after a Notify/Stop cycle — which is
// exactly what clears signal.Ignored (the runtime behavior this package
// works around; asserted too, so a Go change that fixes it is noticed).
func TestIgnoredAtStartSurvivesNotify(t *testing.T) {
	if os.Getenv(childEnv) == "1" {
		child()
		return
	}
	cmd := exec.Command("/bin/sh", "-c",
		`trap '' INT; exec "$0" -test.run='^TestIgnoredAtStartSurvivesNotify$'`, os.Args[0])
	cmd.Env = append(os.Environ(), childEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

func child() {
	fail := func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
		os.Exit(1)
	}
	if !IgnoredAtStart(syscall.SIGINT) {
		fail("IgnoredAtStart(SIGINT) = false in a process started with SIGINT ignored")
	}
	if IgnoredAtStart(syscall.SIGTERM) {
		fail("IgnoredAtStart(SIGTERM) = true, but only SIGINT was ignored")
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT)
	signal.Stop(ch)
	if signal.Ignored(syscall.SIGINT) {
		fmt.Println("note: signal.Ignored survives Notify/Stop in this Go version")
	}
	if !IgnoredAtStart(syscall.SIGINT) {
		fail("IgnoredAtStart(SIGINT) changed after Notify/Stop")
	}
	if got := NotIgnored(os.Interrupt, syscall.SIGTERM); len(got) != 1 || got[0] != syscall.SIGTERM {
		fail("NotIgnored = %v, want [SIGTERM]", got)
	}
	os.Exit(0)
}
