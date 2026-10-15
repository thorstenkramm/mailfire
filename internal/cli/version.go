package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// runVersion implements the version subcommand (FR-6). It needs neither
// configuration nor network access: the values are injected at build time
// via -ldflags -X (see Makefile) and arrive here as a BuildInfo. Unstamped
// development builds carry the defaults declared in main.go ("dev", "none",
// "unknown"), so they print e.g. "mailfire dev (commit none, built unknown)".
// Like validate, it takes no positional arguments; any are a usage error.
func runVersion(args []string, stdout, stderr io.Writer, info BuildInfo) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	if err == nil && fs.NArg() > 0 {
		err = fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err != nil {
		fmt.Fprintf(stderr, "mailfire: version: %v\n\n", err)
		fmt.Fprint(stderr, usageText)
		return ExitUsage
	}
	fmt.Fprintf(stdout, "mailfire %s (commit %s, built %s)\n", info.Version, info.Commit, info.BuildDate)
	return ExitOK
}
