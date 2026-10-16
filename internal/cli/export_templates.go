package cli

import (
	"errors"
	"flag"
	"io"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/render"
)

const exportTemplatesUsage = `Usage:
  mailfire export-templates <folder>

Export all built-in templates as editable HTML template files.
Creates the folder if needed; refuses to overwrite existing templates.
` + moreHelp

func runExportTemplates(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export-templates", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printOutput(stdout, exportTemplatesUsage)
			return ExitOK
		}
		printfOutput(stderr, "mailfire: export-templates: %v\n", err)
		printOutput(stderr, exportTemplatesUsage)
		return ExitUsage
	}
	args = fs.Args()
	if len(args) != 1 || args[0] == "" {
		printOutput(stderr, exportTemplatesUsage)
		return ExitUsage
	}
	if err := render.Export(config.ExpandPath(args[0])); err != nil {
		printfOutput(stderr, "mailfire: export-templates: %v\n", err)
		return ExitUsage
	}
	printfOutput(stdout, "Exported templates to %s\n", args[0])
	return ExitOK
}
