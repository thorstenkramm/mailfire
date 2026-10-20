// Package signals records which signals the process inherited as ignored.
//
// A shell starts background jobs ("mailfire send … &" in a script) with
// SIGINT ignored, and nohup ignores SIGHUP. Mailfire must leave such
// signals ignored. signal.Ignored cannot answer that later on: the first
// signal.Notify for a signal clears its ignored state in the Go runtime,
// and signal.Stop does not restore it — so after "mailfire run" forwarded
// signals to its job, signal.Ignored reports false. The answer is
// therefore taken once, at start, before any package calls Notify.
//
// Go detects an inherited ignore only for SIGHUP and SIGINT; a Go program
// ends on an inherited-ignored SIGTERM regardless, so mailfire does too.
package signals

import (
	"os"
	"os/signal"
	"syscall"
)

// watched are the signals mailfire installs handlers for.
var watched = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGPIPE}

var ignoredAtStart = func() map[os.Signal]bool {
	m := make(map[os.Signal]bool, len(watched))
	for _, sig := range watched {
		m[sig] = signal.Ignored(sig)
	}
	return m
}()

// IgnoredAtStart reports whether sig was ignored when the process started:
// inherited from the parent (a background job, nohup).
func IgnoredAtStart(sig os.Signal) bool { return ignoredAtStart[sig] }

// NotIgnored returns the signals of sigs the process did not inherit as
// ignored: those a handler may be installed for without changing how the
// process reacts to the others.
func NotIgnored(sigs ...os.Signal) []os.Signal {
	var out []os.Signal
	for _, sig := range sigs {
		if !IgnoredAtStart(sig) {
			out = append(out, sig)
		}
	}
	return out
}
