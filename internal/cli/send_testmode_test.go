package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// TestSendTestModeFormats: --test adds o:testmode=yes with every format
// flag (FR-1, Decision Q5), and stdout makes clear that Mailgun only
// validated the request and delivered nothing.
func TestSendTestModeFormats(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string // format flags; nil is the default --txt
		body  string
	}{
		{"default txt", nil, "plain body\n"},
		{"txt", []string{"--txt"}, "plain body\n"},
		{"md", []string{"--md"}, "# Headline\n"},
		{"html", []string{"--html"}, "<p>fragment</p>\n"},
		{"html-raw", []string{"--html-raw"}, "<html><body><p>raw</p></body></html>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			cfg := writeConfig(t, "from = \"ops@example.com\"\n")
			args := append([]string{"-c", cfg, "--to", "a@example.com", "--subject", "s",
				"--test"}, tc.flags...)
			code, stdout, stderr := runMock(t, m, tc.body, args...)
			if code != ExitOK {
				t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
			}
			req := m.LastRequest(t)
			if got := req.Field("o:testmode"); got != "yes" {
				t.Errorf("o:testmode = %q, want %q", got, "yes")
			}
			want := "accepted in test mode, message id <abc123@mg.example.com> (validated only, nothing was delivered)\n"
			if stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
		})
	}
}

// TestSendTestModeAbsent: without --test, the request carries no
// o:testmode field at all, for every format.
func TestSendTestModeAbsent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		body  string
	}{
		{"default txt", nil, "plain body\n"},
		{"md", []string{"--md"}, "# Headline\n"},
		{"html", []string{"--html"}, "<p>fragment</p>\n"},
		{"html-raw", []string{"--html-raw"}, "<html><body><p>raw</p></body></html>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			cfg := writeConfig(t, "from = \"ops@example.com\"\n")
			args := append([]string{"-c", cfg, "--to", "a@example.com", "--subject", "s"},
				tc.flags...)
			code, stdout, stderr := runMock(t, m, tc.body, args...)
			if code != ExitOK {
				t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
			}
			req := m.LastRequest(t)
			if _, ok := req.Fields["o:testmode"]; ok {
				t.Errorf("o:testmode present without --test: %q", req.Fields["o:testmode"])
			}
			if want := "<abc123@mg.example.com>\n"; stdout != want {
				t.Errorf("stdout = %q, want plain message id %q", stdout, want)
			}
		})
	}
}

// TestSendTestModeNotFromConfig: test mode is CLI-flag only; a "test" key
// in any config section must not enable it (unknown keys are ignored).
func TestSendTestModeNotFromConfig(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	path := filepath.Join(t.TempDir(), "mailfire.conf")
	content := "test = true\ntestmode = true\n\n" +
		"[mailgun]\napi_key = \"test-key-not-real\"\nregion = \"us\"\ntest = true\ntestmode = true\n\n" +
		"[sending]\ndomain = \"mg.example.com\"\nfrom = \"ops@example.com\"\ntest = true\ntestmode = true\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runMock(t, m, "body\n",
		"-c", path, "--to", "a@example.com", "--subject", "s")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	req := m.LastRequest(t)
	if _, ok := req.Fields["o:testmode"]; ok {
		t.Errorf("o:testmode enabled by config: %q", req.Fields["o:testmode"])
	}
	if want := "<abc123@mg.example.com>\n"; stdout != want {
		t.Errorf("stdout = %q, want plain message id %q", stdout, want)
	}
}

// TestSendTestModeWithAttachment: --test combines with --attach; the
// attachment is part of the validated (but not delivered) request.
func TestSendTestModeWithAttachment(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	dir := t.TempDir()
	att := writeAttachment(t, dir, "notes.txt", txtBytes)

	code, stdout, stderr := runMock(t, m, "body\n",
		"-c", cfg, "--to", "a@example.com", "--subject", "s",
		"--test", "--attach", att)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	req := m.LastRequest(t)
	if got := req.Field("o:testmode"); got != "yes" {
		t.Errorf("o:testmode = %q, want %q", got, "yes")
	}
	if len(req.Files) != 1 || req.Files[0].FieldName != "attachment" ||
		req.Files[0].Filename != "notes.txt" {
		t.Errorf("files = %+v, want one attachment notes.txt", req.Files)
	}
	if !strings.Contains(stdout, "accepted in test mode") {
		t.Errorf("stdout = %q, want test-mode note", stdout)
	}
}

// TestSendTestModeWithInlineImage: --test combines with --md inline
// images; the image is sent as an inline part and referenced via cid:.
func TestSendTestModeWithInlineImage(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	code, stdout, stderr := runMock(t, m, "# Hi\n\n![the logo](logo.png)\n",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--test", "--md")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	req := m.LastRequest(t)
	if got := req.Field("o:testmode"); got != "yes" {
		t.Errorf("o:testmode = %q, want %q", got, "yes")
	}
	inline := inlineFiles(req)
	if len(inline) != 1 {
		t.Fatalf("got %d inline parts, want 1", len(inline))
	}
	if want := `src="cid:` + inline[0].Filename + `"`; !strings.Contains(req.Field("html"), want) {
		t.Errorf("html missing %q", want)
	}
	if !strings.Contains(stdout, "accepted in test mode") {
		t.Errorf("stdout = %q, want test-mode note", stdout)
	}
}
