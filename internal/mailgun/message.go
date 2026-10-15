package mailgun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"unicode/utf8"
)

// maxResponseBody caps how much of a Mailgun response is read. Real
// responses are a few hundred bytes; the cap guards against abuse.
const maxResponseBody = 1 << 20 // 1 MiB

// File is a file attached to a message, either as a regular attachment or
// as an inline file referenced from the HTML body via cid:.
type File struct {
	// Name is the filename sent to Mailgun (e.g. "report.pdf").
	Name string
	// ContentType is optional; when empty, it is sniffed from the first
	// bytes and falls back to application/octet-stream.
	ContentType string
	// Reader yields the file content and is consumed once per send.
	Reader io.Reader
}

// Message is one email to send via POST /v3/{domain}/messages. Later
// milestones populate attachments, inline files, and both body variants;
// the struct is designed so they can do so without changes here.
type Message struct {
	From    string   // sender, e.g. "Ops Bot <ops@example.com>"
	To      []string // recipients, at least one required by Mailgun
	Cc      []string
	Bcc     []string
	Subject string
	Text    string // plain-text body; optional when HTML is set
	HTML    string // HTML body; optional when Text is set

	// TestMode sends o:testmode=yes: Mailgun validates but does not
	// deliver. Set only from the --test CLI flag, never from config.
	// Invariant: resend (task 15) never sends in test mode, even if the
	// original send used --test (PRD FR-1, Decision Q5). This holds
	// because --test sends are never deferred (review 12 decision, see
	// reportSendError in cli/send.go), so a stored body never carries
	// o:testmode=yes.
	TestMode bool

	Attachments []File // sent as repeated "attachment" file parts
	Inline      []File // sent as repeated "inline" file parts

	// Size is the encoded multipart request size in bytes, set by
	// SendMessage after encoding (0 before the first SendMessage call or
	// when encoding failed). It backs the "size" field of debug-level
	// logging (PRD FR-3). Consumers must not set it.
	Size int

	// Encoded is the exact multipart request body sent to Mailgun and
	// EncodedType its Content-Type (boundary included). Both are set by
	// SendMessage alongside Size (nil/"" before the first call or when
	// encoding failed). Deferral (task 14) persists these bytes: the
	// readers in Attachments/Inline are consumed by encoding, so the
	// message cannot be re-encoded after a send attempt (review 09).
	// Consumers must not set them.
	Encoded     []byte
	EncodedType string
}

// SendResponse is Mailgun's answer to a successful send.
type SendResponse struct {
	ID      string // e.g. "<20261015...@mg.example.com>"
	Message string // e.g. "Queued. Thank you."

	// StatusCode is the HTTP status of the response (200 from Mailgun).
	// StatusCode and RawBody are set by the client, never decoded from
	// the response body.
	StatusCode int `json:"-"`
	// RawBody is the raw response body with any occurrence of the API
	// key redacted; it backs the "response" field of debug-level logging
	// (PRD FR-3). It is capped at maxResponseBody bytes.
	RawBody string `json:"-"`
}

// SendMessage posts the message to /v3/{domain}/messages as
// multipart/form-data and returns Mailgun's message ID on success.
func (c *Client) SendMessage(ctx context.Context, msg *Message) (*SendResponse, error) {
	const op = "send message"

	body, contentType, err := encodeMessage(msg)
	if err != nil {
		return nil, &Error{Op: op, Kind: KindPermanent, Message: c.redact(err.Error())}
	}
	msg.Size = body.Len()
	msg.Encoded = body.Bytes()
	msg.EncodedType = contentType

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v3/"+url.PathEscape(c.domain)+"/messages", body)
	if err != nil {
		return nil, &Error{Op: op, Kind: KindPermanent, Message: c.redact(err.Error())}
	}
	req.Header.Set("Content-Type", contentType)

	respBody, status, err := c.do(req, op)
	if err != nil {
		return nil, err
	}
	var parsed SendResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil || parsed.ID == "" {
		return nil, &Error{Op: op, StatusCode: status, Kind: KindDeferrable,
			Message: "invalid response: expected JSON with a message id"}
	}
	parsed.StatusCode = status
	parsed.RawBody = c.redact(string(respBody))
	return &parsed, nil
}

// DomainInfo is the subset of Mailgun's domain record that validate
// reports on.
type DomainInfo struct {
	Name  string
	State string // e.g. "active"
}

// ValidateDomain calls GET /v3/domains/{domain}. Mailgun has no dedicated
// ping route; this endpoint authenticates the API key and confirms the
// domain in one call (PRD FR-5).
func (c *Client) ValidateDomain(ctx context.Context) (*DomainInfo, error) {
	const op = "validate domain"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/v3/domains/"+url.PathEscape(c.domain), nil)
	if err != nil {
		return nil, &Error{Op: op, Kind: KindPermanent, Message: c.redact(err.Error())}
	}

	respBody, _, err := c.do(req, op)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Domain DomainInfo `json:"domain"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil || parsed.Domain.Name == "" {
		return nil, &Error{Op: op, Kind: KindDeferrable,
			Message: "invalid response: expected JSON with a domain record"}
	}
	return &parsed.Domain, nil
}

// do executes the request with Basic Auth and classifies the response.
// On 2xx it returns the body and the status code; otherwise the error is
// a classified *Error carrying Mailgun's message with the API key
// redacted.
func (c *Client) do(req *http.Request, op string) ([]byte, int, error) {
	req.SetBasicAuth("api", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, c.transportError(op, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		kind, msg := c.classifyTransport(err)
		return nil, 0, &Error{Op: op, StatusCode: resp.StatusCode, Kind: kind,
			Message: c.redact("reading response: " + msg)}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, resp.StatusCode, nil
	}
	return nil, 0, &Error{
		Op:         op,
		StatusCode: resp.StatusCode,
		Kind:       classifyStatus(resp.StatusCode),
		Message:    c.extractMessage(resp.StatusCode, body),
	}
}

// classifyStatus maps an HTTP status to an error Kind per PRD §8.
func classifyStatus(code int) Kind {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return KindAuth
	case code == http.StatusTooManyRequests:
		return KindDeferrable
	case code >= 400 && code < 500:
		return KindPermanent
	default: // 5xx and anything unexpected
		return KindDeferrable
	}
}

// transportError classifies a request that never received an HTTP
// response.
func (c *Client) transportError(op string, err error) error {
	kind, msg := c.classifyTransport(err)
	return &Error{Op: op, Kind: kind, Message: c.redact(msg)}
}

// classifyTransport maps a transport-level error to a Kind and message.
// Timeouts and network errors are deferrable. Cancellation by the caller
// is not: the user aborted, and since the request may already have reached
// Mailgun, deferring it could send the message twice.
func (c *Client) classifyTransport(err error) (Kind, string) {
	var nerr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return KindPermanent, "request canceled"
	case errors.As(err, &nerr) && nerr.Timeout() && c.timeout > 0:
		return KindDeferrable, fmt.Sprintf("request timed out after %s", c.timeout)
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &nerr) && nerr.Timeout()):
		return KindDeferrable, "request timed out: " + err.Error()
	default:
		return KindDeferrable, err.Error()
	}
}

// maxErrorMessage caps the length of a server-provided error message.
const maxErrorMessage = 300

// extractMessage pulls Mailgun's "message" field out of an error body,
// falling back to a raw snippet and finally the status text. The result is
// redacted before truncation so a key straddling the cut cannot leak
// partially.
func (c *Client) extractMessage(statusCode int, body []byte) string {
	var parsed struct {
		Message string `json:"message"`
	}
	msg := ""
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Message != "" {
		msg = parsed.Message
	} else {
		msg = strings.TrimSpace(string(body))
	}
	if msg == "" {
		return http.StatusText(statusCode)
	}
	return truncate(c.redact(msg), maxErrorMessage)
}

// truncate shortens s to at most max runes, appending an ellipsis.
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

// encodeMessage builds the multipart/form-data body. The whole body is
// buffered in memory (file parts are streamed into the buffer via
// io.Copy, so each file is held only once); Mailgun caps requests at
// 25 MB, which bounds the buffer. Streaming the body through an io.Pipe
// would avoid the buffer but loses Content-Length and turns local read
// errors into mid-request transport errors (misclassified as deferrable);
// revisit only if memory becomes a concern.
func encodeMessage(msg *Message) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	writeFields := func(name string, values ...string) error {
		for _, v := range values {
			if err := w.WriteField(name, v); err != nil {
				return err
			}
		}
		return nil
	}
	for _, f := range []struct {
		name   string
		values []string
	}{
		{"from", []string{msg.From}},
		{"to", msg.To},
		{"cc", msg.Cc},
		{"bcc", msg.Bcc},
		{"subject", []string{msg.Subject}},
	} {
		if err := writeFields(f.name, f.values...); err != nil {
			return nil, "", fmt.Errorf("encoding field %q: %w", f.name, err)
		}
	}
	if msg.Text != "" {
		if err := writeFields("text", msg.Text); err != nil {
			return nil, "", fmt.Errorf("encoding field %q: %w", "text", err)
		}
	}
	if msg.HTML != "" {
		if err := writeFields("html", msg.HTML); err != nil {
			return nil, "", fmt.Errorf("encoding field %q: %w", "html", err)
		}
	}
	if msg.TestMode {
		if err := writeFields("o:testmode", "yes"); err != nil {
			return nil, "", fmt.Errorf("encoding field %q: %w", "o:testmode", err)
		}
	}
	for _, part := range []struct {
		name  string
		files []File
	}{
		{"attachment", msg.Attachments},
		{"inline", msg.Inline},
	} {
		for _, file := range part.files {
			if err := writeFile(w, part.name, file); err != nil {
				return nil, "", fmt.Errorf("encoding %s %q: %w", part.name, file.Name, err)
			}
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("finalizing multipart body: %w", err)
	}
	return &buf, w.FormDataContentType(), nil
}

// writeFile adds one file part with an explicit Content-Type, streaming
// the content. Only the first 512 bytes are buffered, for sniffing.
func writeFile(w *multipart.Writer, field string, f File) error {
	if f.Reader == nil {
		return errors.New("no content reader")
	}
	src := f.Reader
	ct := f.ContentType
	if ct == "" {
		head := make([]byte, 512)
		n, err := io.ReadFull(src, head)
		// EOF means empty content and ErrUnexpectedEOF with n > 0 a short
		// file; anything else (including a reader failing on its first
		// read) is a real error.
		if err != nil && err != io.EOF && (err != io.ErrUnexpectedEOF || n == 0) {
			return err
		}
		head = head[:n]
		ct = http.DetectContentType(head)
		src = io.MultiReader(bytes.NewReader(head), src)
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(field), escapeQuotes(f.Name)))
	h.Set("Content-Type", ct)
	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(part, src)
	return err
}

// escapeQuotes escapes special characters in a multipart disposition
// parameter, mirroring mime/multipart's internal escaping.
var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"", "\r", "%0D", "\n", "%0A")

func escapeQuotes(s string) string { return quoteEscaper.Replace(s) }
