package cli

import (
	"fmt"
	"io"
)

// runVersion implements the version subcommand (FR-6). It needs neither
// configuration nor network access: the values are injected at build time
// via -ldflags -X (see Makefile) and arrive here as a BuildInfo. Unstamped
// development builds carry the defaults declared in main.go ("dev", "none",
// "unknown"), so they print e.g. "mailfire dev (commit none, built unknown)".
func runVersion(stdout io.Writer, info BuildInfo) int {
	fmt.Fprintf(stdout, "mailfire %s (commit %s, built %s)\n", info.Version, info.Commit, info.BuildDate)
	return ExitOK
}
