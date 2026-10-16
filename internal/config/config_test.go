package config

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testAPIKey = "TEST-SECRET-API-KEY-9f8e7d6c"

// validTOML is a complete, valid configuration using the test API key.
const validTOML = `
[mailgun]
api_key = "` + testAPIKey + `"
region = "eu"

[sending]
domain = "mg.example.com"
from = "Ops Bot <ops@example.com>"

[logging]
logfile = "~/logs/mailfire.log"
loglevel = "debug"
max_size = "100M"

[defer]
defer = false
defer_dir = "~/deferred"
max_size = "1G"
resend_cooldown = "10m"
`

// minimalTOML contains only the mandatory keys.
const minimalTOML = `
[mailgun]
api_key = "` + testAPIKey + `"
region = "us"

[sending]
domain = "mg.example.com"
`

// writeConfig writes content to <dir>/mailfire.conf with mode 0600 and
// returns the path.
func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "mailfire.conf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

func loadOK(t *testing.T, flagPath string) *Config {
	t.Helper()
	var stderr bytes.Buffer
	cfg, err := Load(flagPath, &stderr)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if strings.Contains(stderr.String(), testAPIKey) {
		t.Fatalf("stderr leaks API key: %q", stderr.String())
	}
	return cfg
}

func loadErr(t *testing.T, flagPath string) string {
	t.Helper()
	var stderr bytes.Buffer
	_, err := Load(flagPath, &stderr)
	if err == nil {
		t.Fatal("Load: expected error, got nil")
	}
	if strings.Contains(err.Error(), testAPIKey) || strings.Contains(stderr.String(), testAPIKey) {
		t.Fatalf("API key leaked; err=%q stderr=%q", err, stderr.String())
	}
	return err.Error()
}

// isolate points HOME at an empty temp dir and chdirs into another empty
// temp dir, so search-order tests are hermetic.
func isolate(t *testing.T) (home, cwd string) {
	t.Helper()
	home = t.TempDir()
	cwd = t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(cwd)
	return home, cwd
}

func TestLoadExplicitPath(t *testing.T) {
	path := writeConfig(t, t.TempDir(), validTOML)
	cfg := loadOK(t, path)

	if cfg.Path != path {
		t.Errorf("Path = %q, want %q", cfg.Path, path)
	}
	if cfg.APIKey != testAPIKey {
		t.Errorf("APIKey not loaded")
	}
	if cfg.Region != "eu" {
		t.Errorf("Region = %q, want %q", cfg.Region, "eu")
	}
	if cfg.Domain != "mg.example.com" {
		t.Errorf("Domain = %q, want %q", cfg.Domain, "mg.example.com")
	}
	if cfg.From != "Ops Bot <ops@example.com>" {
		t.Errorf("From = %q", cfg.From)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "logs", "mailfire.log"); cfg.Logfile != want {
		t.Errorf("Logfile = %q, want %q (~ not expanded)", cfg.Logfile, want)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
	if cfg.LogMaxSize != 100<<20 {
		t.Errorf("LogMaxSize = %d, want %d", cfg.LogMaxSize, 100<<20)
	}
	if cfg.Defer {
		t.Error("Defer = true, want false")
	}
	if want := filepath.Join(home, "deferred"); cfg.DeferDir != want {
		t.Errorf("DeferDir = %q, want %q", cfg.DeferDir, want)
	}
	if cfg.DeferMaxSize != 1<<30 {
		t.Errorf("DeferMaxSize = %d, want %d", cfg.DeferMaxSize, 1<<30)
	}
	if cfg.ResendCooldown != 10*time.Minute {
		t.Errorf("ResendCooldown = %v, want 10m", cfg.ResendCooldown)
	}
}

func TestLoadDefaults(t *testing.T) {
	home, _ := isolate(t)
	path := writeConfig(t, t.TempDir(), minimalTOML)
	cfg := loadOK(t, path)

	if cfg.Logfile != "" {
		t.Errorf("Logfile = %q, want empty", cfg.Logfile)
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, DefaultLogLevel)
	}
	if cfg.LogMaxSize != DefaultMaxSize {
		t.Errorf("LogMaxSize = %d, want %d", cfg.LogMaxSize, DefaultMaxSize)
	}
	if !cfg.Defer {
		t.Error("Defer = false, want true (default)")
	}
	if want := filepath.Join(home, ".local", "share", "mailfire"); cfg.DeferDir != want {
		t.Errorf("DeferDir = %q, want %q", cfg.DeferDir, want)
	}
	if cfg.DeferMaxSize != DefaultMaxSize {
		t.Errorf("DeferMaxSize = %d, want %d", cfg.DeferMaxSize, DefaultMaxSize)
	}
	if cfg.ResendCooldown != DefaultCooldown {
		t.Errorf("ResendCooldown = %v, want %v", cfg.ResendCooldown, DefaultCooldown)
	}
	if cfg.From != "" {
		t.Errorf("From = %q, want empty", cfg.From)
	}
}

func TestLoadSearchOrder(t *testing.T) {
	home, cwd := isolate(t)

	domain := func(cfg *Config) string { return cfg.Domain }
	mk := func(dir, name, domain string) string {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		path := filepath.Join(dir, name)
		content := strings.ReplaceAll(minimalTOML, "mg.example.com", domain)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("writing config: %v", err)
		}
		return path
	}
	load := func(t *testing.T) *Config {
		t.Helper()
		return loadOK(t, "")
	}

	t.Run("current directory wins over home", func(t *testing.T) {
		cwdPath := mk(cwd, ".mailfire.conf", "cwd.example.com")
		defer func() {
			if err := os.Remove(cwdPath); err != nil {
				t.Error(err)
			}
		}()
		mk(filepath.Join(home, ".config", "mailfire"), "mailfire.conf", "home.example.com")
		if got := domain(load(t)); got != "cwd.example.com" {
			t.Errorf("domain = %q, want config from current directory", got)
		}
	})

	t.Run("home config used when no cwd config", func(t *testing.T) {
		if got := domain(load(t)); got != "home.example.com" {
			t.Errorf("domain = %q, want config from home directory", got)
		}
	})
}

func TestLoadNoConfigFound(t *testing.T) {
	isolate(t)
	// /etc/mailfire/mailfire.conf must not exist on this machine for the
	// search to come up empty.
	if _, err := os.Stat("/etc/mailfire/mailfire.conf"); err == nil {
		t.Skip("/etc/mailfire/mailfire.conf exists; cannot test not-found")
	}
	err := loadErr(t, "")
	if !strings.Contains(err, "no configuration file found") {
		t.Errorf("error = %q, want 'no configuration file found'", err)
	}
	if !strings.Contains(err, ".mailfire.conf") {
		t.Errorf("error should list searched paths: %q", err)
	}
}

func TestLoadExplicitPathMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.conf")
	err := loadErr(t, missing)
	if !strings.Contains(err, missing) || !strings.Contains(err, "no such file") {
		t.Errorf("error = %q, want file name and 'no such file'", err)
	}
}

func TestLoadExplicitPathOverridesSearch(t *testing.T) {
	_, cwd := isolate(t)
	// A discoverable config exists in the cwd, but -c must win.
	cwdCfg := filepath.Join(cwd, ".mailfire.conf")
	content := strings.ReplaceAll(minimalTOML, "mg.example.com", "cwd.example.com")
	if err := os.WriteFile(cwdCfg, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	explicit := writeConfig(t, t.TempDir(), minimalTOML)
	cfg := loadOK(t, explicit)
	if cfg.Path != explicit || cfg.Domain != "mg.example.com" {
		t.Errorf("explicit path did not win: Path=%q Domain=%q", cfg.Path, cfg.Domain)
	}
}

func TestLoadInvalidTOML(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "[mailgun\napi_key = broken")
	err := loadErr(t, path)
	if !strings.Contains(err, path) {
		t.Errorf("error missing file name: %q", err)
	}
	if !strings.Contains(err, "line") {
		t.Errorf("error missing line number: %q", err)
	}
}

func TestLoadValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		toml    string
		wantKey string
	}{
		{"missing api_key", "[mailgun]\nregion = \"us\"\n[sending]\ndomain = \"d.example.com\"\n", "mailgun.api_key"},
		{"empty api_key", "[mailgun]\napi_key = \"  \"\nregion = \"us\"\n[sending]\ndomain = \"d.example.com\"\n", "mailgun.api_key"},
		{"missing region", "[mailgun]\napi_key = \"k\"\n[sending]\ndomain = \"d.example.com\"\n", "mailgun.region"},
		{"invalid region", "[mailgun]\napi_key = \"k\"\nregion = \"uk\"\n[sending]\ndomain = \"d.example.com\"\n", "mailgun.region"},
		{"missing domain", "[mailgun]\napi_key = \"k\"\nregion = \"us\"\n[sending]\n", "sending.domain"},
		{"empty domain", "[mailgun]\napi_key = \"k\"\nregion = \"us\"\n[sending]\ndomain = \"\"\n", "sending.domain"},
		{"invalid from", minimalTOML + "from = \"not an address\"\n", "sending.from"},
		{"invalid loglevel", minimalTOML + "\n[logging]\nloglevel = \"chatty\"\n", "logging.loglevel"},
		{"invalid log max_size", minimalTOML + "\n[logging]\nmax_size = \"lots\"\n", "logging.max_size"},
		{"invalid defer max_size", minimalTOML + "\n[defer]\nmax_size = \"-5M\"\n", "defer.max_size"},
		{"invalid resend_cooldown", minimalTOML + "\n[defer]\nresend_cooldown = \"soon\"\n", "defer.resend_cooldown"},
		{"zero resend_cooldown", minimalTOML + "\n[defer]\nresend_cooldown = \"0s\"\n", "defer.resend_cooldown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, t.TempDir(), tc.toml)
			err := loadErr(t, path)
			if !strings.Contains(err, path) {
				t.Errorf("error missing file name: %q", err)
			}
			if !strings.Contains(err, tc.wantKey) {
				t.Errorf("error missing key %q: %q", tc.wantKey, err)
			}
		})
	}
}

func TestLoadFromAddressFormats(t *testing.T) {
	for _, from := range []string{"user@example.com", "My Name <user@example.com>"} {
		path := writeConfig(t, t.TempDir(), minimalTOML+"from = \""+from+"\"\n")
		cfg := loadOK(t, path)
		if cfg.From != from {
			t.Errorf("From = %q, want %q", cfg.From, from)
		}
	}
}

func TestAPIKeyNeverInErrors(t *testing.T) {
	cases := map[string]string{
		"invalid TOML":       "[mailgun]\napi_key = \"" + testAPIKey + "\"\nregion = !!!\n",
		"invalid region":     "[mailgun]\napi_key = \"" + testAPIKey + "\"\nregion = \"uk\"\n[sending]\ndomain = \"d.com\"\n",
		"empty api_key":      "[mailgun]\napi_key = \"\"\nregion = \"us\"\n[sending]\ndomain = \"d.com\"\n",
		"api_key wrong type": "[mailgun]\napi_key = 12345\nregion = \"us\"\n[sending]\ndomain = \"d.com\"\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, t.TempDir(), content)
			loadErr(t, path) // loadErr asserts no key in error and stderr
		})
	}
}

func TestPermissionWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission check is Unix-only")
	}
	for _, tc := range []struct {
		mode     os.FileMode
		wantWarn bool
	}{
		{0o600, false},
		{0o400, false},
		{0o644, true},
		{0o640, true},
		{0o666, true},
	} {
		dir := t.TempDir()
		path := writeConfig(t, dir, minimalTOML)
		if err := os.Chmod(path, tc.mode); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		if _, err := Load(path, &stderr); err != nil {
			t.Fatalf("Load(%04o): %v", tc.mode, err)
		}
		gotWarn := strings.Contains(stderr.String(), "permissions")
		if gotWarn != tc.wantWarn {
			t.Errorf("mode %04o: warning = %v, want %v (stderr=%q)", tc.mode, gotWarn, tc.wantWarn, stderr.String())
		}
		if strings.Contains(stderr.String(), testAPIKey) {
			t.Errorf("warning leaks API key: %q", stderr.String())
		}
	}
}

func TestPermissionWarningSkippedOnWindows(t *testing.T) {
	var stderr bytes.Buffer
	warnIfPermissive("windows", "whatever", &stderr)
	if stderr.Len() != 0 {
		t.Errorf("expected no warning on windows, got %q", stderr.String())
	}
}

func TestSearchPaths(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	unix := searchPaths("linux", env(map[string]string{"HOME": "/home/u"}))
	want := []string{".mailfire.conf", "/home/u/.config/mailfire/mailfire.conf", "/etc/mailfire/mailfire.conf"}
	if strings.Join(unix, "|") != strings.Join(want, "|") {
		t.Errorf("unix searchPaths = %v, want %v", unix, want)
	}

	win := searchPaths("windows", env(map[string]string{"APPDATA": "/roaming", "PROGRAMDATA": "/prog"}))
	wantWin := []string{".mailfire.conf", "/roaming/mailfire/mailfire.conf", "/prog/mailfire/mailfire.conf"}
	if strings.Join(win, "|") != strings.Join(wantWin, "|") {
		t.Errorf("windows searchPaths = %v, want %v", win, wantWin)
	}
}

func TestDefaultDeferDir(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	if got := defaultDeferDir("darwin", env(map[string]string{"HOME": "/home/u"})); got != "/home/u/.local/share/mailfire" {
		t.Errorf("unix defaultDeferDir = %q", got)
	}
	if got := defaultDeferDir("windows", env(map[string]string{"LOCALAPPDATA": "/local"})); got != "/local/mailfire" {
		t.Errorf("windows defaultDeferDir = %q", got)
	}
	if got := defaultDeferDir("windows", env(map[string]string{"APPDATA": "/roaming"})); got != "/roaming/mailfire" {
		t.Errorf("windows fallback defaultDeferDir = %q", got)
	}
}

func TestExpandPath(t *testing.T) {
	env := func(k string) string {
		if k == "HOME" {
			return "/home/u"
		}
		return ""
	}
	for _, tc := range []struct{ in, want string }{
		{"~/logs/mailfire.log", "/home/u/logs/mailfire.log"},
		{"~", "/home/u"},
		{`~\docs`, `/home/u\docs`},
		{"/abs/path", "/abs/path"},
		{"rel/path", "rel/path"},
		{"~other/x", "~other/x"}, // only own home is expanded
		{"", ""},
	} {
		if got := expandPath(tc.in, env); got != tc.want {
			t.Errorf("expandPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// No home available: unchanged.
	if got := expandPath("~/x", func(string) string { return "" }); got != "~/x" {
		t.Errorf("expandPath without home = %q, want unchanged", got)
	}

	// The exported wrapper uses the process environment.
	t.Setenv("HOME", "/home/env")
	if got := ExpandPath("~/body.txt"); got != "/home/env/body.txt" {
		t.Errorf("ExpandPath = %q, want %q", got, "/home/env/body.txt")
	}
}

func TestParseSize(t *testing.T) {
	valid := map[string]int64{
		"1":     1,
		"1024":  1024,
		"512K":  512 << 10,
		"512KB": 512 << 10,
		"50M":   50 << 20,
		"50m":   50 << 20,
		"100MB": 100 << 20,
		"1G":    1 << 30,
		"2GB":   2 << 30,
		"42B":   42,
		"10 M":  10 << 20,
	}
	for in, want := range valid {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d, nil", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "10T", "-5M", "0", "0M", "1.5G", "M"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q): want error, got nil", in)
		}
	}
}

func TestParseErrorWithholdsAPIKeyDetails(t *testing.T) {
	// go-toml quotes single characters of malformed values; none of these
	// may reach the error for the api_key line.
	cases := map[string]string{
		"unquoted":          "[mailgun]\napi_key = " + testAPIKey + "\n",
		"invalid escape":    "[mailgun]\napi_key = \"TEST\\qSECRET\"\n",
		"trailing junk":     "[mailgun]\napi_key = \"" + testAPIKey + "\" junk\n",
		"unterminated":      "[mailgun]\napi_key = \"" + testAPIKey + "\n",
		"multi-line string": "[mailgun]\napi_key = \"\"\"\nTEST\\qSECRET\"\"\"\n",
		"dotted key":        "mailgun.api_key = " + testAPIKey + "\n",
		"inline table":      "mailgun = { api_key = \"TEST\\qSECRET\", region = \"us\" }\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, t.TempDir(), content)
			err := loadErr(t, path)
			if !strings.Contains(err, "mailgun.api_key") || !strings.Contains(err, "withheld") {
				t.Errorf("error should name key and withhold details: %q", err)
			}
			if strings.Contains(err, "U+") || strings.Contains(err, "'q'") {
				t.Errorf("error leaks a character of the value: %q", err)
			}
			if !strings.Contains(err, "line ") {
				t.Errorf("error missing line number: %q", err)
			}
		})
	}
}

func TestParseErrorMessages(t *testing.T) {
	t.Run("unquoted string hints at quoting", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), "[mailgun]\napi_key = \"k\"\nregion = eu\n")
		err := loadErr(t, path)
		if !strings.Contains(err, "line 3") || !strings.Contains(err, "must be quoted") {
			t.Errorf("error = %q, want line 3 and quoting hint", err)
		}
	})
	t.Run("type mismatch names key and types", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), minimalTOML+"\n[defer]\ndefer = \"no\"\n")
		err := loadErr(t, path)
		for _, want := range []string{"defer.defer", "wrong value type", "got TOML string", "want bool"} {
			if !strings.Contains(err, want) {
				t.Errorf("error = %q, missing %q", err, want)
			}
		}
		if strings.Contains(err, "struct") {
			t.Errorf("error exposes Go internals: %q", err)
		}
	})
}

func TestValidationErrorLineNumbers(t *testing.T) {
	content := "[mailgun]\napi_key = \"k\"\n\n  region = \"uk\"  # wrong\n[sending]\ndomain = \"d.example.com\"\n"
	path := writeConfig(t, t.TempDir(), content)
	err := loadErr(t, path)
	if !strings.Contains(err, "line 4: mailgun.region") {
		t.Errorf("error = %q, want line 4 and key", err)
	}
	// A missing key has no line.
	path = writeConfig(t, t.TempDir(), "[mailgun]\nregion = \"us\"\n")
	if err := loadErr(t, path); strings.Contains(err, "line") {
		t.Errorf("missing key should not report a line: %q", err)
	}
}

func TestKeyLine(t *testing.T) {
	data := []byte("# api_key = \"x\"\nmailgun.region = \"us\"\n[ sending ] # c\n\"from\" = \"a = b\"\n[defer]\nmax_size = \"1M\"\n")
	for _, tc := range []struct {
		section, key string
		want         int
	}{
		{"mailgun", "api_key", 0},
		{"mailgun", "region", 2},
		{"sending", "from", 4},
		{"defer", "max_size", 6},
		{"logging", "max_size", 0},
	} {
		if got := keyLine(data, tc.section, tc.key); got != tc.want {
			t.Errorf("keyLine(%s.%s) = %d, want %d", tc.section, tc.key, got, tc.want)
		}
	}
}

func TestLoadEmptyFile(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "")
	if err := loadErr(t, path); !strings.Contains(err, "mailgun.api_key") {
		t.Errorf("error = %q, want missing api_key", err)
	}
}

func TestLoadIgnoresUnknownKeys(t *testing.T) {
	path := writeConfig(t, t.TempDir(), minimalTOML+"color = \"blue\"\n[extra]\nx = 1\n")
	loadOK(t, path)
}

func TestLoadNormalizesLogLevel(t *testing.T) {
	path := writeConfig(t, t.TempDir(), minimalTOML+"\n[logging]\nloglevel = \" Debug \"\n")
	if cfg := loadOK(t, path); cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

func TestLoadExplicitPathIsDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := loadErr(t, dir); !strings.Contains(err, "not a regular file") {
		t.Errorf("error = %q, want 'not a regular file'", err)
	}
}

func TestLoadNoHomeDeferDir(t *testing.T) {
	path := writeConfig(t, t.TempDir(), minimalTOML)
	noEnv := func(string) string { return "" }
	_, err := load(path, nil, "linux", noEnv)
	if err == nil || !strings.Contains(err.Error(), "defer.defer_dir") {
		t.Errorf("err = %v, want defer.defer_dir error", err)
	}
	// With defer disabled the missing default does not matter.
	path = writeConfig(t, t.TempDir(), minimalTOML+"\n[defer]\ndefer = false\n")
	if _, err := load(path, nil, "linux", noEnv); err != nil {
		t.Errorf("defer = false: unexpected error %v", err)
	}
}

func TestSearchPathsSkipUnsetEnv(t *testing.T) {
	noEnv := func(string) string { return "" }
	if got := searchPaths("linux", noEnv); strings.Join(got, "|") != ".mailfire.conf|/etc/mailfire/mailfire.conf" {
		t.Errorf("unix searchPaths without HOME = %v", got)
	}
	if got := searchPaths("windows", noEnv); strings.Join(got, "|") != ".mailfire.conf" {
		t.Errorf("windows searchPaths without env = %v", got)
	}
	if got := defaultDeferDir("linux", noEnv); got != "" {
		t.Errorf("unix defaultDeferDir without HOME = %q, want empty", got)
	}
	if got := defaultDeferDir("windows", noEnv); got != "" {
		t.Errorf("windows defaultDeferDir without env = %q, want empty", got)
	}
}

func TestParseSizeOverflow(t *testing.T) {
	if _, err := parseSize("9999999999999G"); err == nil {
		t.Error("parseSize overflow: want error, got nil")
	}
}
