// Command mailfire sends email from the command line via Mailgun, Gmail, or SMTP.
package main

import (
	"os"

	"github.com/thorstenkramm/mailfire/internal/cli"
)

// Build information, stamped at build time via -ldflags -X (see Makefile).
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	info := cli.BuildInfo{Version: version, Commit: commit, BuildDate: buildDate}
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, info))
}
