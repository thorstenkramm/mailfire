// Package gmail sends RFC 5322 messages through the Gmail API and manages
// the small, send-only OAuth credential used by Mailfire (task 30).
package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thorstenkramm/mailfire/internal/message"
	mailmime "github.com/thorstenkramm/mailfire/internal/mime"
)

// DefaultTimeout applies to Gmail API and OAuth token requests when no
// timeout is configured.
const DefaultTimeout = 30 * time.Second

// maxMessageSize is Gmail's documented limit for a raw message.
const maxMessageSize = 25 << 20

const apiURL = "https://gmail.googleapis.com/gmail/v1"

// Client sends as the authenticated Gmail user. Gmail decides whether the
// From address is the account or one of its configured send-as aliases;
// an unknown alias is replaced by the account's address.
type Client struct {
	token   string // secret: only sent as the bearer token
	http    *http.Client
	timeout time.Duration
	baseURL string
}

// Option customizes a Client; it exists for tests.
type Option func(*Client)

// WithBaseURL points the client at another API root (tests).
func WithBaseURL(u string) Option {
	return func(c *Client) {
		if u != "" {
			c.baseURL = strings.TrimRight(u, "/")
		}
	}
}

// New creates a client for an access token. A non-positive timeout uses
// DefaultTimeout.
func New(accessToken string, timeout time.Duration, opts ...Option) (*Client, error) {
	if accessToken == "" {
		return nil, notLoggedIn("no access token")
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	c := &Client{token: accessToken, http: &http.Client{Timeout: timeout}, timeout: timeout, baseURL: apiURL}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// String deliberately omits the token.
func (c *Client) String() string { return "gmail.Client{base: " + c.baseURL + "}" }

// SendMessage encodes the complete MIME message before contacting Gmail.
func (c *Client) SendMessage(ctx context.Context, msg *message.Message) (*message.SendResponse, error) {
	if msg.TestMode {
		return nil, permanent("send message", 0, "Gmail has no validation-only test mode; use --preview instead")
	}
	raw, err := EncodeMessage(msg)
	if err != nil {
		return nil, err
	}
	msg.Encoded, msg.EncodedType, msg.Size = raw, mailmime.ContentType, len(raw)
	return c.SendRaw(ctx, mailmime.ContentType, raw)
}

// SendRaw sends (or replays) a built RFC 5322 message. contentType is
// checked deliberately: Mailgun multipart queue entries must never reach
// Gmail.
func (c *Client) SendRaw(ctx context.Context, contentType string, raw []byte) (*message.SendResponse, error) {
	const op = "send message"
	if contentType != mailmime.ContentType || len(raw) == 0 {
		return nil, permanent(op, 0, "stored message is not a MIME message")
	}
	if len(raw) > maxMessageSize {
		return nil, permanent(op, 0, "message exceeds Gmail's 25 MiB size limit")
	}
	payload, err := json.Marshal(struct {
		Raw string `json:"raw"`
	}{base64.RawURLEncoding.EncodeToString(raw)})
	if err != nil {
		return nil, permanent(op, 0, err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/users/me/messages/send", bytes.NewReader(payload))
	if err != nil {
		return nil, permanent(op, 0, c.redact(err.Error()))
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.redactErr(transportError(op, err, c.timeout))
	}
	defer closeBody(resp.Body)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, c.redactErr(transportError(op, err, c.timeout))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classify(op, resp.StatusCode, c.redact(string(body)))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ID == "" {
		return nil, temporary(op, resp.StatusCode, "invalid response: expected a message id")
	}
	return &message.SendResponse{ID: c.redact(out.ID), StatusCode: resp.StatusCode, RawBody: c.redact(string(body))}, nil
}

// closeBody closes a response body after it was read. The response
// already decided the outcome, so a close failure is only logged (as in
// the Mailgun client); it never contains a token.
func closeBody(b io.Closer) {
	if err := b.Close(); err != nil {
		log.Printf("mailfire: gmail: closing response: %v", err)
	}
}

// redact keeps the bearer token out of anything a (proxied or malicious)
// server echoes back, in every encoding it could plausibly appear in.
func (c *Client) redact(s string) string {
	if c.token == "" {
		return s
	}
	for _, form := range []string{
		"Bearer " + c.token,
		base64.StdEncoding.EncodeToString([]byte(c.token)),
		base64.RawURLEncoding.EncodeToString([]byte(c.token)),
		url.QueryEscape(c.token),
		c.token,
	} {
		s = strings.ReplaceAll(s, form, "[redacted]")
	}
	return s
}

func (c *Client) redactErr(err error) error {
	var gerr *Error
	if errors.As(err, &gerr) {
		gerr.Message = c.redact(gerr.Message)
	}
	return err
}

// Error is the error type of Gmail API and OAuth operations: the shared
// provider error with Provider "gmail". Message never contains tokens.
type Error = message.APIError

// newError builds a Gmail error.
func newError(op string, status int, kind message.Kind, msg string) error {
	return &Error{Provider: "gmail", Op: op, StatusCode: status, Kind: kind, Message: msg}
}

func permanent(op string, status int, s string) error {
	return newError(op, status, message.KindPermanent, s)
}

func temporary(op string, status int, s string) error {
	return newError(op, status, message.KindDeferrable, s)
}

// transportError classifies a request that got no usable response, like
// the Mailgun client: timeouts and network errors may succeed later;
// cancellation (SIGINT) does not, because the request may already have
// been accepted and a retry could duplicate it.
func transportError(op string, err error, timeout time.Duration) error {
	var nerr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return permanent(op, 0, "request canceled")
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &nerr) && nerr.Timeout()):
		return temporary(op, 0, fmt.Sprintf("request timed out after %s", timeout))
	default:
		return temporary(op, 0, err.Error())
	}
}

// classify maps a Gmail API error response to an error kind.
func classify(op string, status int, body string) error {
	msg := strings.TrimSpace(body)
	var x struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &x) == nil && x.Error.Message != "" {
		msg = x.Error.Message
	}
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	k := message.KindPermanent
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		k = message.KindAuth
		if strings.Contains(strings.ToLower(msg), "insufficient authentication scopes") {
			msg += "; the authorization does not allow sending: run 'mailfire auth login --provider gmail' and allow \"Send email on your behalf\""
		}
	case status == http.StatusTooManyRequests || status >= 500:
		k = message.KindDeferrable
	}
	return newError(op, status, k, msg)
}

// EncodeMessage produces the RFC 5322 message for Gmail with the shared
// MIME builder; failures are permanent Gmail errors. Unlike SMTP, Gmail's raw-message API has no separate
// envelope: it takes the recipients, Bcc included, from the headers and
// removes the Bcc header before delivery, so recipients never see it.
func EncodeMessage(msg *message.Message) ([]byte, error) {
	raw, _, err := mailmime.BuildWithBcc(msg, time.Now())
	if err != nil {
		return nil, permanent("encode message", 0, err.Error())
	}
	if len(raw) > maxMessageSize {
		return nil, permanent("encode message", 0, "message exceeds Gmail's 25 MiB size limit")
	}
	return raw, nil
}
