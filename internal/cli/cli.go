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
	"time"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
	"github.com/thorstenkramm/mailfire/internal/render"
	"github.com/thorstenkramm/mailfire/internal/smtp"
) // BuildInfo carries version details injected at build time via -ldflags -X.
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

// Option customizes Run's behavior. Options exist mainly so tests can
// inject the body reader and point the providers at mock servers.
type Option func(*runContext)

// runContext carries the process resources Run needs.
type runContext struct {
	stdin      io.Reader
	clientOpts []mailgun.Option
	clock      func() time.Time // nil: time.Now
	signals    <-chan os.Signal // nil: run installs its own SIGINT/SIGTERM notify

	// Test seams of the SMTP and Gmail providers (provider.go).
	smtpOpts      []smtp.Option
	gmailAPIURL   string
	gmailTokenURL string
	browser       func(url string) error // nil: the desktop browser
}

// openBrowser opens url for auth login, through the test seam if set.
func (rc *runContext) openBrowser(url string) error {
	if rc.browser != nil {
		return rc.browser(url)
	}
	return openBrowser(url)
}

// now returns the current time from the injected clock, if any.
func (rc *runContext) now() time.Time {
	if rc != nil && rc.clock != nil {
		return rc.clock()
	}
	return time.Now()
}

// WithClock replaces the clock used for deferred-message ages, retry
// backoff, and expiry. Intended for tests.
func WithClock(now func() time.Time) Option {
	return func(rc *runContext) { rc.clock = now }
}

// WithStdin sets the reader the send subcommand reads the message body
// from (default: os.Stdin).
func WithStdin(r io.Reader) Option {
	return func(rc *runContext) { rc.stdin = r }
}

// WithSignals sets the channel run receives forwarded interrupts from
// (default: SIGINT and SIGTERM via signal.Notify). Intended for tests.
func WithSignals(ch <-chan os.Signal) Option {
	return func(rc *runContext) { rc.signals = ch }
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
			printOutput(stdout, usageText)
			return ExitOK
		case "send":
			return runSend(args[1:], rc, stdout, stderr)
		case "run":
			return runJob(args[1:], rc, stdout, stderr)
		case "export-templates":
			return runExportTemplates(args[1:], stdout, stderr)
		case "resend":
			if wantsHelp(args[1:]) {
				printOutput(stdout, resendUsage)
				return ExitOK
			}
			return runResend(args[1:], rc, stdout, stderr)
		case "queue":
			return runQueue(args[1:], rc, stdout, stderr)
		case "validate":
			if wantsHelp(args[1:]) {
				printOutput(stdout, validateUsage)
				return ExitOK
			}
			return runValidate(args[1:], rc, stdout, stderr)
		case "auth":
			return runAuth(args[1:], rc, stdout, stderr)
		case "version":
			if wantsHelp(args[1:]) {
				printOutput(stdout, versionUsage)
				return ExitOK
			}
			return runVersion(args[1:], stdout, stderr, info)
		default:
			if !strings.HasPrefix(args[0], "-") {
				printfOutput(stderr, "mailfire: unknown subcommand %q\n\n", args[0])
				printOutput(stderr, usageText)
				return jsonFail(stdout, jsonRequested(args[1:]), "mailfire", errUsage,
					fmt.Errorf("unknown subcommand %q", args[0]), ExitUsage)
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

// runSend parses the send flags, rejects not-yet-implemented features,
// loads and validates the configuration, and sends the message.
func runSend(args []string, rc *runContext, stdout, stderr io.Writer) int {
	opts, showHelp, err := ParseSendFlags(args)
	if showHelp {
		printOutput(stdout, sendUsage)
		return ExitOK
	}
	if err != nil {
		printfOutput(stderr, "mailfire: send: %v\n\n", err)
		printOutput(stderr, sendUsage)
		return jsonFail(stdout, opts.JSON, "send", errUsage, err, ExitUsage)
	}
	if err := checkFlags(&opts); err != nil {
		printfOutput(stderr, "mailfire: send: %v\n", err)
		return jsonFail(stdout, opts.JSON, "send", errUsage, err, ExitUsage)
	}
	if opts.Preview != "" {
		return runPreview(&opts, rc, stdout, stderr)
	}
	res := &sendResult{envelope: envelope{Command: "send"}, TestMode: opts.Test}
	cfg, err := config.LoadWith(opts.Config, stderr, config.Overrides{
		From:     opts.From != "",
		Template: opts.Template != "" || opts.TemplateFile != "",
		Timeout:  opts.Timeout > 0,
		Cooldown: opts.Cooldown > 0,
	})
	if err != nil {
		printfOutput(stderr, "mailfire: %v\n", err)
		return jsonFail(stdout, opts.JSON, "send", errConfig, err, ExitConfig)
	}
	res.Provider = cfg.Provider
	code := send(cfg, &opts, rc, res, humanOut(stdout, opts.JSON), stderr)
	if opts.JSON {
		return emitJSON(stdout, res, code)
	}
	return code
}

// checkFlags rejects flag combinations that are usage errors (exit 1),
// before any config is loaded or body read. --template, --preheader and
// --footer only take effect when the body is wrapped in a template, i.e.
// with --md or --html (FR-1); with --txt or --html-raw they would be
// silently ignored, so they fail loudly instead.
func checkFlags(opts *SendOptions) error {
	if err := checkPreviewFlags(opts); err != nil {
		return err
	}
	if opts.Template != "" && opts.TemplateFile != "" {
		return errors.New("--template-file is mutually exclusive with --template")
	}
	if opts.Template != "" && !render.Valid(opts.Template) {
		return fmt.Errorf("--template: unknown template %q: must be one of %s", opts.Template, config.TemplateList())
	}
	if opts.Format == "md" || opts.Format == "html" {
		return nil
	}
	if opts.Template != "" {
		return fmt.Errorf("--template requires --md or --html")
	}
	if opts.TemplateFile != "" {
		return errors.New("--template-file requires --md or --html")
	}
	if opts.Preheader != "" {
		return fmt.Errorf("--preheader requires --md or --html")
	}
	if opts.Footer != "" {
		return fmt.Errorf("--footer requires --md or --html")
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
	To           []string      // --to, repeatable, at least one required
	Cc           []string      // --cc, repeatable
	Bcc          []string      // --bcc, repeatable
	From         string        // --from
	Subject      string        // --subject, required
	Format       string        // one of "txt" (default), "md", "html", "html-raw"
	File         string        // --file
	Message      string        // --message / --m / -m
	MessageSet   bool          // explicit body, even when empty
	Template     string        // --template; empty: the config's template
	TemplateFile string        // --template-file; custom template
	Preheader    string        // --preheader
	Footer       string        // --footer
	Attach       []string      // --attach, repeatable
	Test         bool          // --test
	NoDefer      bool          // --no-defer
	Timeout      time.Duration // --timeout; zero: mailgun.timeout or the client default
	DedupKey     string        // --dedup-key; empty: no notification deduplication
	Cooldown     time.Duration // --cooldown; zero: dedup.cooldown or its default
	Config       string        // -c / --config
	Preview      string        // --preview; output path, "-" for stdout; empty: send
	JSON         bool          // --json: print the result as one JSON document
}

// ParseSendFlags parses the flags of the send subcommand. It returns
// showHelp == true when -h/--help was given; in that case the other return
// values are meaningless and the caller should print usage and exit 0. On
// error, only opts.JSON is meaningful: the output mode for the error.
func ParseSendFlags(args []string) (opts SendOptions, showHelp bool, err error) {
	var txt, md, html, htmlRaw bool
	var configShort string
	var timeoutFlag, cooldownFlag string
	var fileSet, dedupKeySet, cooldownSet, previewSet bool
	var templateSet, templateFileSet bool

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
	fs.StringVar(&opts.Message, "message", "", "")
	fs.StringVar(&opts.Message, "m", "", "")
	fs.StringVar(&opts.Template, "template", "", "")
	fs.StringVar(&opts.TemplateFile, "template-file", "", "")
	fs.StringVar(&opts.Preheader, "preheader", "", "")
	fs.StringVar(&opts.Footer, "footer", "", "")
	fs.Var((*stringSlice)(&opts.Attach), "attach", "")
	fs.BoolVar(&opts.Test, "test", false, "")
	fs.BoolVar(&opts.NoDefer, "no-defer", false, "")
	fs.StringVar(&timeoutFlag, "timeout", "", "")
	fs.StringVar(&opts.DedupKey, "dedup-key", "", "")
	fs.StringVar(&cooldownFlag, "cooldown", "", "")
	fs.StringVar(&configShort, "c", "", "")
	fs.StringVar(&opts.Config, "config", "", "")
	fs.StringVar(&opts.Preview, "preview", "", "")
	fs.BoolVar(&opts.JSON, "json", false, "")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return SendOptions{}, true, nil
		}
		return SendOptions{JSON: scanJSON(fs, args, false)}, false, err
	}

	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "file":
			fileSet = true
		case "template-file":
			templateFileSet = true
			if opts.TemplateFile == "" {
				err = errors.New("--template-file: empty path")
			}
		case "template":
			templateSet = true
		case "message", "m":
			opts.MessageSet = true
		case "dedup-key":
			dedupKeySet = true
		case "cooldown":
			cooldownSet = true
		case "preview":
			previewSet = true
			if opts.Preview == "" {
				err = errors.New("--preview: empty path (use - for stdout)")
			}
		}
	})
	if templateSet && templateFileSet {
		return SendOptions{JSON: opts.JSON}, false, errors.New("--template-file is mutually exclusive with --template")
	}
	if err != nil {
		return SendOptions{JSON: opts.JSON}, false, err
	}
	if fileSet && opts.MessageSet {
		return SendOptions{JSON: opts.JSON}, false, errors.New("conflicting body sources: use either --file or --message")
	}
	if fileSet && opts.File == "" {
		return SendOptions{JSON: opts.JSON}, false, errors.New("--file: empty path")
	}
	if opts.Format, err = resolveFormat(txt, md, html, htmlRaw); err != nil {
		return SendOptions{JSON: opts.JSON}, false, err
	}
	// An explicitly supplied but empty --timeout is rejected like any other
	// invalid value; only an absent flag leaves config/default in charge.
	d, terr := parseTimeoutOption(fs, timeoutFlag)
	if terr != nil {
		return SendOptions{JSON: opts.JSON}, false, terr
	}
	opts.Timeout = d
	if dedupKeySet && strings.TrimSpace(opts.DedupKey) == "" {
		return SendOptions{JSON: opts.JSON}, false, errors.New("--dedup-key: empty key")
	}
	if cooldownSet {
		if !dedupKeySet {
			return SendOptions{JSON: opts.JSON}, false, errors.New("--cooldown requires --dedup-key")
		}
		c, cerr := time.ParseDuration(cooldownFlag)
		if cerr != nil || c <= 0 {
			return SendOptions{JSON: opts.JSON}, false, fmt.Errorf("invalid --cooldown %q: use a positive Go duration, e.g. \"30m\", \"1h\"", cooldownFlag)
		}
		opts.Cooldown = c
	}

	if configShort != "" {
		if opts.Config != "" && opts.Config != configShort {
			return SendOptions{JSON: opts.JSON}, false, fmt.Errorf("conflicting config paths: use either -c or --config, not both")
		}
		opts.Config = configShort
	}

	if rest := fs.Args(); len(rest) > 0 {
		return SendOptions{JSON: opts.JSON}, false, fmt.Errorf("unexpected arguments: %s", strings.Join(rest, " "))
	}
	if previewSet {
		// A preview renders the body only: recipients, sender and
		// subject are optional (task 29).
		return opts, false, nil
	}
	if len(opts.To) == 0 {
		return SendOptions{JSON: opts.JSON}, false, errors.New("missing required flag: --to")
	}
	if opts.Subject == "" {
		return SendOptions{JSON: opts.JSON}, false, errors.New("missing required flag: --subject")
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
