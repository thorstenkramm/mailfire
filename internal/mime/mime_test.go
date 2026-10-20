package mime

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	stdmime "mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/message"
)

func TestBuildAlternativesAndBccPrivacy(t *testing.T) {
	msg := &message.Message{
		From: `Jörg <sender@example.com>`, To: []string{"to@example.com"},
		Cc: []string{"cc@example.com"}, Bcc: []string{"secret@example.com"},
		Subject: "Résumé ✓", Text: "plain\n.line\n", HTML: "<p>html</p>",
	}
	raw, id, err := Build(msg, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"MIME-Version: 1.0\r\n", "Content-Type: multipart/alternative", "Content-Transfer-Encoding: quoted-printable", "To: <to@example.com>\r\n", "Cc: <cc@example.com>\r\n", "plain\r\n.line\r\n", "<p>html</p>"} {
		if !strings.Contains(s, want) {
			t.Errorf("message missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "Bcc:") || strings.Contains(s, "secret@example.com") {
		t.Errorf("Bcc leaked into MIME:\n%s", s)
	}
	if !strings.HasPrefix(id, "<20261018T120000.") || !strings.Contains(s, "Message-ID: "+id+"\r\n") {
		t.Errorf("Message-ID = %q not present", id)
	}
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Error("message contains bare LF")
	}
}

// parse decodes a built message into its header and leaf parts (decoded
// content keyed by Content-Type media type), following nested multiparts.
func parse(t *testing.T, raw []byte) (*mail.Message, []part) {
	t.Helper()
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	var parts []part
	var walk func(h textproto.MIMEHeader, body io.Reader, path string)
	walk = func(h textproto.MIMEHeader, body io.Reader, path string) {
		mt, params, err := stdmime.ParseMediaType(h.Get("Content-Type"))
		if err != nil {
			t.Fatalf("%s: Content-Type %q: %v", path, h.Get("Content-Type"), err)
		}
		if strings.HasPrefix(mt, "multipart/") {
			mr := multipart.NewReader(body, params["boundary"])
			for i := 0; ; i++ {
				p, err := mr.NextRawPart()
				if err == io.EOF {
					return
				}
				if err != nil {
					t.Fatalf("%s: %v", path, err)
				}
				walk(p.Header, p, path+"/"+mt)
			}
		}
		r := body
		switch h.Get("Content-Transfer-Encoding") {
		case "base64":
			r = base64.NewDecoder(base64.StdEncoding, body)
		case "quoted-printable":
			r = quotedprintable.NewReader(body)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("%s: decoding %s: %v", path, mt, err)
		}
		parts = append(parts, part{path: path, mediaType: mt, header: h, data: data})
	}
	walk(textproto.MIMEHeader(m.Header), m.Body, "")
	return m, parts
}

type part struct {
	path, mediaType string
	header          textproto.MIMEHeader
	data            []byte
}

func TestBuildAttachmentsInlineImagesAndStructure(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("image-bytes", 30))
	pdf := bytes.Repeat([]byte{0, 1, 2, 250}, 100)
	msg := &message.Message{
		From: "ops@example.com", To: []string{"to@example.com"}, Subject: "s",
		Text: "text part", HTML: `<p><img src="cid:img-1234.png"></p>`,
		Inline:      []message.File{{Name: "img-1234.png", Reader: bytes.NewReader(png)}},
		Attachments: []message.File{{Name: "Bericht März ü.pdf", ContentType: "application/pdf", Reader: bytes.NewReader(pdf)}},
	}
	raw, _, err := Build(msg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, parts := parse(t, raw)
	if len(parts) != 4 {
		t.Fatalf("got %d leaf parts, want 4 (text, html, image, attachment)", len(parts))
	}
	byType := map[string]part{}
	for _, p := range parts {
		byType[p.mediaType] = p
	}
	if p := byType["text/plain"]; string(p.data) != "text part" || p.path != "/multipart/mixed/multipart/related/multipart/alternative" {
		t.Errorf("text part = %q at %q", p.data, p.path)
	}
	img := byType["image/png"]
	if !bytes.Equal(img.data, png) || img.header.Get("Content-ID") != "<img-1234.png>" ||
		!strings.HasPrefix(img.header.Get("Content-Disposition"), "inline") || img.path != "/multipart/mixed/multipart/related" {
		t.Errorf("inline image: path %q, headers %v, intact=%v", img.path, img.header, bytes.Equal(img.data, png))
	}
	att := byType["application/pdf"]
	if !bytes.Equal(att.data, pdf) {
		t.Error("attachment content changed")
	}
	if _, params, err := stdmime.ParseMediaType(att.header.Get("Content-Disposition")); err != nil || params["filename"] != "Bericht März ü.pdf" {
		t.Errorf("attachment disposition %q: filename %q, err %v", att.header.Get("Content-Disposition"), params["filename"], err)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line exceeds 998 characters: %d", len(line))
		}
	}
}

func TestBuildFoldsLongHeaders(t *testing.T) {
	var to []string
	for i := 0; i < 25; i++ {
		to = append(to, fmt.Sprintf("Recipient Number %d <recipient%d@example.com>", i, i))
	}
	subject := strings.Repeat("Größenänderung des Speichers ✓ ", 8)
	msg := &message.Message{From: "Jörg Müller <ops@example.com>", To: to, Subject: subject, Text: "x"}
	raw, _, err := Build(msg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	head := string(raw[:bytes.Index(raw, []byte("\r\n\r\n"))])
	for _, line := range strings.Split(head, "\r\n") {
		if len(line) > maxHeaderLine {
			t.Errorf("header line has %d characters (> %d): %q", len(line), maxHeaderLine, line)
		}
	}
	m, _ := parse(t, raw)
	addrs, err := m.Header.AddressList("To")
	if err != nil || len(addrs) != len(to) || addrs[24].Address != "recipient24@example.com" || addrs[3].Name != "Recipient Number 3" {
		t.Errorf("folded To does not parse back: %v, %v", addrs, err)
	}
	var dec stdmime.WordDecoder
	if got, err := dec.DecodeHeader(m.Header.Get("Subject")); err != nil || got != subject {
		t.Errorf("subject decodes to %q (%v), want %q", got, err, subject)
	}
	if from, err := m.Header.AddressList("From"); err != nil || from[0].Name != "Jörg Müller" {
		t.Errorf("From = %v, %v", from, err)
	}
}

func TestBuildWithBccForGmail(t *testing.T) {
	raw, _, err := BuildWithBcc(&message.Message{From: "ops@example.com", To: []string{"to@example.com"}, Bcc: []string{"b@example.com"}, Text: "x"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("Bcc: <b@example.com>\r\n")) {
		t.Errorf("BuildWithBcc lacks the Bcc header:\n%s", raw)
	}
}

func TestBuildAttachmentOnly(t *testing.T) {
	raw, _, err := Build(&message.Message{From: "ops@example.com", To: []string{"to@example.com"},
		Attachments: []message.File{{Name: "a.txt", Reader: strings.NewReader("x")}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, parts := parse(t, raw); len(parts) != 2 || parts[0].mediaType != "text/plain" {
		t.Fatalf("parts = %+v", parts)
	}
}

// zeros is an infinite zero reader: a large attachment without the
// allocation.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type failAfter struct {
	left int
}

func (r *failAfter) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, errors.New("injected read failure")
	}
	n := min(len(p), r.left)
	clear(p[:n])
	r.left -= n
	return n, nil
}

// TestBuildToStreamsAttachments guards the memory contract of task 34:
// encoding a large attachment must not scale with its size. TotalAlloc is
// exact (not GC-timing dependent), so this cannot regress silently.
func TestBuildToStreamsAttachments(t *testing.T) {
	const size = 20 << 20
	msg := &message.Message{
		From: "ops@example.com", To: []string{"to@example.com"}, Subject: "s",
		Text: "text",
		Attachments: []message.File{{Name: "big.bin", ContentType: "application/octet-stream",
			Reader: io.LimitReader(zeros{}, size)}},
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if _, err := BuildTo(io.Discard, msg, time.Now()); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if delta := after.TotalAlloc - before.TotalAlloc; delta > 2<<20 {
		t.Errorf("TotalAlloc grew by %d bytes encoding a %d-byte attachment", delta, size)
	}
}

func TestBuildToReportsAttachmentReadError(t *testing.T) {
	src := &failAfter{left: 1024}
	msg := &message.Message{
		From: "ops@example.com", To: []string{"to@example.com"}, Text: "text",
		Attachments: []message.File{{Name: "broken.bin", ContentType: "application/octet-stream",
			Reader: src}},
	}
	var dst bytes.Buffer
	if _, err := BuildTo(&dst, msg, time.Now()); err == nil || !strings.Contains(err.Error(), `reading file "broken.bin": injected read failure`) {
		t.Fatalf("BuildTo error = %v", err)
	}
	if src.left != 0 {
		t.Fatalf("reader has %d bytes left; test did not reach the injected failure", src.left)
	}
}

func TestBuildRejectsInvalidInput(t *testing.T) {
	for _, msg := range []*message.Message{
		nil,
		{From: "not an address", To: []string{"to@example.com"}},
		{From: "ops@example.com", To: []string{"bogus"}},
		{From: "ops@example.com", To: []string{"to@example.com"}, Attachments: []message.File{{Name: "nil.txt"}}},
	} {
		if _, _, err := Build(msg, time.Now()); err == nil {
			t.Errorf("Build(%+v) succeeded", msg)
		}
	}
}
