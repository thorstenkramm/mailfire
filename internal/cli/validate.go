package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
)

// validate.go implements the validate subcommand (PRD FR-5): it checks
// configuration and credentials without sending mail. Three steps are
// checked in order and each is reported on stdout as "ok" or "FAIL";
// later steps are skipped once a step fails:
//
//  1. The config file is found and parses as valid TOML.
//  2. The mandatory keys are present and well-formed (region, domain,
//     api_key, and from when set — config.Load validates all of them
//     and names the offending key).
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
	configPath, err := parseValidateFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "mailfire: validate: %v\n\n", err)
		fmt.Fprint(stderr, validateUsage)
		return ExitUsage
	}
	return validate(configPath, rc, stdout, stderr)
}

// parseValidateFlags parses the validate subcommand's flags: only
// -c/--config. Positional arguments are a usage error.
func parseValidateFlags(args []string) (string, error) {
	var configPath, configShort string

	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&configShort, "c", "", "")
	fs.StringVar(&configPath, "config", "", "")
	if err := fs.Parse(args); err != nil {
		return "", err
	}

	if configShort != "" {
		if configPath != "" && configPath != configShort {
			return "", fmt.Errorf("conflicting config paths: use either -c or --config, not both")
		}
		configPath = configShort
	}
	if rest := fs.Args(); len(rest) > 0 {
		return "", fmt.Errorf("unexpected arguments: %s", strings.Join(rest, " "))
	}
	return configPath, nil
}

// validate runs the three checks and reports each step.
func validate(flagPath string, rc *runContext, stdout, stderr io.Writer) int {
	// Step 1: config file found and syntactically valid TOML.
	path, err := config.Find(flagPath)
	if err != nil {
		return validateFail(stdout, stderr, "config file", err, ExitConfig)
	}
	if err := config.CheckSyntax(path); err != nil {
		return validateFail(stdout, stderr, "config file", err, ExitConfig)
	}
	fmt.Fprintf(stdout, "ok   config file found and parses as valid TOML: %s\n", path)

	// Step 2: mandatory keys present and well-formed. config.Load
	// re-reads the file (cheap, and it applies defaults) and names the
	// offending key in its error.
	cfg, err := config.Load(path, stderr)
	if err != nil {
		return validateFail(stdout, stderr, "mandatory keys", err, ExitConfig)
	}
	keys := fmt.Sprintf("region %q, domain %q", cfg.Region, cfg.Domain)
	if cfg.From != "" {
		keys += fmt.Sprintf(", from %q", cfg.From)
	}
	fmt.Fprintf(stdout, "ok   mandatory keys present and well-formed (%s)\n", keys)

	// Step 3: API key and domain verified against Mailgun.
	client, err := mailgun.New(cfg.APIKey, cfg.Domain, cfg.Region, rc.clientOpts...)
	if err != nil {
		// Cannot happen: config.Load already validated these fields.
		return validateFail(stdout, stderr, "API key and domain", err, ExitConfig)
	}
	info, err := client.ValidateDomain(context.Background())
	if err != nil {
		switch {
		case mailgun.IsAuth(err):
			return validateFail(stdout, stderr, "API key and domain",
				fmt.Errorf("mailgun rejected the API key: %w", err), ExitAuth)
		case mailgun.IsDeferrable(err):
			return validateFail(stdout, stderr, "API key and domain",
				fmt.Errorf("could not reach Mailgun reliably (retry later): %w", err), ExitDeferred)
		default:
			if merr, ok := mailgun.As(err); ok && merr.StatusCode == 404 {
				return validateFail(stdout, stderr, "API key and domain",
					fmt.Errorf("domain %q is unknown to Mailgun: %w", cfg.Domain, err), ExitConfig)
			}
			return validateFail(stdout, stderr, "API key and domain", err, ExitConfig)
		}
	}
	fmt.Fprintf(stdout, "ok   API key and domain verified with Mailgun: domain %q is %s\n",
		info.Name, info.State)
	return ExitOK
}

// validateFail reports a failed step: the FAIL line goes to stdout (the
// per-step report, PRD FR-5) and the error detail to stderr (PRD §8).
func validateFail(stdout, stderr io.Writer, step string, err error, code int) int {
	fmt.Fprintf(stdout, "FAIL %s: %v\n", step, err)
	fmt.Fprintf(stderr, "mailfire: validate: %v\n", err)
	return code
}
