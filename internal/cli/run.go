package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/jobrun"
	"github.com/thorstenkramm/mailfire/internal/locale"
	"github.com/thorstenkramm/mailfire/internal/render"
	"github.com/thorstenkramm/mailfire/internal/signals"
)

// run.go implements "mailfire run" (task 33): run a command, capture its
// output, and email a report. It is a drop-in wrapper for cron, systemd,
// and Task Scheduler jobs: mailfire exits with the job's exit code, so
// schedulers still see the job's result. If the job succeeded but its
// report fails, mailfire's own code (2-5) wins.
//
// Mailfire's flags come first; the first non-flag argument (or the
// argument after "--") is the command, and everything after it belongs to
// the command, unparsed — like sudo, timeout, env.

// runUsage is printed by "mailfire run --help" and on run usage errors.
const runUsage = `Usage:
  mailfire run [flags] [--] <command> [args...]

Run a command and email a report: status, times, duration, exit code, the
last lines of stdout and stderr, and the full logs as attachments.
Mailfire's flags come first; everything from the command on belongs to
the command, unparsed. The job always runs for real; --test and --preview
only affect the email. mailfire exits with the job's exit code; when the
job succeeded but the report could not be sent, mailfire's own code
(2-5) applies.

Flags (run):
  --to <addr>        recipient, repeatable (required to send)
  --cc <addr>        cc recipient, repeatable
  --bcc <addr>       bcc recipient, repeatable
  --from <addr>      sender, overrides config ("Name <user@example.com>")
  --subject <text>   subject template (default "{emoji} {job} {status} on {host}")
  --name <name>      job name (default: the command's file name)
  --on <when>        send a report "always" (default) or on "failure" only
  --max-runtime <dur>  stop the job when exceeded
  --success-codes <list>  comma-separated exit codes meaning success (default 0)
  --tail <n>         last lines of stdout and stderr in the body (default 10)
  --tee              copy the job's stdout and stderr live to mailfire's own
  --no-attach-logs   do not attach the full logs
  --locale <locale>  report date locale (e.g. en_US, de_DE); overrides [run].locale
  --report-template <path>  custom report content template (Markdown)
  --template <name>  HTML layout template: card, card-serif, plain, plain-serif
  --template-file <path>  custom HTML layout template (conflicts with --template)
  --preheader <txt>  inbox preview text
  --footer <path>    HTML fragment appended as footer
  --test             Mailgun test mode: validate the report, deliver nothing
  --no-defer         fail immediately instead of deferring on error
  --timeout <dur>    per-request timeout (e.g. 15s, 1m); overrides provider timeout
  --dedup-key <key>  skip repeats of this notification within the cooldown
  --cooldown <dur>   suppression window for --dedup-key (default 1h)
  --preview <path>   run the job and write the rendered report to <path>
                     (- for stdout); send nothing (conflicts with --tee -)
  --json             print the result as one JSON document
  -c, --config <p>   use this config file
  -h, --help         show help
` + moreHelp

// RunOptions holds the parsed flags of the run subcommand.
type RunOptions struct {
	To             []string      // --to, repeatable, required unless --preview
	Cc             []string      // --cc, repeatable
	Bcc            []string      // --bcc, repeatable
	From           string        // --from
	Subject        string        // --subject; empty: the default subject template
	Template       string        // --template; empty: the config's template
	TemplateFile   string        // --template-file; custom layout template
	Preheader      string        // --preheader
	Footer         string        // --footer
	Name           string        // --name; empty: the command's file name
	On             string        // --on: "always" (default) or "failure"
	MaxRuntime     time.Duration // --max-runtime; zero: no limit
	SuccessCodes   map[int]bool  // --success-codes; default {0}
	Tail           int           // --tail; default 10
	Tee            bool          // --tee
	NoAttachLogs   bool          // --no-attach-logs
	Locale         string        // --locale; empty: [run].locale, environment, or en_US
	ReportTemplate string        // --report-template; empty: the built-in
	Test           bool          // --test
	NoDefer        bool          // --no-defer
	Timeout        time.Duration // --timeout; zero: the provider's timeout
	DedupKey       string        // --dedup-key; empty: no notification deduplication
	Cooldown       time.Duration // --cooldown; zero: dedup.cooldown or its default
	Config         string        // -c / --config
	Preview        string        // --preview; output path, "-" for stdout; empty: send
	JSON           bool          // --json
	Command        []string      // the job: command and its arguments, unparsed
}

// parseRunFlags parses the run flags. It returns showHelp == true when
// -h/--help was given before the command. On error, only opts.JSON is
// meaningful: the output mode for the error.
func parseRunFlags(args []string) (opts RunOptions, showHelp bool, err error) {
	var configShort, timeoutFlag, cooldownFlag, maxRuntimeFlag, successCodes string
	var tail int
	var tailSet, dedupKeySet, cooldownSet, previewSet, templateSet, templateFileSet bool

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.Var((*stringSlice)(&opts.To), "to", "")
	fs.Var((*stringSlice)(&opts.Cc), "cc", "")
	fs.Var((*stringSlice)(&opts.Bcc), "bcc", "")
	fs.StringVar(&opts.From, "from", "", "")
	fs.StringVar(&opts.Subject, "subject", "", "")
	fs.StringVar(&opts.Template, "template", "", "")
	fs.StringVar(&opts.TemplateFile, "template-file", "", "")
	fs.StringVar(&opts.Preheader, "preheader", "", "")
	fs.StringVar(&opts.Footer, "footer", "", "")
	fs.StringVar(&opts.Name, "name", "", "")
	fs.StringVar(&opts.On, "on", "always", "")
	fs.StringVar(&maxRuntimeFlag, "max-runtime", "", "")
	fs.StringVar(&successCodes, "success-codes", "0", "")
	fs.IntVar(&tail, "tail", 10, "")
	fs.BoolVar(&opts.Tee, "tee", false, "")
	fs.BoolVar(&opts.NoAttachLogs, "no-attach-logs", false, "")
	fs.StringVar(&opts.Locale, "locale", "", "")
	fs.StringVar(&opts.ReportTemplate, "report-template", "", "")
	fs.BoolVar(&opts.Test, "test", false, "")
	fs.BoolVar(&opts.NoDefer, "no-defer", false, "")
	fs.StringVar(&timeoutFlag, "timeout", "", "")
	fs.StringVar(&opts.DedupKey, "dedup-key", "", "")
	fs.StringVar(&cooldownFlag, "cooldown", "", "")
	bindRenderCommandFlags(fs, &configShort, &opts.Config, &opts.Preview, &opts.JSON)

	// flag.Parse stops at the first non-flag argument: everything from
	// the command on belongs to the command, unparsed.
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return RunOptions{}, true, nil
		}
		return RunOptions{JSON: scanJSON(fs, args, false)}, false, err
	}
	opts.Command = fs.Args()

	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "template-file":
			templateFileSet = true
		case "template":
			templateSet = true
		case "dedup-key":
			dedupKeySet = true
		case "cooldown":
			cooldownSet = true
		case "preview":
			previewSet = true
		case "tail":
			tailSet = true
		}
	})
	if templateSet && templateFileSet {
		return RunOptions{JSON: opts.JSON}, false, errors.New("--template-file is mutually exclusive with --template")
	}
	if templateFileSet && opts.TemplateFile == "" {
		return RunOptions{JSON: opts.JSON}, false, errors.New("--template-file: empty path")
	}
	if previewSet && opts.Preview == "" {
		return RunOptions{JSON: opts.JSON}, false, errors.New("--preview: empty path (use - for stdout)")
	}

	if len(opts.Command) == 0 {
		return RunOptions{JSON: opts.JSON}, false, errors.New("missing command: mailfire run [flags] [--] <command> [args...]")
	}
	switch opts.On {
	case "always", "failure":
	default:
		return RunOptions{JSON: opts.JSON}, false, fmt.Errorf("invalid --on %q: must be \"always\" or \"failure\"", opts.On)
	}
	if tailSet && tail < 0 {
		return RunOptions{JSON: opts.JSON}, false, fmt.Errorf("invalid --tail %d: must not be negative", tail)
	}
	opts.Tail = tail
	if codes, cerr := parseSuccessCodes(successCodes); cerr != nil {
		return RunOptions{JSON: opts.JSON}, false, cerr
	} else {
		opts.SuccessCodes = codes
	}
	if maxRuntimeFlag != "" {
		d, merr := time.ParseDuration(maxRuntimeFlag)
		if merr != nil || d <= 0 {
			return RunOptions{JSON: opts.JSON}, false, fmt.Errorf("invalid --max-runtime %q: use a positive Go duration, e.g. \"30m\", \"2h\"", maxRuntimeFlag)
		}
		opts.MaxRuntime = d
	}
	if d, terr := parseTimeoutOption(fs, timeoutFlag); terr != nil {
		return RunOptions{JSON: opts.JSON}, false, terr
	} else {
		opts.Timeout = d
	}
	if dedupKeySet && strings.TrimSpace(opts.DedupKey) == "" {
		return RunOptions{JSON: opts.JSON}, false, errors.New("--dedup-key: empty key")
	}
	if cooldownSet {
		if !dedupKeySet {
			return RunOptions{JSON: opts.JSON}, false, errors.New("--cooldown requires --dedup-key")
		}
		c, cerr := time.ParseDuration(cooldownFlag)
		if cerr != nil || c <= 0 {
			return RunOptions{JSON: opts.JSON}, false, fmt.Errorf("invalid --cooldown %q: use a positive Go duration, e.g. \"30m\", \"1h\"", cooldownFlag)
		}
		opts.Cooldown = c
	}
	if opts.Tee && opts.Preview == "-" {
		return RunOptions{JSON: opts.JSON}, false, errors.New("--tee cannot be combined with --preview -: both write to stdout")
	}
	if previewSet {
		// Sending-only flags would be silently ignored by a preview;
		// refuse them before the job runs.
		s := opts.sendOptions(nil)
		s.Preview = opts.Preview
		if err := checkPreviewFlags(&s); err != nil {
			return RunOptions{JSON: opts.JSON}, false, err
		}
	}
	if opts.Locale != "" {
		if _, ok := locale.Normalize(opts.Locale); !ok {
			return RunOptions{JSON: opts.JSON}, false, fmt.Errorf("invalid --locale %q: unsupported locale (use e.g. \"en_US\", \"de_DE\", \"fr_FR\")", opts.Locale)
		}
	}
	if opts.Template != "" && !render.Valid(opts.Template) {
		return RunOptions{JSON: opts.JSON}, false, fmt.Errorf("--template: unknown template %q: must be one of %s", opts.Template, config.TemplateList())
	}
	if err := validateSubjectTemplate(opts.Subject); err != nil {
		return RunOptions{JSON: opts.JSON}, false, fmt.Errorf("invalid --subject: %w", err)
	}
	if err := checkSubject(opts.Subject); err != nil {
		return RunOptions{JSON: opts.JSON}, false, err
	}

	if configShort != "" {
		if opts.Config != "" && opts.Config != configShort {
			return RunOptions{JSON: opts.JSON}, false, errors.New("conflicting config paths: use either -c or --config, not both")
		}
		opts.Config = configShort
	}
	if previewSet {
		// A preview renders the report only: recipients and sender are
		// optional (as in send --preview).
		return opts, false, nil
	}
	if len(opts.To) == 0 {
		return RunOptions{JSON: opts.JSON}, false, errors.New("missing required flag: --to")
	}
	return opts, false, nil
}

// parseSuccessCodes parses --success-codes: a comma-separated list of
// exit codes 0-255.
func parseSuccessCodes(s string) (map[int]bool, error) {
	codes := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 || n > 255 {
			return nil, fmt.Errorf("invalid --success-codes %q: use a comma-separated list of exit codes 0-255, e.g. \"0,24\"", s)
		}
		codes[n] = true
	}
	return codes, nil
}

// runJob executes the run subcommand.
func runJob(args []string, rc *runContext, stdout, stderr io.Writer) int {
	opts, showHelp, err := parseRunFlags(args)
	if showHelp {
		printOutput(stdout, runUsage)
		return ExitOK
	}
	if err != nil {
		printfOutput(stderr, "mailfire: run: %v\n\n", err)
		printOutput(stderr, runUsage)
		return jsonFail(stdout, opts.JSON, "run", errUsage, err, ExitUsage)
	}

	// Preflight: everything that does not depend on the job's result is
	// checked before the job starts, so a usage or configuration error
	// never runs the job.
	reportTmpl, err := loadReportTemplate(opts.ReportTemplate)
	if err != nil {
		printfOutput(stderr, "mailfire: run: %v\n", err)
		return jsonFail(stdout, opts.JSON, "run", errUsage, err, ExitUsage)
	}
	cfg, err := loadRunConfig(&opts, stderr)
	if err != nil {
		printfOutput(stderr, "mailfire: %v\n", err)
		return jsonFail(stdout, opts.JSON, "run", errConfig, err, ExitConfig)
	}
	if opts.Preview != "" {
		// A preview still runs the job, whose captured output can spill.
		// Unlike a real report it does not prepare delivery, so validate
		// the configured directory here before the job starts.
		if err := cfg.CheckTempDir(); err != nil {
			printfOutput(stderr, "mailfire: %v\n", err)
			return jsonFail(stdout, opts.JSON, "run", errConfig, err, ExitConfig)
		}
	}
	res := &runResult{sendResult: sendResult{envelope: envelope{Command: "run"}, TestMode: opts.Test}}
	res.Provider = cfg.Provider
	if err := preflightRun(cfg, &opts); err != nil {
		printfOutput(stderr, "mailfire: run: %v\n", err)
		return jsonFail(stdout, opts.JSON, "run", errUsage, err, ExitUsage)
	}
	sendOpts := opts.sendOptions(nil)
	rendering, err := prepareRendering(cfg, &sendOpts)
	if err == nil {
		_, err = rendering.renderHTML("Job report", opts.Preheader, "<p>Job output</p>")
	}
	if err != nil {
		printfOutput(stderr, "mailfire: run: %v\n", err)
		return jsonFail(stdout, opts.JSON, "run", errUsage, err, ExitUsage)
	}
	var delivery *preparedDelivery
	if opts.Preview == "" {
		if delivery, err = prepareRunDelivery(cfg, &sendOpts, rc); err != nil {
			printfOutput(stderr, "mailfire: run: %v\n", err)
			return jsonFail(stdout, opts.JSON, "run", errConfig, err, ExitConfig)
		}
		defer closeReported(delivery, stderr)
	}
	loc := runLocale(&opts, cfg)
	location, err := loadReportLocation()
	if err != nil {
		printfOutput(stderr, "mailfire: run: %v\n", err)
		return jsonFail(stdout, opts.JSON, "run", errConfig, err, ExitConfig)
	}

	job := runJobProcess(cfg.TempDir, &opts, rc, stdout, stderr)
	defer closeReported(job, stderr)
	captureErrs := reportCaptureErrors(job, [2]error{}, stderr)

	rep := newReport(&opts, loc, reportTmpl, location, job)
	if opts.Preview != "" {
		return finishRunPreview(cfg, rendering, &opts, rc, rep, job, stdout, stderr)
	}
	if opts.On == "failure" && rep.Status == statusSuccess {
		printfOutput(humanOut(stdout, opts.JSON), "report not sent (--on failure; the job succeeded)\n")
		res.Outcome = "not_sent"
		return emitRunResult(stdout, &opts, res, rep, job.ExitCode)
	}

	msg, err := composeReport(cfg, &opts, rep, rendering)
	if err != nil {
		printfOutput(stderr, "mailfire: run: %v\n", err)
		res.setError(errUsage, err)
		return emitRunResult(stdout, &opts, res, rep, reportExitCode(rep, ExitUsage))
	}
	defer closeFiles(msg.Attachments)
	defer closeFiles(msg.Inline)
	code := delivery.sendMessage(rc, msg, &res.sendResult, humanOut(stdout, opts.JSON), stderr)
	// Spilled logs are read while the message is composed: a read error
	// there shortens an attachment and surfaces only now.
	reportCaptureErrors(job, captureErrs, stderr)
	if code != ExitOK {
		return emitRunResult(stdout, &opts, res, rep, reportExitCode(rep, code))
	}
	return emitRunResult(stdout, &opts, res, rep, job.ExitCode)
}

// reportCaptureErrors prints each stream's capture error that is not
// already in seen and returns the errors now known.
func reportCaptureErrors(job *jobrun.Result, seen [2]error, stderr io.Writer) [2]error {
	for i, s := range []*jobrun.Stream{job.Stdout(), job.Stderr()} {
		if err := s.CaptureErr(); err != nil && !errors.Is(err, seen[i]) {
			printfOutput(stderr, "mailfire: run: output capture incomplete: %v\n", err)
			seen[i] = err
		}
	}
	return seen
}

// loadRunConfig loads the configuration for run: the full configuration
// for a send, the template selection and report locale for a preview.
func loadRunConfig(opts *RunOptions, stderr io.Writer) (*config.Config, error) {
	ov := config.Overrides{
		From:     opts.From != "",
		Template: opts.Template != "" || opts.TemplateFile != "",
		Timeout:  opts.Timeout > 0,
		Cooldown: opts.Cooldown > 0,
		Locale:   opts.Locale != "",
	}
	if opts.Preview != "" {
		return config.LoadRunRender(opts.Config, stderr, ov)
	}
	return config.LoadWith(opts.Config, stderr, ov)
}

// A reporting failure must not hide the failed job's scheduler exit code.
func reportExitCode(rep *report, code int) int {
	if rep.Status != statusSuccess {
		return rep.ExitCode
	}
	return code
}

// preflightRun checks everything that does not depend on the job's
// result: recipients, sender, and provider-flag combinations. A problem
// here is a usage error and the job does not run.
func preflightRun(cfg *config.Config, opts *RunOptions) error {
	from := opts.From
	if from == "" {
		from = cfg.From
	}
	if from == "" && opts.Preview == "" {
		return errors.New("no sender: set sending.from in the config file or MAILFIRE_SENDING_FROM, or pass --from")
	}
	if from != "" {
		if _, err := parseAddress(from); err != nil {
			return fmt.Errorf("invalid sender %q: %w", from, err)
		}
	}
	for _, r := range []struct {
		flag  string
		addrs []string
	}{{"--to", opts.To}, {"--cc", opts.Cc}, {"--bcc", opts.Bcc}} {
		if _, err := parseAddresses(r.flag, r.addrs); err != nil {
			return err
		}
	}
	if opts.Preview == "" && cfg.Provider != config.ProviderMailgun && opts.Test {
		return errTestUnsupported(cfg.Provider)
	}
	return nil
}

// runLocale resolves the report's date locale: --locale, then
// [run].locale / MAILFIRE_RUN_LOCALE (validated at load), then the
// environment, then en_US. An unsupported environment locale silently
// falls back.
func runLocale(opts *RunOptions, cfg *config.Config) string {
	for _, s := range []string{opts.Locale, cfg.RunLocale} {
		if l, ok := locale.Normalize(s); ok {
			return string(l)
		}
	}
	if l, ok := locale.FromEnvironment(os.Getenv); ok {
		return string(l)
	}
	return string(locale.Fallback)
}

// runJobProcess starts and waits for the job, wiring the signal source
// and the --tee copies. tempDir (sending.temp_dir) holds spilled capture
// files; empty: the system temporary directory.
func runJobProcess(tempDir string, opts *RunOptions, rc *runContext, stdout, stderr io.Writer) *jobrun.Result {
	signals, stop := rc.signalSource()
	defer stop()

	// --tee copies the job's output live: stdout to mailfire's stdout,
	// stderr to mailfire's stderr; with --json, both go to stderr, so
	// stdout carries only the JSON document.
	var teeOut, teeErr io.Writer
	if opts.Tee {
		teeOut, teeErr = stdout, stderr
		if opts.JSON {
			teeOut = stderr
		}
	}
	res, err := jobrun.Run(&jobrun.Spec{
		Path:       opts.Command[0],
		Args:       opts.Command[1:],
		MaxRuntime: opts.MaxRuntime,
		Signals:    signals,
		TeeOut:     teeOut,
		TeeErr:     teeErr,
		TempDir:    tempDir,
	})
	if err != nil {
		// An internal failure; the result reports it as a job that
		// could not start, and the report is still sent.
		printfOutput(stderr, "mailfire: run: %v\n", err)
	}
	return res
}

// signalSource returns the interrupt channel for the job: the injected
// test channel, or SIGINT/SIGTERM notified on a fresh channel. Signals
// mailfire inherited as ignored (a background job in a script) are not
// notified: they stay ignored and are not forwarded to the job. The
// returned stop function releases the notification.
func (rc *runContext) signalSource() (<-chan os.Signal, func()) {
	if rc.signals != nil {
		return rc.signals, func() {}
	}
	ch := make(chan os.Signal, 2)
	sigs := signals.NotIgnored(os.Interrupt, syscall.SIGTERM)
	if len(sigs) == 0 {
		return ch, func() {} // Notify without signals would relay every signal
	}
	signal.Notify(ch, sigs...)
	return ch, func() { signal.Stop(ch) }
}

// finishRunPreview renders the report locally and writes it to
// opts.Preview. The job already ran for real; the preview only affects
// the email.
func finishRunPreview(cfg *config.Config, rendering *preparedRendering, opts *RunOptions, rc *runContext, rep *report, job *jobrun.Result,
	stdout, stderr io.Writer) int {
	if rep.err != nil {
		printfOutput(stderr, "mailfire: run: preview: %v\n", rep.err)
		return ExitUsage
	}
	sendOpts := opts.sendOptions(rep)
	sendOpts.Preview = opts.Preview
	doc, inputs, err := renderPreviewPrepared(cfg, &sendOpts, rc.stdin, rendering)
	if err != nil {
		printfOutput(stderr, "mailfire: run: preview: %v\n", err)
		return ExitUsage
	}
	if opts.ReportTemplate != "" {
		inputs = append(inputs, previewInput{path: config.ExpandPath(opts.ReportTemplate)})
	}
	if opts.Preview == "-" {
		if _, err := io.WriteString(stdout, doc); err != nil {
			printfOutput(stderr, "mailfire: run: preview: writing to stdout: %v\n", err)
			return ExitUsage
		}
		return job.ExitCode
	}
	path, err := writePreview(opts.Preview, doc, inputs)
	if err != nil {
		printfOutput(stderr, "mailfire: run: preview: %v\n", err)
		return ExitUsage
	}
	printfOutput(stdout, "preview written to %s\n", path)
	return job.ExitCode
}
