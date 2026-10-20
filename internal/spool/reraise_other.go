//go:build !windows

package spool

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/thorstenkramm/mailfire/internal/signals"
)

// handledSignals end the process by default and so need the spool cleanup.
// SIGHUP comes with a closed terminal or a dropped ssh session.
var handledSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

// holdSIGPIPE turns a write to a closed stdout or stderr pipe into an EPIPE
// error until restore, instead of the SIGPIPE that ends the process — no
// deferred call survives that, and a deferral that is still printing its
// error would lose the message and leave the spool. Notifying SIGPIPE has
// that effect (os/signal); stopping restores the default, so the process
// ends on the next such write once the spool is gone. A SIGPIPE inherited
// as ignored stays ignored.
func holdSIGPIPE() (restore func()) {
	if signals.IgnoredAtStart(syscall.SIGPIPE) {
		return func() {}
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGPIPE)
	return func() { signal.Stop(ch) }
}

// reraise restores the default handling of sig and delivers it again, so
// the process ends exactly as it would have without CleanupOnSignal
// (killed by the signal). Should delivery fail, it exits with the shell
// convention 128+signal.
func reraise(sig os.Signal) {
	signal.Reset(sig)
	code := 130 // SIGINT
	if s, ok := sig.(syscall.Signal); ok {
		code = 128 + int(s)
	}
	if p, err := os.FindProcess(os.Getpid()); err == nil && p.Signal(sig) == nil {
		time.Sleep(time.Second) // delivery is asynchronous; the default action ends the process
	}
	os.Exit(code)
}
