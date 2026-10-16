package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

type unreadableInput struct{ terminal bool }

func (r unreadableInput) IsTerminal() bool         { return r.terminal }
func (r unreadableInput) Read([]byte) (int, error) { panic("unexpected stdin read") }

func TestRecipientDomainsRejectedBeforeRequest(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	dir := filepath.Join(t.TempDir(), "deferred")
	cfg := writeConfig(t, "from = \"ops@example.com\"\n[defer]\ndefer_dir = "+`"`+filepath.ToSlash(dir)+"\"\n")
	for _, flag := range []string{"--to", "--cc", "--bcc"} {
		for _, addr := range []string{"tk@system42", "a@-example.com", "a@example-.com", "a@example..com", "a@exa_mple.com"} {
			var out, stderr bytes.Buffer
			code := Run([]string{"-c", cfg, "--to", "ok@example.com", "--subject", "s", flag, addr}, &out, &stderr, testInfo,
				WithStdin(unreadableInput{}), WithBaseURL(m.URL()))
			if code != ExitUsage || !strings.Contains(stderr.String(), "invalid "+flag+" address") {
				t.Errorf("%s %s: %d %s", flag, addr, code, stderr.String())
			}
		}
	}
	if len(m.Requests()) != 0 {
		t.Fatal("invalid recipient caused request")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("usage error created deferral directory: %v", err)
	}
}

func TestBodySourcesAndAttachmentOnly(t *testing.T) {
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	file := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(file, []byte("attached"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		input io.Reader
		args  []string
		text  string
		code  int
	}{
		{"terminal missing", unreadableInput{true}, nil, "", ExitUsage},
		{"terminal attachment", unreadableInput{true}, []string{"--attach", file}, "", ExitOK},
		{"empty pipe attachment", strings.NewReader(""), []string{"--attach", file}, "", ExitOK},
		{"pipe attachment", strings.NewReader("pipe body"), []string{"--attach", file}, "pipe body", ExitOK},
		{"message", unreadableInput{}, []string{"--message", "explicit"}, "explicit", ExitOK},
		{"long alias", unreadableInput{}, []string{"--m", "explicit"}, "explicit", ExitOK},
		{"short alias", unreadableInput{}, []string{"-m", "explicit"}, "explicit", ExitOK},
		{"empty explicit", unreadableInput{}, []string{"--message="}, "", ExitUsage},
		{"empty explicit attachment", unreadableInput{}, []string{"--message=", "--attach", file}, "", ExitOK},
		{"file", unreadableInput{}, []string{"--file", file}, "attached", ExitOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			var out, stderr bytes.Buffer
			args := append([]string{"-c", cfg, "--to", "a@example.com", "--subject", "s"}, tc.args...)
			code := Run(args, &out, &stderr, testInfo, WithStdin(tc.input), WithBaseURL(m.URL()))
			if code != tc.code {
				t.Fatalf("exit %d: %s", code, stderr.String())
			}
			if code == ExitUsage {
				if !strings.Contains(stderr.String(), "mail body missing: use stdin, --file or --message") || len(m.Requests()) != 0 {
					t.Fatal(stderr.String())
				}
				return
			}
			req := m.LastRequest(t)
			if req.ParseErr != nil {
				t.Fatal(req.ParseErr)
			}
			if values := req.Fields["text"]; len(values) != 1 || values[0] != tc.text {
				t.Fatalf("text part = %#v", values)
			}
			if strings.Contains(tc.name, "attachment") && len(req.Files) != 1 {
				t.Fatalf("attachments = %#v", req.Files)
			}
		})
	}
}

func TestExplicitBodySourceConflicts(t *testing.T) {
	for _, alias := range []string{"--message", "--m", "-m"} {
		for _, values := range [][2]string{{"", ""}, {"file", ""}, {"", "body"}, {"file", "body"}} {
			_, _, err := ParseSendFlags([]string{"--to", "a@example.com", "--subject", "s", "--file", values[0], alias, values[1]})
			if err == nil || !strings.Contains(err.Error(), "conflicting body sources") {
				t.Fatalf("%s %q: %v", alias, values, err)
			}
		}
	}
}

func TestQuotedLocalPartRoundTrip(t *testing.T) {
	for _, input := range []string{`"a@b"@example.com`, `"a b"@example.com`} {
		got, err := parseAddress(input)
		if err != nil || got != input {
			t.Errorf("parseAddress(%q) = %q, %v", input, got, err)
		}
	}
}

func TestReaderIsTerminalFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	if readerIsTerminal(f) {
		t.Fatal("regular file is not a terminal")
	}
}
