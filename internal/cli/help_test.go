package cli

import (
	"os"
	"strings"
	"testing"
)

// cliDocPath is the command-line reference of the documentation site. Its
// help blocks are the documentation of record, so they are pinned to the
// program's output.
const cliDocPath = "../../docs/content/reference/cli.md"

// helpBlock is one "$ mailfire <args>" text block of the CLI reference.
type helpBlock struct {
	args []string
	want string
}

// docHelpBlocks extracts every text block that starts with "$ mailfire",
// with the command's arguments and the text it must print. Line endings
// are normalized to LF, so a CRLF checkout (core.autocrlf) still matches.
func docHelpBlocks(t *testing.T) []helpBlock {
	t.Helper()
	data, err := os.ReadFile(cliDocPath)
	if err != nil {
		t.Skipf("%s not available (%v); skipping docs pin test", cliDocPath, err)
	}
	const marker = "```text\n$ mailfire "
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	var blocks []helpBlock
	for {
		start := strings.Index(s, marker)
		if start < 0 {
			return blocks
		}
		s = s[start+len(marker):]
		cmd, rest, ok := strings.Cut(s, "\n")
		end := strings.Index(rest, "```")
		if !ok || end < 0 {
			t.Fatalf("%s: help block %q is not terminated", cliDocPath, cmd)
		}
		blocks = append(blocks, helpBlock{args: strings.Fields(cmd), want: rest[:end]})
		s = rest[end:]
	}
}

// trimTrailingSpace removes trailing blanks from every line. Editors
// commonly add or strip them in Markdown, and they are invisible in the
// rendered page, so they must not decide the comparison.
func trimTrailingSpace(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}

// TestHelpMatchesDocs pins every help text in the CLI reference to the
// program's output, and requires the reference to cover every command.
func TestHelpMatchesDocs(t *testing.T) {
	blocks := docHelpBlocks(t)
	documented := map[string]bool{}
	for _, b := range blocks {
		documented[strings.Join(b.args, " ")] = true
		code, stdout, stderr := run(t, b.args...)
		if code != ExitOK || stderr != "" {
			t.Errorf("mailfire %s: exit %d, stderr %q", strings.Join(b.args, " "), code, stderr)
		}
		if trimTrailingSpace(stdout) != trimTrailingSpace(b.want) {
			t.Errorf("mailfire %s output differs from %s\n--- docs ---\n%s\n--- mailfire ---\n%s",
				strings.Join(b.args, " "), cliDocPath, b.want, stdout)
		}
	}
	for _, cmd := range []string{"--help", "send --help", "run --help", "validate --help", "auth --help", "resend --help",
		"queue --help", "export-templates --help", "version --help"} {
		if !documented[cmd] {
			t.Errorf("%s does not document \"mailfire %s\"", cliDocPath, cmd)
		}
	}
	// The short form prints the same text as the documented long form.
	if code, short, _ := run(t, "-h"); code != ExitOK || short != mustRun(t, "--help") {
		t.Error("mailfire -h differs from mailfire --help")
	}
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	code, stdout, stderr := run(t, args...)
	if code != ExitOK {
		t.Fatalf("mailfire %v: exit %d: %s", args, code, stderr)
	}
	return stdout
}

// TestHelpMatchesDocs ignores trailing blanks, so pin them here: the
// help texts themselves must not carry any, and must end in a newline.
func TestHelpTextsAreClean(t *testing.T) {
	for name, text := range map[string]string{
		"usageText": usageText, "sendUsage": sendUsage, "resendUsage": resendUsage,
		"validateUsage": validateUsage, "versionUsage": versionUsage, "queueUsage": queueUsage,
		"exportTemplatesUsage": exportTemplatesUsage, "authUsage": authUsage, "runUsage": runUsage,
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
		{args: []string{"queue", "--help"}, want: []string{"Usage:\n  mailfire queue list", "queue show [--raw] <id>", "queue delete <id>...", "queue stats", "Flags (queue):", "--raw", "-c, --config <p>"}, wantNot: []string{"--to <addr>", "--force"}},
		{args: []string{"queue", "show", "-h"}, want: []string{"Flags (queue):"}},
		{args: []string{"queue", "delete", "--help"}, want: []string{"Flags (queue):"}},
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
		"--no-defer", "--timeout", "--dedup-key", "--cooldown", "--preview", "--json", "--config", "--help",
	} {
		if !strings.Contains(sendUsage, f+" ") && !strings.Contains(sendUsage, f+"\n") {
			t.Errorf("send help does not document %s", f)
		}
	}
	for name, text := range map[string]string{
		"resend": resendUsage, "queue": queueUsage, "validate": validateUsage,
		"version": versionUsage, "export-templates": exportTemplatesUsage,
	} {
		if !strings.Contains(text, "--json ") {
			t.Errorf("%s help does not document --json", name)
		}
	}
	for _, f := range []string{
		"--to", "--cc", "--bcc", "--from", "--subject", "--name", "--on", "--max-runtime", "--success-codes",
		"--tail", "--tee", "--no-attach-logs", "--locale", "--report-template", "--template", "--template-file",
		"--preheader", "--footer", "--test", "--no-defer", "--timeout", "--dedup-key", "--cooldown",
		"--preview", "--json", "--config", "--help",
	} {
		if !strings.Contains(runUsage, f+" ") && !strings.Contains(runUsage, f+"\n") {
			t.Errorf("run help does not document %s", f)
		}
	}
	for _, sub := range []string{"run", "validate", "resend", "queue", "version"} {
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
		{[]string{"queue", "list", "--bogus"}, "Flags (queue):"},
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
