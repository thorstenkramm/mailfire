// Package mailgun implements a minimal client for the Mailgun v3 API:
// sending messages (POST /v3/{domain}/messages) and looking up domains
// (GET /v3/domains/{domain}, used by the validate subcommand).
//
// Responses are classified into auth, permanent, and deferrable errors,
// which drive mailfire's exit codes (PRD §8). The API key is a secret: it
// is sent only in the Authorization header and is redacted from every
// error message, even when a (malicious) server echoes it back.
package mailgun

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// DefaultTimeout is the HTTP timeout applied when neither WithTimeout nor
// WithHTTPClient is used.
const DefaultTimeout = 30 * time.Second

// regionBaseURLs maps the config region to the Mailgun base URL (PRD §4).
var regionBaseURLs = map[string]string{
	"us": "https://api.mailgun.net",
	"eu": "https://api.eu.mailgun.net",
}

// Kind classifies a failure so callers can map it to an exit code and
// decide whether the message may be deferred for resend (PRD §8).
type Kind int

const (
	// KindUnknown is the zero value and never returned by this package.
	KindUnknown Kind = iota
	// KindAuth means the API key was rejected (HTTP 401/403); exit 3.
	KindAuth
	// KindPermanent means the request will never succeed as-is (other
	// HTTP 4xx, local encoding errors, or the caller canceling the
	// context — e.g. on SIGINT, where the outcome is unknown and deferring
	// could cause a duplicate send); exit 5, no deferral.
	KindPermanent
	// KindDeferrable means retrying later may succeed (HTTP 429, 5xx,
	// network error, timeout, or an unparseable success response);
	// exit 4 when the message is deferred.
	KindDeferrable
)

func (k Kind) String() string {
	switch k {
	case KindAuth:
		return "auth"
	case KindPermanent:
		return "permanent"
	case KindDeferrable:
		return "deferrable"
	default:
		return "unknown"
	}
}

// Error is the single error type returned by Client operations. The
// Message field never contains the API key.
type Error struct {
	Op         string // operation, e.g. "send message" or "validate domain"
	StatusCode int    // HTTP status code, or 0 for transport errors
	Message    string // Mailgun's message or the transport error detail
	Kind       Kind
}

func (e *Error) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("mailgun: %s: status %d: %s", e.Op, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("mailgun: %s: %s", e.Op, e.Message)
}

// As unwraps err into a *Error, reporting whether it is one.
func As(err error) (*Error, bool) {
	var merr *Error
	if errors.As(err, &merr) {
		return merr, true
	}
	return nil, false
}

// IsAuth reports whether err is an authentication failure (exit 3).
func IsAuth(err error) bool {
	merr, ok := As(err)
	return ok && merr.Kind == KindAuth
}

// IsPermanent reports whether err is a non-retryable client failure
// (exit 5, no deferral).
func IsPermanent(err error) bool {
	merr, ok := As(err)
	return ok && merr.Kind == KindPermanent
}

// IsDeferrable reports whether err may succeed on a later retry and the
// message should be deferred (exit 4 when deferred).
func IsDeferrable(err error) bool {
	merr, ok := As(err)
	return ok && merr.Kind == KindDeferrable
}

// Client talks to the Mailgun v3 API for one sending domain.
type Client struct {
	baseURL string
	domain  string
	apiKey  string // secret: only ever sent as the Basic Auth password
	http    *http.Client
	timeout time.Duration // remembered for timeout error messages
}

// String deliberately omits the API key.
func (c *Client) String() string {
	return fmt.Sprintf("mailgun.Client{base: %s, domain: %s}", c.baseURL, c.domain)
}

// Option customizes a Client. It exists mainly so tests can point the
// client at an httptest server.
type Option func(*Client)

// WithBaseURL overrides the region-derived base URL (trailing slashes are
// trimmed). Intended for tests.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		c.baseURL = strings.TrimRight(baseURL, "/")
	}
}

// WithTimeout sets the HTTP timeout. Non-positive values are ignored. The
// HTTP client is copied first, so a client passed via WithHTTPClient is
// never mutated.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			hc := *c.http
			hc.Timeout = d
			c.http = &hc
			c.timeout = d
		}
	}
}

// WithHTTPClient replaces the HTTP client (e.g. to install a custom
// transport). Its Timeout replaces the default (zero means no client
// timeout); use WithTimeout afterwards to override it.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.http = hc
			c.timeout = hc.Timeout
		}
	}
}

// New creates a Client for the given API key, sending domain, and region
// ("us" or "eu"). The API key is never logged or included in errors.
func New(apiKey, domain, region string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("mailgun: API key must not be empty")
	}
	if domain == "" {
		return nil, errors.New("mailgun: domain must not be empty")
	}
	baseURL, ok := regionBaseURLs[region]
	if !ok {
		return nil, fmt.Errorf("mailgun: unknown region %q: must be \"us\" or \"eu\"", region)
	}
	c := &Client{
		baseURL: baseURL,
		domain:  domain,
		apiKey:  apiKey,
		timeout: DefaultTimeout,
		http:    &http.Client{Timeout: DefaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// redact replaces any occurrence of the API key in s, including its
// percent-encoded forms and the base64 Basic Auth credential (a server
// echoing the Authorization header would otherwise leak it). Mailgun never
// legitimately echoes the key, but a malformed or malicious server could,
// and the key must never reach stderr or log files.
func (c *Client) redact(s string) string {
	if c.apiKey == "" {
		return s
	}
	for _, secret := range c.secretForms() {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	return s
}

// secretForms lists the encodings of the API key that might appear in
// text, longest first so a longer form is not partially replaced by a
// shorter one.
func (c *Client) secretForms() []string {
	forms := []string{
		base64.StdEncoding.EncodeToString([]byte("api:" + c.apiKey)),
		base64.StdEncoding.EncodeToString([]byte(c.apiKey)),
		url.QueryEscape(c.apiKey),
		url.PathEscape(c.apiKey),
		c.apiKey,
	}
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	return forms
}
