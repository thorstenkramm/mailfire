package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/gmail"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
	"github.com/thorstenkramm/mailfire/internal/message"
	"github.com/thorstenkramm/mailfire/internal/mime"
	"github.com/thorstenkramm/mailfire/internal/smtp"
	"github.com/thorstenkramm/mailfire/internal/spool"
)

// provider.go wires the sending providers (Mailgun, SMTP, Gmail; tasks 30
// and 31) into send, resend, and validate: client construction with the
// effective timeout and test seams, the non-secret account identity
// persisted with deferred messages, and replaying a deferred message
// through the provider it was originally encoded for.

// WithSMTPOptions adds SMTP client options (tests trust a test CA with
// smtp.WithTLSConfig).
func WithSMTPOptions(opts ...smtp.Option) Option {
	return func(rc *runContext) { rc.smtpOpts = append(rc.smtpOpts, opts...) }
}

// WithGmailEndpoints points the Gmail API and OAuth token requests at test
// servers; empty values keep Google's endpoints.
func WithGmailEndpoints(apiURL, tokenURL string) Option {
	return func(rc *runContext) { rc.gmailAPIURL, rc.gmailTokenURL = apiURL, tokenURL }
}

// WithBrowser replaces the desktop browser auth login opens (tests).
func WithBrowser(open func(url string) error) Option {
	return func(rc *runContext) { rc.browser = open }
}

// providerTimeout resolves the per-request timeout of provider: --timeout,
// else the provider's own setting ([mailgun], [smtp], or [gmail]
// timeout), else its built-in default. Settings of other providers never
// apply.
func providerTimeout(cfg *config.Config, provider string, flagTimeout time.Duration) time.Duration {
	if flagTimeout > 0 {
		return flagTimeout
	}
	switch provider {
	case config.ProviderSMTP:
		if cfg.SMTPTimeout > 0 {
			return cfg.SMTPTimeout
		}
		return smtp.DefaultTimeout
	case config.ProviderGmail:
		if cfg.GmailTimeout > 0 {
			return cfg.GmailTimeout
		}
		return gmail.DefaultTimeout
	default:
		if cfg.Timeout > 0 {
			return cfg.Timeout
		}
		return mailgun.DefaultTimeout
	}
}

// smtpClient builds the SMTP client from the [smtp] settings.
func smtpClient(cfg *config.Config, flagTimeout time.Duration, rc *runContext) (*smtp.Client, error) {
	return smtp.New(smtp.Config{
		Host: cfg.SMTPHost, Port: cfg.SMTPPort, Security: cfg.SMTPSecurity,
		Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
		Timeout: providerTimeout(cfg, config.ProviderSMTP, flagTimeout),
	}, rc.smtpOpts...)
}

// gmailStore is the credential store of the [gmail] settings.
func gmailStore(cfg *config.Config) *gmail.Store {
	path := cfg.GmailCredentials
	if path == "" {
		path = gmail.DefaultCredentialPath()
	}
	return &gmail.Store{Path: path}
}

// gmailOAuth is the OAuth client with the Gmail timeout; token refreshes
// are bounded like the API requests they precede.
func gmailOAuth(cfg *config.Config, flagTimeout time.Duration, rc *runContext) gmail.OAuthClient {
	o := gmail.DefaultOAuthClient(providerTimeout(cfg, config.ProviderGmail, flagTimeout))
	if rc.gmailTokenURL != "" {
		o.TokenURL = rc.gmailTokenURL
	}
	return o
}

// gmailClient refreshes the access token if needed and builds the API
// client. Authorization problems (not logged in, revoked grant) and
// transient refresh failures are classified send errors; anything else
// (no OAuth client in this build, unreadable credential file) is a
// configuration problem.
func gmailClient(ctx context.Context, cfg *config.Config, flagTimeout time.Duration, rc *runContext) (*gmail.Client, error) {
	creds, err := gmailStore(cfg).AccessToken(ctx, gmailOAuth(cfg, flagTimeout, rc), false)
	if err != nil {
		return nil, err
	}
	return gmail.New(creds.AccessToken, providerTimeout(cfg, config.ProviderGmail, flagTimeout), gmail.WithBaseURL(rc.gmailAPIURL))
}

// preparedProvider captures local provider settings and reusable clients.
// Gmail credentials are deliberately refreshed only at delivery time: a job
// may outlive an access token. checkLocal performs no network requests.
type preparedProvider struct {
	send       func(*message.Message) (resp *message.SendResponse, sendErr, setupErr error)
	checkLocal func() error
}

// prepareProvider is the single provider-selection point for send and run.
func prepareProvider(cfg *config.Config, opts *SendOptions, rc *runContext) (*preparedProvider, error) {
	switch cfg.Provider {
	case config.ProviderSMTP:
		client, err := smtpClient(cfg, opts.Timeout, rc)
		if err != nil {
			return nil, err
		}
		return messageProvider(client.SendMessage), nil
	case config.ProviderGmail:
		store, oauth := gmailStore(cfg), gmailOAuth(cfg, opts.Timeout, rc)
		timeout, apiURL := providerTimeout(cfg, config.ProviderGmail, opts.Timeout), rc.gmailAPIURL
		return &preparedProvider{
			checkLocal: func() error {
				if _, err := store.Read(); err != nil {
					return err
				}
				return oauth.Configured()
			},
			send: func(msg *message.Message) (*message.SendResponse, error, error) {
				return deliverGmail(store, oauth, timeout, apiURL, msg)
			},
		}, nil
	case config.ProviderMailgun, "":
		client, err := mailgun.New(cfg.APIKey, cfg.Domain, cfg.Region, clientOptions(rc, cfg, opts.Timeout)...)
		if err != nil {
			return nil, err
		}
		return messageProvider(client.SendMessage), nil
	default:
		return nil, fmt.Errorf("unsupported sending provider %q", cfg.Provider)
	}
}

func messageProvider(send func(context.Context, *message.Message) (*message.SendResponse, error)) *preparedProvider {
	return &preparedProvider{send: func(msg *message.Message) (*message.SendResponse, error, error) {
		resp, err := send(context.Background(), msg)
		return resp, err, nil
	}}
}

// deliverGmail sends msg through Gmail. setupErr reports a
// configuration problem found before anything was sent (exit 2, nothing
// stored); sendErr is the classified outcome of the attempt. For Gmail the
// message is spooled before the token is refreshed, so a transient refresh
// failure still leaves an encoded message to defer.
func deliverGmail(store *gmail.Store, oauth gmail.OAuthClient, timeout time.Duration, apiURL string, msg *message.Message) (resp *message.SendResponse, sendErr, setupErr error) {
	ctx := context.Background()
	s, id, err := gmail.EncodeMessageToSpool(msg)
	if err != nil {
		return nil, err, nil
	}
	msg.Spool, msg.EncodedType, msg.Size, msg.MessageID = s, mime.ContentType, int(s.Size()), id
	creds, err := store.AccessToken(ctx, oauth, false)
	switch {
	case err != nil && !classified(err):
		return nil, nil, err
	case err != nil:
		return nil, err, nil
	}
	client, err := gmail.New(creds.AccessToken, timeout, gmail.WithBaseURL(apiURL))
	if err != nil {
		return nil, nil, err
	}
	f, err := s.Open()
	if err != nil {
		return nil, &message.APIError{Provider: "gmail", Op: "send message", Kind: message.KindPermanent, Message: err.Error()}, nil
	}
	defer spool.CloseRead(f)
	resp, err = client.SendReader(ctx, msg.EncodedType, f, s.Size())
	return resp, err, nil
}

// storedProvider is the provider a deferred message was encoded for;
// messages stored before provider identity existed are Mailgun's.
func storedProvider(meta deferral.Metadata) string {
	if meta.Provider == "" {
		return config.ProviderMailgun
	}
	return meta.Provider
}

// providerAccount is the non-secret identity of provider's configured
// account, persisted with deferred messages: the Mailgun sending domain;
// for SMTP the username (when set), host, port, and security mode — never
// the password; for Gmail Google's opaque account ID from the stored
// credentials (or the credential file, for logins made before account IDs
// were recorded).
func providerAccount(cfg *config.Config, provider string) string {
	switch provider {
	case config.ProviderGmail:
		store := gmailStore(cfg)
		if c, err := store.Read(); err == nil && c.Account != "" {
			return "google:" + c.Account
		}
		return "credentials:" + store.Path
	case config.ProviderSMTP:
		if cfg.SMTPUsername != "" {
			return fmt.Sprintf("%s@%s:%d/%s", cfg.SMTPUsername, cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPSecurity)
		}
		return fmt.Sprintf("%s:%d/%s", cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPSecurity)
	default:
		return cfg.Domain
	}
}

// accountMatches reports whether the configured account of provider is the
// one a deferred message was stored for. A Gmail message stored with a
// credential-file identity (login without an account ID) matches the same
// file.
func accountMatches(stored string, cfg *config.Config, provider string) bool {
	if provider == config.ProviderGmail && strings.HasPrefix(stored, "credentials:") {
		return stored == "credentials:"+gmailStore(cfg).Path
	}
	return stored == providerAccount(cfg, provider)
}

// blockedResend is why a deferred message could not be attempted: its
// provider is not configured (or unknown), or the configured account is
// not the original one. Nothing was sent and the message's retry state is
// left untouched, so fixing the configuration lets a normal resend
// proceed — it is never marked permanently failed.
type blockedResend struct {
	reason string // "account_mismatch" or "provider_unavailable"
	err    error
}

// replay resends a stored payload through the provider and account it was
// encoded for, using that provider's current credentials — not whichever
// provider is now the default (tasks 30 and 31).
func replay(cfg *config.Config, flagTimeout time.Duration, rc *runContext, meta deferral.Metadata,
	body io.Reader, size int64) (*message.SendResponse, *blockedResend, error) {
	ctx := context.Background()
	provider := storedProvider(meta)
	unavailable := func(err error) (*message.SendResponse, *blockedResend, error) {
		return nil, &blockedResend{reason: "provider_unavailable",
			err: fmt.Errorf("the %s provider this message was deferred through is not usable: %w", provider, err)}, nil
	}
	if err := cfg.CheckProvider(provider); err != nil {
		return unavailable(err)
	}
	// Messages stored before account identity existed have no Account and
	// keep their historical behavior.
	if meta.Account != "" && !accountMatches(meta.Account, cfg, provider) {
		return nil, &blockedResend{reason: "account_mismatch", err: fmt.Errorf(
			"deferred through %s account %q, but the configured account is %q; restore the original configuration to resend it",
			provider, meta.Account, providerAccount(cfg, provider))}, nil
	}
	switch provider {
	case config.ProviderSMTP:
		client, err := smtpClient(cfg, flagTimeout, rc)
		if err != nil {
			return unavailable(err)
		}
		env := message.Envelope{From: meta.EnvelopeFrom, Recipients: meta.EnvelopeRecipients}
		resp, err := client.SendReader(ctx, env, body, size, meta.MessageID)
		return resp, nil, err
	case config.ProviderGmail:
		client, err := gmailClient(ctx, cfg, flagTimeout, rc)
		switch {
		case err != nil && !classified(err):
			return unavailable(err)
		case err != nil:
			return nil, nil, err
		}
		resp, err := client.SendReader(ctx, meta.ContentType, body, size)
		return resp, nil, err
	default:
		client, err := mailgun.New(cfg.APIKey, cfg.Domain, cfg.Region, clientOptions(rc, cfg, flagTimeout)...)
		if err != nil {
			return unavailable(err)
		}
		resp, err := client.SendReader(ctx, meta.ContentType, body, size)
		return resp, nil, err
	}
}

// classified reports whether err is a provider's classified send error
// (as opposed to a configuration problem found before any request).
func classified(err error) bool {
	serr, ok := message.As(err)
	return ok && serr != nil
}

// errTestUnsupported is the usage error for --test with a provider that has
// no server-side validation mode: it must never deliver instead.
func errTestUnsupported(provider string) error {
	return fmt.Errorf("--test is not available with the %s provider: it has no validation-only send mode, "+
		"so a test would deliver real email; use 'mailfire validate' to check the setup or --preview to check rendering", provider)
}
