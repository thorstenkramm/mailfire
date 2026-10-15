package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var testInfo = BuildInfo{Version: "1.2.3", Commit: "abc1234", BuildDate: "2026-10-15"}

// run executes Run with captured output and returns exit code, stdout, stderr.
func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr, testInfo)
	return code, stdout.String(), stderr.String()
}

func TestRunHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"send", "--help"}, {"send", "-h"}, {"--help", "send"}} {
		code, stdout, stderr := run(t, args...)
		if code != ExitOK {
			t.Errorf("Run(%v) = exit %d, want %d", args, code, ExitOK)
		}
		if !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "--html-raw") {
			t.Errorf("Run(%v): stdout does not contain usage text:\n%s", args, stdout)
		}
		if stderr != "" {
			t.Errorf("Run(%v): stderr = %q, want empty", args, stderr)
		}
	}
}

func TestRunSubcommandHelp(t *testing.T) {
	for _, args := range [][]string{
		{"validate", "--help"},
		{"resend", "-h"},
		{"resend", "--list", "--help"},
		{"version", "-help"},
	} {
		code, stdout, stderr := run(t, args...)
		if code != ExitOK {
			t.Errorf("Run(%v) = exit %d, want %d", args, code, ExitOK)
		}
		if !strings.Contains(stdout, "Usage:") {
			t.Errorf("Run(%v): stdout does not contain usage text:\n%s", args, stdout)
		}
		if stderr != "" {
			t.Errorf("Run(%v): stderr = %q, want empty", args, stderr)
		}
	}

	// After "--", -h is an operand, not a help request.
	if code, _, _ := run(t, "resend", "--", "-h"); code == ExitOK {
		t.Errorf("Run(resend -- -h) = exit 0, want non-zero")
	}
}

func TestRunUnknownSubcommand(t *testing.T) {
	code, stdout, stderr := run(t, "frobnicate")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, `unknown subcommand "frobnicate"`) {
		t.Errorf("stderr missing unknown-subcommand message:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr missing usage text:\n%s", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestRunStubs(t *testing.T) {
	for _, sub := range []string{"version"} {
		code, stdout, stderr := run(t, sub)
		if code != ExitUsage {
			t.Errorf("%s: exit = %d, want %d", sub, code, ExitUsage)
		}
		want := "mailfire: " + sub + ": not implemented yet\n"
		if stderr != want {
			t.Errorf("%s: stderr = %q, want %q", sub, stderr, want)
		}
		if stdout != "" {
			t.Errorf("%s: stdout = %q, want empty", sub, stdout)
		}
	}
}

func TestRunSendConfigErrors(t *testing.T) {
	base := []string{"--to", "a@example.com", "--subject", "hi"}

	t.Run("explicit config file missing", func(t *testing.T) {
		args := append([]string{"-c", filepath.Join(t.TempDir(), "nope.conf")}, base...)
		code, _, stderr := run(t, args...)
		if code != ExitConfig {
			t.Errorf("exit = %d, want %d", code, ExitConfig)
		}
		if !strings.Contains(stderr, "no such file") {
			t.Errorf("stderr missing message:\n%s", stderr)
		}
	})

	t.Run("invalid config", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mailfire.conf")
		if err := os.WriteFile(path, []byte("[mailgun]\nregion = \"us\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		args := append([]string{"-c", path}, base...)
		code, _, stderr := run(t, args...)
		if code != ExitConfig {
			t.Errorf("exit = %d, want %d", code, ExitConfig)
		}
		if !strings.Contains(stderr, "mailgun.api_key") {
			t.Errorf("stderr missing key name:\n%s", stderr)
		}
	})

	t.Run("no config found", func(t *testing.T) {
		// Isolate from any config on the host: empty HOME and an empty
		// working directory.
		if _, err := os.Stat("/etc/mailfire/mailfire.conf"); err == nil {
			t.Skip("/etc/mailfire/mailfire.conf exists; cannot test not-found")
		}
		t.Setenv("HOME", t.TempDir())
		t.Chdir(t.TempDir())
		code, _, stderr := run(t, base...)
		if code != ExitConfig {
			t.Errorf("exit = %d, want %d", code, ExitConfig)
		}
		if !strings.Contains(stderr, "no configuration file found") {
			t.Errorf("stderr missing message:\n%s", stderr)
		}
	})
}

func TestRunSendUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no args", nil, "missing required flag: --to"},
		{"missing subject", []string{"send", "--to", "a@example.com"}, "missing required flag: --subject"},
		{"conflicting formats", []string{"--md", "--html", "--to", "a@example.com", "--subject", "s"}, "conflicting body formats"},
		{"unknown flag", []string{"--bogus"}, "flag provided but not defined"},
		{"missing flag value", []string{"--to"}, "flag needs an argument"},
		{"positional args", []string{"send", "--to", "a@example.com", "--subject", "s", "extra"}, "unexpected arguments: extra"},
		{"conflicting config flags", []string{"-c", "a.conf", "--config", "b.conf", "--to", "a@example.com", "--subject", "s"}, "conflicting config paths"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := run(t, tc.args...)
			if code != ExitUsage {
				t.Errorf("exit = %d, want %d", code, ExitUsage)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr missing %q:\n%s", tc.want, stderr)
			}
			if !strings.Contains(stderr, "Usage:") {
				t.Errorf("stderr missing usage text:\n%s", stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
		})
	}
}

func TestParseSendFlagsDefaults(t *testing.T) {
	opts, help, err := ParseSendFlags([]string{"--to", "a@example.com", "--subject", "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if help {
		t.Fatal("showHelp = true, want false")
	}
	if opts.Format != "txt" {
		t.Errorf("Format = %q, want %q", opts.Format, "txt")
	}
	if opts.Test || opts.NoDefer {
		t.Errorf("Test/NoDefer = %v/%v, want false/false", opts.Test, opts.NoDefer)
	}
	if opts.From != "" || opts.File != "" || opts.Preheader != "" || opts.Footer != "" || opts.Config != "" {
		t.Errorf("optional string flags not empty: %+v", opts)
	}
}

func TestParseSendFlagsFull(t *testing.T) {
	args := []string{
		"--to", "A <a@example.com>", "--to", "b@example.com",
		"--cc", "c@example.com", "--bcc", "d@example.com", "--bcc", "e@example.com",
		"--from", "Sender <s@example.com>",
		"--subject", "Doe, John style: commas stay intact",
		"--html", "--file", "body.html",
		"--preheader", "preview", "--footer", "foot.html",
		"--attach", "one.pdf", "--attach", "two.png",
		"--test", "--no-defer",
		"--config", "/etc/mailfire/mailfire.conf",
	}
	opts, help, err := ParseSendFlags(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if help {
		t.Fatal("showHelp = true, want false")
	}
	want := SendOptions{
		To:        []string{"A <a@example.com>", "b@example.com"},
		Cc:        []string{"c@example.com"},
		Bcc:       []string{"d@example.com", "e@example.com"},
		From:      "Sender <s@example.com>",
		Subject:   "Doe, John style: commas stay intact",
		Format:    "html",
		File:      "body.html",
		Preheader: "preview",
		Footer:    "foot.html",
		Attach:    []string{"one.pdf", "two.png"},
		Test:      true,
		NoDefer:   true,
		Config:    "/etc/mailfire/mailfire.conf",
	}
	if !reflect.DeepEqual(opts, want) {
		t.Errorf("opts = %+v\nwant %+v", opts, want)
	}
}

func TestParseSendFlagsFormats(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "txt"},
		{[]string{"--txt"}, "txt"},
		{[]string{"--md"}, "md"},
		{[]string{"--html"}, "html"},
		{[]string{"--html-raw"}, "html-raw"},
	} {
		args := append(tc.args, "--to", "a@example.com", "--subject", "s")
		opts, _, err := ParseSendFlags(args)
		if err != nil {
			t.Fatalf("ParseSendFlags(%v): %v", args, err)
		}
		if opts.Format != tc.want {
			t.Errorf("ParseSendFlags(%v): Format = %q, want %q", args, opts.Format, tc.want)
		}
	}
}

func TestParseSendFlagsConflictingFormats(t *testing.T) {
	for _, args := range [][]string{
		{"--txt", "--md"},
		{"--md", "--html"},
		{"--html", "--html-raw"},
		{"--txt", "--md", "--html", "--html-raw"},
	} {
		args = append(args, "--to", "a@example.com", "--subject", "s")
		_, _, err := ParseSendFlags(args)
		if err == nil {
			t.Errorf("ParseSendFlags(%v): want error, got nil", args)
		}
	}
}

func TestParseSendFlagsConfig(t *testing.T) {
	// -c alone
	opts, _, err := ParseSendFlags([]string{"-c", "x.conf", "--to", "a@example.com", "--subject", "s"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Config != "x.conf" {
		t.Errorf("Config = %q, want %q", opts.Config, "x.conf")
	}

	// -c and --config with the same value are accepted.
	opts, _, err = ParseSendFlags([]string{"-c", "x.conf", "--config", "x.conf", "--to", "a@example.com", "--subject", "s"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Config != "x.conf" {
		t.Errorf("Config = %q, want %q", opts.Config, "x.conf")
	}
}

func TestParseSendFlagsHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		_, help, err := ParseSendFlags(args)
		if err != nil {
			t.Errorf("ParseSendFlags(%v): err = %v, want nil", args, err)
		}
		if !help {
			t.Errorf("ParseSendFlags(%v): showHelp = false, want true", args)
		}
	}
}

func TestParseSendFlagsEqualsSyntax(t *testing.T) {
	opts, _, err := ParseSendFlags([]string{"--to=a@example.com", "--subject=s", "--md"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(opts.To, []string{"a@example.com"}) || opts.Subject != "s" || opts.Format != "md" {
		t.Errorf("unexpected opts: %+v", opts)
	}
}

func TestStringSliceString(t *testing.T) {
	s := stringSlice{"a", "b"}
	if got := s.String(); got != "a,b" {
		t.Errorf("String() = %q, want %q", got, "a,b")
	}
}
