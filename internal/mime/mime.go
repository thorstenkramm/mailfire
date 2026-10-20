// Package mime builds standards-compliant RFC 5322/MIME messages from the
// provider-neutral message.Message (task 31). BuildTo streams the message
// to a writer — the send paths spool it to a temporary file, so memory
// use is independent of attachment size (task 34); Build returns the
// same bytes in memory. The SMTP provider submits the message over DATA;
// deferral stores it verbatim so a resend replays the identical message,
// Message-ID included.
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
	"bufio"
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
//
// Build holds the whole message in memory; the send paths use BuildTo
// with a spool file instead (task 34).
func Build(msg *message.Message, now time.Time) (raw []byte, messageID string, err error) {
	return build(msg, now, false)
}

// BuildWithBcc is for APIs, such as Gmail's message-send endpoint, that
// infer envelope recipients from RFC 5322 headers. Gmail consumes the Bcc
// list when accepting the message and strips it before delivery. SMTP
// must use Build because it supplies Bcc solely in its envelope.
func BuildWithBcc(msg *message.Message, now time.Time) (raw []byte, messageID string, err error) {
	return build(msg, now, true)
}

// BuildTo is Build streaming to w: attachments and inline files are read,
// base64-encoded, and written in small pieces, so memory use does not
// depend on their size. A write or read error aborts the build; the
// partial output is worthless and the caller discards it (the spool file
// is removed, nothing is sent or deferred).
func BuildTo(w io.Writer, msg *message.Message, now time.Time) (messageID string, err error) {
	return buildTo(w, msg, now, false, nil)
}

// BuildWithBccTo is BuildWithBcc streaming to w; see BuildTo.
func BuildWithBccTo(w io.Writer, msg *message.Message, now time.Time) (messageID string, err error) {
	return buildTo(w, msg, now, true, nil)
}

func build(msg *message.Message, now time.Time, includeBcc bool) (raw []byte, messageID string, err error) {
	return buildB(msg, now, includeBcc, nil)
}

// buildB is build with a test hook for fixed multipart boundaries.
func buildB(msg *message.Message, now time.Time, includeBcc bool, bnd *boundaryCursor) (raw []byte, messageID string, err error) {
	var buf bytes.Buffer
	id, err := buildTo(&buf, msg, now, includeBcc, bnd)
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), id, nil
}

func buildTo(w io.Writer, msg *message.Message, now time.Time, includeBcc bool, bnd *boundaryCursor) (messageID string, err error) {
	if msg == nil {
		return "", fmt.Errorf("mime: no message")
	}
	from, err := formatAddress(msg.From)
	if err != nil {
		return "", fmt.Errorf("mime: sender: %w", err)
	}
	to, err := formatAddressList(msg.To)
	if err != nil {
		return "", fmt.Errorf("mime: to: %w", err)
	}
	cc, err := formatAddressList(msg.Cc)
	if err != nil {
		return "", fmt.Errorf("mime: cc: %w", err)
	}
	var bcc string
	if includeBcc {
		bcc, err = formatAddressList(msg.Bcc)
		if err != nil {
			return "", fmt.Errorf("mime: bcc: %w", err)
		}
	}
	id, err := NewMessageID(msg.From, now)
	if err != nil {
		return "", err
	}
	// A nil file reader must fail the build before anything is written.
	for _, files := range [][]message.File{msg.Attachments, msg.Inline} {
		for _, f := range files {
			if f.Reader == nil {
				return "", fmt.Errorf("mime: file %q: no content reader", f.Name)
			}
		}
	}
	plan := planBody(msg, bnd)

	bw := bufio.NewWriter(w)
	hdr := func(name, value string) error {
		if value == "" {
			return nil
		}
		_, err := io.WriteString(bw, foldHeader(name, value))
		return err
	}
	for _, h := range [][2]string{
		{"From", from},
		{"To", to},
		{"Cc", cc},
		{"Bcc", bcc},
		{"Subject", stdmime.QEncoding.Encode("utf-8", msg.Subject)},
		{"Date", now.Format(time.RFC1123Z)},
		{"Message-ID", id},
		{"MIME-Version", "1.0"},
		{"Content-Type", plan.contentType(msg)},
	} {
		if err := hdr(h[0], h[1]); err != nil {
			return "", fmt.Errorf("mime: writing headers: %w", err)
		}
	}
	if _, err := io.WriteString(bw, "\r\n"); err != nil {
		return "", fmt.Errorf("mime: writing headers: %w", err)
	}
	if err := plan.write(bw, msg); err != nil {
		return "", err
	}
	if err := bw.Flush(); err != nil {
		return "", fmt.Errorf("mime: writing message: %w", err)
	}
	return id, nil
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

// boundaryCursor hands out fixed multipart boundaries, innermost first
// (alternative, related, mixed — the order the golden tests supply them);
// nil or exhausted means the random default in the form
// multipart.NewWriter generates. It exists so golden tests can pin the
// exact bytes (task 34).
type boundaryCursor struct{ list []string }

func (c *boundaryCursor) next() string {
	if c != nil && len(c.list) > 0 {
		b := c.list[0]
		c.list = c.list[1:]
		return b
	}
	return multipart.NewWriter(io.Discard).Boundary()
}

// bodyPlan is the MIME nesting of a message: the content parts (text,
// html), joined by multipart/alternative when both exist, wrapped in
// multipart/related for inline files and in multipart/mixed for
// attachments. The boundaries are decided up front, so the Content-Type
// headers of the enclosing parts are known before any content streams.
type bodyPlan struct {
	alternative, related, mixed bool
	altB, relB, mixB            string
}

func planBody(msg *message.Message, bnd *boundaryCursor) bodyPlan {
	p := bodyPlan{
		alternative: msg.Text != "" && msg.HTML != "",
		related:     len(msg.Inline) > 0,
		mixed:       len(msg.Attachments) > 0,
	}
	if p.alternative {
		p.altB = bnd.next()
	}
	if p.related {
		p.relB = bnd.next()
	}
	if p.mixed {
		p.mixB = bnd.next()
	}
	return p
}

// multipartCT renders the Content-Type of a multipart part.
func multipartCT(subtype, boundary string) string {
	return fmt.Sprintf("multipart/%s; boundary=%q", subtype, boundary)
}

// leafContentType is the Content-Type of a non-multipart body.
func leafContentType(msg *message.Message) string {
	if msg.Text != "" || msg.HTML == "" {
		return `text/plain; charset="utf-8"`
	}
	return `text/html; charset="utf-8"`
}

// contentType is the Content-Type of the message's root body part.
func (p bodyPlan) contentType(msg *message.Message) string {
	switch {
	case p.mixed:
		return multipartCT("mixed", p.mixB)
	case p.related:
		return multipartCT("related", p.relB)
	case p.alternative:
		return multipartCT("alternative", p.altB)
	}
	return leafContentType(msg)
}

// write streams the body to w: headers first (by the caller), then the
// content parts and files, nesting writers outermost-first. Only the
// 512-byte sniff head of each file is ever held in memory.
func (p bodyPlan) write(w io.Writer, msg *message.Message) error {
	out := w
	var mw, rw, aw *multipart.Writer
	newMultipart := func(subtype, boundary string) (*multipart.Writer, error) {
		v := multipart.NewWriter(out)
		if err := v.SetBoundary(boundary); err != nil {
			return nil, fmt.Errorf("mime: building multipart/%s: %w", subtype, err)
		}
		return v, nil
	}
	// nest opens a part of the multipart v for a nested multipart and
	// directs the output into it. A multipart whose content is the text
	// leaf does not nest: the leaf becomes a direct part of it.
	nest := func(v *multipart.Writer, subtype, childCT string) error {
		pw, err := v.CreatePart(textproto.MIMEHeader{"Content-Type": {childCT}})
		if err != nil {
			return fmt.Errorf("mime: building multipart/%s: %w", subtype, err)
		}
		out = pw
		return nil
	}
	var err error
	if p.mixed {
		if mw, err = newMultipart("mixed", p.mixB); err != nil {
			return err
		}
		switch {
		case p.related:
			if err := nest(mw, "mixed", multipartCT("related", p.relB)); err != nil {
				return err
			}
		case p.alternative:
			if err := nest(mw, "mixed", multipartCT("alternative", p.altB)); err != nil {
				return err
			}
		}
	}
	if p.related {
		if rw, err = newMultipart("related", p.relB); err != nil {
			return err
		}
		if p.alternative {
			if err := nest(rw, "related", multipartCT("alternative", p.altB)); err != nil {
				return err
			}
		}
	}
	if p.alternative {
		if aw, err = newMultipart("alternative", p.altB); err != nil {
			return err
		}
	}

	// The innermost multipart (or the bare message body) holds the text
	// parts; a single-leaf body without any multipart writes the
	// quoted-printable content without a Content-Transfer-Encoding
	// header, exactly as the buffered builder did. Each multipart closes
	// as soon as its last part is done: Close finishes the enclosing
	// part, so a level can no longer be written once a later part of its
	// parent begins.
	switch {
	case aw != nil:
		if err := writeTextPart(aw, "text/plain", msg.Text); err != nil {
			return err
		}
		if err := writeTextPart(aw, "text/html", msg.HTML); err != nil {
			return err
		}
		if err := closeMultipart("alternative", aw); err != nil {
			return err
		}
	case rw != nil:
		if err := writeTextPart(rw, leafType(msg), leafBody(msg)); err != nil {
			return err
		}
	case mw != nil:
		if err := writeTextPart(mw, leafType(msg), leafBody(msg)); err != nil {
			return err
		}
	default:
		if err := writeTextContent(out, leafType(msg), leafBody(msg)); err != nil {
			return err
		}
	}
	if rw != nil {
		for _, f := range msg.Inline {
			if err := writeFilePart(rw, f, true); err != nil {
				return err
			}
		}
		if err := closeMultipart("related", rw); err != nil {
			return err
		}
	}
	if mw != nil {
		for _, f := range msg.Attachments {
			if err := writeFilePart(mw, f, false); err != nil {
				return err
			}
		}
		if err := closeMultipart("mixed", mw); err != nil {
			return err
		}
	}
	return nil
}

func closeMultipart(subtype string, w *multipart.Writer) error {
	if err := w.Close(); err != nil {
		return fmt.Errorf("mime: building multipart/%s: %w", subtype, err)
	}
	return nil
} // leafType and leafBody pick the single content part of a non-alternative
// body (an attachment-only message gets an empty text part).
func leafType(msg *message.Message) string {
	if msg.Text != "" || msg.HTML == "" {
		return "text/plain"
	}
	return "text/html"
}

func leafBody(msg *message.Message) string {
	if msg.Text != "" || msg.HTML == "" {
		return msg.Text
	}
	return msg.HTML
}

// writeTextPart renders a UTF-8 text part with quoted-printable transfer
// encoding as one part of a multipart.
func writeTextPart(w *multipart.Writer, contentType, s string) error {
	h := textproto.MIMEHeader{}
	h.Set("Content-Transfer-Encoding", "quoted-printable")
	h.Set("Content-Type", contentType+`; charset="utf-8"`)
	pw, err := w.CreatePart(h)
	if err != nil {
		return fmt.Errorf("mime: encoding %s: %w", contentType, err)
	}
	return writeTextContent(pw, contentType, s)
}

// writeTextContent streams the quoted-printable encoding of s to w. Line
// endings are normalized to CRLF first: bodies arrive LF-normalized
// (except --html-raw, which may carry CRLF), and SMTP DATA requires CRLF.
func writeTextContent(w io.Writer, contentType, s string) error {
	qw := quotedprintable.NewWriter(w)
	if _, err := io.WriteString(qw, toCRLF(s)); err != nil {
		return fmt.Errorf("mime: encoding %s: %w", contentType, err)
	}
	if err := qw.Close(); err != nil {
		return fmt.Errorf("mime: encoding %s: %w", contentType, err)
	}
	return nil
}

// writeFilePart renders an attachment or inline file part: base64 content
// wrapped at 76 columns, a sniffed Content-Type when the file does not
// name one, and a Content-Disposition carrying the (RFC 2231-encoded when
// non-ASCII) filename. Inline parts additionally carry a Content-ID
// matching the cid: URL the HTML references: the filename is the cid
// (Decision Q4, internal/cli/images.go). Only the 512-byte sniff head is
// buffered; the content streams through the encoder.
func writeFilePart(w *multipart.Writer, f message.File, inline bool) error {
	src := f.Reader
	var head []byte
	ct := f.ContentType
	if ct == "" {
		sniff := make([]byte, 512)
		n, err := io.ReadFull(src, sniff)
		// EOF means empty content and ErrUnexpectedEOF with n > 0 a short
		// file; anything else (including a reader failing on its first
		// read) is a real error.
		if err != nil && err != io.EOF && (err != io.ErrUnexpectedEOF || n == 0) {
			return fmt.Errorf("mime: reading file %q: %w", f.Name, err)
		}
		head = sniff[:n]
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
	h.Set("Content-Type", ct)
	pw, err := w.CreatePart(h)
	if err != nil {
		return fmt.Errorf("mime: file %q: %w", f.Name, err)
	}
	lw := &lineWrapper{w: pw}
	enc := base64.NewEncoder(base64.StdEncoding, lw)
	if _, err := io.Copy(enc, io.MultiReader(bytes.NewReader(head), src)); err != nil {
		return fmt.Errorf("mime: reading file %q: %w", f.Name, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("mime: encoding file %q: %w", f.Name, err)
	}
	if err := lw.Close(); err != nil {
		return fmt.Errorf("mime: encoding file %q: %w", f.Name, err)
	}
	return nil
}

// lineWrapper wraps base64 output in the MIME base64 transfer encoding:
// lines of at most 76 characters, each CRLF-terminated (including the
// last). No input produces no output.
type lineWrapper struct {
	w     io.Writer
	col   int
	wrote bool
	err   error // sticky: base64.Encoder keeps calling after a failure
}

// crlf is shared so per-line termination never allocates.
var crlf = []byte("\r\n")

func (l *lineWrapper) Write(p []byte) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	written := 0
	for len(p) > 0 {
		n := min(76-l.col, len(p))
		if _, err := l.w.Write(p[:n]); err != nil {
			l.err = err
			return written, err
		}
		p = p[n:]
		l.col += n
		written += n
		l.wrote = true
		if l.col == 76 {
			if _, err := l.w.Write(crlf); err != nil {
				l.err = err
				return written, err
			}
			l.col = 0
		}
	}
	return written, nil
}

// Close terminates a partial last line with CRLF. A line that filled
// exactly was already terminated; empty input produced no output.
func (l *lineWrapper) Close() error {
	if l.err != nil {
		return l.err
	}
	if l.wrote && l.col > 0 {
		if _, err := l.w.Write(crlf); err != nil {
			return err
		}
	}
	return nil
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
