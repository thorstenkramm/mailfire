package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// runValidateMock runs `mailfire validate` against a mock Mailgun server
// and returns exit code, stdout, stderr.
func runValidateMock(t *testing.T, m *testutil.MockMailgun, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(append([]string{"validate"}, args...), &stdout, &stderr, testInfo,
		WithBaseURL(m.URL()))
	return code, stdout.String(), stderr.String()
}

func TestValidateAllPass(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"Ops Bot <ops@example.com>\"\n")

	for _, args := range [][]string{
		{"-c", cfg},
		{"--config", cfg},
	} {
		code, stdout, stderr := runValidateMock(t, m, args...)
		if code != ExitOK {
			t.Fatalf("validate %v: exit = %d, want %d; stderr:\n%s", args, code, ExitOK, stderr)
		}
		for _, want := range []string{
			"ok   config file found and parses as valid TOML: " + cfg,
			`ok   mandatory keys present and well-formed (region "us" from config file, domain "mg.example.com" from config file, from "Ops Bot <ops@example.com>" from config file, api key from config file, temporary directory ` + strconv.Quote(os.TempDir()) + ` from system default)`,
			`ok   API key and domain verified with Mailgun: domain "mg.example.com" is active`,
		} {
			if !strings.Contains(stdout, want) {
				t.Errorf("validate %v: stdout missing %q; stdout:\n%s", args, want, stdout)
			}
		}
		if stderr != "" {
			t.Errorf("validate %v: stderr = %q, want empty", args, stderr)
		}
	}

	// Exactly the domain lookup happened, authenticated with the key.
	reqs := m.Requests()
	if len(reqs) != 2 {
		t.Fatalf("mock received %d requests, want 2 (one per run)", len(reqs))
	}
	req := reqs[0]
	if req.Method != http.MethodGet || req.Path != "/v3/domains/mg.example.com" {
		t.Errorf("request = %s %s, want GET /v3/domains/mg.example.com", req.Method, req.Path)
	}
	if req.User != "api" || req.Password != "test-key-not-real" {
		t.Errorf("basic auth = %q/%q, want api/test-key-not-real", req.User, req.Password)
	}
}

func TestValidateConfigNotFound(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.conf")

	code, stdout, stderr := runValidateMock(t, m, "-c", missing)
	if code != ExitConfig {
		t.Fatalf("exit = %d, want %d", code, ExitConfig)
	}
	if !strings.Contains(stdout, "FAIL config file:") {
		t.Errorf("stdout missing FAIL line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "no such file") {
		t.Errorf("stdout should name the problem:\n%s", stdout)
	}
	if !strings.Contains(stderr, "mailfire: validate:") {
		t.Errorf("stderr missing error detail:\n%s", stderr)
	}
	assertNoAPIRequest(t, m)
}

func TestValidateBadTOML(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	path := filepath.Join(t.TempDir(), "mailfire.conf")
	if err := os.WriteFile(path, []byte("[mailgun\napi_key = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := runValidateMock(t, m, "-c", path)
	if code != ExitConfig {
		t.Fatalf("exit = %d, want %d", code, ExitConfig)
	}
	if !strings.Contains(stdout, "FAIL config file:") || !strings.Contains(stdout, "parsing") {
		t.Errorf("stdout should report the TOML parse failure:\n%s", stdout)
	}
	// Step skipping: steps 2 and 3 never ran.
	assertNoAPIRequest(t, m)
}

func TestValidateMissingKeys(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	path := filepath.Join(t.TempDir(), "mailfire.conf")
	// Valid TOML, but [mailgun].region and [sending].domain are missing.
	content := "[mailgun]\napi_key = \"test-key-not-real\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := runValidateMock(t, m, "-c", path)
	if code != ExitConfig {
		t.Fatalf("exit = %d, want %d", code, ExitConfig)
	}
	if !strings.Contains(stdout, "ok   config file found") {
		t.Errorf("step 1 should have passed:\n%s", stdout)
	}
	if !strings.Contains(stdout, "FAIL mandatory keys:") || !strings.Contains(stdout, "mailgun.region") {
		t.Errorf("stdout should name the failing key:\n%s", stdout)
	}
	assertNoAPIRequest(t, m)
}

func TestValidateInvalidRegion(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	path := filepath.Join(t.TempDir(), "mailfire.conf")
	content := "[mailgun]\napi_key = \"k\"\nregion = \"asia\"\n\n[sending]\ndomain = \"mg.example.com\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := runValidateMock(t, m, "-c", path)
	if code != ExitConfig {
		t.Fatalf("exit = %d, want %d", code, ExitConfig)
	}
	if !strings.Contains(stdout, "FAIL mandatory keys:") || !strings.Contains(stdout, `invalid value "asia"`) {
		t.Errorf("stdout should report the invalid region:\n%s", stdout)
	}
	assertNoAPIRequest(t, m)
}

func TestValidateInvalidFrom(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"not an address\"\n")

	code, stdout, _ := runValidateMock(t, m, "-c", cfg)
	if code != ExitConfig {
		t.Fatalf("exit = %d, want %d", code, ExitConfig)
	}
	if !strings.Contains(stdout, "FAIL mandatory keys:") || !strings.Contains(stdout, "sending.from") {
		t.Errorf("stdout should name sending.from:\n%s", stdout)
	}
	assertNoAPIRequest(t, m)
}

func TestValidateAuthFailure(t *testing.T) {
	for _, resp := range []testutil.Response{testutil.Unauthorized(), testutil.Forbidden()} {
		m := testutil.NewMockMailgun(t)
		m.DomainResponse = resp
		cfg := writeConfig(t, "")

		code, stdout, stderr := runValidateMock(t, m, "-c", cfg)
		if code != ExitAuth {
			t.Fatalf("status %d: exit = %d, want %d", resp.Status, code, ExitAuth)
		}
		if !strings.Contains(stdout, "ok   mandatory keys") {
			t.Errorf("status %d: steps 1-2 should have passed:\n%s", resp.Status, stdout)
		}
		if !strings.Contains(stdout, "FAIL API key and domain: mailgun rejected the API key") {
			t.Errorf("status %d: stdout should report the auth failure:\n%s", resp.Status, stdout)
		}
		if !strings.Contains(stderr, "mailfire: validate:") {
			t.Errorf("status %d: stderr missing error detail:\n%s", resp.Status, stderr)
		}
	}
}

func TestValidateUnknownDomain(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.DomainResponse = testutil.NotFound()
	cfg := writeConfig(t, "")

	code, stdout, _ := runValidateMock(t, m, "-c", cfg)
	if code != ExitConfig {
		t.Fatalf("exit = %d, want %d", code, ExitConfig)
	}
	if !strings.Contains(stdout, `domain "mg.example.com" is unknown to Mailgun`) {
		t.Errorf("stdout should clearly report the unknown domain:\n%s", stdout)
	}
}

func TestValidateDeferrableFailures(t *testing.T) {
	t.Run("5xx", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		m.DomainResponse = testutil.InternalError()
		cfg := writeConfig(t, "")

		code, stdout, _ := runValidateMock(t, m, "-c", cfg)
		if code != ExitDeferred {
			t.Fatalf("exit = %d, want %d", code, ExitDeferred)
		}
		if !strings.Contains(stdout, "FAIL API key and domain: could not reach Mailgun reliably") {
			t.Errorf("stdout should report the retryable failure:\n%s", stdout)
		}
	})
	t.Run("network error", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close() // connection refused: a deferrable transport error
		cfg := writeConfig(t, "")

		var stdout, stderr bytes.Buffer
		code := Run([]string{"validate", "-c", cfg}, &stdout, &stderr, testInfo,
			WithBaseURL(url))
		if code != ExitDeferred {
			t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitDeferred, stderr.String())
		}
		if !strings.Contains(stdout.String(), "FAIL API key and domain: could not reach Mailgun reliably") {
			t.Errorf("stdout should report the retryable failure:\n%s", stdout.String())
		}
	})
}

func TestValidateOtherPermanentError(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.DomainResponse = testutil.BadRequest() // 400: neither auth nor 404
	cfg := writeConfig(t, "")

	code, stdout, _ := runValidateMock(t, m, "-c", cfg)
	if code != ExitConfig {
		t.Fatalf("exit = %d, want %d", code, ExitConfig)
	}
	if !strings.Contains(stdout, "FAIL API key and domain:") {
		t.Errorf("stdout should report the failure:\n%s", stdout)
	}
}

func TestValidateUsageErrors(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	for _, args := range [][]string{
		{"bogus-positional"},
		{"-c", "a.conf", "--config", "b.conf"},
	} {
		code, _, stderr := runValidateMock(t, m, args...)
		if code != ExitUsage {
			t.Errorf("validate %v: exit = %d, want %d", args, code, ExitUsage)
		}
		if !strings.Contains(stderr, "mailfire: validate:") {
			t.Errorf("validate %v: stderr missing error:\n%s", args, stderr)
		}
	}
	assertNoAPIRequest(t, m)
}

func TestValidateHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"validate", "--help"}, &stdout, &stderr, testInfo)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(stdout.String(), "Flags (validate):") {
		t.Errorf("help should list the validate flags:\n%s", stdout.String())
	}
}

// TestValidateNeverPrintsAPIKey covers every step: TOML errors on the
// api_key line, key errors, and a server echoing the key in error and
// success responses.
func TestValidateNeverPrintsAPIKey(t *testing.T) {
	const key = "LEAKY-SECRET-KEY-1234"
	base := "region = \"us\"\n\n[sending]\ndomain = \"mg.example.com\"\n"
	cases := []struct {
		name    string
		content string
		resp    *testutil.Response
	}{
		{"unquoted", "[mailgun]\napi_key = " + key + "\n", nil},
		{"bad escape", "[mailgun]\napi_key = \"" + key + "\\q\"\n", nil},
		{"inline table", "mailgun = { api_key = \"" + key + "\\q\", region = \"us\" }\n", nil},
		{"trailing junk", "[mailgun]\napi_key = \"" + key + "\" x\n", nil},
		{"wrong type", "[mailgun]\napi_key = [\"" + key + "\"]\n", nil},
		{"bad region", "[mailgun]\napi_key = \"" + key + "\"\nregion = \"mars\"\n", nil},
		{"401 echo", "[mailgun]\napi_key = \"" + key + "\"\n" + base,
			&testutil.Response{Status: http.StatusUnauthorized, Body: `{"message":"bad key ` + key + `"}`}},
		{"404 echo", "[mailgun]\napi_key = \"" + key + "\"\n" + base,
			&testutil.Response{Status: http.StatusNotFound, Body: `{"message":"` + key + `"}`}},
		{"200 echo", "[mailgun]\napi_key = \"" + key + "\"\n" + base,
			&testutil.Response{Status: http.StatusOK, Body: `{"domain":{"name":"` + key + `","state":"` + key + `"}}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			if tc.resp != nil {
				m.DomainResponse = *tc.resp
			}
			path := filepath.Join(t.TempDir(), "mailfire.conf")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, stdout, stderr := runValidateMock(t, m, "-c", path)
			if strings.Contains(stdout+stderr, key) || strings.Contains(stdout+stderr, "U+") {
				t.Errorf("output leaks the API key:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
			}
		})
	}
}

// assertNoAPIRequest fails the test when the mock received any request.
func assertNoAPIRequest(t *testing.T, m *testutil.MockMailgun) {
	t.Helper()
	if reqs := m.Requests(); len(reqs) > 0 {
		t.Fatalf("mock received %d requests, want none (earlier step failed)", len(reqs))
	}
}
