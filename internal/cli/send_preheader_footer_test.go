package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// writeFooter writes an HTML footer fragment to a temp file and returns
// its path.
func writeFooter(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "footer.html")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSendPreheaderFooterGolden sends --md with --preheader and --footer
// and compares the html part byte-for-byte against
// testdata/preheader_footer_inlined.html. The explicit checks below keep
// the golden honest: the preheader lands in the hidden preview block and
// the footer fragment in the template's footer block (FR-1, FR-7).
func TestSendPreheaderFooterGolden(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	footer := writeFooter(t,
		`<p>Sent by the nightly job on ops01. <a href="https://example.com/backups/settings">Change who gets this report</a>.</p>`+"\n")

	code, _, stderr := runMock(t, m, "# Report\n\nBackup **finished**.\n",
		"-c", cfg, "--to", "a@example.com", "--subject", "Backup report", "--md",
		"--preheader", "All five hosts were backed up; files01 needed a retry.",
		"--footer", footer)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}

	html := m.LastRequest(t).Field("html")
	testutil.GoldenString(t, "preheader_footer_inlined.html", html)

	// The preheader div is the hidden preview block; its style attribute
	// survives inlining unchanged (no template rule targets it).
	for _, want := range []string{
		`<div style="display:none;max-height:0;max-width:0;overflow:hidden;opacity:0;mso-hide:all;font-size:1px;line-height:1px;">All five hosts were backed up; files01 needed a retry.`,
		// The footer block is the template row below the content; the
		// fragment is inserted as is, the link's rule inlined.
		`class="footer-text"`,
		`<p>Sent by the nightly job on ops01. <a href="https://example.com/backups/settings" style="color:#62656d;text-decoration:underline">Change who gets this report</a>.</p>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("html part missing %q", want)
		}
	}
}

// TestSendPreheaderFooterOmitted: without the flags neither the hidden
// preheader block nor the footer block is rendered (the template's
// `with` blocks), for both template formats.
func TestSendPreheaderFooterOmitted(t *testing.T) {
	for _, format := range []string{"--md", "--html"} {
		t.Run(format, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			cfg := writeConfig(t, "from = \"ops@example.com\"\n")

			code, _, stderr := runMock(t, m, "body text\n",
				"-c", cfg, "--to", "a@example.com", "--subject", "s", format)
			if code != ExitOK {
				t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
			}
			html := m.LastRequest(t).Field("html")
			for _, bad := range []string{"mso-hide:all", `class="footer-text"`} {
				if strings.Contains(html, bad) {
					t.Errorf("%s: html part contains %q although the flag was not given", format, bad)
				}
			}
		})
	}
}

// TestSendPreheaderFooterHTMLFormat wires both flags through --html
// (the golden test covers --md): the fragment is wrapped in the template
// together with preheader and footer.
func TestSendPreheaderFooterHTMLFormat(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	footer := writeFooter(t, `<p>Footer <em>markup</em></p>`+"\n")

	code, _, stderr := runMock(t, m, "<p>Hi</p>\n",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--html",
		"--preheader", "preview text", "--footer", footer)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	html := m.LastRequest(t).Field("html")
	for _, want := range []string{
		"mso-hide:all",
		"preview text",
		`class="footer-text"`,
		`<p>Footer <em>markup</em></p>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("html part missing %q", want)
		}
	}
}

// TestSendPreheaderEscaped: the preheader is plain text; HTML
// metacharacters must not reach the markup unescaped (the template
// escapes it, and the inliner re-escapes text nodes on serialization).
func TestSendPreheaderEscaped(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	code, _, stderr := runMock(t, m, "body\n",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md",
		"--preheader", `2 < 3 & "fine" </div>`)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	html := m.LastRequest(t).Field("html")
	if !strings.Contains(html, `2 &lt; 3 &amp;`) {
		t.Errorf("preheader is not HTML-escaped in html part")
	}
	for _, bad := range []string{`2 < 3`, `& "fine"`, "</div><div"} {
		if strings.Contains(html, bad) {
			t.Errorf("html part contains unescaped preheader content %q", bad)
		}
	}
}

// TestSendFooterNotEscaped: the footer file is trusted HTML and must be
// inserted as is — markup stays markup, it is never entity-escaped. (The
// inliner still normalizes the document, so named entities like &euro;
// come back as literal UTF-8; &amp; round-trips.)
func TestSendFooterNotEscaped(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	footer := writeFooter(t, `<p>Fish &amp; Chips — <strong>fresh</strong></p>`+"\n")

	code, _, stderr := runMock(t, m, "body\n",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md",
		"--footer", footer)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	html := m.LastRequest(t).Field("html")
	if !strings.Contains(html, `<p>Fish &amp; Chips — <strong>fresh</strong></p>`) {
		t.Errorf("footer fragment not inserted as is:\n%s", html)
	}
	if strings.Contains(html, `&lt;p&gt;Fish`) {
		t.Errorf("footer fragment was escaped")
	}
}

// TestSendFooterTildeExpansion: a leading ~ in --footer expands to the
// home directory, like every other path flag.
func TestSendFooterTildeExpansion(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "footer.html"), []byte("<p>tilde footer</p>\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runMock(t, m, "body\n",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md",
		"--footer", "~/footer.html")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if html := m.LastRequest(t).Field("html"); !strings.Contains(html, "<p>tilde footer</p>") {
		t.Errorf("html part missing footer fragment:\n%s", html)
	}
}

// TestSendPreheaderFooterFormatRejected: --preheader and --footer are
// template features and valid only with --md and --html; with --txt or
// --html-raw they exit 1 before any config is loaded (usage error).
func TestSendPreheaderFooterFormatRejected(t *testing.T) {
	footer := writeFooter(t, "<p>f</p>\n")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"preheader with txt", []string{"--txt", "--preheader", "preview"}, "--preheader requires --md or --html"},
		{"preheader with default txt", []string{"--preheader", "preview"}, "--preheader requires --md or --html"},
		{"preheader with html-raw", []string{"--html-raw", "--preheader", "preview"}, "--preheader requires --md or --html"},
		{"footer with txt", []string{"--txt", "--footer", footer}, "--footer requires --md or --html"},
		{"footer with html-raw", []string{"--html-raw", "--footer", footer}, "--footer requires --md or --html"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No config at all: the check must fire before config
			// loading, as it is a usage error.
			args := append([]string{"--to", "a@example.com", "--subject", "s"}, tc.args...)
			code, stdout, stderr := run(t, args...)
			if code != ExitUsage {
				t.Errorf("exit = %d, want %d", code, ExitUsage)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr missing %q:\n%s", tc.want, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
		})
	}
}

// TestSendFooterFileErrors: a missing footer file or a directory fails
// the send with exit 1 and the resolved path in the message; nothing is
// sent.
func TestSendFooterFileErrors(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	t.Run("missing file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope.html")
		code, _, stderr := runMock(t, m, "body\n",
			"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md",
			"--footer", missing)
		if code != ExitUsage {
			t.Errorf("exit = %d, want %d", code, ExitUsage)
		}
		if !strings.Contains(stderr, "reading footer file") || !strings.Contains(stderr, missing) {
			t.Errorf("stderr missing footer error with path:\n%s", stderr)
		}
	})

	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		code, _, stderr := runMock(t, m, "body\n",
			"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md",
			"--footer", dir)
		if code != ExitUsage {
			t.Errorf("exit = %d, want %d", code, ExitUsage)
		}
		if !strings.Contains(stderr, "reading footer file") || !strings.Contains(stderr, dir) {
			t.Errorf("stderr missing footer error with path:\n%s", stderr)
		}
	})

	if len(m.Requests()) != 0 {
		t.Errorf("mock received %d requests, want 0", len(m.Requests()))
	}
}
