package cli

import (
	"errors"
	"flag"
	"io"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/render"
)

const exportTemplatesUsage = `Usage:
  mailfire export-templates [--json] <folder>

Export all built-in templates as editable files: the HTML layout templates
and job-report.md, the report content template of mailfire run.
Creates the folder if needed; refuses to overwrite existing templates.

Flags (export-templates):
  --json             print the result as one JSON document
  -h, --help         show help
` + moreHelp

// exportResult is the JSON document of export-templates.
type exportResult struct {
	envelope
	Directory string `json:"directory"`
}

func runExportTemplates(args []string, stdout, stderr io.Writer) int {
	const command = "export-templates"
	var jsonMode bool
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&jsonMode, "json", false, "")
	folders, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printOutput(stdout, exportTemplatesUsage)
			return ExitOK
		}
		printfOutput(stderr, "mailfire: export-templates: %v\n", err)
		printOutput(stderr, exportTemplatesUsage)
		return jsonFail(stdout, scanJSON(fs, args, true), command, errUsage, err, ExitUsage)
	}
	if len(folders) != 1 || folders[0] == "" {
		printOutput(stderr, exportTemplatesUsage)
		return jsonFail(stdout, jsonMode, command, errUsage, errors.New("expected exactly one folder"), ExitUsage)
	}
	dir := config.ExpandPath(folders[0])
	if err := render.Export(dir); err != nil {
		printfOutput(stderr, "mailfire: export-templates: %v\n", err)
		return jsonFail(stdout, jsonMode, command, errExport, err, ExitUsage)
	}
	if jsonMode {
		return emitJSON(stdout, &exportResult{envelope: envelope{Command: command}, Directory: dir}, ExitOK)
	}
	printfOutput(stdout, "Exported templates to %s\n", folders[0])
	return ExitOK
}
