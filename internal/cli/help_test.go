package cli

import (
	"os"
	"strings"
	"testing"
)

const readmePath = "../../README.md"

// readmeHelpBlock extracts the text block that follows "$ mailfire --help"
// in README.md, as the text mailfire --help must print. Line endings are
// normalized to LF, so a CRLF checkout (core.autocrlf) still matches.
func readmeHelpBlock(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Skipf("README.md not available (%v); skipping docs pin test", err)
	}
	const marker = "```text\n$ mailfire --help\n"
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	start := strings.Index(s, marker)
	if start < 0 {
		t.Fatalf("README.md has no %q block", marker)
	}
	s = s[start+len(marker):]
	end := strings.Index(s, "```")
	if end < 0 {
		t.Fatal("README.md help block is not terminated")
	}
	return s[:end]
}

// trimTrailingSpace removes trailing blanks from every line. Editors
// commonly add or strip them in Markdown, and they are invisible in the
// rendered README, so they must not decide the comparison.
func trimTrailingSpace(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}

func TestUsageMatchesREADME(t *testing.T) {
	want := trimTrailingSpace(readmeHelpBlock(t))
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		code, stdout, stderr := run(t, args...)
		if code != ExitOK {
			t.Errorf("Run(%v) = exit %d, want %d", args, code, ExitOK)
		}
		if trimTrailingSpace(stdout) != want {
			t.Errorf("Run(%v) output differs from the README help block\n--- README ---\n%s\n--- mailfire ---\n%s", args, want, stdout)
		}
		if stderr != "" {
			t.Errorf("Run(%v): stderr = %q, want empty", args, stderr)
		}
	}
}

// TestUsageMatchesREADME ignores trailing blanks, so pin them here: the
// help texts themselves must not carry any, and must end in a newline.
func TestHelpTextsAreClean(t *testing.T) {
	for name, text := range map[string]string{
		"usageText": usageText, "sendUsage": sendUsage, "resendUsage": resendUsage,
		"validateUsage": validateUsage, "versionUsage": versionUsage,
	} {
		if text != trimTrailingSpace(text) {
			t.Errorf("%s has trailing whitespace", name)
		}
		if !strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\n\n") {
			t.Errorf("%s must end in exactly one newline", name)
		}
	}
}

func TestSubcommandHelp(t *testing.T) {
	cases := []struct {
		args    []string
		want    []string
		wantNot []string
	}{
		{args: []string{"send", "--help"}, want: []string{"Usage:\n  mailfire [send] [flags]", "Flags (send):", "--to <addr>", "--test", "--no-defer", "-c, --config <p>", "-h, --help"}, wantNot: []string{"--force", "--list"}},
		{args: []string{"send", "-h"}, want: []string{"Flags (send):"}},
		{args: []string{"--to", "a@example.com", "--help"}, want: []string{"Flags (send):"}},
		{args: []string{"validate", "-h"}, want: []string{"Usage:\n  mailfire validate", "Flags (validate):", "-c, --config <p>", "-h, --help"}, wantNot: []string{"--to <addr>", "--force"}},
		{args: []string{"validate", "--help"}, want: []string{"Flags (validate):"}},
		{args: []string{"resend", "--help"}, want: []string{"Usage:\n  mailfire resend", "Flags (resend):", "--list", "--force", "-c, --config <p>", "-h, --help"}, wantNot: []string{"--to <addr>"}},
		{args: []string{"resend", "-h"}, want: []string{"Flags (resend):"}},
		{args: []string{"version", "--help"}, want: []string{"Usage:\n  mailfire version", "Flags (version):", "-h, --help"}, wantNot: []string{"--config", "--to <addr>"}},
		{args: []string{"version", "-h"}, want: []string{"Flags (version):"}},
	}
	for _, tc := range cases {
		code, stdout, stderr := run(t, tc.args...)
		if code != ExitOK {
			t.Errorf("Run(%v) = exit %d, want %d", tc.args, code, ExitOK)
		}
		if stderr != "" {
			t.Errorf("Run(%v): stderr = %q, want empty", tc.args, stderr)
		}
		for _, w := range tc.want {
			if !strings.Contains(stdout, w) {
				t.Errorf("Run(%v): help lacks %q:\n%s", tc.args, w, stdout)
			}
		}
		for _, w := range tc.wantNot {
			if strings.Contains(stdout, w) {
				t.Errorf("Run(%v): help unexpectedly contains %q:\n%s", tc.args, w, stdout)
			}
		}
	}
}

// Every flag the send flag set accepts must be documented in the send
// help, and the top-level help must document every subcommand.
func TestSendHelpListsAllFlags(t *testing.T) {
	for _, f := range []string{
		"--to", "--cc", "--bcc", "--from", "--subject", "--txt", "--md", "--html",
		"--html-raw", "--file", "--preheader", "--footer", "--attach", "--test",
		"--no-defer", "--config", "--help",
	} {
		if !strings.Contains(sendUsage, f+" ") && !strings.Contains(sendUsage, f+"\n") {
			t.Errorf("send help does not document %s", f)
		}
	}
	for _, sub := range []string{"validate", "resend", "version"} {
		if !strings.Contains(usageText, "mailfire "+sub) {
			t.Errorf("top-level help does not mention %q", sub)
		}
	}
}

func TestUsageErrorsShowSubcommandUsage(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--subject", "s"}, "Flags (send):"},
		{[]string{"validate", "extra"}, "Flags (validate):"},
		{[]string{"resend", "--bogus"}, "Flags (resend):"},
		{[]string{"version", "extra"}, "Flags (version):"},
	}
	for _, tc := range cases {
		code, stdout, stderr := run(t, tc.args...)
		if code != ExitUsage {
			t.Errorf("Run(%v) = exit %d, want %d", tc.args, code, ExitUsage)
		}
		if stdout != "" {
			t.Errorf("Run(%v): stdout = %q, want empty", tc.args, stdout)
		}
		if !strings.Contains(stderr, tc.want) {
			t.Errorf("Run(%v): stderr lacks %q:\n%s", tc.args, tc.want, stderr)
		}
	}
}
