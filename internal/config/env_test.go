package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/render"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// envTestKey is the API key supplied through the environment in these
// tests; distinct from testAPIKey so a mix-up of sources is visible.
const envTestKey = "TEST-ENV-SECRET-KEY-1a2b3c4d"

// TestMain clears MAILFIRE_* variables so the developer's shell
// environment cannot change which configuration the tests load.
func TestMain(m *testing.M) {
	if err := testutil.ClearMailfireEnv(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// skipIfSystemConfig skips tests that need the config search to come up
// empty when a system-wide config exists on this machine.
func skipIfSystemConfig(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/etc/mailfire/mailfire.conf"); err == nil {
		t.Skip("/etc/mailfire/mailfire.conf exists; cannot test not-found")
	}
}

// TestEnvOverlayPrecedence: every documented variable overrides the
// corresponding file setting, and EnvSource records the variable.
func TestEnvOverlayPrecedence(t *testing.T) {
	isolate(t)
	path := writeConfig(t, t.TempDir(), validTOML)

	t.Setenv("MAILFIRE_MAILGUN_API_KEY", envTestKey)
	t.Setenv("MAILFIRE_MAILGUN_REGION", "us")
	t.Setenv("MAILFIRE_MAILGUN_TIMEOUT", "42s")
	t.Setenv("MAILFIRE_SENDING_DOMAIN", "env.example.com")
	t.Setenv("MAILFIRE_SENDING_FROM", "Env Bot <env@example.com>")
	t.Setenv("MAILFIRE_SENDING_TEMP_DIR", "env-temp")
	t.Setenv("MAILFIRE_LOGGING_LOGFILE", "env.log")
	t.Setenv("MAILFIRE_LOGGING_LOGLEVEL", "ERROR")
	t.Setenv("MAILFIRE_LOGGING_MAX_SIZE", "7M")
	t.Setenv("MAILFIRE_DEFER_DEFER", "true") // file says defer = false
	t.Setenv("MAILFIRE_DEFER_DEFER_DIR", "env-deferred")
	t.Setenv("MAILFIRE_DEFER_MAX_SIZE", "2G")
	t.Setenv("MAILFIRE_DEFER_RESEND_COOLDOWN", "1h")
	t.Setenv("MAILFIRE_DEFER_MAX_AGE", "72h")
	t.Setenv("MAILFIRE_DEDUP_STATE_DIR", "env-dedup")
	t.Setenv("MAILFIRE_DEDUP_COOLDOWN", "15m")

	cfg := loadOK(t, path)
	if cfg.APIKey != envTestKey {
		t.Errorf("APIKey not overridden")
	}
	if cfg.Region != "us" {
		t.Errorf("Region = %q, want us (the file says eu)", cfg.Region)
	}
	if cfg.Timeout != 42*time.Second {
		t.Errorf("Timeout = %v, want 42s", cfg.Timeout)
	}
	if cfg.Domain != "env.example.com" {
		t.Errorf("Domain = %q, want env.example.com", cfg.Domain)
	}
	if cfg.From != "Env Bot <env@example.com>" {
		t.Errorf("From = %q", cfg.From)
	}
	if cfg.TempDir != "env-temp" {
		t.Errorf("TempDir = %q, want env-temp", cfg.TempDir)
	}
	if cfg.Logfile != "env.log" {
		t.Errorf("Logfile = %q, want env.log", cfg.Logfile)
	}
	if cfg.LogLevel != "error" { // lowercased like a file value
		t.Errorf("LogLevel = %q, want error", cfg.LogLevel)
	}
	if cfg.LogMaxSize != 7<<20 {
		t.Errorf("LogMaxSize = %d, want %d", cfg.LogMaxSize, 7<<20)
	}
	if !cfg.Defer {
		t.Error("Defer = false, want true (environment overrides the file's false)")
	}
	if cfg.DeferDir != "env-deferred" {
		t.Errorf("DeferDir = %q, want env-deferred", cfg.DeferDir)
	}
	if cfg.DeferMaxSize != int64(2)<<30 {
		t.Errorf("DeferMaxSize = %d, want %d", cfg.DeferMaxSize, int64(2)<<30)
	}
	if cfg.ResendCooldown != time.Hour {
		t.Errorf("ResendCooldown = %v, want 1h", cfg.ResendCooldown)
	}
	if cfg.DeferMaxAge != 72*time.Hour {
		t.Errorf("DeferMaxAge = %v, want 72h", cfg.DeferMaxAge)
	}
	if cfg.DedupDir != "env-dedup" || cfg.DedupCooldown != 15*time.Minute {
		t.Errorf("DedupDir = %q, DedupCooldown = %v, want env-dedup, 15m", cfg.DedupDir, cfg.DedupCooldown)
	}
	for key, env := range map[string]string{
		"mailgun.api_key":       "MAILFIRE_MAILGUN_API_KEY",
		"mailgun.region":        "MAILFIRE_MAILGUN_REGION",
		"mailgun.timeout":       "MAILFIRE_MAILGUN_TIMEOUT",
		"sending.domain":        "MAILFIRE_SENDING_DOMAIN",
		"sending.from":          "MAILFIRE_SENDING_FROM",
		"sending.temp_dir":      "MAILFIRE_SENDING_TEMP_DIR",
		"logging.logfile":       "MAILFIRE_LOGGING_LOGFILE",
		"logging.loglevel":      "MAILFIRE_LOGGING_LOGLEVEL",
		"logging.max_size":      "MAILFIRE_LOGGING_MAX_SIZE",
		"defer.defer":           "MAILFIRE_DEFER_DEFER",
		"defer.defer_dir":       "MAILFIRE_DEFER_DEFER_DIR",
		"defer.max_size":        "MAILFIRE_DEFER_MAX_SIZE",
		"defer.resend_cooldown": "MAILFIRE_DEFER_RESEND_COOLDOWN",
		"defer.max_age":         "MAILFIRE_DEFER_MAX_AGE",
		"dedup.state_dir":       "MAILFIRE_DEDUP_STATE_DIR",
		"dedup.cooldown":        "MAILFIRE_DEDUP_COOLDOWN",
	} {
		if cfg.EnvSource[key] != env {
			t.Errorf("EnvSource[%q] = %q, want %q", key, cfg.EnvSource[key], env)
		}
	}
	if len(cfg.EnvSource) != 16 {
		t.Errorf("EnvSource has %d entries, want 16: %v", len(cfg.EnvSource), cfg.EnvSource)
	}
}

// TestEnvEmptyIsUnset: an empty or whitespace-only variable never clears
// a file setting or default; it falls back as if unset.
func TestEnvEmptyIsUnset(t *testing.T) {
	isolate(t)
	path := writeConfig(t, t.TempDir(), validTOML)

	t.Setenv("MAILFIRE_MAILGUN_REGION", "")
	t.Setenv("MAILFIRE_MAILGUN_TIMEOUT", "   ")
	t.Setenv("MAILFIRE_DEFER_DEFER", "")
	t.Setenv("MAILFIRE_SENDING_DOMAIN", " \t ")

	cfg := loadOK(t, path)
	if cfg.Region != "eu" || cfg.Domain != "mg.example.com" {
		t.Errorf("file values not preserved: region=%q domain=%q", cfg.Region, cfg.Domain)
	}
	if cfg.Timeout != 15*time.Second {
		t.Errorf("Timeout = %v, want the file's 15s", cfg.Timeout)
	}
	if cfg.Defer {
		t.Error("Defer = true, want the file's false (empty variable is unset)")
	}
	if len(cfg.EnvSource) != 0 {
		t.Errorf("EnvSource = %v, want empty for unset variables", cfg.EnvSource)
	}
}

// TestEnvDeferFalseOverridesFileTrue: the false boolean must override a
// file's defer = true; a false flag value is not "unset".
func TestEnvDeferFalseOverridesFileTrue(t *testing.T) {
	isolate(t)
	path := writeConfig(t, t.TempDir(), minimalTOML) // defer defaults to true
	t.Setenv("MAILFIRE_DEFER_DEFER", "false")
	if cfg := loadOK(t, path); cfg.Defer {
		t.Error("Defer = true, want false from MAILFIRE_DEFER_DEFER")
	}
	t.Setenv("MAILFIRE_DEFER_DEFER", "FALSE") // case-insensitive
	if cfg := loadOK(t, path); cfg.Defer {
		t.Error("Defer = true, want false (FALSE)")
	}
}

func TestEnvDeferInvalidBoolean(t *testing.T) {
	isolate(t)
	path := writeConfig(t, t.TempDir(), minimalTOML)
	for _, v := range []string{"yes", "0", "no", "on"} {
		t.Setenv("MAILFIRE_DEFER_DEFER", v)
		err := loadErr(t, path)
		if !strings.Contains(err, "MAILFIRE_DEFER_DEFER") || !strings.Contains(err, `"true" or "false"`) {
			t.Errorf("MAILFIRE_DEFER_DEFER=%q: error = %q, want the variable named with a hint", v, err)
		}
	}
}

// TestEnvTemplateSelection: the environment's template name/file choice
// replaces the file's selection of either kind; setting both variables
// is an error.
func TestEnvTemplateSelection(t *testing.T) {
	isolate(t)

	t.Run("env name replaces file file", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), minimalTOML+"template_file = \"/tpl/custom.html\"\n")
		t.Setenv("MAILFIRE_SENDING_TEMPLATE", "plain")
		cfg := loadOK(t, path)
		if cfg.Template != "plain" || cfg.TemplateFile != "" {
			t.Errorf("template=%q file=%q, want plain and no file", cfg.Template, cfg.TemplateFile)
		}
	})

	t.Run("env file replaces file name", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), minimalTOML+"template = \"plain\"\n")
		t.Setenv("MAILFIRE_SENDING_TEMPLATE_FILE", "~/tpl/custom.html")
		cfg := loadOK(t, path)
		if cfg.TemplateFile == "" || !strings.HasSuffix(cfg.TemplateFile, filepath.Join("tpl", "custom.html")) {
			t.Errorf("TemplateFile = %q, want the env path (~ expanded)", cfg.TemplateFile)
		}
		if cfg.Template != render.DefaultTemplate {
			t.Errorf("Template = %q, want the default %q", cfg.Template, render.DefaultTemplate)
		}
	})

	t.Run("env name replaces file name", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), minimalTOML+"template = \"plain\"\n")
		t.Setenv("MAILFIRE_SENDING_TEMPLATE", "card-serif")
		if cfg := loadOK(t, path); cfg.Template != "card-serif" {
			t.Errorf("Template = %q, want card-serif", cfg.Template)
		}
	})

	t.Run("both variables set is an error", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), minimalTOML)
		t.Setenv("MAILFIRE_SENDING_TEMPLATE", "plain")
		t.Setenv("MAILFIRE_SENDING_TEMPLATE_FILE", "/tpl/custom.html")
		err := loadErr(t, path)
		if !strings.Contains(err, "MAILFIRE_SENDING_TEMPLATE") || !strings.Contains(err, "mutually exclusive") {
			t.Errorf("error = %q, want both variables named as mutually exclusive", err)
		}
	})

	t.Run("invalid env template names the variable", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), minimalTOML)
		t.Setenv("MAILFIRE_SENDING_TEMPLATE", "gothic")
		err := loadErr(t, path)
		if !strings.Contains(err, "MAILFIRE_SENDING_TEMPLATE") || !strings.Contains(err, `"gothic"`) {
			t.Errorf("error = %q, want MAILFIRE_SENDING_TEMPLATE and the value", err)
		}
	})
}

// TestEnvOnlyLoad: with no config file, the environment alone supplies a
// complete, valid configuration.
func TestEnvOnlyLoad(t *testing.T) {
	skipIfSystemConfig(t)
	isolate(t)

	t.Setenv("MAILFIRE_MAILGUN_API_KEY", envTestKey)
	t.Setenv("MAILFIRE_MAILGUN_REGION", "EU") // normalized to lowercase
	t.Setenv("MAILFIRE_SENDING_DOMAIN", "env.example.com")

	cfg := loadOK(t, "")
	if cfg.Path != "" {
		t.Errorf("Path = %q, want empty for environment-only configuration", cfg.Path)
	}
	if cfg.APIKey != envTestKey || cfg.Region != "eu" || cfg.Domain != "env.example.com" {
		t.Errorf("unexpected values: %+v", cfg)
	}
	if cfg.Template != render.DefaultTemplate || cfg.LogLevel != DefaultLogLevel || !cfg.Defer {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.EnvSource["mailgun.region"] != "MAILFIRE_MAILGUN_REGION" {
		t.Errorf("EnvSource = %v", cfg.EnvSource)
	}
}

// TestEnvOnlyMissingRequired: with some variables set but a required
// setting missing, the error names the config key and the variable that
// could supply it — not a "file not found" error.
func TestEnvOnlyMissingRequired(t *testing.T) {
	skipIfSystemConfig(t)
	isolate(t)
	t.Setenv("MAILFIRE_MAILGUN_API_KEY", envTestKey)
	t.Setenv("MAILFIRE_SENDING_DOMAIN", "env.example.com")

	err := loadErr(t, "")
	for _, want := range []string{"mailgun.region", "MAILFIRE_MAILGUN_REGION"} {
		if !strings.Contains(err, want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

// TestEnvOnlyInvalidValue: an invalid environment value produces an
// actionable error naming the variable, never the API key.
func TestEnvOnlyInvalidValue(t *testing.T) {
	skipIfSystemConfig(t)
	isolate(t)
	t.Setenv("MAILFIRE_MAILGUN_API_KEY", envTestKey)
	t.Setenv("MAILFIRE_MAILGUN_REGION", "uk")
	t.Setenv("MAILFIRE_SENDING_DOMAIN", "env.example.com")

	var stderr strings.Builder
	_, err := Load("", &stderr)
	if err == nil {
		t.Fatal("Load: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "MAILFIRE_MAILGUN_REGION") || !strings.Contains(err.Error(), `"uk"`) {
		t.Errorf("error = %q, want MAILFIRE_MAILGUN_REGION and the value", err)
	}
	if strings.Contains(err.Error(), envTestKey) || strings.Contains(stderr.String(), envTestKey) {
		t.Fatalf("API key leaked; err=%q stderr=%q", err, stderr.String())
	}
}

// TestEnvOnlyTimeout: MAILFIRE_MAILGUN_TIMEOUT feeds the same validation
// as the file setting.
func TestEnvOnlyTimeout(t *testing.T) {
	skipIfSystemConfig(t)
	isolate(t)
	t.Setenv("MAILFIRE_MAILGUN_API_KEY", envTestKey)
	t.Setenv("MAILFIRE_MAILGUN_REGION", "us")
	t.Setenv("MAILFIRE_SENDING_DOMAIN", "env.example.com")

	t.Setenv("MAILFIRE_MAILGUN_TIMEOUT", "90s")
	if cfg := loadOK(t, ""); cfg.Timeout != 90*time.Second {
		t.Errorf("Timeout = %v, want 90s", cfg.Timeout)
	}
	for _, bad := range []string{"0s", "-5s", "soon", "10"} {
		t.Setenv("MAILFIRE_MAILGUN_TIMEOUT", bad)
		err := loadErr(t, "")
		if !strings.Contains(err, "MAILFIRE_MAILGUN_TIMEOUT") || !strings.Contains(err, "invalid duration") {
			t.Errorf("MAILFIRE_MAILGUN_TIMEOUT=%q: error = %q, want the variable named", bad, err)
		}
	}
}

// TestEnvInvalidFileValues: invalid values supplied by the environment
// for size, address, and loglevel name the variable, not a file line.
func TestEnvInvalidFileValues(t *testing.T) {
	isolate(t)
	path := writeConfig(t, t.TempDir(), minimalTOML)
	for _, tc := range []struct {
		env, value, want string
	}{
		{"MAILFIRE_LOGGING_MAX_SIZE", "lots", "invalid size"},
		{"MAILFIRE_DEFER_MAX_SIZE", "-5M", "invalid size"},
		{"MAILFIRE_DEFER_RESEND_COOLDOWN", "soon", "invalid duration"},
		{"MAILFIRE_DEFER_MAX_AGE", "0s", "invalid duration"},
		{"MAILFIRE_SENDING_FROM", "not an address", "invalid address"},
		{"MAILFIRE_LOGGING_LOGLEVEL", "chatty", "invalid value"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			err := loadErr(t, path)
			if !strings.Contains(err, tc.env) || !strings.Contains(err, tc.want) {
				t.Errorf("error = %q, want %s and %q", err, tc.env, tc.want)
			}
			if strings.Contains(err, "line") {
				t.Errorf("error cites a file line for an environment value: %q", err)
			}
		})
	}
}

// TestEnvExplicitPathStillFails: an explicitly requested config file
// that is missing fails clearly, even when the environment could supply
// everything.
func TestEnvExplicitPathStillFails(t *testing.T) {
	isolate(t)
	t.Setenv("MAILFIRE_MAILGUN_API_KEY", envTestKey)
	t.Setenv("MAILFIRE_MAILGUN_REGION", "us")
	t.Setenv("MAILFIRE_SENDING_DOMAIN", "env.example.com")

	missing := filepath.Join(t.TempDir(), "nope.conf")
	err := loadErr(t, missing)
	if !strings.Contains(err, missing) || !strings.Contains(err, "no such file") {
		t.Errorf("error = %q, want the explicit path and 'no such file'", err)
	}
}

// TestNoConfigNoEnvMessage: with neither a file nor environment
// variables, the not-found error stays and hints at the environment
// alternative.
func TestNoConfigNoEnvMessage(t *testing.T) {
	skipIfSystemConfig(t)
	isolate(t)
	err := loadErr(t, "")
	for _, want := range []string{"no configuration file found", ".mailfire.conf", "MAILFIRE_MAILGUN_API_KEY"} {
		if !strings.Contains(err, want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

// TestEnvDoesNotTriggerOnUnknownVariables: variables outside the
// documented set do not enable environment-only mode.
func TestEnvDoesNotTriggerOnUnknownVariables(t *testing.T) {
	skipIfSystemConfig(t)
	isolate(t)
	t.Setenv("MAILFIRE_MAILGUN_APIKEY", envTestKey) // missing underscore: not a binding
	t.Setenv("MAILFIRE_DOMAIN", "env.example.com")
	err := loadErr(t, "")
	if !strings.Contains(err, "no configuration file found") {
		t.Errorf("error = %q, want 'no configuration file found'", err)
	}
}

// TestEnvActive mirrors the gating used by Load and validate.
func TestEnvActive(t *testing.T) {
	if envActive(func(string) string { return "" }) {
		t.Error("envActive = true with nothing set")
	}
	env := map[string]string{"MAILFIRE_DEFER_DEFER": "false"}
	if !envActive(func(k string) string { return env[k] }) {
		t.Error("envActive = false with MAILFIRE_DEFER_DEFER set")
	}
	env = map[string]string{"MAILFIRE_SENDING_DOMAIN": "   "}
	if envActive(func(k string) string { return env[k] }) {
		t.Error("envActive = true with a whitespace-only value (treated as unset)")
	}
}

// TestEnvOnlyPlatforms: environment-only configuration works with the
// Windows and Unix search paths and applies the platform's default
// defer_dir. The environment is injected, so both run on every OS.
func TestEnvOnlyPlatforms(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	vars := map[string]string{
		"MAILFIRE_MAILGUN_API_KEY": envTestKey,
		"MAILFIRE_MAILGUN_REGION":  "us",
		"MAILFIRE_SENDING_DOMAIN":  "env.example.com",
		"HOME":                     filepath.Join(base, "home"),
		"APPDATA":                  filepath.Join(base, "roaming"),
		"PROGRAMDATA":              filepath.Join(base, "program"),
		"LOCALAPPDATA":             filepath.Join(base, "local"),
	}
	getenv := func(k string) string { return vars[k] }

	for _, tc := range []struct{ goos, deferDir string }{
		{"windows", filepath.Join(base, "local", "mailfire")},
		{"linux", filepath.Join(base, "home", ".local", "share", "mailfire")},
		{"darwin", filepath.Join(base, "home", ".local", "share", "mailfire")},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			if tc.goos != "windows" {
				skipIfSystemConfig(t)
			}
			cfg, err := load("", io.Discard, tc.goos, getenv, Overrides{})
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.Path != "" || cfg.APIKey != envTestKey || cfg.Domain != "env.example.com" {
				t.Errorf("unexpected config: path=%q domain=%q", cfg.Path, cfg.Domain)
			}
			if cfg.DeferDir != tc.deferDir {
				t.Errorf("DeferDir = %q, want %q", cfg.DeferDir, tc.deferDir)
			}
		})
	}
}

// TestEnvExplicitInvalidFileStillFails: environment variables never
// paper over an explicitly requested file that does not parse.
func TestEnvExplicitInvalidFileStillFails(t *testing.T) {
	isolate(t)
	t.Setenv("MAILFIRE_MAILGUN_API_KEY", envTestKey)
	t.Setenv("MAILFIRE_MAILGUN_REGION", "us")
	t.Setenv("MAILFIRE_SENDING_DOMAIN", "env.example.com")

	path := writeConfig(t, t.TempDir(), "[mailgun\napi_key = \"x\"\n")
	if err := loadErr(t, path); !strings.Contains(err, path) {
		t.Errorf("error = %q, want the explicit path", err)
	}
}

// TestEnvFillsMissingFileKey: a file lacking a required key is completed
// by the environment; without the variable, the error names both the
// key and the variable.
func TestEnvFillsMissingFileKey(t *testing.T) {
	isolate(t)
	path := writeConfig(t, t.TempDir(), strings.Replace(minimalTOML, "region = \"us\"\n", "", 1))

	err := loadErr(t, path)
	if !strings.Contains(err, "mailgun.region") || !strings.Contains(err, "MAILFIRE_MAILGUN_REGION") {
		t.Errorf("error = %q, want mailgun.region and MAILFIRE_MAILGUN_REGION", err)
	}
	t.Setenv("MAILFIRE_MAILGUN_REGION", "eu")
	if cfg := loadOK(t, path); cfg.Region != "eu" || cfg.Path != path {
		t.Errorf("Region = %q, Path = %q", cfg.Region, cfg.Path)
	}
}

// TestEnvDocumented: every supported variable appears in the
// environment reference and the man page.
func TestEnvDocumented(t *testing.T) {
	for _, doc := range []string{"../../docs/content/reference/environment.md", "../../docs/mailfire.1"} {
		data, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("reading %s: %v", doc, err)
		}
		for _, n := range envNames() {
			if !strings.Contains(string(data), n) {
				t.Errorf("%s does not document %s", doc, n)
			}
		}
	}
}

// TestOverridesDiscardLowerPriority: settings supplied on the command
// line are not validated or attributed from the environment or file, so
// an invalid lower-priority value cannot fail an overriding run.
func TestOverridesDiscardLowerPriority(t *testing.T) {
	isolate(t)
	path := writeConfig(t, t.TempDir(), minimalTOML+"from = \"not an address\"\n")
	t.Setenv("MAILFIRE_SENDING_TEMPLATE", "gothic")
	t.Setenv("MAILFIRE_MAILGUN_TIMEOUT", "soon")
	t.Setenv("MAILFIRE_DEDUP_COOLDOWN", "later")

	// Each invalid value still fails when its setting is not overridden.
	for _, tc := range []struct {
		ov   Overrides
		want string
	}{
		{Overrides{Template: true, Timeout: true, Cooldown: true}, "sending.from"},
		{Overrides{From: true, Timeout: true, Cooldown: true}, "MAILFIRE_SENDING_TEMPLATE"},
		{Overrides{From: true, Template: true, Cooldown: true}, "MAILFIRE_MAILGUN_TIMEOUT"},
		{Overrides{From: true, Template: true, Timeout: true}, "MAILFIRE_DEDUP_COOLDOWN"},
	} {
		if _, err := LoadWith(path, io.Discard, tc.ov); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %s named", tc.ov, err, tc.want)
		}
	}

	cfg, err := LoadWith(path, io.Discard, Overrides{From: true, Template: true, Timeout: true, Cooldown: true})
	if err != nil {
		t.Fatalf("LoadWith: %v", err)
	}
	if cfg.From != "" || cfg.Template != render.DefaultTemplate || cfg.TemplateFile != "" || cfg.Timeout != 0 ||
		cfg.DedupCooldown != DefaultDedupCooldown {
		t.Errorf("overridden settings kept lower-priority values: %+v", cfg)
	}
	if len(cfg.EnvSource) != 0 {
		t.Errorf("EnvSource = %v, want overridden variables not attributed", cfg.EnvSource)
	}
}

// TestOverridesKeepEarlierErrors: overrides never mask an explicit
// file's syntax error or the conflicting template variables.
func TestOverridesKeepEarlierErrors(t *testing.T) {
	isolate(t)
	all := Overrides{From: true, Template: true, Timeout: true}

	bad := writeConfig(t, t.TempDir(), "[mailgun\n")
	if _, err := LoadWith(bad, io.Discard, all); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("syntax error: err = %v, want the path named", err)
	}
	if _, err := LoadWith(filepath.Join(t.TempDir(), "nope.conf"), io.Discard, all); err == nil {
		t.Error("missing explicit file: expected error")
	}

	path := writeConfig(t, t.TempDir(), minimalTOML)
	t.Setenv("MAILFIRE_SENDING_TEMPLATE", "plain")
	t.Setenv("MAILFIRE_SENDING_TEMPLATE_FILE", "/tpl/custom.html")
	if _, err := LoadWith(path, io.Discard, all); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("both template variables: err = %v, want mutually exclusive", err)
	}
}
