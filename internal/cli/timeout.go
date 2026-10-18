package cli

import (
	"flag"
	"fmt"
	"time"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
)

// parseTimeoutFlag validates a --timeout value: Go duration syntax,
// positive, and within range (time.ParseDuration already rejects values
// overflowing a time.Duration). Zero and negative durations are rejected
// too: a zero HTTP timeout disables it and a stalled request could hang
// forever, silently defying the flag's purpose.
func parseTimeoutFlag(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid --timeout %q: use a positive Go duration, e.g. \"15s\", \"1m\"", s)
	}
	return d, nil
}

// parseTimeoutOption resolves the --timeout flag registered on fs as
// timeoutFlag. An absent flag yields 0 (config or client default
// decides); an explicitly supplied value must be a valid positive
// duration — an empty one (--timeout=) is an error, not a silent default.
func parseTimeoutOption(fs *flag.FlagSet, timeoutFlag string) (time.Duration, error) {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "timeout" {
			set = true
		}
	})
	if !set {
		return 0, nil
	}
	return parseTimeoutFlag(timeoutFlag)
}

// effectiveTimeout resolves the Mailgun per-request timeout: --timeout
// wins over [mailgun].timeout; with neither, the mailgun client's built-in
// default (mailgun.DefaultTimeout) applies. Other providers use
// providerTimeout (provider.go).
func effectiveTimeout(cfg *config.Config, flagTimeout time.Duration) time.Duration {
	return providerTimeout(cfg, config.ProviderMailgun, flagTimeout)
}

// clientOptions assembles the Mailgun client options for a command: the
// run context's options (tests point the client at a mock server) plus
// the effective per-request timeout. With neither flag nor config value,
// no WithTimeout option is added, so the client's built-in default — and
// any test-supplied HTTP client's timeout — applies unchanged.
func clientOptions(rc *runContext, cfg *config.Config, flagTimeout time.Duration) []mailgun.Option {
	opts := append([]mailgun.Option(nil), rc.clientOpts...)
	if flagTimeout > 0 || cfg.Timeout > 0 {
		// Appended last, so it also wins over a test-supplied HTTP client.
		opts = append(opts, mailgun.WithTimeout(effectiveTimeout(cfg, flagTimeout)))
	}
	return opts
}
