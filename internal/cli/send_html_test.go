package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// TestSendHTMLFragment sends an --html fragment with inline styles and a
// color and checks both multipart/alternative parts: the html part is the
// fragment wrapped in the base template (styles survive, FR-1), the text
// part is derived by stripping tags.
func TestSendHTMLFragment(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	body := `<h2 style="color:#b23b12;">Deploy <em>done</em></h2>` + "\n" +
		`<p>Host <strong>web01</strong>: all &quot;green&quot; &amp; running</p>` + "\n"
	code, stdout, stderr := runMock(t, m, body,
		"-c", cfg, "--to", "a@example.com", "--subject", "Deploy status", "--html")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if !strings.HasPrefix(stdout, "<abc123@") {
		t.Errorf("stdout = %q, want message ID", stdout)
	}

	req := m.LastRequest(t)
	text, html := req.Field("text"), req.Field("html")
	if text == "" || html == "" {
		t.Fatalf("multipart parts missing: text=%q html=%q", text, html)
	}

	// The html part carries the template frame around the fragment.
	for _, want := range []string{
		`<html lang="en"`,
		`<title>Deploy status</title>`,
		`<!--[if mso]>`,
		`class="content"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("html part missing template frame %q", want)
		}
	}
	// The fragment's markup survives inlining: its own inline style wins
	// over the template rules now merged into the same attribute, and the
	// text content is intact (entities are re-encoded by the inliner, so
	// &quot; comes back as &#34;).
	for _, want := range []string{
		`<h2 style="font-weight:bold;margin:36px 0 12px;font-size:22px;line-height:29px;color:#b23b12">Deploy <em>done</em></h2>`,
		`<p style="margin:0 0 18px">Host <strong>web01</strong>: all &#34;green&#34; &amp; running</p>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("html part missing fragment %q", want)
		}
	}

	// The text part is derived: entities decoded, no tags, no styles.
	for _, want := range []string{
		"Deploy done",
		`Host web01: all "green" & running`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text part missing %q:\n%s", want, text)
		}
	}
	for _, bad := range []string{"<h2", "<p>", "style=", "color:", "&quot;", "&amp;"} {
		if strings.Contains(text, bad) {
			t.Errorf("text part contains markup %q:\n%s", bad, text)
		}
	}
}

// TestSendHTMLRaw sends a complete HTML document with --html-raw: the
// html part must be byte-identical to the input — no template frame, no
// additions. The text part is still derived.
func TestSendHTMLRaw(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	doc := "<!DOCTYPE html>\n<html>\n<head>\n<title>Custom &amp; raw</title>\n" +
		"<style>p{color:red}</style>\n</head>\n<body>\n" +
		`<h1 style="color:#00ff00;">Hi there</h1>` + "\n" +
		"<p>Fish &amp; Chips &mdash; 5&euro;</p>\n</body>\n</html>\n"
	code, _, stderr := runMock(t, m, doc,
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--html-raw")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}

	req := m.LastRequest(t)
	html, text := req.Field("html"), req.Field("text")
	if html != doc {
		t.Errorf("html part not byte-identical to input:\n--- got ---\n%q\n--- want ---\n%q", html, doc)
	}
	if strings.Contains(html, `class="content"`) {
		t.Errorf("html part contains template frame, want raw document only")
	}

	// Text part: content kept, entities decoded, markup, styles and the
	// title (the subject's duplicate) dropped.
	for _, want := range []string{"Hi there", "Fish & Chips — 5€"} {
		if !strings.Contains(text, want) {
			t.Errorf("text part missing %q:\n%s", want, text)
		}
	}
	for _, bad := range []string{"<h1", "<p>", "color:", "Custom", "&amp;", "&mdash;"} {
		if strings.Contains(text, bad) {
			t.Errorf("text part contains %q:\n%s", bad, text)
		}
	}
}

// TestSendHTMLFromFile composes an --html message from --file input;
// stdin is ignored.
func TestSendHTMLFromFile(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	bodyFile := filepath.Join(t.TempDir(), "body.html")
	if err := os.WriteFile(bodyFile, []byte("<p>from a <b>file</b></p>\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runMock(t, m, "stdin is ignored",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--html", "--file", bodyFile)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	req := m.LastRequest(t)
	html, text := req.Field("html"), req.Field("text")
	if !strings.Contains(html, `<p style="margin:0 0 18px">from a <b>file</b></p>`) ||
		!strings.Contains(html, `class="content"`) {
		t.Errorf("html part missing fragment or template frame:\n%s", html)
	}
	if got, want := text, "from a file\n"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

// TestSendHTMLRawFromFile composes an --html-raw message from --file
// input and checks byte-identity of the html part.
func TestSendHTMLRawFromFile(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	doc := "<!DOCTYPE html>\n<html><body><p>raw &amp; <i>file</i></p></body></html>\n"
	bodyFile := filepath.Join(t.TempDir(), "doc.html")
	if err := os.WriteFile(bodyFile, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runMock(t, m, "stdin is ignored",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--html-raw", "--file", bodyFile)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	req := m.LastRequest(t)
	if got := req.Field("html"); got != doc {
		t.Errorf("html part not byte-identical to file: got %q, want %q", got, doc)
	}
	if got, want := req.Field("text"), "raw & file\n"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

// TestSendHTMLRawNoNormalization checks that --html-raw applies no
// normalization at all: a BOM, CRLF line endings and a missing trailing
// newline all reach the html part unchanged, while the text part is
// still derived cleanly.
func TestSendHTMLRawNoNormalization(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	doc := "\ufeff<!DOCTYPE html>\r\n<html><body>\r\n<p>crlf &amp; bom</p>\r\n</body></html>"
	code, _, stderr := runMock(t, m, doc,
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--html-raw")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	req := m.LastRequest(t)
	if got := req.Field("html"); got != doc {
		t.Errorf("html part not byte-identical:\ngot  %q\nwant %q", got, doc)
	}
	if got, want := req.Field("text"), "crlf & bom\n"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}
