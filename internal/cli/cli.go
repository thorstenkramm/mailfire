// Package cli implements mailfire's command-line interface: subcommand
// dispatch, flag parsing, and wiring to the configuration loader. API
// calls and sending live in their own packages.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
)

// BuildInfo carries version details injected at build time via -ldflags -X.
// It is consumed by the version subcommand.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// Exit codes as defined in PRD §8.
const (
	ExitOK       = 0 // Success.
	ExitUsage    = 1 // Usage error: missing flags, unknown subcommand, conflicting formats.
	ExitConfig   = 2 // Configuration error.
	ExitAuth     = 3 // Authentication error (API key rejected).
	ExitDeferred = 4 // Send failed, message deferred for resend.
	ExitSendFail = 5 // Send failed, message not deferred.
)

// usageText mirrors the CLI reference in PRD §7 and README.md.
const usageText = `mailfire — send email from the command line via the Mailgun API

Usage:
  mailfire [send] [flags]     compose and send an email (default)
  mailfire validate           check config and Mailgun credentials
  mailfire resend [--list | --force | <id>...]
                              retry deferred messages
  mailfire version            show version information

Flags (send):
  --to <addr>        recipient, repeatable (required)
  --cc <addr>        cc recipient, repeatable
  --bcc <addr>       bcc recipient, repeatable
  --from <addr>      sender, overrides config ("Name <user@example.com>")
  --subject <text>   subject line (required)
  --txt              body is plain text (default)
  --md               body is Markdown, rendered into the built-in template
  --html             body is an HTML fragment, wrapped in the template
  --html-raw         body is a complete HTML document, sent as-is
  --file <path>      read body from file instead of stdin
  --preheader <txt>  inbox preview text (HTML formats only)
  --footer <path>    HTML fragment appended as footer (HTML formats only)
  --attach <path>    attach a file, repeatable
  --test             use Mailgun test mode: validate the request, deliver nothing
  --no-defer         fail immediately instead of deferring on error
  -c, --config <p>   use this config file
  -h, --help         show help
`

// Option customizes Run's behavior. Options exist mainly so tests can
// inject the body reader and point the Mailgun client at a mock server.
type Option func(*runContext)

// runContext carries the process resources Run needs.
type runContext struct {
	stdin      io.Reader
	clientOpts []mailgun.Option
}

// WithStdin sets the reader the send subcommand reads the message body
// from (default: os.Stdin).
func WithStdin(r io.Reader) Option {
	return func(rc *runContext) { rc.stdin = r }
}

// WithBaseURL points the Mailgun client at baseURL instead of the
// region-derived production URL. Intended for tests.
func WithBaseURL(baseURL string) Option {
	return func(rc *runContext) {
		rc.clientOpts = append(rc.clientOpts, mailgun.WithBaseURL(baseURL))
	}
}

// Run dispatches the command line given in args (excluding the program
// name) and returns the process exit code. With no subcommand, or when the
// first argument is a flag, the send subcommand is assumed.
func Run(args []string, stdout, stderr io.Writer, info BuildInfo, options ...Option) int {
	rc := &runContext{stdin: os.Stdin}
	for _, opt := range options {
		opt(rc)
	}
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help":
			fmt.Fprint(stdout, usageText)
			return ExitOK
		case "send":
			return runSend(args[1:], rc, stdout, stderr)
		case "validate", "resend", "version":
			if wantsHelp(args[1:]) {
				fmt.Fprint(stdout, usageText)
				return ExitOK
			}
			return notImplemented(stderr, args[0])
		default:
			if !strings.HasPrefix(args[0], "-") {
				fmt.Fprintf(stderr, "mailfire: unknown subcommand %q\n\n", args[0])
				fmt.Fprint(stderr, usageText)
				return ExitUsage
			}
			return runSend(args, rc, stdout, stderr)
		}
	}
	return runSend(nil, rc, stdout, stderr)
}

// wantsHelp reports whether args request help before any "--" terminator,
// matching the spellings the flag package accepts.
func wantsHelp(args []string) bool {
	for _, a := range args {
		switch a {
		case "--":
			return false
		case "-h", "--help", "-help", "--h":
			return true
		}
	}
	return false
}

// notImplemented reports a recognized but not yet implemented subcommand.
func notImplemented(stderr io.Writer, name string) int {
	fmt.Fprintf(stderr, "mailfire: %s: not implemented yet\n", name)
	return ExitUsage
}

// runSend parses the send flags, rejects not-yet-implemented features,
// loads and validates the configuration, and sends the message.
func runSend(args []string, rc *runContext, stdout, stderr io.Writer) int {
	opts, showHelp, err := ParseSendFlags(args)
	if showHelp {
		fmt.Fprint(stdout, usageText)
		return ExitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "mailfire: send: %v\n\n", err)
		fmt.Fprint(stderr, usageText)
		return ExitUsage
	}
	if err := checkImplemented(&opts); err != nil {
		fmt.Fprintf(stderr, "mailfire: send: %v\n", err)
		return ExitUsage
	}
	cfg, err := config.Load(opts.Config, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "mailfire: %v\n", err)
		return ExitConfig
	}
	return send(cfg, &opts, rc, stdout, stderr)
}

// checkImplemented rejects flags whose features land in later milestones,
// so they fail loudly instead of being silently ignored.
func checkImplemented(opts *SendOptions) error {
	if len(opts.Attach) > 0 {
		return fmt.Errorf("--attach: not implemented yet")
	}
	if opts.Preheader != "" {
		return fmt.Errorf("--preheader: not implemented yet")
	}
	if opts.Footer != "" {
		return fmt.Errorf("--footer: not implemented yet")
	}
	return nil
}

// stringSlice collects the values of a repeatable flag.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }

func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// SendOptions holds the parsed flags of the send subcommand.
type SendOptions struct {
	To        []string // --to, repeatable, at least one required
	Cc        []string // --cc, repeatable
	Bcc       []string // --bcc, repeatable
	From      string   // --from
	Subject   string   // --subject, required
	Format    string   // one of "txt" (default), "md", "html", "html-raw"
	File      string   // --file
	Preheader string   // --preheader
	Footer    string   // --footer
	Attach    []string // --attach, repeatable
	Test      bool     // --test
	NoDefer   bool     // --no-defer
	Config    string   // -c / --config
}

// ParseSendFlags parses the flags of the send subcommand. It returns
// showHelp == true when -h/--help was given; in that case the other return
// values are meaningless and the caller should print usage and exit 0.
func ParseSendFlags(args []string) (opts SendOptions, showHelp bool, err error) {
	var txt, md, html, htmlRaw bool
	var configShort string

	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.Var((*stringSlice)(&opts.To), "to", "")
	fs.Var((*stringSlice)(&opts.Cc), "cc", "")
	fs.Var((*stringSlice)(&opts.Bcc), "bcc", "")
	fs.StringVar(&opts.From, "from", "", "")
	fs.StringVar(&opts.Subject, "subject", "", "")
	fs.BoolVar(&txt, "txt", false, "")
	fs.BoolVar(&md, "md", false, "")
	fs.BoolVar(&html, "html", false, "")
	fs.BoolVar(&htmlRaw, "html-raw", false, "")
	fs.StringVar(&opts.File, "file", "", "")
	fs.StringVar(&opts.Preheader, "preheader", "", "")
	fs.StringVar(&opts.Footer, "footer", "", "")
	fs.Var((*stringSlice)(&opts.Attach), "attach", "")
	fs.BoolVar(&opts.Test, "test", false, "")
	fs.BoolVar(&opts.NoDefer, "no-defer", false, "")
	fs.StringVar(&configShort, "c", "", "")
	fs.StringVar(&opts.Config, "config", "", "")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return SendOptions{}, true, nil
		}
		return SendOptions{}, false, err
	}

	if opts.Format, err = resolveFormat(txt, md, html, htmlRaw); err != nil {
		return SendOptions{}, false, err
	}

	if configShort != "" {
		if opts.Config != "" && opts.Config != configShort {
			return SendOptions{}, false, fmt.Errorf("conflicting config paths: use either -c or --config, not both")
		}
		opts.Config = configShort
	}

	if rest := fs.Args(); len(rest) > 0 {
		return SendOptions{}, false, fmt.Errorf("unexpected arguments: %s", strings.Join(rest, " "))
	}
	if len(opts.To) == 0 {
		return SendOptions{}, false, errors.New("missing required flag: --to")
	}
	if opts.Subject == "" {
		return SendOptions{}, false, errors.New("missing required flag: --subject")
	}
	return opts, false, nil
}

// resolveFormat maps the format flags to a single format name. Exactly one
// of --txt, --md, --html, --html-raw may be given; with none, "txt" is the
// default.
func resolveFormat(txt, md, html, htmlRaw bool) (string, error) {
	set := 0
	format := "txt"
	for _, f := range []struct {
		flag bool
		name string
	}{
		{txt, "txt"},
		{md, "md"},
		{html, "html"},
		{htmlRaw, "html-raw"},
	} {
		if f.flag {
			set++
			format = f.name
		}
	}
	if set > 1 {
		return "", errors.New("conflicting body formats: use only one of --txt, --md, --html, --html-raw")
	}
	return format, nil
}
