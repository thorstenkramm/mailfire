package cli

import (
	"errors"
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
	var jsonMode bool
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&jsonMode, "json", false, "")
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		printOutput(stdout, versionUsage)
		return ExitOK
	}
	if err != nil {
		jsonMode = scanJSON(fs, args, false)
	} else if fs.NArg() > 0 {
		err = fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err != nil {
		printfOutput(stderr, "mailfire: version: %v\n\n", err)
		printOutput(stderr, versionUsage)
		return jsonFail(stdout, jsonMode, "version", errUsage, err, ExitUsage)
	}
	if jsonMode {
		return emitJSON(stdout, &versionResult{envelope: envelope{Command: "version"},
			Version: info.Version, Commit: info.Commit, BuildDate: info.BuildDate}, ExitOK)
	}
	printfOutput(stdout, "mailfire %s (commit %s, built %s)\n", info.Version, info.Commit, info.BuildDate)
	return ExitOK
}

// versionResult is the JSON document of version.
type versionResult struct {
	envelope
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
}
