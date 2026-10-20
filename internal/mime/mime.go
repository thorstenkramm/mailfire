// Package mime builds standards-compliant RFC 5322/MIME messages from the
// provider-neutral message.Message (task 31). The SMTP provider submits
// these bytes over DATA; deferral stores them verbatim so a resend replays
// the identical message, Message-ID included.
//
// The builder supplies the headers a submission server expects from the
// client — Date and Message-ID (SMTP servers do not consistently return an
// API-like ID, so the generated Message-ID is what mailfire reports), From,
// To, Cc, Subject, and MIME-Version — and it never emits a Bcc header: Bcc
// recipients live in the envelope only.
//
// Layout: a text/plain part and, when present, a text/html part form a
// multipart/alternative; inline files (referenced via cid:) wrap the
// alternative in multipart/related; attachments wrap everything in
// multipart/mixed. Text parts are quoted-printable (UTF-8), file parts
// base64 with 76-column lines. All line endings are CRLF; the SMTP
// transport additionally dot-stuffs (net/smtp's DATA writer).
package mime

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	stdmime "mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thorstenkramm/mailfire/internal/message"
)

// ContentType is the stored/transport content type of a built message.
const ContentType = "message/rfc822"

// Build renders msg as an RFC 5322 message and returns the raw bytes and
// the generated Message-ID (angle brackets included). The file readers in
// Attachments and Inline are consumed. now supplies the Date header and
// the Message-ID's timestamp part.
func Build(msg *message.Message, now time.Time) (raw []byte, messageID string, err error) {
	return build(msg, now, false)
}

// BuildWithBcc is for APIs, such as Gmail's raw-message endpoint, that infer
// envelope recipients from RFC 5322 headers. Gmail consumes the Bcc list when
// accepting the raw message and strips it before delivery. SMTP must use Build
// because it supplies Bcc solely in its envelope.
func BuildWithBcc(msg *message.Message, now time.Time) (raw []byte, messageID string, err error) {
	return build(msg, now, true)
}

func build(msg *message.Message, now time.Time, includeBcc bool) (raw []byte, messageID string, err error) {
	return buildB(msg, now, includeBcc, nil)
}

// buildB is build with a test hook for fixed multipart boundaries.
func buildB(msg *message.Message, now time.Time, includeBcc bool, bnd *boundaryCursor) (raw []byte, messageID string, err error) {
	if msg == nil {
		return nil, "", fmt.Errorf("mime: no message")
	}
	from, err := formatAddress(msg.From)
	if err != nil {
		return nil, "", fmt.Errorf("mime: sender: %w", err)
	}
	to, err := formatAddressList(msg.To)
	if err != nil {
		return nil, "", fmt.Errorf("mime: to: %w", err)
	}
	cc, err := formatAddressList(msg.Cc)
	if err != nil {
		return nil, "", fmt.Errorf("mime: cc: %w", err)
	}
	id, err := NewMessageID(msg.From, now)
	if err != nil {
		return nil, "", err
	}
	body, err := buildBody(msg, bnd)
	if err != nil {
		return nil, "", err
	}

	var buf bytes.Buffer
	hdr := func(name, value string) {
		if value == "" {
			return
		}
		buf.WriteString(foldHeader(name, value))
	}
	hdr("From", from)
	hdr("To", to)
	hdr("Cc", cc)
	if includeBcc {
		bcc, err := formatAddressList(msg.Bcc)
		if err != nil {
			return nil, "", fmt.Errorf("mime: bcc: %w", err)
		}
		hdr("Bcc", bcc)
	}
	hdr("Subject", stdmime.QEncoding.Encode("utf-8", msg.Subject))
	hdr("Date", now.Format(time.RFC1123Z))
	hdr("Message-ID", id)
	hdr("MIME-Version", "1.0")
	hdr("Content-Type", body.contentType)
	buf.WriteString("\r\n")
	buf.Write(body.data)
	return buf.Bytes(), id, nil
}

// NewMessageID generates a Message-ID for a message sent from the address
// from: a UTC timestamp and 16 random hex characters at the sender's
// domain. It is stable once built — deferral stores the built message, so
// retries keep the ID (task 31).
func NewMessageID(from string, now time.Time) (string, error) {
	a, err := mail.ParseAddress(from)
	if err != nil {
		return "", fmt.Errorf("mime: sender: %w", err)
	}
	domain := a.Address[strings.LastIndex(a.Address, "@")+1:]
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("mime: generating message id: %w", err)
	}
	return fmt.Sprintf("<%s.%s@%s>",
		now.UTC().Format("20060102T150405"), hex.EncodeToString(rnd[:]), domain), nil
}

// formatAddress normalizes one mailbox for a header: the display name is
// quoted and RFC 2047-encoded as needed, the address left bare.
func formatAddress(s string) (string, error) {
	a, err := mail.ParseAddress(s)
	if err != nil {
		return "", err
	}
	return a.String(), nil
}

// formatAddressList formats a recipient list for a To/Cc header.
func formatAddressList(addrs []string) (string, error) {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		f, err := formatAddress(a)
		if err != nil {
			return "", err
		}
		out = append(out, f)
	}
	return strings.Join(out, ", "), nil
}

// rendered is a MIME body part: a leaf (encoded content with extra
// headers) or a nested multipart (data holds the whole body, including
// the closing boundary).
type rendered struct {
	contentType string
	data        []byte
	header      textproto.MIMEHeader
}

// boundaryCursor hands out fixed multipart boundaries, innermost first
// (alternative, related, mixed — the order buildBody creates them); nil
// or exhausted means the random default. It exists so golden tests can
// pin the exact bytes (task 34).
type boundaryCursor struct{ list []string }

func (c *boundaryCursor) next() string {
	if c == nil || len(c.list) == 0 {
		return ""
	}
	b := c.list[0]
	c.list = c.list[1:]
	return b
}

// buildBody renders the message body: the innermost content (a single
// part or multipart/alternative), wrapped in multipart/related for inline
// files and in multipart/mixed for attachments.
func buildBody(msg *message.Message, bnd *boundaryCursor) (rendered, error) {
	leaves, err := contentLeaves(msg)
	if err != nil {
		return rendered{}, err
	}
	content := leaves[0]
	if len(leaves) > 1 {
		if content, err = renderMultipart("alternative", leaves, bnd); err != nil {
			return rendered{}, err
		}
	}

	if len(msg.Inline) > 0 {
		leaves := []rendered{content}
		for _, f := range msg.Inline {
			leaf, err := fileLeaf(f, true)
			if err != nil {
				return rendered{}, err
			}
			leaves = append(leaves, leaf)
		}
		if content, err = renderMultipart("related", leaves, bnd); err != nil {
			return rendered{}, err
		}
	}
	if len(msg.Attachments) > 0 {
		leaves := []rendered{content}
		for _, f := range msg.Attachments {
			leaf, err := fileLeaf(f, false)
			if err != nil {
				return rendered{}, err
			}
			leaves = append(leaves, leaf)
		}
		if content, err = renderMultipart("mixed", leaves, bnd); err != nil {
			return rendered{}, err
		}
	}
	return content, nil
}

// contentLeaves returns the text (and, when present, HTML) parts. An
// attachment-only message gets an empty text part.
func contentLeaves(msg *message.Message) ([]rendered, error) {
	var leaves []rendered
	if msg.Text != "" || msg.HTML == "" {
		leaf, err := textLeaf("text/plain", msg.Text)
		if err != nil {
			return nil, err
		}
		leaves = append(leaves, leaf)
	}
	if msg.HTML != "" {
		leaf, err := textLeaf("text/html", msg.HTML)
		if err != nil {
			return nil, err
		}
		leaves = append(leaves, leaf)
	}
	return leaves, nil
}

// textLeaf renders a UTF-8 text part with quoted-printable transfer
// encoding. Line endings are normalized to CRLF first: bodies arrive
// LF-normalized (except --html-raw, which may carry CRLF), and SMTP DATA
// requires CRLF.
func textLeaf(contentType, s string) (rendered, error) {
	var buf bytes.Buffer
	w := quotedprintable.NewWriter(&buf)
	if _, err := w.Write([]byte(toCRLF(s))); err != nil {
		return rendered{}, fmt.Errorf("mime: encoding %s: %w", contentType, err)
	}
	if err := w.Close(); err != nil {
		return rendered{}, fmt.Errorf("mime: encoding %s: %w", contentType, err)
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Transfer-Encoding", "quoted-printable")
	return rendered{
		contentType: contentType + `; charset="utf-8"`,
		data:        buf.Bytes(),
		header:      h,
	}, nil
}

// fileLeaf renders an attachment or inline file part: base64 content
// wrapped at 76 columns, a sniffed Content-Type when the file does not
// name one, and a Content-Disposition carrying the (RFC 2231-encoded when
// non-ASCII) filename. Inline parts additionally carry a Content-ID
// matching the cid: URL the HTML references: the filename is the cid
// (Decision Q4, internal/cli/images.go).
func fileLeaf(f message.File, inline bool) (rendered, error) {
	if f.Reader == nil {
		return rendered{}, fmt.Errorf("mime: file %q: no content reader", f.Name)
	}
	data, err := io.ReadAll(f.Reader)
	if err != nil {
		return rendered{}, fmt.Errorf("mime: reading file %q: %w", f.Name, err)
	}
	ct := f.ContentType
	if ct == "" {
		head := data
		if len(head) > 512 {
			head = head[:512]
		}
		ct = http.DetectContentType(head)
	}

	h := textproto.MIMEHeader{}
	h.Set("Content-Transfer-Encoding", "base64")
	disp := "attachment"
	if inline {
		disp = "inline"
		// The cid: URL references the bare filename; RFC 2392 matches it
		// against the Content-ID without the angle brackets.
		h.Set("Content-ID", "<"+sanitizeHeaderValue(f.Name)+">")
	}
	h.Set("Content-Disposition", disp+"; "+filenameParam(f.Name))
	return rendered{contentType: ct, data: encodeBase64(data), header: h}, nil
}

// renderMultipart assembles children into one multipart body.
func renderMultipart(subtype string, children []rendered, bnd *boundaryCursor) (rendered, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if b := bnd.next(); b != "" {
		if err := w.SetBoundary(b); err != nil {
			return rendered{}, fmt.Errorf("mime: building multipart/%s: %w", subtype, err)
		}
	}
	for _, c := range children {
		h := c.header
		if h == nil {
			h = textproto.MIMEHeader{}
		}
		h.Set("Content-Type", c.contentType)
		pw, err := w.CreatePart(h)
		if err != nil {
			return rendered{}, fmt.Errorf("mime: building multipart/%s: %w", subtype, err)
		}
		if _, err := pw.Write(c.data); err != nil {
			return rendered{}, fmt.Errorf("mime: building multipart/%s: %w", subtype, err)
		}
	}
	if err := w.Close(); err != nil {
		return rendered{}, fmt.Errorf("mime: building multipart/%s: %w", subtype, err)
	}
	return rendered{
		contentType: fmt.Sprintf("multipart/%s; boundary=%q", subtype, w.Boundary()),
		data:        buf.Bytes(),
	}, nil
}

// encodeBase64 renders data in the MIME base64 transfer encoding: lines
// of at most 76 characters, CRLF-terminated.
func encodeBase64(data []byte) []byte {
	var buf bytes.Buffer
	for len(data) > 0 {
		n := min(57, len(data)) // 57 bytes encode to exactly 76 characters
		buf.WriteString(base64.StdEncoding.EncodeToString(data[:n]))
		buf.WriteString("\r\n")
		data = data[n:]
	}
	return buf.Bytes()
}

// toCRLF normalizes all line endings to CRLF.
func toCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// filenameParam renders a Content-Disposition filename parameter: a
// quoted string for ASCII names (with CR/LF neutralized), the RFC 2231
// extended form for non-ASCII names.
func filenameParam(name string) string {
	if isASCII(name) {
		return `filename="` + escapeQuotes(name) + `"`
	}
	return "filename*=utf-8''" + rfc2231Encode(name)
}

// escapeQuotes escapes special characters in a quoted disposition
// parameter, mirroring mime/multipart's internal escaping.
var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"", "\r", "%0D", "\n", "%0A")

func escapeQuotes(s string) string { return quoteEscaper.Replace(s) }

// sanitizeHeaderValue strips CR/LF so a filename can never break out of
// its header.
func sanitizeHeaderValue(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, s)
}

// rfc2231Encode percent-encodes s for an extended parameter value
// (RFC 2231): everything outside the attr-char set is %-encoded.
func rfc2231Encode(s string) string {
	const attrChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!#$&+-.^_`|~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(attrChars, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// isASCII reports whether s is pure ASCII.
func isASCII(s string) bool {
	return !strings.ContainsRune(s, utf8.RuneError) && strings.IndexFunc(s, func(r rune) bool { return r > 127 }) < 0
}

// maxHeaderLine is the recommended header line length (RFC 5322 §2.1.1);
// the hard limit is 998.
const maxHeaderLine = 78

// foldHeader renders "Name: value" CRLF-terminated, folding at spaces so
// lines stay within maxHeaderLine where the value allows it (long address
// lists, Unicode subjects encoded as several encoded-words). Folding
// inserts CRLF before an existing space, so unfolding restores the value
// exactly. A single word longer than the limit is never split.
func foldHeader(name, value string) string {
	var b strings.Builder
	line := name + ":"
	for _, w := range strings.Split(value, " ") {
		// Folding is also allowed directly after the colon, which keeps a
		// long first encoded-word within the limit.
		if len(line)+1+len(w) > maxHeaderLine && len(line) > 1 && w != "" {
			b.WriteString(line)
			b.WriteString("\r\n")
			line = " " + w
			continue
		}
		line += " " + w
	}
	b.WriteString(line)
	b.WriteString("\r\n")
	return b.String()
}
