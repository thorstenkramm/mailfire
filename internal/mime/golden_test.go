package mime

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/message"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// goldenNow pins the Date header (and the Message-ID timestamp part).
var goldenNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

// messageIDPattern matches the generated Message-ID header line; golden
// comparison normalizes it because the ID carries random hex.
var messageIDPattern = regexp.MustCompile(`Message-ID: <[^>\r\n]*>`)

// goldenBytes builds msg with fixed boundaries and normalizes the
// Message-ID, so the bytes are deterministic.
func goldenBytes(t *testing.T, msg *message.Message, includeBcc bool, boundaries ...string) []byte {
	t.Helper()
	raw, _, err := buildB(msg, goldenNow, includeBcc, &boundaryCursor{list: boundaries})
	if err != nil {
		t.Fatalf("buildB: %v", err)
	}
	return messageIDPattern.ReplaceAll(raw, []byte("Message-ID: <normalized@example.com>"))
}

// TestBuildGolden pins the exact bytes of the MIME builder, so the
// streaming rewrite (task 34) can prove it changes nothing. Boundaries
// are supplied innermost first (alternative, related, mixed), matching
// the order buildBody creates the multiparts.
func TestBuildGolden(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("image-bytes", 30))
	plain := func(name string, data []byte) message.File {
		return message.File{Name: name, ContentType: "application/octet-stream", Reader: bytes.NewReader(data)}
	}

	for _, tc := range []struct {
		name       string
		msg        *message.Message
		bcc        bool
		boundaries []string
	}{
		{"text-only", &message.Message{
			From: "Jörg Müller <ops@example.com>", To: []string{"To One <to1@example.com>", "to2@example.com"},
			Cc: []string{"cc@example.com"}, Subject: "Résumé ✓", Text: "plain\n.line\n",
		}, false, nil},
		{"alternative", &message.Message{
			From: "ops@example.com", To: []string{"to@example.com"}, Subject: "s",
			Text: "text part", HTML: "<p>html</p>",
		}, false, []string{"golden-alt"}},
		{"related", &message.Message{
			From: "ops@example.com", To: []string{"to@example.com"}, Subject: "s",
			Text: "text part", HTML: `<p><img src="cid:img-1234.png"></p>`,
			Inline: []message.File{{Name: "img-1234.png", Reader: bytes.NewReader(png)}},
		}, false, []string{"golden-alt", "golden-rel"}},
		{"mixed", &message.Message{
			From: "ops@example.com", To: []string{"to@example.com"}, Subject: "s",
			Text:        "text part",
			Attachments: []message.File{plain("report.bin", bytes.Repeat([]byte{0, 1, 2, 250}, 40))},
		}, false, []string{"golden-mix"}},
		{"full", &message.Message{
			From: "ops@example.com", To: []string{"to@example.com"}, Subject: "s",
			Text: "text part", HTML: `<p><img src="cid:img-1234.png"></p>`,
			Inline: []message.File{{Name: "img-1234.png", Reader: bytes.NewReader(png)}},
			Attachments: []message.File{
				{Name: "Bericht März ü.pdf", ContentType: "application/pdf", Reader: bytes.NewReader(bytes.Repeat([]byte{9, 8, 7}, 25))},
				plain("data.bin", bytes.Repeat([]byte{0, 1, 2, 250}, 40)),
			},
		}, false, []string{"golden-alt", "golden-rel", "golden-mix"}},
		{"attachment-only", &message.Message{
			From: "ops@example.com", To: []string{"to@example.com"}, Subject: "s",
			Attachments: []message.File{plain("a.txt", []byte("x"))},
		}, false, []string{"golden-mix"}},
		{"empty-attachment", &message.Message{
			From: "ops@example.com", To: []string{"to@example.com"}, Subject: "s",
			Text:        "text part",
			Attachments: []message.File{plain("empty.bin", nil)},
		}, false, []string{"golden-mix"}},
		{"base64-exactly-57", &message.Message{
			From: "ops@example.com", To: []string{"to@example.com"}, Subject: "s",
			Text:        "text part",
			Attachments: []message.File{plain("b57.bin", bytes.Repeat([]byte{0xAB}, 57))},
		}, false, []string{"golden-mix"}},
		{"base64-exactly-58", &message.Message{
			From: "ops@example.com", To: []string{"to@example.com"}, Subject: "s",
			Text:        "text part",
			Attachments: []message.File{plain("b58.bin", bytes.Repeat([]byte{0xAB}, 58))},
		}, false, []string{"golden-mix"}},
		{"base64-exactly-114", &message.Message{
			From: "ops@example.com", To: []string{"to@example.com"}, Subject: "s",
			Text:        "text part",
			Attachments: []message.File{plain("b114.bin", bytes.Repeat([]byte{0xAB}, 114))},
		}, false, []string{"golden-mix"}},
		{"sniffed-content-type", &message.Message{
			From: "ops@example.com", To: []string{"to@example.com"}, Subject: "s",
			Text:        "text part",
			Attachments: []message.File{{Name: "photo.png", Reader: bytes.NewReader(png)}},
		}, false, []string{"golden-mix"}},
		{"bcc", &message.Message{
			From: "ops@example.com", To: []string{"to@example.com"}, Subject: "s",
			Bcc: []string{"secret@example.com"}, Text: "text part",
		}, true, nil},
		{"folded-headers", &message.Message{
			From: "Jörg Müller <ops@example.com>",
			To: []string{
				"Recipient Number One Longname <recipient.one@example.com>",
				"Recipient Number Two Longname <recipient.two@example.com>",
				"Recipient Number Three Longname <recipient.three@example.com>",
			},
			Subject: strings.Repeat("Größenänderung des Speichers ✓ ", 4), Text: "x",
		}, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.Golden(t, "build/"+tc.name+".eml", goldenBytes(t, tc.msg, tc.bcc, tc.boundaries...))
		})
	}
}
