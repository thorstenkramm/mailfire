package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const readmePath = "../../README.md"

// readmeConfigExample extracts the ```toml block from README.md, with
// line endings normalized to LF.
func readmeConfigExample(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Skipf("README.md not available (%v); skipping docs test", err)
	}
	const marker = "```toml\n"
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	start := strings.Index(s, marker)
	if start < 0 {
		t.Fatal("README.md has no toml block")
	}
	s = s[start+len(marker):]
	end := strings.Index(s, "```")
	if end < 0 {
		t.Fatal("README.md toml block is not terminated")
	}
	return s[:end]
}

func loadExample(t *testing.T, content string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailfire.conf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, io.Discard)
	if err != nil {
		t.Fatalf("documented config example does not load: %v\n%s", err, content)
	}
	return cfg
}

// TestREADMEConfigExampleLoads makes sure the annotated example in the
// README is valid TOML and passes mailfire's own validation, so users can
// copy it as-is.
func TestREADMEConfigExampleLoads(t *testing.T) {
	cfg := loadExample(t, readmeConfigExample(t))

	if cfg.Region != "eu" || cfg.Domain != "example.com" || cfg.From != "My Name <user@example.com>" {
		t.Errorf("unexpected mailgun/sending values: %+v", cfg)
	}
	if cfg.LogLevel != "debug" || cfg.LogMaxSize != 100<<20 {
		t.Errorf("unexpected logging values: level=%q max=%d", cfg.LogLevel, cfg.LogMaxSize)
	}
	if cfg.Defer || cfg.DeferDir != "/var/tmp/mailfire/deferred" || cfg.DeferMaxSize != 100<<20 {
		t.Errorf("unexpected defer values: %+v", cfg)
	}
	if cfg.ResendCooldown != 5*time.Minute {
		t.Errorf("ResendCooldown = %v, want 5m", cfg.ResendCooldown)
	}
}

// TestREADMEConfigExampleCommentedLogfile uncomments each documented
// logfile line in turn: users will do exactly that, and the line must be
// valid TOML (quoted string) once they do.
func TestREADMEConfigExampleCommentedLogfile(t *testing.T) {
	example := readmeConfigExample(t)
	found := 0
	for _, line := range strings.Split(example, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#logfile") {
			continue
		}
		found++
		uncommented := strings.Replace(example, line, strings.TrimPrefix(trimmed, "#"), 1)
		cfg := loadExample(t, uncommented)
		if cfg.Logfile == "" {
			t.Errorf("uncommenting %q did not set the logfile", trimmed)
		}
	}
	if found == 0 {
		t.Error("README example documents no #logfile line")
	}
}

// TestManPageConfigExampleLoads keeps the example in docs/mailfire.1
// valid: it is a separate copy and would otherwise drift silently. The
// example uses no roff escapes, so the .nf block is the TOML verbatim.
func TestManPageConfigExampleLoads(t *testing.T) {
	data, err := os.ReadFile("../../docs/mailfire.1")
	if err != nil {
		t.Skipf("docs/mailfire.1 not available (%v); skipping docs test", err)
	}
	const marker = ".SS Example\n.nf\n"
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	start := strings.Index(s, marker)
	if start < 0 {
		t.Fatalf("man page has no %q block", marker)
	}
	s = s[start+len(marker):]
	end := strings.Index(s, "\n.fi\n")
	if end < 0 {
		t.Fatal("man page example is not terminated by .fi")
	}
	example := s[:end+1]
	if strings.Contains(example, `\`) {
		t.Fatalf("man page example contains roff escapes; extend this test to unescape them:\n%s", example)
	}
	cfg := loadExample(t, example)
	if cfg.Region != "eu" || cfg.Domain != "example.com" || cfg.Logfile == "" || !cfg.Defer {
		t.Errorf("unexpected values from man page example: %+v", cfg)
	}
}

func TestREADMEHasNoLeftoverMarkers(t *testing.T) {
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Skipf("README.md not available (%v)", err)
	}
	for _, marker := range []string{"TODO", "FIXME", "FILL-IN", "FILL IN", "XXX", "TBD"} {
		if strings.Contains(string(data), marker) {
			t.Errorf("README.md contains leftover marker %q", marker)
		}
	}
}
