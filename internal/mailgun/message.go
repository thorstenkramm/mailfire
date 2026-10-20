package mailgun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/thorstenkramm/mailfire/internal/message"
	"github.com/thorstenkramm/mailfire/internal/spool"
)

// maxResponseBody caps how much of a Mailgun response is read. Real
// responses are a few hundred bytes; the cap guards against abuse.
const maxResponseBody = 1 << 20 // 1 MiB

// File is a file attached to a message, either as a regular attachment or
// as an inline file referenced from the HTML body via cid:. It is the
// provider-neutral type defined in internal/message (task 31).
type File = message.File

// Message is one email to send via POST /v3/{domain}/messages. It is the
// provider-neutral type defined in internal/message (task 31), shared with
// the SMTP provider. For Mailgun, Attachments are sent as repeated
// "attachment" file parts and Inline files as "inline" parts; Encoded
// holds the exact multipart request body.
type Message = message.Message

// SendResponse is Mailgun's answer to a successful send.
type SendResponse = message.SendResponse

// SendMessage posts the message to /v3/{domain}/messages as
// multipart/form-data and returns Mailgun's message ID on success.
func (c *Client) SendMessage(ctx context.Context, msg *Message) (*SendResponse, error) {
	var contentType string
	s, err := spool.Compose(func(w io.Writer) (err error) {
		contentType, err = encodeMessageTo(w, msg)
		return err
	})
	if err != nil {
		return nil, &Error{Op: "send message", Kind: KindPermanent, Message: c.redact(err.Error())}
	}
	msg.Size, msg.Spool, msg.EncodedType = int(s.Size()), s, contentType
	f, err := s.Open()
	if err != nil {
		return nil, &Error{Op: "send message", Kind: KindPermanent, Message: c.redact(err.Error())}
	}
	defer spool.CloseRead(f)
	return c.postMessages(ctx, "send message", contentType, f, s.Size())
}

// SendRaw posts body verbatim to /v3/{domain}/messages with the given
// Content-Type. It replays a previously encoded request — a deferred
// message restored from the defer_dir (FR-4): the stored bytes are sent
// byte-identically with the stored multipart Content-Type (boundary
// included), never re-composed. Response classification is exactly that
// of SendMessage. An empty body or Content-Type is a permanent error:
// there is nothing meaningful to replay.
//
// SendRaw is SendReader for an in-memory body; mailfire itself streams
// from files, so it remains as a convenience for small payloads and tests.
func (c *Client) SendRaw(ctx context.Context, contentType string, body []byte) (*SendResponse, error) {
	return c.SendReader(ctx, contentType, bytes.NewReader(body), int64(len(body)))
}

// SendReader replays a stored request body of size bytes from r without
// re-composing it. When r is an io.ReaderAt (a file), net/http can rewind
// it to retry a request that failed on a stale connection. A read error
// from r mid-request is reported like a transport error (deferrable): r is
// a local, already complete file, so such an error is rare and transient,
// and attachment read errors happened earlier, while composing the spool.
func (c *Client) SendReader(ctx context.Context, contentType string, r io.Reader, size int64) (*SendResponse, error) {
	const op = "resend message"
	if size <= 0 || r == nil || contentType == "" {
		return nil, &Error{Op: op, Kind: KindPermanent,
			Message: "nothing to replay: stored request body or content type is empty"}
	}
	return c.postMessages(ctx, op, contentType, r, size)
}

// postMessages posts body to /v3/{domain}/messages with the given
// Content-Type and classifies the response: the error kinds (and hence
// the caller's exit codes and deferral decisions) are identical whether
// the body was just encoded or replayed from the defer_dir.
func (c *Client) postMessages(ctx context.Context, op, contentType string, r io.Reader, size int64) (*SendResponse, error) {
	body, getBody := spool.HTTPBody(r, size)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v3/"+url.PathEscape(c.domain)+"/messages", body)
	if err != nil {
		return nil, &Error{Op: op, Kind: KindPermanent, Message: c.redact(err.Error())}
	}
	req.Header.Set("Content-Type", contentType)
	req.ContentLength, req.GetBody = size, getBody

	respBody, status, err := c.do(req, op)
	if err != nil {
		return nil, err
	}
	var parsed SendResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil || parsed.ID == "" {
		return nil, &Error{Op: op, StatusCode: status, Kind: KindDeferrable,
			Message: "invalid response: expected JSON with a message id"}
	}
	// The decoded fields are server-supplied and reach stdout (also as
	// JSON) and the log; redact them like the raw body.
	parsed.ID = c.redact(parsed.ID)
	parsed.Message = c.redact(parsed.Message)
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
	// The fields are server-supplied and end up on stdout; redact them
	// like error messages.
	parsed.Domain.Name = c.redact(parsed.Domain.Name)
	parsed.Domain.State = c.redact(parsed.Domain.State)
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
	defer func() {
		if err := resp.Body.Close(); err != nil {
			// The response determines delivery, not transport cleanup. Never
			// return a retryable error after a successful response.
			log.Printf("mailfire: closing response: %s", c.redact(err.Error()))
		}
	}()

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

// encodeMessageTo builds the multipart/form-data body in w. The send path
// uses an owner-only spool file: this preserves Content-Length and makes
// attachment read errors permanent local errors before any HTTP request,
// without buffering the body in memory (task 34).
func encodeMessageTo(dst io.Writer, msg *Message) (string, error) {
	return encodeMessageToBoundary(dst, msg, "")
}

// encodeMessageToBoundary is encodeMessageTo with a fixed-boundary test hook.
func encodeMessageToBoundary(dst io.Writer, msg *Message, boundary string) (string, error) {
	w := multipart.NewWriter(dst)
	if boundary != "" {
		if err := w.SetBoundary(boundary); err != nil {
			return "", fmt.Errorf("setting multipart boundary: %w", err)
		}
	}

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
			return "", fmt.Errorf("encoding field %q: %w", f.name, err)
		}
	}
	// Attachment-only messages still need a body field in the API request.
	if msg.Text != "" || msg.HTML == "" {
		if err := writeFields("text", msg.Text); err != nil {
			return "", fmt.Errorf("encoding field %q: %w", "text", err)
		}
	}
	if msg.HTML != "" {
		if err := writeFields("html", msg.HTML); err != nil {
			return "", fmt.Errorf("encoding field %q: %w", "html", err)
		}
	}
	if msg.TestMode {
		if err := writeFields("o:testmode", "yes"); err != nil {
			return "", fmt.Errorf("encoding field %q: %w", "o:testmode", err)
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
				return "", fmt.Errorf("encoding %s %q: %w", part.name, file.Name, err)
			}
		}
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("finalizing multipart body: %w", err)
	}
	return w.FormDataContentType(), nil
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
