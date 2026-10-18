package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTemplate(t *testing.T) {
	isolate(t)
	for _, tc := range []struct{ line, want string }{
		{"", "card"},
		{`template = ""` + "\n", "card"},
		{`template = "plain-serif"` + "\n", "plain-serif"},
		{`template = " Card-Serif "` + "\n", "card-serif"},
	} {
		path := writeConfig(t, t.TempDir(), minimalTOML+tc.line)
		if got := loadOK(t, path).Template; got != tc.want {
			t.Errorf("%q: Template = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestLoadTemplateInvalid(t *testing.T) {
	isolate(t)
	path := writeConfig(t, t.TempDir(), minimalTOML+`template = "base"`+"\n")
	msg := loadErr(t, path)
	want := `line 8: sending.template: invalid value "base": must be one of "card", "card-serif", "plain", or "plain-serif"`
	if !strings.Contains(msg, want) {
		t.Errorf("error = %q, want it to contain %q", msg, want)
	}
}

func TestLoadTemplateFile(t *testing.T) {
	isolate(t)
	path := writeConfig(t, t.TempDir(), minimalTOML+"template_file = \"./Custom.html\"\n")
	if got := loadOK(t, path).TemplateFile; got != "./Custom.html" {
		t.Errorf("TemplateFile = %q", got)
	}
	path = writeConfig(t, t.TempDir(), minimalTOML+"template = \"card\"\ntemplate_file = \"custom.html\"\n")
	if msg := loadErr(t, path); !strings.Contains(msg, "mutually exclusive") {
		t.Errorf("error = %s", msg)
	}
}

// TestLoadRender: the render-only loader needs no config file and no
// Mailgun settings, but still validates the template selection.
func TestLoadRender(t *testing.T) {
	_, cwd := isolate(t)
	var stderr strings.Builder
	cfg, err := LoadRender("", &stderr, Overrides{})
	if err != nil || cfg.Path != "" || cfg.Template != "card" || cfg.TemplateFile != "" {
		t.Fatalf("no config: cfg = %+v, err = %v", cfg, err)
	}

	writeConfig(t, cwd, "[sending]\ntemplate_file = \"custom.html\"\n[logging]\nloglevel = \"bogus\"\n")
	if err := os.Rename(filepath.Join(cwd, "mailfire.conf"), filepath.Join(cwd, ".mailfire.conf")); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadRender("", &stderr, Overrides{})
	if err != nil || cfg.TemplateFile != "custom.html" || !strings.HasSuffix(cfg.Path, ".mailfire.conf") {
		t.Fatalf("discovered config: cfg = %+v, err = %v", cfg, err)
	}
	if cfg, err = LoadRender("", &stderr, Overrides{Template: true}); err != nil || cfg.TemplateFile != "" {
		t.Fatalf("override: cfg = %+v, err = %v", cfg, err)
	}

	t.Setenv("MAILFIRE_SENDING_TEMPLATE", "plain")
	// Variables unrelated to rendering are neither applied nor validated.
	t.Setenv("MAILFIRE_DEFER_DEFER", "maybe")
	t.Setenv("MAILFIRE_LOGGING_LOGLEVEL", "loud")
	if cfg, err = LoadRender("", &stderr, Overrides{}); err != nil || cfg.Template != "plain" || cfg.TemplateFile != "" {
		t.Fatalf("environment: cfg = %+v, err = %v", cfg, err)
	}
	if len(cfg.EnvSource) != 1 || cfg.EnvSource["sending.template"] != "MAILFIRE_SENDING_TEMPLATE" {
		t.Errorf("EnvSource = %v", cfg.EnvSource)
	}
	if _, err := Load("", &stderr); err == nil || !strings.Contains(err.Error(), "MAILFIRE_DEFER_DEFER") {
		t.Errorf("Load still validates MAILFIRE_DEFER_DEFER: %v", err)
	}

	t.Setenv("MAILFIRE_SENDING_TEMPLATE", "")
	bad := writeConfig(t, t.TempDir(), "[sending]\ntemplate = \"base\"\n")
	if _, err := LoadRender(bad, &stderr, Overrides{}); err == nil || !strings.Contains(err.Error(), "line 2: sending.template") {
		t.Errorf("invalid template: err = %v", err)
	}
	if _, err := LoadRender(filepath.Join(cwd, "missing.conf"), &stderr, Overrides{}); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("missing -c: err = %v", err)
	}
}
