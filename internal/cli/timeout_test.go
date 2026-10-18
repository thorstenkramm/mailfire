package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// timeoutConfig writes a config with an optional [mailgun] timeout value
// and, when deferDir is non-empty, a pinned [defer] defer_dir (so a
// deferrable failure cannot leak into the real user data directory).
// Paths go into TOML literal strings, so Windows backslashes survive.
func timeoutConfig(t *testing.T, mailgunTimeout, deferDir string) string {
	t.Helper()
	cfg := "[mailgun]\napi_key = \"test-key-not-real\"\nregion = \"us\"\n"
	if mailgunTimeout != "" {
		cfg += "timeout = \"" + mailgunTimeout + "\"\n"
	}
	cfg += "\n[sending]\ndomain = \"mg.example.com\"\nfrom = \"ops@example.com\"\n"
	if deferDir != "" {
		cfg += "\n[defer]\ndefer_dir = '" + deferDir + "'\n"
	}
	path := filepath.Join(t.TempDir(), "mailfire.conf")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

// TestTimeoutFlagParsing: valid Go durations parse; empty, zero,
// negative, malformed, unitless, and overflowing values are rejected at
// flag-parse time for all three commands that accept --timeout.
func TestTimeoutFlagParsing(t *testing.T) {
	opts, _, err := ParseSendFlags([]string{"--to", "a@example.com", "--subject", "s", "--timeout", "90s"})
	if err != nil {
		t.Fatalf("ParseSendFlags(--timeout 90s): %v", err)
	}
	if opts.Timeout != 90*time.Second {
		t.Errorf("Timeout = %v, want 90s", opts.Timeout)
	}

	// "" covers an explicitly supplied empty value (--timeout= or
	// --timeout ''): presence is tracked separately from omission.
	for _, bad := range []string{"", "0s", "-5s", "soon", "10", "9999999h"} {
		if _, _, err := ParseSendFlags([]string{
			"--to", "a@example.com", "--subject", "s", "--timeout", bad,
		}); err == nil || !strings.Contains(err.Error(), "--timeout") {
			t.Errorf("ParseSendFlags(--timeout %q): err = %v, want a --timeout error", bad, err)
		}
		if _, err := parseResendFlags([]string{"--timeout", bad}); err == nil ||
			!strings.Contains(err.Error(), "--timeout") {
			t.Errorf("parseResendFlags(--timeout %q): err = %v, want a --timeout error", bad, err)
		}
		if _, err := parseValidateFlags([]string{"--timeout", bad}); err == nil ||
			!strings.Contains(err.Error(), "--timeout") {
			t.Errorf("parseValidateFlags(--timeout %q): err = %v, want a --timeout error", bad, err)
		}
	}
}

// Invalid --timeout values — including an explicitly empty one — are
// usage errors (exit 1) reported with the subcommand's usage, before any
// config or network access.
func TestTimeoutFlagUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--timeout", "0s", "--to", "a@example.com", "--subject", "s"}, "Flags (send):"},
		{[]string{"--timeout=", "--to", "a@example.com", "--subject", "s"}, "Flags (send):"},
		{[]string{"resend", "--timeout", "-1m"}, "Flags (resend):"},
		{[]string{"resend", "--timeout="}, "Flags (resend):"},
		{[]string{"validate", "--timeout", "soon"}, "Flags (validate):"},
		{[]string{"validate", "--timeout", ""}, "Flags (validate):"},
	} {
		code, stdout, stderr := run(t, tc.args...)
		if code != ExitUsage {
			t.Errorf("Run(%v) = exit %d, want %d", tc.args, code, ExitUsage)
		}
		if !strings.Contains(stderr, "invalid --timeout") || !strings.Contains(stderr, tc.want) {
			t.Errorf("Run(%v): stderr missing the error or %q:\n%s", tc.args, tc.want, stderr)
		}
		if stdout != "" {
			t.Errorf("Run(%v): stdout = %q, want empty", tc.args, stdout)
		}
	}
}

// TestEffectiveTimeout pins the exact resolved timeout for every
// flag/config combination, including the preserved 30s client default.
func TestEffectiveTimeout(t *testing.T) {
	if mailgun.DefaultTimeout != 30*time.Second {
		t.Fatalf("mailgun.DefaultTimeout = %v, want 30s (the documented, preserved default)", mailgun.DefaultTimeout)
	}
	for _, tc := range []struct {
		name string
		cfg  time.Duration
		flag time.Duration
		want time.Duration
	}{
		{"neither: client default", 0, 0, 30 * time.Second},
		{"config only", 15 * time.Second, 0, 15 * time.Second},
		{"flag only", 0, 20 * time.Second, 20 * time.Second},
		{"flag overrides config", 15 * time.Second, 20 * time.Second, 20 * time.Second},
	} {
		if got := effectiveTimeout(&config.Config{Timeout: tc.cfg}, tc.flag); got != tc.want {
			t.Errorf("%s: effectiveTimeout = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSendTimeoutStallsServer: a stalled Mailgun is interrupted by the
// configured per-request timeout (well before the server would reply, with
// generous scheduling tolerance), the failure is classified as a timeout,
// and no credentials leak into the diagnostics.
func TestSendTimeoutStallsServer(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.Timeout(500 * time.Millisecond)
	deferDir := filepath.Join(t.TempDir(), "deferred")
	cfg := timeoutConfig(t, "50ms", deferDir)

	start := time.Now()
	code, stdout, stderr := runMock(t, m, "the body",
		"-c", cfg, "--to", "a@example.com", "--subject", "slow")
	elapsed := time.Since(start)

	if code != ExitDeferred {
		t.Fatalf("exit = %d, want %d (timeout is deferrable); stderr:\n%s", code, ExitDeferred, stderr)
	}
	if elapsed >= 450*time.Millisecond {
		t.Errorf("took %v, want the request interrupted well before the server's 500ms reply", elapsed)
	}
	if !strings.Contains(stderr, "request timed out after 50ms") {
		t.Errorf("stderr missing the timeout diagnostic:\n%s", stderr)
	}
	if strings.Contains(stderr, "test-key-not-real") || strings.Contains(stdout, "test-key-not-real") {
		t.Errorf("output leaks the API key:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	// The timeout is deferrable: the message was stored for resend.
	if ids := deferredIDs(t, deferDir); len(ids) != 1 {
		t.Errorf("deferred messages = %v, want exactly 1", ids)
	}
}

// TestSendTimeoutPrecedence: flag, config, and default precedence for
// send, against a server stalling 500ms. Timeout cases use --no-defer
// (exit 5) and verify the effective deadline through the client's
// diagnostic, which names the timeout actually applied; the default case
// must let the 500ms reply through (the exact 30s is pinned by
// TestEffectiveTimeout).
func TestSendTimeoutPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name          string
		configTimeout string
		flagTimeout   string
		wantCode      int
		wantErr       string // effective timeout named in the diagnostic; "": send succeeds
	}{
		{"flag only", "", "50ms", ExitSendFail, "request timed out after 50ms"},
		{"config only", "70ms", "", ExitSendFail, "request timed out after 70ms"},
		{"flag overrides config", "70ms", "50ms", ExitSendFail, "request timed out after 50ms"},
		{"default preserved", "", "", ExitOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			m.SendResponse = testutil.Timeout(500 * time.Millisecond)
			cfg := timeoutConfig(t, tc.configTimeout, "")
			args := []string{"-c", cfg, "--no-defer",
				"--to", "a@example.com", "--subject", "slow"}
			if tc.flagTimeout != "" {
				args = append(args, "--timeout", tc.flagTimeout)
			}
			code, stdout, stderr := runMock(t, m, "the body", args...)
			if code != tc.wantCode {
				t.Fatalf("exit = %d, want %d; stderr:\n%s", code, tc.wantCode, stderr)
			}
			if tc.wantErr == "" {
				if !strings.Contains(stdout, "<x>") {
					t.Errorf("stdout missing the message ID:\n%s", stdout)
				}
			} else if !strings.Contains(stderr, tc.wantErr) {
				t.Errorf("stderr missing %q (the effective timeout):\n%s", tc.wantErr, stderr)
			}
		})
	}
}

// TestSendTimeoutDeferSemantics: a timeout defers when deferral is
// enabled; --no-defer and --test keep their existing semantics (exit 5,
// nothing stored).
func TestSendTimeoutDeferSemantics(t *testing.T) {
	for _, tc := range []struct {
		name     string
		extraArg []string
		wantCode int
		wantErr  string
	}{
		{"no-defer", []string{"--no-defer"}, ExitSendFail, "not deferring (--no-defer)"},
		{"test mode", []string{"--test"}, ExitSendFail, "--test sends are never deferred"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			m.SendResponse = testutil.Timeout(500 * time.Millisecond)
			deferDir := filepath.Join(t.TempDir(), "deferred")
			cfg := timeoutConfig(t, "", deferDir)

			args := append([]string{"-c", cfg, "--timeout", "50ms",
				"--to", "a@example.com", "--subject", "slow"}, tc.extraArg...)
			code, _, stderr := runMock(t, m, "the body", args...)
			if code != tc.wantCode {
				t.Fatalf("exit = %d, want %d; stderr:\n%s", code, tc.wantCode, stderr)
			}
			if !strings.Contains(stderr, "request timed out after 50ms") ||
				!strings.Contains(stderr, tc.wantErr) {
				t.Errorf("stderr missing the timeout or %q:\n%s", tc.wantErr, stderr)
			}
			// Nothing may be stored; the directory is not even created.
			entries, err := os.ReadDir(deferDir)
			if err != nil && !os.IsNotExist(err) {
				t.Fatalf("reading defer_dir: %v", err)
			}
			if len(entries) != 0 {
				t.Errorf("defer_dir = %v, want nothing stored", entries)
			}
		})
	}
}

// TestResendTimeout: the timeout applies to each resent message; flag,
// config, and default precedence are verified through the effective
// deadline named in the diagnostic. A timeout fails retryable (exit 4)
// and the message stays deferred; the default lets the 500ms reply
// through and removes the message.
func TestResendTimeout(t *testing.T) {
	for _, tc := range []struct {
		name          string
		configTimeout string
		flagTimeout   string
		wantCode      int
		wantErr       string // effective timeout named in the diagnostic; "": resend succeeds
	}{
		{"flag only", "", "60ms", ExitDeferred, "request timed out after 60ms"},
		{"config only", "80ms", "", ExitDeferred, "request timed out after 80ms"},
		{"flag overrides config", "80ms", "60ms", ExitDeferred, "request timed out after 60ms"},
		{"default preserved", "", "", ExitOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			m.SendResponse = testutil.Timeout(500 * time.Millisecond)
			deferDir := filepath.Join(t.TempDir(), "deferred")
			cfg := timeoutConfig(t, tc.configTimeout, deferDir)
			meta := seedDeferred(t, deferDir, "a@example.com", "slow", time.Hour)

			args := []string{"resend", "-c", cfg}
			if tc.flagTimeout != "" {
				args = append(args, "--timeout", tc.flagTimeout)
			}
			code, stdout, stderr := runMock(t, m, "", args...)
			if code != tc.wantCode {
				t.Fatalf("exit = %d, want %d; stderr:\n%s", code, tc.wantCode, stderr)
			}
			if tc.wantErr == "" {
				if !strings.Contains(stdout, meta.ID+": sent") {
					t.Errorf("stdout missing the sent line:\n%s", stdout)
				}
				if ids := deferredIDs(t, deferDir); len(ids) != 0 {
					t.Errorf("defer_dir = %v, want the message removed after delivery", ids)
				}
				return
			}
			if !strings.Contains(stdout, meta.ID+": failed (retryable") {
				t.Errorf("stdout missing the failure line:\n%s", stdout)
			}
			if !strings.Contains(stderr, tc.wantErr) {
				t.Errorf("stderr missing %q (the effective timeout):\n%s", tc.wantErr, stderr)
			}
			if strings.Contains(stderr, "test-key-not-real") {
				t.Errorf("stderr leaks the API key:\n%s", stderr)
			}
			if ids := deferredIDs(t, deferDir); len(ids) != 1 || ids[0] != meta.ID {
				t.Errorf("defer_dir = %v, want the failed message kept", ids)
			}
		})
	}
}

// stalledDomain stalls the domain lookup validate performs; the reply is
// a valid domain record for runs whose timeout lets the reply through.
func stalledDomain(m *testutil.MockMailgun) {
	m.DomainResponse = testutil.Response{
		Status: 200,
		Delay:  500 * time.Millisecond,
		Body:   `{"domain":{"name":"mg.example.com","state":"active"}}`,
	}
}

// TestValidateTimeout: the timeout applies to validate's domain check;
// flag, config, and default precedence are verified through the effective
// deadline named in the diagnostic. A stalled server means Mailgun cannot
// be reached reliably (exit 4); the default lets the 500ms reply through.
func TestValidateTimeout(t *testing.T) {
	for _, tc := range []struct {
		name          string
		configTimeout string
		flagTimeout   string
		wantCode      int
		wantErr       string // effective timeout named in the diagnostic; "": validate succeeds
	}{
		{"flag only", "", "50ms", ExitDeferred, "request timed out after 50ms"},
		{"config only", "75ms", "", ExitDeferred, "request timed out after 75ms"},
		{"flag overrides config", "75ms", "50ms", ExitDeferred, "request timed out after 50ms"},
		{"default preserved", "", "", ExitOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			stalledDomain(m)
			cfg := timeoutConfig(t, tc.configTimeout, "")

			args := []string{"-c", cfg}
			if tc.flagTimeout != "" {
				args = append(args, "--timeout", tc.flagTimeout)
			}
			code, stdout, stderr := runValidateMock(t, m, args...)
			if code != tc.wantCode {
				t.Fatalf("exit = %d, want %d; stderr:\n%s", code, tc.wantCode, stderr)
			}
			if tc.wantErr == "" {
				if !strings.Contains(stdout, "ok   API key and domain verified") {
					t.Errorf("stdout missing the success line:\n%s", stdout)
				}
				return
			}
			if !strings.Contains(stdout, "FAIL API key and domain") {
				t.Errorf("stdout missing the FAIL line:\n%s", stdout)
			}
			if !strings.Contains(stderr, tc.wantErr) {
				t.Errorf("stderr missing %q (the effective timeout):\n%s", tc.wantErr, stderr)
			}
			if strings.Contains(stderr, "test-key-not-real") {
				t.Errorf("stderr leaks the API key:\n%s", stderr)
			}
		})
	}
}
