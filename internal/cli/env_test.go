package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// envKey is the API key supplied through MAILFIRE_MAILGUN_API_KEY; it is
// distinct from the config-file key so a mix-up of sources is visible.
const envKey = "env-key-not-real-7c1d"

// TestMain clears MAILFIRE_* variables so the developer's shell
// environment cannot change which configuration the tests load.
func TestMain(m *testing.M) {
	if err := testutil.ClearMailfireEnv(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// envOnly isolates the config search (no file anywhere) and supplies a
// complete configuration through MAILFIRE_* variables. It returns the
// defer directory.
func envOnly(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		if _, err := os.Stat("/etc/mailfire/mailfire.conf"); err == nil {
			t.Skip("/etc/mailfire/mailfire.conf exists; cannot test environment-only configuration")
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "roaming"))
	t.Setenv("PROGRAMDATA", filepath.Join(home, "program"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "local"))
	t.Chdir(t.TempDir())

	deferDir := filepath.Join(t.TempDir(), "deferred")
	t.Setenv("MAILFIRE_MAILGUN_API_KEY", envKey)
	t.Setenv("MAILFIRE_MAILGUN_REGION", "us")
	t.Setenv("MAILFIRE_SENDING_DOMAIN", "mg.example.com")
	t.Setenv("MAILFIRE_DEFER_DEFER_DIR", deferDir)
	return deferDir
}

func TestSendEnvOnly(t *testing.T) {
	envOnly(t)
	t.Setenv("MAILFIRE_SENDING_FROM", "Env Bot <env@example.com>")
	m := testutil.NewMockMailgun(t)

	code, stdout, stderr := runMock(t, m, "hello\n", "--to", "a@example.com", "--subject", "s")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if stdout != "<abc123@mg.example.com>\n" || stderr != "" {
		t.Errorf("stdout = %q, stderr = %q", stdout, stderr)
	}
	req := m.LastRequest(t)
	if req.Path != "/v3/mg.example.com/messages" {
		t.Errorf("path = %q", req.Path)
	}
	if req.User != "api" || req.Password != envKey {
		t.Errorf("basic auth = %q/%q, want the environment key", req.User, req.Password)
	}
	if got := req.Field("from"); got != `"Env Bot" <env@example.com>` {
		t.Errorf("from = %q", got)
	}
}

// TestSendEnvPrecedence: command-line flags > environment > config file,
// for the sender and the template selection.
func TestSendEnvPrecedence(t *testing.T) {
	cfg := writeConfig(t, "from = \"file@example.com\"\ntemplate = \"card-serif\"\n")
	t.Setenv("MAILFIRE_SENDING_FROM", "env@example.com")
	t.Setenv("MAILFIRE_SENDING_TEMPLATE", "plain-serif")

	for _, tc := range []struct {
		name         string
		args         []string
		from         string
		want, absent []string
	}{
		{"environment over file", nil, "env@example.com",
			[]string{plainMarker, serifMarker}, []string{cardMarker}},
		{"flags over environment", []string{"--from", "flag@example.com", "--template", "card"}, "flag@example.com",
			[]string{cardMarker}, []string{plainMarker, serifMarker}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			args := append([]string{"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md"}, tc.args...)
			code, _, stderr := runMock(t, m, "hello\n", args...)
			if code != ExitOK {
				t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
			}
			req := m.LastRequest(t)
			if got := req.Field("from"); got != tc.from {
				t.Errorf("from = %q, want %q", got, tc.from)
			}
			html := req.Field("html")
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
		})
	}
}

// TestSendEnvDeferDisabled: with MAILFIRE_DEFER_DEFER=false and no
// config file, the diagnostic names the variable instead of a file.
func TestSendEnvDeferDisabled(t *testing.T) {
	deferDir := envOnly(t)
	t.Setenv("MAILFIRE_SENDING_FROM", "env@example.com")
	t.Setenv("MAILFIRE_DEFER_DEFER", "false")
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.InternalError()

	code, _, stderr := runMock(t, m, "body", "--to", "a@example.com", "--subject", "s")
	if code != ExitSendFail {
		t.Errorf("exit = %d, want %d", code, ExitSendFail)
	}
	if !strings.Contains(stderr, "MAILFIRE_DEFER_DEFER=false") || !strings.Contains(stderr, "NOT saved") {
		t.Errorf("stderr missing the variable:\n%s", stderr)
	}
	if strings.Contains(stderr, envKey) {
		t.Errorf("stderr leaks the API key:\n%s", stderr)
	}
	if _, err := os.Stat(deferDir); !os.IsNotExist(err) {
		t.Errorf("defer_dir exists despite deferral being disabled: %v", err)
	}
}

func TestValidateEnvOnly(t *testing.T) {
	envOnly(t)
	m := testutil.NewMockMailgun(t)

	code, stdout, stderr := runValidateMock(t, m)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stdout:\n%s\nstderr:\n%s", code, ExitOK, stdout, stderr)
	}
	for _, want := range []string{
		"ok   no configuration file; using MAILFIRE_* environment variables",
		`region "us" from MAILFIRE_MAILGUN_REGION`,
		`domain "mg.example.com" from MAILFIRE_SENDING_DOMAIN`,
		"api key from MAILFIRE_MAILGUN_API_KEY",
		"ok   API key and domain verified with Mailgun",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout+stderr, envKey) {
		t.Errorf("output leaks the API key:\n%s%s", stdout, stderr)
	}
	if req := m.LastRequest(t); req.Password != envKey {
		t.Errorf("basic auth password is not the environment key")
	}
}

// TestValidateEnvMixedSources: the report distinguishes file values from
// environment overrides.
func TestValidateEnvMixedSources(t *testing.T) {
	cfg := writeConfig(t, "")
	t.Setenv("MAILFIRE_MAILGUN_API_KEY", envKey)
	m := testutil.NewMockMailgun(t)

	code, stdout, stderr := runValidateMock(t, m, "-c", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	want := `(region "us" from config file, domain "mg.example.com" from config file, api key from MAILFIRE_MAILGUN_API_KEY)`
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout missing %q:\n%s", want, stdout)
	}
}

// TestValidateEnvExplicitMissingFile: -c with a missing file fails even
// when the environment would be sufficient on its own.
func TestValidateEnvExplicitMissingFile(t *testing.T) {
	envOnly(t)
	m := testutil.NewMockMailgun(t)
	missing := filepath.Join(t.TempDir(), "nope.conf")

	code, stdout, _ := runValidateMock(t, m, "-c", missing)
	if code != ExitConfig {
		t.Fatalf("exit = %d, want %d", code, ExitConfig)
	}
	if !strings.Contains(stdout, "FAIL config file:") || !strings.Contains(stdout, "no such file") {
		t.Errorf("stdout:\n%s", stdout)
	}
	assertNoAPIRequest(t, m)
}

// TestValidateEnvInvalidValue: an invalid environment value fails the
// mandatory-keys step and names the variable, never the key.
func TestValidateEnvInvalidValue(t *testing.T) {
	envOnly(t)
	t.Setenv("MAILFIRE_MAILGUN_REGION", "uk")
	m := testutil.NewMockMailgun(t)

	code, stdout, stderr := runValidateMock(t, m)
	if code != ExitConfig {
		t.Fatalf("exit = %d, want %d", code, ExitConfig)
	}
	if !strings.Contains(stdout, "FAIL mandatory keys:") || !strings.Contains(stdout, "MAILFIRE_MAILGUN_REGION") {
		t.Errorf("stdout:\n%s", stdout)
	}
	if strings.Contains(stdout+stderr, envKey) {
		t.Errorf("output leaks the API key:\n%s%s", stdout, stderr)
	}
	assertNoAPIRequest(t, m)
}

// TestValidateNoConfigNoEnv: without a file or variables the not-found
// error remains and points at the environment alternative.
func TestValidateNoConfigNoEnv(t *testing.T) {
	envOnly(t)
	for _, n := range []string{"MAILFIRE_MAILGUN_API_KEY", "MAILFIRE_MAILGUN_REGION", "MAILFIRE_SENDING_DOMAIN", "MAILFIRE_DEFER_DEFER_DIR"} {
		t.Setenv(n, "")
	}
	m := testutil.NewMockMailgun(t)

	code, stdout, _ := runValidateMock(t, m)
	if code != ExitConfig {
		t.Fatalf("exit = %d, want %d", code, ExitConfig)
	}
	if !strings.Contains(stdout, "no configuration file found") || !strings.Contains(stdout, "MAILFIRE_MAILGUN_API_KEY") {
		t.Errorf("stdout:\n%s", stdout)
	}
	assertNoAPIRequest(t, m)
}

// TestCLIOverridesInvalidEnv: a valid command-line option wins over an
// invalid lower-priority environment value instead of failing the run.
func TestCLIOverridesInvalidEnv(t *testing.T) {
	envOnly(t)
	t.Setenv("MAILFIRE_SENDING_FROM", "not an address")
	t.Setenv("MAILFIRE_SENDING_TEMPLATE", "gothic")
	t.Setenv("MAILFIRE_MAILGUN_TIMEOUT", "soon")

	m := testutil.NewMockMailgun(t)
	send := []string{"--to", "a@example.com", "--subject", "s", "--md"}

	// Without the overriding options, the invalid values fail the config.
	code, _, stderr := runMock(t, m, "hi\n", append(send, "--from", "sender@example.com", "--template", "card")...)
	if code != ExitConfig || !strings.Contains(stderr, "MAILFIRE_MAILGUN_TIMEOUT") {
		t.Errorf("no --timeout: exit = %d, stderr = %q", code, stderr)
	}

	code, _, stderr = runMock(t, m, "hi\n",
		append(send, "--from", "sender@example.com", "--template", "card", "--timeout", "5s")...)
	if code != ExitOK {
		t.Fatalf("send: exit = %d; stderr:\n%s", code, stderr)
	}
	req := m.LastRequest(t)
	if got := req.Field("from"); got != "sender@example.com" {
		t.Errorf("from = %q", got)
	}
	if !strings.Contains(req.Field("html"), cardMarker) {
		t.Error("html part does not use the card template")
	}

	// --template-file also overrides an invalid environment template name.
	tpl := filepath.Join(t.TempDir(), "t.html")
	if err := os.WriteFile(tpl, []byte("<html><body>CUSTOM {{.Body}}</body></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runMock(t, m, "hi\n",
		append(send, "--from", "sender@example.com", "--template-file", tpl, "--timeout", "5s")...)
	if code != ExitOK {
		t.Fatalf("send --template-file: exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(m.LastRequest(t).Field("html"), "CUSTOM") {
		t.Error("html part does not use the custom template")
	}

	// validate and resend override only the timeout; sender and template
	// are still validated from the environment there.
	if code, stdout, _ := runValidateMock(t, m, "--timeout", "5s"); code != ExitConfig || !strings.Contains(stdout, "MAILFIRE_SENDING_FROM") {
		t.Errorf("validate with invalid sender: exit = %d; stdout:\n%s", code, stdout)
	}
	t.Setenv("MAILFIRE_SENDING_FROM", "")
	t.Setenv("MAILFIRE_SENDING_TEMPLATE", "")
	if code, stdout, stderr := runValidateMock(t, m, "--timeout", "5s"); code != ExitOK {
		t.Errorf("validate --timeout: exit = %d; stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	var out, errOut strings.Builder
	if code := Run([]string{"resend", "--timeout", "5s"}, &out, &errOut, testInfo, WithBaseURL(m.URL())); code != ExitOK {
		t.Errorf("resend --timeout: exit = %d; stderr:\n%s", code, errOut.String())
	}
}

// TestCLIOverridesInvalidFileFrom: --from also wins over an invalid file
// value.
func TestCLIOverridesInvalidFileFrom(t *testing.T) {
	cfg := writeConfig(t, "from = \"not an address\"\n")
	m := testutil.NewMockMailgun(t)
	code, _, stderr := runMock(t, m, "hi\n", "-c", cfg, "--to", "a@example.com", "--subject", "s", "--from", "sender@example.com")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
}
