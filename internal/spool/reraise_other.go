//go:build !windows

package spool

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// handledSignals end the process by default and so need the spool cleanup.
// SIGHUP comes with a closed terminal or a dropped ssh session.
var handledSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

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
