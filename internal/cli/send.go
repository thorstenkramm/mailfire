package cli

import (
	"context"
	"fmt"
	"io"
	"net/mail"
	"os"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
)

// send composes a plain-text message from the parsed flags and the body
// read from stdin or --file, posts it via the Mailgun client, and maps the
// outcome to an exit code per PRD §8.
func send(cfg *config.Config, opts *SendOptions, rc *runContext, stdout, stderr io.Writer) int {
	msg, err := compose(cfg, opts, rc.stdin)
	if err != nil {
		fmt.Fprintf(stderr, "mailfire: send: %v\n", err)
		return ExitUsage
	}

	client, err := mailgun.New(cfg.APIKey, cfg.Domain, cfg.Region, rc.clientOpts...)
	if err != nil {
		// Cannot happen: config.Load already validated these fields.
		fmt.Fprintf(stderr, "mailfire: %v\n", err)
		return ExitConfig
	}

	resp, err := client.SendMessage(context.Background(), msg)
	if err != nil {
		return reportSendError(err, stderr)
	}
	fmt.Fprintln(stdout, resp.ID)
	return ExitOK
}

// compose builds the Mailgun message from flags and config. All failures
// are usage errors (exit 1): bad addresses, a missing sender, or an
// unreadable body.
func compose(cfg *config.Config, opts *SendOptions, stdin io.Reader) (*mailgun.Message, error) {
	from := opts.From
	if from == "" {
		from = cfg.From
	}
	if from == "" {
		return nil, fmt.Errorf("no sender: set sending.from in the config file or pass --from")
	}
	fromAddr, err := parseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("invalid sender %q: %v", from, err)
	}

	to, err := parseAddresses("--to", opts.To)
	if err != nil {
		return nil, err
	}
	cc, err := parseAddresses("--cc", opts.Cc)
	if err != nil {
		return nil, err
	}
	bcc, err := parseAddresses("--bcc", opts.Bcc)
	if err != nil {
		return nil, err
	}

	body, err := readBody(opts.File, stdin)
	if err != nil {
		return nil, err
	}

	return &mailgun.Message{
		From:     fromAddr,
		To:       to,
		Cc:       cc,
		Bcc:      bcc,
		Subject:  opts.Subject,
		Text:     body,
		TestMode: opts.Test,
	}, nil
}

// parseAddress validates one address and returns its normalized form:
// "user@example.com" stays as-is, `"Doe, John" <john@example.com>` keeps
// its (quoted) display name.
func parseAddress(s string) (string, error) {
	addr, err := mail.ParseAddress(s)
	if err != nil {
		return "", err
	}
	if addr.Name == "" {
		return addr.Address, nil
	}
	return addr.String(), nil
}

// parseAddresses validates a repeatable recipient flag. Commas are not
// treated as separators (Decision Q1), so each flag value is exactly one
// address.
func parseAddresses(flagName string, values []string) ([]string, error) {
	addrs := make([]string, 0, len(values))
	for _, v := range values {
		addr, err := parseAddress(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s address %q: %v", flagName, v, err)
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

// readBody reads the message body from file (~ is expanded) or, when file
// is empty, from stdin.
func readBody(file string, stdin io.Reader) (string, error) {
	if file == "" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("reading body from stdin: %v", err)
		}
		return string(data), nil
	}
	path := config.ExpandPath(file)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading body file %s: %v", path, err)
	}
	return string(data), nil
}

// reportSendError maps a client failure to an exit code per PRD §8.
// Deferral is not implemented yet, so a deferrable failure exits 4 with an
// explicit note instead of storing the message.
func reportSendError(err error, stderr io.Writer) int {
	switch {
	case mailgun.IsAuth(err):
		fmt.Fprintf(stderr, "mailfire: send: %v\n", err)
		return ExitAuth
	case mailgun.IsDeferrable(err):
		fmt.Fprintf(stderr, "mailfire: send: %v\n", err)
		fmt.Fprintf(stderr, "mailfire: send: deferral is not implemented yet; the message was NOT saved and is lost\n")
		return ExitDeferred
	default:
		fmt.Fprintf(stderr, "mailfire: send: %v\n", err)
		return ExitSendFail
	}
}
