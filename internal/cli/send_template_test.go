package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// Markers telling the templates apart in the html part: the layout (card
// cell or plain content cell) and the font variant.
const (
	cardMarker  = `<td class="card content"`
	plainMarker = `<td class="content"`
	serifMarker = "font-family:Charter,"
)

func TestSendTemplateSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string // [sending] extra
		args   []string
		want   []string
		absent []string
	}{
		{"default is card", "", nil, []string{cardMarker}, []string{plainMarker, serifMarker}},
		{"config", "template = \"plain-serif\"\n", nil, []string{plainMarker, serifMarker}, []string{cardMarker}},
		{"flag", "", []string{"--template", "card-serif"}, []string{cardMarker, serifMarker}, []string{plainMarker}},
		{"flag overrides config", "template = \"card-serif\"\n", []string{"--template", "plain"}, []string{plainMarker}, []string{cardMarker, serifMarker}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			cfg := writeConfig(t, "from = \"ops@example.com\"\n"+tc.config)
			args := append([]string{"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md"}, tc.args...)
			code, _, stderr := runMock(t, m, "> [!NOTE]\n> Hello\n", args...)
			if code != ExitOK {
				t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
			}
			html := m.LastRequest(t).Field("html")
			for _, w := range tc.want {
				if !strings.Contains(html, w) {
					t.Errorf("html part missing %q", w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(html, a) {
					t.Errorf("html part contains %q", a)
				}
			}
			if !strings.Contains(html, `class="alert alert-note"`) {
				t.Error("html part has no rendered alert")
			}
			if text := m.LastRequest(t).Field("text"); text != "> NOTE\n> Hello\n" {
				t.Errorf("text part = %q", text)
			}
		})
	}
}

func TestSendTemplateUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--md", "--template", "fancy"}, `--template: unknown template "fancy": must be one of "card", "card-serif", "plain", or "plain-serif"`},
		{[]string{"--txt", "--template", "plain"}, "--template requires --md or --html"},
		{[]string{"--html-raw", "--template", "plain"}, "--template requires --md or --html"},
		{[]string{"--md", "--template", "plain", "--template-file", "custom.html"}, "mutually exclusive"},
		{[]string{"--md", "--template=", "--template-file", "custom.html"}, "mutually exclusive"},
		{[]string{"--md", "--template", "plain", "--template-file="}, "mutually exclusive"},
		{[]string{"--md", "--template=", "--template-file="}, "mutually exclusive"},
		{[]string{"--txt", "--template-file", "custom.html"}, "--template-file requires --md or --html"},
		{[]string{"--html-raw", "--template-file", "custom.html"}, "--template-file requires --md or --html"},
		{[]string{"--md", "--template-file", ""}, "--template-file: empty path"},
		{[]string{"--md", "--template-file", filepath.Join(t.TempDir(), "missing")}, "template file:"},
	} {
		m := testutil.NewMockMailgun(t)
		cfg := writeConfig(t, "from = \"ops@example.com\"\n")
		args := append([]string{"-c", cfg, "--to", "a@example.com", "--subject", "s"}, tc.args...)
		code, _, stderr := runMock(t, m, "body", args...)
		if code != ExitUsage {
			t.Errorf("%v: exit = %d, want %d", tc.args, code, ExitUsage)
		}
		if !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: stderr = %q, want it to contain %q", tc.args, stderr, tc.want)
		}
		if len(m.Requests()) != 0 {
			t.Errorf("%v: a request was sent", tc.args)
		}
	}
}

func TestSendCustomTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.html")
	source := `<html><head><style>p { color: #123456; }</style></head><body><h1>{{html .Subject}}</h1>{{.Body}}</body></html>`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"--md", "--html"} {
		for _, tc := range []struct {
			name, config string
			args         []string
			custom       bool
		}{
			{"flag", "template = \"plain\"\n", []string{"--template-file", path}, true},
			{"config", fmt.Sprintf("template_file = %q\n", path), nil, true},
			{"builtin override", fmt.Sprintf("template_file = %q\n", path), []string{"--template", "card"}, false},
			{"file override", "template_file = \"missing.html\"\n", []string{"--template-file", path}, true},
		} {
			t.Run(format+tc.name, func(t *testing.T) {
				m := testutil.NewMockMailgun(t)
				cfg := writeConfig(t, "from = \"ops@example.com\"\n"+tc.config)
				args := append([]string{"-c", cfg, "--to", "a@example.com", "--subject", "A < B", format}, tc.args...)
				body := "Hello"
				if format == "--html" {
					body = "<p>Hello</p>"
				}
				code, _, stderr := runMock(t, m, body, args...)
				if code != ExitOK {
					t.Fatalf("exit %d: %s", code, stderr)
				}
				html := m.LastRequest(t).Field("html")
				if tc.custom && (!strings.Contains(html, "A &lt; B") || !strings.Contains(html, "color:#123456")) {
					t.Errorf("custom rendering/inlining failed: %s", html)
				}
				if !tc.custom && !strings.Contains(html, cardMarker) {
					t.Error("built-in override not applied")
				}
				if text := m.LastRequest(t).Field("text"); strings.TrimSpace(text) != "Hello" {
					t.Errorf("text = %q", text)
				}
			})
		}
	}
}
