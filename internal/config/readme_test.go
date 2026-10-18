package config

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/render"
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

// TestREADMEConfigExampleLoads makes sure the minimal quick-start
// configuration in the README is valid TOML and passes mailfire's own
// validation, so users can copy it as-is. Everything beyond the shown
// keys must stay at its documented default.
func TestREADMEConfigExampleLoads(t *testing.T) {
	isolate(t)
	cfg := loadExample(t, readmeConfigExample(t))

	if cfg.Region != "eu" || cfg.Domain != "example.com" || cfg.From != "My Name <user@example.com>" {
		t.Errorf("unexpected mailgun/sending values: %+v", cfg)
	}
	if cfg.Logfile != "" || cfg.LogLevel != DefaultLogLevel || cfg.LogMaxSize != DefaultMaxSize {
		t.Errorf("logging defaults not preserved: logfile=%q level=%q max=%d",
			cfg.Logfile, cfg.LogLevel, cfg.LogMaxSize)
	}
	if !cfg.Defer || cfg.DeferDir == "" || cfg.DeferMaxSize != DefaultMaxSize || cfg.ResendCooldown != DefaultCooldown {
		t.Errorf("defer defaults not preserved: %+v", cfg)
	}
	if cfg.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0 (mailgun client default preserved)", cfg.Timeout)
	}
}

// exampleConf reads the annotated mailfire.example.conf at the repo root,
// with line endings normalized to LF.
func exampleConf(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../mailfire.example.conf")
	if err != nil {
		t.Skipf("mailfire.example.conf not available (%v); skipping docs test", err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

// TestExampleConfLoads: the annotated example is valid TOML, passes
// validation, and — with all optional settings commented out — keeps the
// documented defaults.
func TestExampleConfLoads(t *testing.T) {
	isolate(t)
	cfg := loadExample(t, exampleConf(t))

	if cfg.APIKey != "<your-api-key>" || cfg.Region != "eu" || cfg.Domain != "example.com" {
		t.Errorf("unexpected required values: %+v", cfg)
	}
	if cfg.From != "My Name <user@example.com>" {
		t.Errorf("From = %q", cfg.From)
	}
	if cfg.Template != render.DefaultTemplate || cfg.TemplateFile != "" {
		t.Errorf("template defaults not preserved: template=%q file=%q", cfg.Template, cfg.TemplateFile)
	}
	if cfg.Logfile != "" || cfg.LogLevel != DefaultLogLevel || cfg.LogMaxSize != DefaultMaxSize {
		t.Errorf("logging defaults not preserved: logfile=%q level=%q max=%d",
			cfg.Logfile, cfg.LogLevel, cfg.LogMaxSize)
	}
	if !cfg.Defer || cfg.DeferDir == "" || cfg.DeferMaxSize != DefaultMaxSize || cfg.ResendCooldown != DefaultCooldown {
		t.Errorf("defer defaults not preserved: %+v", cfg)
	}
	if cfg.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0 (mailgun client default preserved)", cfg.Timeout)
	}
}

// reCommentedSetting matches the example's commented-out settings
// ("# key = value"), not prose comments.
var reCommentedSetting = regexp.MustCompile(`^# [a-z_]+ = `)

// TestExampleConfCommentedSettingsLoad: users uncomment the documented
// optional settings, so every commented "# key = value" line in the
// example must be valid TOML accepted by the loader once uncommented.
func TestExampleConfCommentedSettingsLoad(t *testing.T) {
	isolate(t)
	example := exampleConf(t)
	seen := map[string]bool{}
	for _, line := range strings.Split(example, "\n") {
		trimmed := strings.TrimSpace(line)
		if !reCommentedSetting.MatchString(trimmed) || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		uncommented := strings.Replace(example, line, strings.TrimPrefix(trimmed, "# "), 1)
		if cfg := loadExample(t, uncommented); cfg == nil {
			t.Errorf("uncommenting %q did not load", trimmed)
		}
	}
	if len(seen) == 0 {
		t.Error("mailfire.example.conf documents no commented-out settings")
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
