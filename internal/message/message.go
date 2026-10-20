// Package message defines the provider-neutral message type, the error
// taxonomy shared by all sending providers, and the SMTP envelope used to
// replay deferred messages (task 31).
//
// Providers (internal/mailgun, internal/smtp) accept a *Message, encode
// it for their transport, and return a *SendResponse. Failures are
// classified into auth, permanent, and deferrable kinds, which drive
// mailfire's exit codes and deferral decisions (PRD §8). Provider error
// types implement SendError; the IsAuth/IsPermanent/IsDeferrable helpers
// classify any error from any provider.
package message

import (
	"errors"
	"fmt"
	"io"
)

// Payload is a completed transport payload held on disk (internal/spool
// implements it). Providers set it on the Message; the caller removes it
// after the attempt (Abort) or adopts the file into the deferral store
// (Path, Size).
type Payload interface {
	Path() string
	Size() int64
	CopyTo(w io.Writer) error
	Abort() error
}

// File is a file attached to a message, either as a regular attachment or
// as an inline file referenced from the HTML body via cid:. For inline
// files, Name doubles as the Content-ID (matching the cid: URL the HTML
// references).
type File struct {
	// Name is the filename sent to the provider (e.g. "report.pdf").
	Name string
	// ContentType is optional; when empty, it is sniffed from the first
	// bytes and falls back to application/octet-stream.
	ContentType string
	// Reader yields the file content and is consumed once per send.
	Reader io.Reader
}

// Message is one email to send, independent of the transport. Later
// milestones populate attachments, inline files, and both body variants;
// the struct is designed so they can do so without changes here.
type Message struct {
	From    string   // sender, e.g. "Ops Bot <ops@example.com>"
	To      []string // recipients
	Cc      []string
	Bcc     []string
	Subject string
	Text    string // plain-text body; optional when HTML is set
	HTML    string // HTML body; optional when Text is set

	// TestMode requests the provider's server-side validation mode
	// (Mailgun's o:testmode=yes). Providers without such a mode (SMTP)
	// reject test sends at the CLI before reaching here. Set only from
	// the --test CLI flag, never from config. Invariant: resend never
	// sends in test mode, because --test sends are never deferred
	// (review 12 decision, see reportSendError in cli/send.go), so a
	// stored body never carries a test-mode marker.
	TestMode bool

	Attachments []File // regular attachments
	Inline      []File // inline files referenced via cid:

	// Size is the encoded transport request size in bytes, set by the
	// provider's SendMessage after encoding (0 before the first
	// SendMessage call or when encoding failed). It backs the "size"
	// field of debug-level logging (PRD FR-3). Consumers must not set it.
	Size int

	// Spool is the exact transport payload sent (multipart/form-data for
	// Mailgun, RFC 5322 MIME for SMTP/Gmail), held in an owner-only temporary
	// file rather than memory. EncodedType includes the multipart boundary
	// where applicable. MessageID is the generated RFC 5322 ID. Providers set
	// these fields alongside Size; callers remove or persist Spool.
	Spool       Payload
	EncodedType string
	MessageID   string
}

// SendResponse is a provider's answer to a successful send.
type SendResponse struct {
	ID      string // Mailgun's ID, or the generated Message-ID for SMTP
	Message string // e.g. "Queued. Thank you."

	// StatusCode is the provider's reply code of the response (HTTP 200
	// for Mailgun, SMTP 250). StatusCode and RawBody are set by the
	// client, never decoded from the response body.
	StatusCode int `json:"-"`
	// RawBody is the raw response with credentials redacted; it backs
	// the "response" field of debug-level logging (PRD FR-3).
	RawBody string `json:"-"`
}

// Envelope is the SMTP delivery envelope of a message: the bare MAIL FROM
// address and every envelope recipient (to, cc, and bcc — bcc recipients
// are delivered to but never appear in the transmitted message headers).
// Providers that embed recipients in the encoded payload (Mailgun) ignore
// it. Deferred messages store it in their metadata (task 31), so a resend
// replays to exactly the original recipients.
type Envelope struct {
	From       string
	Recipients []string
}

// Kind classifies a failure so callers can map it to an exit code and
// decide whether the message may be deferred for resend (PRD §8).
type Kind int

const (
	// KindUnknown is the zero value and never returned.
	KindUnknown Kind = iota
	// KindAuth means the credentials were rejected (HTTP 401/403, SMTP
	// 535); exit 3.
	KindAuth
	// KindPermanent means the request will never succeed as-is (other
	// client-side 4xx, permanent SMTP 5xx replies, TLS certificate
	// verification failures, local encoding errors, or the caller
	// canceling the context — e.g. on SIGINT, where the outcome is
	// unknown and deferring could cause a duplicate send); exit 5, no
	// deferral.
	KindPermanent
	// KindDeferrable means retrying later may succeed (HTTP 429, 5xx,
	// SMTP 4xx, network error, timeout, or an unparseable success
	// response); exit 4 when the message is deferred.
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

// SendError is the provider-neutral view of a send failure. Every
// provider error type implements it. Detail never contains credentials.
type SendError interface {
	error
	// SendKind is the failure classification (exit codes, deferral).
	SendKind() Kind
	// ReplyCode is the provider's reply or status code (HTTP status,
	// SMTP reply code), or 0 for transport errors and local failures.
	ReplyCode() int
	// Detail is the provider's error detail without a package prefix.
	Detail() string
}

// As unwraps err into a SendError, reporting whether it is one.
func As(err error) (SendError, bool) {
	var serr SendError
	if errors.As(err, &serr) {
		return serr, true
	}
	return nil, false
}

// IsAuth reports whether err is an authentication failure (exit 3).
func IsAuth(err error) bool {
	serr, ok := As(err)
	return ok && serr.SendKind() == KindAuth
}

// IsPermanent reports whether err is a non-retryable failure (exit 5, no
// deferral).
func IsPermanent(err error) bool {
	serr, ok := As(err)
	return ok && serr.SendKind() == KindPermanent
}

// IsDeferrable reports whether err may succeed on a later retry and the
// message should be deferred (exit 4 when deferred).
func IsDeferrable(err error) bool {
	serr, ok := As(err)
	return ok && serr.SendKind() == KindDeferrable
}

// APIError is the error type of HTTP API providers such as Gmail: the
// classification plus the HTTP status and the provider's (redacted)
// message. Provider prefixes Error(), e.g. "gmail: send message: status
// 403: ...". (The Mailgun client keeps its own, older mailgun.Error.)
type APIError struct {
	Provider   string // e.g. "gmail"
	Op         string // operation, e.g. "send message"
	StatusCode int    // HTTP status, or 0 for transport and local errors
	Message    string // detail; never contains credentials
	Kind       Kind
}

func (e *APIError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s: %s: status %d: %s", e.Provider, e.Op, e.StatusCode, e.Message)
	}
	return e.Provider + ": " + e.Op + ": " + e.Message
}

// SendKind implements SendError.
func (e *APIError) SendKind() Kind { return e.Kind }

// ReplyCode implements SendError.
func (e *APIError) ReplyCode() int { return e.StatusCode }

// Detail implements SendError.
func (e *APIError) Detail() string { return e.Message }
