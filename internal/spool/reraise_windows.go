package spool

import (
	"os"
	"syscall"
)

// handledSignals end the process by default and so need the spool cleanup:
// Go maps Ctrl+C and Ctrl+Break to os.Interrupt, and console close, logoff
// and shutdown to SIGTERM.
var handledSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// exitControlC is STATUS_CONTROL_C_EXIT, the status Windows gives a console
// process ended by Ctrl+C (or a console close) when it does not handle it.
const exitControlC uint32 = 0xC000013A

// reraise ends the process as Windows would have without CleanupOnSignal.
// A process cannot deliver a console control event to itself, so it exits
// with the status the default handler uses. The conversion wraps on 32-bit
// platforms; Windows reads the status back as the same unsigned value.
func reraise(os.Signal) {
	code := exitControlC
	os.Exit(int(code))
}
