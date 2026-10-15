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
	From     string   // sender, e.g. "Ops Bot <ops@example.com>"
	To       []string // recipients, at least one required by Mailgun
	Cc       []string
	Bcc      []string
	Subject  string
	Text     string // plain-text body; optional when HTML is set
	HTML     string // HTML body; optional when Text is set
	TestMode bool   // sends o:testmode=yes: Mailgun validates but does not deliver

	Attachments []File // sent as repeated "attachment" file parts
	Inline      []File // sent as repeated "inline" file parts
}

// SendResponse is Mailgun's answer to a successful send.
type SendResponse struct {
	ID      string // e.g. "<20261015...@mg.example.com>"
	Message string // e.g. "Queued. Thank you."
}

// SendMessage posts the message to /v3/{domain}/messages as
// multipart/form-data and returns Mailgun's message ID on success.
func (c *Client) SendMessage(ctx context.Context, msg *Message) (*SendResponse, error) {
	const op = "send message"

	body, contentType, err := encodeMessage(msg)
	if err != nil {
		return nil, &Error{Op: op, Kind: KindPermanent, Message: c.redact(err.Error())}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v3/"+url.PathEscape(c.domain)+"/messages", body)
	if err != nil {
		return nil, &Error{Op: op, Kind: KindPermanent, Message: c.redact(err.Error())}
	}
	req.Header.Set("Content-Type", contentType)

	respBody, err := c.do(req, op)
	if err != nil {
		return nil, err
	}
	var parsed SendResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil || parsed.ID == "" {
		return nil, &Error{Op: op, Kind: KindDeferrable,
			Message: "invalid response: expected JSON with a message id"}
	}
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

	respBody, err := c.do(req, op)
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
// On 2xx it returns the body; otherwise the error is a classified *Error
// carrying Mailgun's message with the API key redacted.
func (c *Client) do(req *http.Request, op string) ([]byte, error) {
	req.SetBasicAuth("api", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.transportError(op, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, &Error{Op: op, StatusCode: resp.StatusCode, Kind: KindDeferrable,
			Message: c.redact("reading response: " + err.Error())}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, nil
	}
	return nil, &Error{
		Op:         op,
		StatusCode: resp.StatusCode,
		Kind:       classifyStatus(resp.StatusCode),
		Message:    c.redact(extractMessage(resp.StatusCode, body)),
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
// response: timeouts and network errors are deferrable.
func (c *Client) transportError(op string, err error) error {
	msg := err.Error()
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		msg = fmt.Sprintf("request timed out after %s", c.timeout)
	} else if errors.Is(err, context.DeadlineExceeded) {
		msg = "request timed out: context deadline exceeded"
	}
	return &Error{Op: op, Kind: KindDeferrable, Message: c.redact(msg)}
}

// extractMessage pulls Mailgun's "message" field out of an error body,
// falling back to a truncated raw snippet and finally the status text.
func extractMessage(statusCode int, body []byte) string {
	var parsed struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Message != "" {
		return parsed.Message
	}
	if snippet := strings.TrimSpace(string(body)); snippet != "" {
		const max = 300
		if len(snippet) > max {
			snippet = snippet[:max] + "…"
		}
		return snippet
	}
	return http.StatusText(statusCode)
}

// encodeMessage builds the multipart/form-data body. The whole body is
// buffered in memory; Mailgun caps requests at 25 MB, which bounds the
// buffer.
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

// writeFile adds one file part with an explicit Content-Type.
func writeFile(w *multipart.Writer, field string, f File) error {
	content, err := io.ReadAll(f.Reader)
	if err != nil {
		return err
	}
	ct := f.ContentType
	if ct == "" {
		ct = http.DetectContentType(content)
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(field), escapeQuotes(f.Name)))
	h.Set("Content-Type", ct)
	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = part.Write(content)
	return err
}

// escapeQuotes escapes special characters in a multipart disposition
// parameter, mirroring mime/multipart's internal escaping.
var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"", "\r", "%0D", "\n", "%0A")

func escapeQuotes(s string) string { return quoteEscaper.Replace(s) }
