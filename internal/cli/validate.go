package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
	"github.com/thorstenkramm/mailfire/internal/message"
)

// validate.go implements the validate subcommand (PRD FR-5): it checks
// configuration and credentials without sending mail. Three steps are
// checked in order and each is reported on stdout as "ok" or "FAIL";
// later steps are skipped once a step fails:
//
//  1. The config file is found and parses as valid TOML. With no config
//     file but MAILFIRE_* variables set, the environment is the
//     configuration source and this step passes with a note.
//  2. The mandatory keys are present and well-formed (region, domain,
//     api_key, and from when set — config.Load applies environment
//     overrides, validates all of them, and names the offending key or
//     variable). The report names where each value came from, never the
//     API key itself.
//  3. The API key and domain are verified against Mailgun via
//     GET /v3/domains/{domain} (Mailgun has no dedicated ping route).
//
// Exit codes: 0 when all steps pass; 2 when step 1 or 2 fails, and also
// when Mailgun does not know the domain (404 — a configuration error);
// 3 when the API key is rejected (401/403); 4 when Mailgun cannot be
// reached reliably (network error, timeout, 429, 5xx) — nothing is
// wrong with the config, the check can simply be retried later.
// validate never sends mail and never logs: it is not a sending
// attempt (PRD FR-3).

// runValidate parses the validate flags and runs the checks.
func runValidate(args []string, rc *runContext, stdout, stderr io.Writer) int {
	opts, err := parseValidateFlags(args)
	if errors.Is(err, flag.ErrHelp) {
		printOutput(stdout, validateUsage)
		return ExitOK
	}
	if err != nil {
		printfOutput(stderr, "mailfire: validate: %v\n\n", err)
		printOutput(stderr, validateUsage)
		return jsonFail(stdout, opts.JSON, "validate", errUsage, err, ExitUsage)
	}
	res := &validateResult{envelope: envelope{Command: "validate"}, Checks: []validateCheck{}}
	code := validate(opts, rc, res, humanOut(stdout, opts.JSON), stderr)
	if opts.JSON {
		return emitJSON(stdout, res, code)
	}
	return code
}

// validateResult is the JSON document of validate: the checks performed,
// in order (a failed check ends the list), and the verified domain.
type validateResult struct {
	envelope
	Checks []validateCheck `json:"checks"`
	Domain *domainInfo     `json:"domain,omitempty"`
}

type validateCheck struct {
	Name    string `json:"name"` // "config_file", "mandatory_keys", or "api_key_and_domain"
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type domainInfo struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// validateOptions holds the parsed flags of the validate subcommand.
type validateOptions struct {
	Config  string        // -c / --config
	Timeout time.Duration // --timeout; zero: mailgun.timeout or the client default
	JSON    bool          // --json: print the result as one JSON document
}

// parseValidateFlags parses the validate subcommand's flags: -c/--config
// and --timeout. Positional arguments are a usage error. On error, only
// the returned JSON field is meaningful: the output mode.
func parseValidateFlags(args []string) (validateOptions, error) {
	var opts validateOptions
	var configShort string
	var timeoutFlag string

	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&configShort, "c", "", "")
	fs.StringVar(&opts.Config, "config", "", "")
	fs.StringVar(&timeoutFlag, "timeout", "", "")
	fs.BoolVar(&opts.JSON, "json", false, "")
	if err := fs.Parse(args); err != nil {
		return validateOptions{JSON: scanJSON(fs, args, false)}, err
	}
	fail := func(err error) (validateOptions, error) { return validateOptions{JSON: opts.JSON}, err }

	if configShort != "" {
		if opts.Config != "" && opts.Config != configShort {
			return fail(fmt.Errorf("conflicting config paths: use either -c or --config, not both"))
		}
		opts.Config = configShort
	}
	// Presence matters: an explicitly empty --timeout is an error, while an
	// absent flag leaves config/default in charge.
	d, err := parseTimeoutOption(fs, timeoutFlag)
	if err != nil {
		return fail(err)
	}
	opts.Timeout = d
	if rest := fs.Args(); len(rest) > 0 {
		return fail(fmt.Errorf("unexpected arguments: %s", strings.Join(rest, " ")))
	}
	return opts, nil
}

// validate runs the three checks and reports each step.
func validate(opts validateOptions, rc *runContext, res *validateResult, stdout, stderr io.Writer) int {
	// Step 1: config file found and syntactically valid TOML — or, with
	// no file but MAILFIRE_* variables set, an environment-only setup.
	// An explicitly requested file (-c) that is missing or invalid
	// always fails, regardless of the environment.
	path, err := config.Find(opts.Config)
	switch {
	case err == nil:
		if err := config.CheckSyntax(path); err != nil {
			return validateFail(stdout, stderr, res, "config file", err, ExitConfig)
		}
		validatePass(stdout, res, "config file", "config file found and parses as valid TOML: "+path)
	case opts.Config == "" && config.EnvActive():
		validatePass(stdout, res, "config file", "no configuration file; using MAILFIRE_* environment variables")
	default:
		return validateFail(stdout, stderr, res, "config file", err, ExitConfig)
	}

	// Step 2: mandatory keys present and well-formed. config.Load
	// re-reads the file (cheap, and it applies defaults and environment
	// overrides) and names the offending key or variable in its error.
	cfg, err := config.LoadWith(opts.Config, stderr, config.Overrides{Timeout: opts.Timeout > 0})
	if err != nil {
		return validateFail(stdout, stderr, res, "mandatory keys", err, ExitConfig)
	}
	src := func(key string) string {
		if env := cfg.EnvSource[key]; env != "" {
			return env
		}
		return "config file"
	}
	var keys string
	switch cfg.Provider {
	case config.ProviderMailgun:
		// Preserve the established Mailgun validation wording for scripts
		// and existing tests; the provider is implicit for the default.
		keys = fmt.Sprintf("region %q from %s, domain %q from %s", cfg.Region, src("mailgun.region"), cfg.Domain, src("sending.domain"))
	case config.ProviderGmail:
		keys = fmt.Sprintf("provider %q from %s, Gmail credentials %q", cfg.Provider, src("sending.provider"), gmailStore(cfg).Path)
		if cfg.Domain != "" {
			keys += fmt.Sprintf(", domain %q from %s", cfg.Domain, src("sending.domain"))
		}
	case config.ProviderSMTP:
		keys = fmt.Sprintf("provider %q from %s, domain %q from %s, SMTP host %q from %s, port %d from %s, security %q from %s",
			cfg.Provider, src("sending.provider"), cfg.Domain, src("sending.domain"), cfg.SMTPHost, src("smtp.host"),
			cfg.SMTPPort, src("smtp.port"), cfg.SMTPSecurity, src("smtp.security"))
	}
	if cfg.From != "" {
		keys += fmt.Sprintf(", from %q from %s", cfg.From, src("sending.from"))
	}
	// The API key is named by source only, never by value.
	if cfg.Provider == "mailgun" {
		keys += fmt.Sprintf(", api key from %s", src("mailgun.api_key"))
	} else if cfg.SMTPUsername != "" {
		keys += fmt.Sprintf(", SMTP credentials from %s / %s", src("smtp.username"), src("smtp.password"))
	}
	validatePass(stdout, res, "mandatory keys", "mandatory keys present and well-formed ("+keys+")")
	if cfg.Provider == "gmail" {
		// Refreshes the access token when it is (nearly) expired; never
		// contacts the send endpoint.
		if _, err := gmailStore(cfg).AccessToken(context.Background(), gmailOAuth(cfg, opts.Timeout, rc), false); err != nil {
			switch {
			case message.IsAuth(err):
				return validateFail(stdout, stderr, res, "Gmail authorization", err, ExitAuth)
			case message.IsDeferrable(err):
				return validateFail(stdout, stderr, res, "Gmail authorization", fmt.Errorf("could not refresh Gmail authorization reliably (retry later): %w", err), ExitDeferred)
			default:
				return validateFail(stdout, stderr, res, "Gmail authorization", err, ExitConfig)
			}
		}
		validatePass(stdout, res, "Gmail authorization", "Gmail send authorization is available (token refreshed if needed; no email sent)")
		return ExitOK
	}

	if cfg.Provider == "smtp" {
		client, err := smtpClient(cfg, opts.Timeout, rc)
		if err != nil {
			return validateFail(stdout, stderr, res, "SMTP connection", err, ExitConfig)
		}
		if err := client.Validate(context.Background()); err != nil {
			switch {
			case message.IsAuth(err):
				return validateFail(stdout, stderr, res, "SMTP connection", fmt.Errorf("SMTP authentication failed: %w", err), ExitAuth)
			case message.IsDeferrable(err):
				return validateFail(stdout, stderr, res, "SMTP connection", fmt.Errorf("could not reach SMTP server reliably (retry later): %w", err), ExitDeferred)
			default:
				return validateFail(stdout, stderr, res, "SMTP connection", err, ExitConfig)
			}
		}
		validatePass(stdout, res, "SMTP connection", fmt.Sprintf("SMTP connection, TLS negotiation, and authentication verified with %s:%d", cfg.SMTPHost, cfg.SMTPPort))
		return ExitOK
	}

	// Step 3: API key and domain verified against Mailgun.
	client, err := mailgun.New(cfg.APIKey, cfg.Domain, cfg.Region, clientOptions(rc, cfg, opts.Timeout)...)
	if err != nil {
		// Cannot happen: config.Load already validated these fields.
		return validateFail(stdout, stderr, res, "API key and domain", err, ExitConfig)
	}
	info, err := client.ValidateDomain(context.Background())
	if err != nil {
		switch {
		case mailgun.IsAuth(err):
			return validateFail(stdout, stderr, res, "API key and domain",
				fmt.Errorf("mailgun rejected the API key: %w", err), ExitAuth)
		case mailgun.IsDeferrable(err):
			return validateFail(stdout, stderr, res, "API key and domain",
				fmt.Errorf("could not reach Mailgun reliably (retry later): %w", err), ExitDeferred)
		default:
			if merr, ok := mailgun.As(err); ok && merr.StatusCode == 404 {
				return validateFail(stdout, stderr, res, "API key and domain",
					fmt.Errorf("domain %q is unknown to Mailgun: %w", cfg.Domain, err), ExitConfig)
			}
			return validateFail(stdout, stderr, res, "API key and domain", err, ExitConfig)
		}
	}
	validatePass(stdout, res, "API key and domain",
		fmt.Sprintf("API key and domain verified with Mailgun: domain %q is %s", info.Name, info.State))
	res.Domain = &domainInfo{Name: info.Name, State: info.State}
	return ExitOK
}

// checkName is the JSON name of a validation step.
func checkName(step string) string {
	return strings.ReplaceAll(strings.ToLower(step), " ", "_")
}

// validatePass reports a passed step.
func validatePass(stdout io.Writer, res *validateResult, step, msg string) {
	printfOutput(stdout, "ok   %s\n", msg)
	res.Checks = append(res.Checks, validateCheck{Name: checkName(step), OK: true, Message: msg})
}

// validateFail reports a failed step: the FAIL line goes to stdout (the
// per-step report, PRD FR-5) and the error detail to stderr (PRD §8).
func validateFail(stdout, stderr io.Writer, res *validateResult, step string, err error, code int) int {
	printfOutput(stdout, "FAIL %s: %v\n", step, err)
	printfOutput(stderr, "mailfire: validate: %v\n", err)
	res.Checks = append(res.Checks, validateCheck{Name: checkName(step), Message: err.Error()})
	res.setError(exitErrorCode(code), err)
	return code
}
