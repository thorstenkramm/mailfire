package cli

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// writeConfig writes a config file with the given extra lines appended to
// the mandatory keys and returns its path.
func writeConfig(t *testing.T, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailfire.conf")
	content := "[mailgun]\napi_key = \"test-key-not-real\"\nregion = \"us\"\n\n[sending]\ndomain = \"mg.example.com\"\n" + extra
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

// runMock runs Run against a mock Mailgun server with the given stdin and
// returns exit code, stdout, stderr.
func runMock(t *testing.T, m *testutil.MockMailgun, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr, testInfo,
		WithStdin(strings.NewReader(stdin)),
		WithBaseURL(m.URL()))
	return code, stdout.String(), stderr.String()
}

func TestSendFromStdin(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"Ops Bot <ops@example.com>\"\n")

	for _, args := range [][]string{
		{"send", "-c", cfg, "--to", "a@example.com", "--subject", "hi there"},
		{"-c", cfg, "--to", "a@example.com", "--subject", "hi there"}, // implicit send
	} {
		code, stdout, stderr := runMock(t, m, "hello\nbody\n", args...)
		if code != ExitOK {
			t.Fatalf("Run(%v): exit = %d, want %d; stderr:\n%s", args, code, ExitOK, stderr)
		}
		if want := "<abc123@mg.example.com>\n"; stdout != want {
			t.Errorf("Run(%v): stdout = %q, want %q", args, stdout, want)
		}
		if stderr != "" {
			t.Errorf("Run(%v): stderr = %q, want empty", args, stderr)
		}
	}

	req := m.LastRequest(t)
	if req.ParseErr != nil {
		t.Fatalf("mock could not parse request body: %v", req.ParseErr)
	}
	if req.User != "api" || req.Password != "test-key-not-real" {
		t.Errorf("basic auth = %q/%q, want api/test-key-not-real", req.User, req.Password)
	}
	if got := req.Field("from"); got != `"Ops Bot" <ops@example.com>` {
		t.Errorf("from = %q, want %q", got, `"Ops Bot" <ops@example.com>`)
	}
	if got := req.Field("to"); got != "a@example.com" {
		t.Errorf("to = %q, want %q", got, "a@example.com")
	}
	if got := req.Field("subject"); got != "hi there" {
		t.Errorf("subject = %q, want %q", got, "hi there")
	}
	if got := req.Field("text"); got != "hello\nbody\n" {
		t.Errorf("text = %q, want %q", got, "hello\\nbody\\n")
	}
}

func TestSendFromFile(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	bodyFile := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(bodyFile, []byte("from a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runMock(t, m, "stdin is ignored",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--file", bodyFile)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if !strings.HasPrefix(stdout, "<abc123@") {
		t.Errorf("stdout = %q, want message ID", stdout)
	}
	if got := m.LastRequest(t).Field("text"); got != "from a file\n" {
		t.Errorf("text = %q, want %q", got, "from a file\\n")
	}
}

func TestSendFromFileTildeExpansion(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "body.txt"), []byte("tilde body"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runMock(t, m, "",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--file", "~/body.txt")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if got := m.LastRequest(t).Field("text"); got != "tilde body" {
		t.Errorf("text = %q, want %q", got, "tilde body")
	}
}

func TestSendFileMissing(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	missing := filepath.Join(t.TempDir(), "nope.txt")

	code, _, stderr := runMock(t, m, "",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--file", missing)
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "reading body file") || !strings.Contains(stderr, missing) {
		t.Errorf("stderr missing file error:\n%s", stderr)
	}
	if len(m.Requests()) != 0 {
		t.Errorf("mock received %d requests, want 0", len(m.Requests()))
	}
}

func TestSendFromOverride(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"Configured <cfg@example.com>\"\n")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s",
		"--from", "Flag Sender <flag@example.com>")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if got := m.LastRequest(t).Field("from"); got != `"Flag Sender" <flag@example.com>` {
		t.Errorf("from = %q, want %q", got, `"Flag Sender" <flag@example.com>`)
	}
}

func TestSendNoSender(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "") // no sending.from

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "no sender") {
		t.Errorf("stderr missing no-sender message:\n%s", stderr)
	}
	if len(m.Requests()) != 0 {
		t.Errorf("mock received %d requests, want 0", len(m.Requests()))
	}
}

func TestSendAddressFormats(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--subject", "s",
		"--to", "plain@example.com",
		"--to", `"Doe, John" <john@example.com>`, // comma in display name: no splitting (Q1)
		"--cc", "Carol <carol@example.com>",
		"--bcc", "dave@example.com")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	req := m.LastRequest(t)
	wantTo := []string{"plain@example.com", `"Doe, John" <john@example.com>`}
	if got := req.Fields["to"]; !equalStrings(got, wantTo) {
		t.Errorf("to = %q, want %q", got, wantTo)
	}
	if got := req.Field("cc"); got != `"Carol" <carol@example.com>` {
		t.Errorf("cc = %q, want %q", got, `"Carol" <carol@example.com>`)
	}
	if got := req.Field("bcc"); got != "dave@example.com" {
		t.Errorf("bcc = %q, want %q", got, "dave@example.com")
	}
}

func TestSendInvalidAddresses(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"to", []string{"--to", "not-an-address"}, `invalid --to address "not-an-address"`},
		{"cc", []string{"--to", "a@example.com", "--cc", "@@@"}, `invalid --cc address "@@@"`},
		{"bcc", []string{"--to", "a@example.com", "--bcc", ""}, `invalid --bcc address ""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"-c", cfg, "--subject", "s"}, tc.args...)
			code, _, stderr := runMock(t, m, "body", args...)
			if code != ExitUsage {
				t.Errorf("exit = %d, want %d", code, ExitUsage)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr missing %q:\n%s", tc.want, stderr)
			}
		})
	}
	if len(m.Requests()) != 0 {
		t.Errorf("mock received %d requests, want 0", len(m.Requests()))
	}
}

func TestSendInvalidFromFlag(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--from", "bogus")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, `invalid sender "bogus"`) {
		t.Errorf("stderr missing sender error:\n%s", stderr)
	}
}

func TestSendErrorExitCodes(t *testing.T) {
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	base := []string{"-c", cfg, "--to", "a@example.com", "--subject", "s"}

	t.Run("auth error exits 3", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		m.SendResponse = testutil.Unauthorized()
		code, stdout, stderr := runMock(t, m, "body", base...)
		if code != ExitAuth {
			t.Errorf("exit = %d, want %d", code, ExitAuth)
		}
		if !strings.Contains(stderr, "401") {
			t.Errorf("stderr missing status:\n%s", stderr)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty", stdout)
		}
	})

	t.Run("permanent error exits 5", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		m.SendResponse = testutil.BadRequest()
		code, _, stderr := runMock(t, m, "body", base...)
		if code != ExitSendFail {
			t.Errorf("exit = %d, want %d", code, ExitSendFail)
		}
		if !strings.Contains(stderr, "400") {
			t.Errorf("stderr missing status:\n%s", stderr)
		}
	})

	t.Run("deferrable error exits 4 until deferral lands", func(t *testing.T) {
		for _, resp := range []testutil.Response{
			testutil.InternalError(),
			testutil.RateLimited(),
			testutil.MalformedJSON(),
		} {
			m := testutil.NewMockMailgun(t)
			m.SendResponse = resp
			code, _, stderr := runMock(t, m, "body", base...)
			if code != ExitDeferred {
				t.Errorf("status %d: exit = %d, want %d", resp.Status, code, ExitDeferred)
			}
			if !strings.Contains(stderr, "deferral is not implemented yet") {
				t.Errorf("status %d: stderr missing deferral note:\n%s", resp.Status, stderr)
			}
		}
	})

	t.Run("network error exits 4", func(t *testing.T) {
		// Grab a port and release it, so connecting is refused.
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		url := "http://" + l.Addr().String()
		l.Close()
		var stdout, stderr bytes.Buffer
		code := Run(base, &stdout, &stderr, testInfo,
			WithStdin(strings.NewReader("body")), WithBaseURL(url))
		if code != ExitDeferred {
			t.Errorf("exit = %d, want %d; stderr:\n%s", code, ExitDeferred, stderr.String())
		}
	})
}

func TestSendTestMode(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--test")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if got := m.LastRequest(t).Field("o:testmode"); got != "yes" {
		t.Errorf("o:testmode = %q, want %q", got, "yes")
	}
}

func TestSendClientConstructionError(t *testing.T) {
	// config.Load guarantees valid credentials, so this branch is
	// defensive: send must map a client construction error to exit 2.
	cfg := &config.Config{APIKey: "k", Domain: "d", Region: "atlantis", From: "ops@example.com"}
	opts := &SendOptions{To: []string{"a@example.com"}, Subject: "s", Format: "txt"}
	var stdout, stderr bytes.Buffer
	rc := &runContext{stdin: strings.NewReader("body")}
	if code := send(cfg, opts, rc, &stdout, &stderr); code != ExitConfig {
		t.Errorf("exit = %d, want %d", code, ExitConfig)
	}
	if !strings.Contains(stderr.String(), "unknown region") {
		t.Errorf("stderr missing region error:\n%s", stderr.String())
	}
}

func TestSendNotImplementedFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"md", []string{"--md"}, "--md: not implemented yet"},
		{"html", []string{"--html"}, "--html: not implemented yet"},
		{"html-raw", []string{"--html-raw"}, "--html-raw: not implemented yet"},
		{"attach", []string{"--attach", "x.pdf"}, "--attach: not implemented yet"},
		{"preheader", []string{"--preheader", "preview"}, "--preheader: not implemented yet"},
		{"footer", []string{"--footer", "f.html"}, "--footer: not implemented yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No config at all: the not-implemented check must fire before
			// config loading, as it is a usage error.
			args := append([]string{"--to", "a@example.com", "--subject", "s"}, tc.args...)
			code, _, stderr := run(t, args...)
			if code != ExitUsage {
				t.Errorf("exit = %d, want %d", code, ExitUsage)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr missing %q:\n%s", tc.want, stderr)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
