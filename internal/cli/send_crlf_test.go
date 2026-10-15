package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// Bodies piped on Windows (PowerShell, cmd.exe) or saved by Windows
// editors use CRLF line endings. mailfire normalizes CRLF to LF for
// --txt, --md and --html, so the composed message is identical no matter
// which platform produced the body. --html-raw is the exception: it is
// sent byte-identical (covered by TestSendHTMLRawNoNormalization).

// TestSendTxtCRLF: a CRLF body arrives as an LF-only text part, from
// stdin and from --file alike.
func TestSendTxtCRLF(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	code, _, stderr := runMock(t, m, "line one\r\nline two\r\n",
		"-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitOK {
		t.Fatalf("stdin: exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if got, want := m.LastRequest(t).Field("text"), "line one\nline two\n"; got != want {
		t.Errorf("stdin: text = %q, want %q", got, want)
	}

	body := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(body, []byte("from file\r\nsecond\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runMock(t, m, "",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--file", body)
	if code != ExitOK {
		t.Fatalf("--file: exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if got, want := m.LastRequest(t).Field("text"), "from file\nsecond\n"; got != want {
		t.Errorf("--file: text = %q, want %q", got, want)
	}
}

// TestSendHTMLCRLF: the wrapped --html fragment carries no CR into the
// html part, and the derived text part is LF-only.
func TestSendHTMLCRLF(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	code, _, stderr := runMock(t, m, "<p>one</p>\r\n<p>two</p>\r\n",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--html")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	req := m.LastRequest(t)
	if html := req.Field("html"); strings.Contains(html, "\r") {
		t.Errorf("html part contains CR:\n%q", html)
	}
	if got, want := req.Field("text"), "one\n\ntwo\n"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

// TestSendMarkdownCRLF: the Markdown renderer normalizes CRLF itself;
// assert at the CLI level that neither alternative part carries a CR.
func TestSendMarkdownCRLF(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	code, _, stderr := runMock(t, m, "# Title\r\n\r\npara text\r\n",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	req := m.LastRequest(t)
	if html := req.Field("html"); strings.Contains(html, "\r") {
		t.Errorf("html part contains CR:\n%q", html)
	}
	if got, want := req.Field("text"), "Title\n\npara text\n"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}
