// Package config loads and validates mailfire's TOML configuration from
// the platform-specific search locations, with an explicit -c/--config
// override. MAILFIRE_* environment variables override file settings and
// can replace the file entirely when they supply all required settings
// (see env.go and docs/content/reference/environment.md).
//
// The API key is a secret: it is never included in error messages, and the
// loader warns on stderr when the config file is readable by group/others
// (Unix only).
package config

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/thorstenkramm/mailfire/internal/dedup"
	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/mailbox"
	"github.com/thorstenkramm/mailfire/internal/render"
)

// Defaults per PRD FR-2.
const (
	DefaultLogLevel       = "information"
	DefaultMaxSize  int64 = 50 * 1024 * 1024 // 50M
	DefaultCooldown       = 5 * time.Minute
	// DefaultDedupCooldown is the notification-deduplication window
	// (task 24).
	DefaultDedupCooldown = dedup.DefaultCooldown
)

// Config is the validated, normalized mailfire configuration. Sizes are in
// bytes; paths have ~ expanded.
type Config struct {
	// Path is the file the configuration was loaded from, or "" for
	// environment-only configuration.
	Path string

	// EnvSource maps each config key whose value came from the
	// environment to the MAILFIRE_* variable that supplied it. It lets
	// validate explain where settings came from without exposing
	// secrets. File settings and defaults are not listed.
	EnvSource map[string]string

	// [mailgun] (required only when sending.provider is "mailgun")
	APIKey           string        // required; never log or include in errors
	Region           string        // "us" or "eu"
	Timeout          time.Duration // legacy Mailgun-specific timeout; zero uses provider default
	GmailCredentials string        // owner-only OAuth token file; empty uses platform default
	GmailTimeout     time.Duration

	// [smtp] (required only when sending.provider is "smtp")
	SMTPHost     string
	SMTPPort     int
	SMTPSecurity string        // "tls" (implicit TLS), "starttls", or "none"
	SMTPUsername string        // optional; must be paired with SMTPPassword
	SMTPPassword string        // optional; never log or include in errors
	SMTPTimeout  time.Duration // provider-specific timeout; zero uses provider default

	// [sending]
	Domain       string // required
	Provider     string // "mailgun" (default) or "smtp"
	From         string // optional; valid mailbox ("user@example.com" or "Name <user@example.com>")
	Template     string // email template for --md and --html; default render.DefaultTemplate
	TemplateFile string // custom template path; mutually exclusive with Template in TOML

	// [logging]
	Logfile    string // optional; logging disabled when empty
	LogLevel   string // "error", "information" (default), or "debug"
	LogMaxSize int64  // default 50M

	// [defer]
	Defer          bool          // default true
	DeferDir       string        // default per-platform data dir
	DeferMaxSize   int64         // default 50M
	ResendCooldown time.Duration // default 5m; base of the resend backoff
	DeferMaxAge    time.Duration // optional; zero (default) keeps messages until sent or deleted

	// [dedup]
	DedupDir      string        // notification-deduplication state; default per-platform data dir + "dedup"
	DedupCooldown time.Duration // default 1h; suppression window after a delivered notification
}

// rawConfig mirrors the TOML schema. Everything is optional at parse time;
// presence and validity are enforced in build. Unknown keys and sections
// are silently ignored (the PRD does not ask for strict decoding).
type rawConfig struct {
	Mailgun struct {
		APIKey string `toml:"api_key"`
		Region string `toml:"region"`
		// A pointer distinguishes an absent key (client default) from an
		// explicitly empty value, which is a configuration error.
		Timeout *string `toml:"timeout"`
	} `toml:"mailgun"`
	Gmail struct {
		Credentials string  `toml:"credentials"`
		Timeout     *string `toml:"timeout"`
	} `toml:"gmail"`
	SMTP struct {
		Host     string  `toml:"host"`
		Port     int     `toml:"port"`
		Security string  `toml:"security"`
		Username string  `toml:"username"`
		Password string  `toml:"password"`
		Timeout  *string `toml:"timeout"`
	} `toml:"smtp"`
	Sending struct {
		Domain       string `toml:"domain"`
		Provider     string `toml:"provider"`
		From         string `toml:"from"`
		Template     string `toml:"template"`
		TemplateFile string `toml:"template_file"`
	} `toml:"sending"`
	Logging struct {
		Logfile string `toml:"logfile"`
		Level   string `toml:"loglevel"`
		MaxSize string `toml:"max_size"`
	} `toml:"logging"`
	Defer struct {
		Defer          *bool  `toml:"defer"`
		Dir            string `toml:"defer_dir"`
		MaxSize        string `toml:"max_size"`
		ResendCooldown string `toml:"resend_cooldown"`
		MaxAge         string `toml:"max_age"`
	} `toml:"defer"`
	Dedup struct {
		StateDir string `toml:"state_dir"`
		Cooldown string `toml:"cooldown"`
	} `toml:"dedup"`
}

// Load finds, parses, and validates the configuration. When flagPath is
// non-empty (the -c/--config flag), only that file is tried; otherwise the
// platform search order applies and the first existing file wins. A warning
// about overly permissive file permissions is written to stderr (Unix only).
// MAILFIRE_* environment variables override file settings; with no config
// file and at least one variable set, the environment is the only source.
func Load(flagPath string, stderr io.Writer) (*Config, error) {
	return LoadWith(flagPath, stderr, Overrides{})
}

// Overrides names the settings a command-line option supplies. Their file
// and environment values are discarded before validation, so an invalid
// lower-priority value cannot fail a run that overrides it; the caller
// validates and applies its own value. The corresponding Config fields
// hold no value from lower-priority sources: From is empty, Template is
// the default, TemplateFile is empty, Timeout is zero, and DedupCooldown
// is the default.
type Overrides struct {
	From     bool // --from: sending.from
	Template bool // --template or --template-file: the template selection
	Timeout  bool // --timeout: mailgun.timeout
	Cooldown bool // --cooldown: dedup.cooldown
}

// LoadWith is Load for a command whose options override the settings
// named in ov.
func LoadWith(flagPath string, stderr io.Writer, ov Overrides) (*Config, error) {
	return load(flagPath, stderr, runtime.GOOS, os.Getenv, ov)
}

// discard drops the overridden settings from raw and from the
// environment sources, after file parsing and the environment overlay.
func (ov Overrides) discard(raw *rawConfig, sources map[string]string) {
	if ov.From {
		raw.Sending.From = ""
		delete(sources, "sending.from")
	}
	if ov.Template {
		raw.Sending.Template, raw.Sending.TemplateFile = "", ""
		delete(sources, "sending.template")
		delete(sources, "sending.template_file")
	}
	if ov.Timeout {
		raw.Mailgun.Timeout = nil
		raw.SMTP.Timeout = nil
		raw.Gmail.Timeout = nil
		for _, k := range []string{"mailgun.timeout", "smtp.timeout", "gmail.timeout"} {
			delete(sources, k)
		}
	}
	if ov.Cooldown {
		raw.Dedup.Cooldown = ""
		delete(sources, "dedup.cooldown")
	}
}

func load(flagPath string, stderr io.Writer, goos string, getenv func(string) string, ov Overrides) (*Config, error) {
	path, err := find(flagPath, goos, getenv)
	if err != nil {
		// An explicitly requested file (-c) that is missing or unusable
		// always fails. Otherwise fall back to environment-only
		// configuration when MAILFIRE_* variables are set; without any,
		// keep reporting that no configuration file was found.
		if flagPath != "" || !envActive(getenv) {
			return nil, err
		}
		var raw rawConfig
		sources, oerr := applyEnv(&raw, getenv)
		if oerr != nil {
			return nil, oerr
		}
		ov.discard(&raw, sources)
		return build("", nil, &raw, goos, getenv, sources)
	}
	warnIfPermissive(goos, path, stderr)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}
	var raw rawConfig
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, parseError(path, data, err)
	}
	sources, err := applyEnv(&raw, getenv)
	if err != nil {
		return nil, err
	}
	ov.discard(&raw, sources)
	return build(path, data, &raw, goos, getenv, sources)
}

// Find resolves the config file path for flagPath (the -c/--config
// value) without reading the file, applying the same search order as
// Load. It exists for the validate subcommand, which reports locating
// the config file as its own step (PRD FR-5).
func Find(flagPath string) (string, error) {
	return find(flagPath, runtime.GOOS, os.Getenv)
}

// CheckSyntax reads path and reports whether it parses as valid TOML.
// Errors are formatted exactly like Load's read/parse errors (in
// particular, details of an invalid api_key value are withheld). It
// exists for the validate subcommand's step 1 (PRD FR-5); key presence
// and validity are step 2, checked by Load.
func CheckSyntax(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: reading %s: %w", path, err)
	}
	var raw rawConfig
	if err := toml.Unmarshal(data, &raw); err != nil {
		return parseError(path, data, err)
	}
	return nil
}

// find resolves which config file to use.
func find(flagPath, goos string, getenv func(string) string) (string, error) {
	if flagPath != "" {
		p := expandPath(flagPath, getenv)
		fi, err := os.Stat(p)
		switch {
		case err != nil:
			return "", fmt.Errorf("config: %s: no such file", p)
		case !fi.Mode().IsRegular():
			return "", fmt.Errorf("config: %s: not a regular file", p)
		}
		return p, nil
	}
	paths := searchPaths(goos, getenv)
	for _, p := range paths {
		if fileExists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("config: no configuration file found (searched: %s); create one, or set MAILFIRE_MAILGUN_API_KEY, MAILFIRE_MAILGUN_REGION, and MAILFIRE_SENDING_DOMAIN for Mailgun, or MAILFIRE_SENDING_PROVIDER=smtp with MAILFIRE_SMTP_HOST, MAILFIRE_SMTP_PORT, MAILFIRE_SMTP_SECURITY, and MAILFIRE_SENDING_DOMAIN for SMTP, or MAILFIRE_SENDING_PROVIDER=gmail for Gmail", strings.Join(paths, ", "))
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

var (
	reTypeMismatch = regexp.MustCompile(`cannot decode TOML (\w+) into .* of type (\S+)$`)
	reAssignment   = regexp.MustCompile(`^\s*([A-Za-z0-9_.\-"' ]+?)\s*=`)
)

// parseError formats a TOML decode error with the file name, the line
// number and, when known, the key. go-toml's messages may quote a character
// of the offending value (e.g. "unexpected character U+0053 'S'"), so when
// the error lies within a secret's assignment (mailgun.api_key,
// smtp.password) the detail is withheld.
func parseError(path string, data []byte, err error) error {
	var derr *toml.DecodeError
	if !errors.As(err, &derr) {
		return fmt.Errorf("config: parsing %s: %w", path, err)
	}
	row, _ := derr.Position()
	key := strings.Join(derr.Key(), ".")
	msg := strings.TrimPrefix(derr.Error(), "toml: ")

	if m := reTypeMismatch.FindStringSubmatch(msg); m != nil {
		msg = fmt.Sprintf("wrong value type: got TOML %s, want %s", m[1], m[2])
	} else if strings.Contains(msg, "at start of value") {
		msg += ` (strings must be quoted, e.g. region = "eu")`
	}
	for _, s := range secretKeys {
		if strings.HasSuffix(key, s.name) || assignedKeyAt(data, row) == s.name || lineMentions(data, row, s.name) {
			key = s.key
			msg = "invalid value (details withheld: the line contains " + s.what + ")"
			break
		}
	}
	if key != "" {
		return fmt.Errorf("config: parsing %s: line %d: %s: %s", path, row, key, msg)
	}
	return fmt.Errorf("config: parsing %s: line %d: %s", path, row, msg)
}

// secretKeys are the configuration keys whose values must never appear in
// diagnostics: the full key is reported instead, with a description.
var secretKeys = []struct{ name, key, what string }{
	{"api_key", "mailgun.api_key", "the API key"},
	{"password", "smtp.password", "a password"},
}

// lineMentions reports whether the 1-based line row contains name
// anywhere. This covers inline tables such as
// mailgun = { api_key = "..." }, where the assignment at the start of
// the line names a different key.
func lineMentions(data []byte, row int, name string) bool {
	lines := strings.Split(string(data), "\n")
	return row >= 1 && row <= len(lines) && strings.Contains(lines[row-1], name)
}

// assignedKeyAt returns the last path component of the key assigned on, or
// most recently before, the 1-based line row. This attributes errors inside
// multi-line values to the key that owns them. Returns "" when unknown.
func assignedKeyAt(data []byte, row int) string {
	lines := strings.Split(string(data), "\n")
	if row > len(lines) {
		row = len(lines)
	}
	for i := row - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "[") {
			return ""
		}
		if m := reAssignment.FindStringSubmatch(l); m != nil {
			k := m[1]
			if j := strings.LastIndex(k, "."); j >= 0 {
				k = k[j+1:]
			}
			return strings.Trim(strings.TrimSpace(k), `"'`)
		}
	}
	return ""
}

// keyLine returns the 1-based line on which section.key is assigned, or 0
// if it cannot be found (e.g. the key is missing). It understands
// [section] headers and dotted keys, which covers the documented schema.
func keyLine(data []byte, section, key string) int {
	current := ""
	for i, raw := range strings.Split(string(data), "\n") {
		l := strings.TrimSpace(raw)
		if strings.HasPrefix(l, "[") {
			current = strings.TrimSpace(strings.Trim(l, "[] \t"))
			if j := strings.Index(current, "]"); j >= 0 {
				current = strings.TrimSpace(current[:j])
			}
			continue
		}
		m := reAssignment.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		parts := strings.Split(m[1], ".")
		for j := range parts {
			parts[j] = strings.Trim(strings.TrimSpace(parts[j]), `"'`)
		}
		full := strings.Join(parts, ".")
		if current != "" {
			full = current + "." + full
		}
		if full == section+"."+key {
			return i + 1
		}
	}
	return 0
}

// build validates the raw config and applies defaults. envSource maps
// config keys to the MAILFIRE_* variable that supplied their value;
// validation errors for those keys name the variable, not the file.
func build(path string, data []byte, raw *rawConfig, goos string, getenv func(string) string, envSource map[string]string) (*Config, error) {
	cfg := &Config{Path: path, EnvSource: envSource}
	keyErr := keyErrorFunc(path, data, envSource)
	sizeOrDefault := func(key, s string) (int64, error) {
		if strings.TrimSpace(s) == "" {
			return DefaultMaxSize, nil
		}
		n, err := parseSize(s)
		if err != nil {
			return 0, keyErr(key, fmt.Sprintf("invalid size %q: use bytes or a K, M, or G suffix, e.g. \"50M\"", s))
		}
		return n, nil
	}

	cfg.Provider = strings.ToLower(strings.TrimSpace(raw.Sending.Provider))
	if cfg.Provider == "" {
		cfg.Provider = ProviderMailgun
	}
	switch cfg.Provider {
	case ProviderMailgun, ProviderSMTP, ProviderGmail:
	default:
		return nil, keyErr("sending.provider", fmt.Sprintf("invalid value %q: must be \"mailgun\", \"gmail\", or \"smtp\"", cfg.Provider))
	}

	// Every provider section is parsed; only the selected provider's
	// required settings are enforced here. Resending a message deferred
	// through another provider checks that provider's section with
	// CheckProvider.
	cfg.APIKey = strings.TrimSpace(raw.Mailgun.APIKey)
	cfg.Region = strings.ToLower(strings.TrimSpace(raw.Mailgun.Region))
	if cfg.Provider == ProviderMailgun {
		if err := cfg.checkMailgun(keyErr); err != nil {
			return nil, err
		}
	}
	var err error
	// Empty, zero, negative, malformed, and overflowing durations are
	// rejected: empty and zero would disable the timeout and let a stalled
	// request hang forever. An absent key keeps the provider's built-in
	// default.
	if cfg.Timeout, err = parseTimeout(keyErr, "mailgun.timeout", raw.Mailgun.Timeout); err != nil {
		return nil, err
	}

	cfg.SMTPHost = strings.TrimSpace(raw.SMTP.Host)
	cfg.SMTPPort = raw.SMTP.Port
	cfg.SMTPSecurity = strings.ToLower(strings.TrimSpace(raw.SMTP.Security))
	cfg.SMTPUsername = strings.TrimSpace(raw.SMTP.Username)
	cfg.SMTPPassword = raw.SMTP.Password
	if cfg.Provider == ProviderSMTP {
		if err := cfg.checkSMTP(keyErr); err != nil {
			return nil, err
		}
	}
	if cfg.SMTPTimeout, err = parseTimeout(keyErr, "smtp.timeout", raw.SMTP.Timeout); err != nil {
		return nil, err
	}
	if err := cfg.setGmail(raw, getenv, keyErr); err != nil {
		return nil, err
	}

	cfg.Domain = strings.TrimSpace(raw.Sending.Domain)
	if cfg.Domain == "" && cfg.Provider != ProviderGmail {
		return nil, keyErr("sending.domain", "is required and must not be empty (or set MAILFIRE_SENDING_DOMAIN)")
	}

	cfg.From = strings.TrimSpace(raw.Sending.From)
	if cfg.From != "" {
		if _, err := mailbox.Parse(cfg.From); err != nil {
			return nil, keyErr("sending.from", fmt.Sprintf("invalid address %q: expected \"user@example.com\" or \"Name <user@example.com>\"", cfg.From))
		}
	}

	if err := cfg.setTemplate(raw, getenv, keyErr); err != nil {
		return nil, err
	}

	cfg.Logfile = expandPath(strings.TrimSpace(raw.Logging.Logfile), getenv)
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(raw.Logging.Level))
	if cfg.LogLevel == "" {
		cfg.LogLevel = DefaultLogLevel
	}
	switch cfg.LogLevel {
	case "error", "information", "debug":
	default:
		return nil, keyErr("logging.loglevel", fmt.Sprintf("invalid value %q: must be \"error\", \"information\", or \"debug\"", cfg.LogLevel))
	}

	if cfg.LogMaxSize, err = sizeOrDefault("logging.max_size", raw.Logging.MaxSize); err != nil {
		return nil, err
	}

	cfg.Defer = true
	if raw.Defer.Defer != nil {
		cfg.Defer = *raw.Defer.Defer
	}
	cfg.DeferDir = expandPath(strings.TrimSpace(raw.Defer.Dir), getenv)
	if cfg.DeferDir == "" {
		cfg.DeferDir = defaultDeferDir(goos, getenv)
	}
	if cfg.DeferDir == "" && cfg.Defer {
		return nil, keyErr("defer.defer_dir", "no default available (home directory unknown): set defer_dir or defer = false")
	}
	if cfg.DeferMaxSize, err = sizeOrDefault("defer.max_size", raw.Defer.MaxSize); err != nil {
		return nil, err
	}
	cfg.ResendCooldown = DefaultCooldown
	if s := strings.TrimSpace(raw.Defer.ResendCooldown); s != "" {
		d, derr := time.ParseDuration(s)
		if derr != nil || d <= 0 {
			return nil, keyErr("defer.resend_cooldown", fmt.Sprintf("invalid duration %q: use e.g. \"30s\", \"5m\", \"1h\"", s))
		}
		cfg.ResendCooldown = d
	}
	if s := strings.TrimSpace(raw.Defer.MaxAge); s != "" {
		d, derr := time.ParseDuration(s)
		if derr != nil || d <= 0 {
			return nil, keyErr("defer.max_age", fmt.Sprintf("invalid duration %q: use a positive Go duration, e.g. \"72h\", \"168h\"", s))
		}
		cfg.DeferMaxAge = d
	}

	cfg.DedupDir = expandPath(strings.TrimSpace(raw.Dedup.StateDir), getenv)
	if cfg.DedupDir == "" {
		if d := defaultDeferDir(goos, getenv); d != "" {
			cfg.DedupDir = filepath.Join(d, deferral.ReservedDir)
		}
	}
	cfg.DedupCooldown = DefaultDedupCooldown
	if s := strings.TrimSpace(raw.Dedup.Cooldown); s != "" {
		d, derr := time.ParseDuration(s)
		if derr != nil || d <= 0 {
			return nil, keyErr("dedup.cooldown", fmt.Sprintf("invalid duration %q: use a positive Go duration, e.g. \"30m\", \"1h\"", s))
		}
		cfg.DedupCooldown = d
	}
	return cfg, nil
}

// Sending providers ([sending].provider).
const (
	ProviderMailgun = "mailgun"
	ProviderSMTP    = "smtp"
	ProviderGmail   = "gmail"
)

// parseTimeout validates an optional positive duration setting; nil (key
// absent) yields zero, meaning the provider's default.
func parseTimeout(keyErr func(key, msg string) error, key string, raw *string) (time.Duration, error) {
	if raw == nil {
		return 0, nil
	}
	s := strings.TrimSpace(*raw)
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, keyErr(key, fmt.Sprintf("invalid duration %q: use a positive Go duration, e.g. \"15s\", \"1m\"", s))
	}
	return d, nil
}

// checkMailgun enforces the Mailgun provider's required settings.
func (c *Config) checkMailgun(keyErr func(key, msg string) error) error {
	if c.APIKey == "" {
		return keyErr("mailgun.api_key", "is required for the Mailgun provider (or set MAILFIRE_MAILGUN_API_KEY)")
	}
	switch c.Region {
	case "":
		return keyErr("mailgun.region", `is required for the Mailgun provider: must be "us" or "eu" (or set MAILFIRE_MAILGUN_REGION)`)
	case "us", "eu":
		return nil
	default:
		return keyErr("mailgun.region", fmt.Sprintf("invalid value %q: must be \"us\" or \"eu\"", c.Region))
	}
}

// checkSMTP enforces the SMTP provider's required settings. Credentials
// are optional (unauthenticated relays) but must come as a pair and never
// with plaintext transport.
func (c *Config) checkSMTP(keyErr func(key, msg string) error) error {
	if c.SMTPHost == "" {
		return keyErr("smtp.host", "is required for the SMTP provider (or set MAILFIRE_SMTP_HOST)")
	}
	if c.SMTPPort < 1 || c.SMTPPort > 65535 {
		return keyErr("smtp.port", "is required for the SMTP provider and must be between 1 and 65535 (or set MAILFIRE_SMTP_PORT)")
	}
	switch c.SMTPSecurity {
	case "tls", "starttls", "none":
	case "":
		return keyErr("smtp.security", `is required for the SMTP provider: must be "tls", "starttls", or "none"`)
	default:
		return keyErr("smtp.security", fmt.Sprintf("invalid value %q: must be \"tls\", \"starttls\", or \"none\"", c.SMTPSecurity))
	}
	if (c.SMTPUsername == "") != (c.SMTPPassword == "") {
		return keyErr("smtp.username", "username and password must be set together")
	}
	if c.SMTPSecurity == "none" && c.SMTPUsername != "" {
		return keyErr("smtp.security", "plaintext SMTP cannot be used with credentials; use tls or starttls")
	}
	return nil
}

// CheckProvider reports whether the settings of provider p are complete.
// The selected provider is validated by Load; resend uses this to check
// the provider a deferred message was originally sent through, which may
// differ from the current default.
func (c *Config) CheckProvider(p string) error {
	keyErr := func(key, msg string) error { return fmt.Errorf("config: %s: %s", key, msg) }
	switch p {
	case ProviderMailgun:
		if err := c.checkMailgun(keyErr); err != nil {
			return err
		}
		if c.Domain == "" {
			return keyErr("sending.domain", "is required for the Mailgun provider (or set MAILFIRE_SENDING_DOMAIN)")
		}
		return nil
	case ProviderSMTP:
		return c.checkSMTP(keyErr)
	case ProviderGmail:
		return nil
	}
	return fmt.Errorf("config: unknown provider %q", p)
}

// setGmail validates and applies the [gmail] settings.
func (c *Config) setGmail(raw *rawConfig, getenv func(string) string, keyErr func(key, msg string) error) error {
	c.GmailCredentials = expandPath(strings.TrimSpace(raw.Gmail.Credentials), getenv)
	var err error
	c.GmailTimeout, err = parseTimeout(keyErr, "gmail.timeout", raw.Gmail.Timeout)
	return err
}

// LoadGmail loads only the [gmail] settings and their MAILFIRE_GMAIL_*
// variables, for "mailfire auth": authorizing Gmail must work before the
// rest of the configuration (or any configuration file) exists. Like
// LoadRender, a missing config file is not an error unless flagPath names
// one; a file that exists must parse. Only Path, EnvSource,
// GmailCredentials, and GmailTimeout are set.
func LoadGmail(flagPath string, stderr io.Writer, ov Overrides) (*Config, error) {
	return loadGmail(flagPath, stderr, runtime.GOOS, os.Getenv, ov)
}

func loadGmail(flagPath string, stderr io.Writer, goos string, getenv func(string) string, ov Overrides) (*Config, error) {
	path, data, raw, err := readOptional(flagPath, stderr, goos, getenv)
	if err != nil {
		return nil, err
	}
	sources := map[string]string{}
	for _, b := range envBindings {
		if !strings.HasPrefix(b.key, "gmail.") {
			continue
		}
		if v := strings.TrimSpace(getenv(b.env)); v != "" {
			b.set(raw, v)
			sources[b.key] = b.env
		}
	}
	ov.discard(raw, sources)
	cfg := &Config{Path: path, EnvSource: sources, Provider: ProviderGmail}
	if err := cfg.setGmail(raw, getenv, keyErrorFunc(path, data, sources)); err != nil {
		return nil, err
	}
	return cfg, nil
}

// keyErrorFunc returns the error constructor for an invalid key: it names
// the MAILFIRE_* variable that supplied the value, or the file and line.
func keyErrorFunc(path string, data []byte, envSource map[string]string) func(key, msg string) error {
	return func(key, msg string) error {
		if env := envSource[key]; env != "" {
			return fmt.Errorf("config: environment variable %s: %s", env, msg)
		}
		if path == "" {
			// Environment-only configuration: there is no file to cite.
			return fmt.Errorf("config: %s: %s", key, msg)
		}
		section, name, _ := strings.Cut(key, ".")
		return keyError(path, keyLine(data, section, name), key, msg)
	}
}

// setTemplate validates and applies the template selection.
func (c *Config) setTemplate(raw *rawConfig, getenv func(string) string, keyErr func(key, msg string) error) error {
	c.Template = strings.ToLower(strings.TrimSpace(raw.Sending.Template))
	c.TemplateFile = expandPath(strings.TrimSpace(raw.Sending.TemplateFile), getenv)
	if c.Template != "" && c.TemplateFile != "" {
		return keyErr("sending.template_file", "mutually exclusive with sending.template")
	}
	if c.Template == "" {
		c.Template = render.DefaultTemplate
	}
	if !render.Valid(c.Template) {
		return keyErr("sending.template", fmt.Sprintf("invalid value %q: must be one of %s", c.Template, TemplateList()))
	}
	return nil
}

// readOptional reads the configuration file for the partial loaders
// (LoadRender, LoadGmail): a missing file is not an error unless flagPath
// names one (path "", empty raw), but a file that exists must parse.
func readOptional(flagPath string, stderr io.Writer, goos string, getenv func(string) string) (string, []byte, *rawConfig, error) {
	raw := &rawConfig{}
	path, err := findOptional(flagPath, goos, getenv)
	if err != nil || path == "" {
		return "", nil, raw, err
	}
	warnIfPermissive(goos, path, stderr)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, nil, fmt.Errorf("config: reading %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, raw); err != nil {
		return "", nil, nil, parseError(path, data, err)
	}
	return path, data, raw, nil
}

// findOptional is find for the partial loaders: without -c, the absence
// of every searched file yields "" and no error.
func findOptional(flagPath, goos string, getenv func(string) string) (string, error) {
	if flagPath != "" {
		return find(flagPath, goos, getenv)
	}
	for _, p := range searchPaths(goos, getenv) {
		if fileExists(p) {
			return p, nil
		}
	}
	return "", nil
}

// LoadRender loads only the settings needed to render a message locally
// (send --preview, task 29): the template selection from the config file
// and the MAILFIRE_SENDING_TEMPLATE / MAILFIRE_SENDING_TEMPLATE_FILE
// variables; other variables are ignored. Unlike Load, a missing config file is not an
// error unless flagPath names one, and the Mailgun credentials, domain,
// sender, and the other sections are neither required nor validated. Only
// Path, EnvSource, Template, and TemplateFile are set.
func LoadRender(flagPath string, stderr io.Writer, ov Overrides) (*Config, error) {
	return loadRender(flagPath, stderr, runtime.GOOS, os.Getenv, ov)
}

func loadRender(flagPath string, stderr io.Writer, goos string, getenv func(string) string, ov Overrides) (*Config, error) {
	path, data, raw, err := readOptional(flagPath, stderr, goos, getenv)
	if err != nil {
		return nil, err
	}
	// Only the template variables matter for rendering: an invalid
	// unrelated variable (e.g. MAILFIRE_DEFER_DEFER) must not fail it.
	sources := map[string]string{}
	if err := applyTemplateEnv(raw, getenv, sources); err != nil {
		return nil, err
	}
	ov.discard(raw, sources)
	cfg := &Config{Path: path, EnvSource: sources}
	if err := cfg.setTemplate(raw, getenv, keyErrorFunc(path, data, sources)); err != nil {
		return nil, err
	}
	return cfg, nil
}

// CheckDedupDir reports a dedup state_dir that is unusable: unknown (no
// home directory), or nested inside the defer_dir anywhere but its
// reserved "dedup" subdirectory, where the deferred-message cleanup would
// mistake it for a broken message and delete it. Symbolic links in the
// existing parts of both paths are resolved. It is checked only when
// deduplication is used (send with --dedup-key, resend of a message
// sent with one), so configurations without it are unaffected.
func (c *Config) CheckDedupDir() error {
	if c.DedupDir == "" {
		return errors.New("config: dedup.state_dir: no default available (home directory unknown): set state_dir or MAILFIRE_DEDUP_STATE_DIR")
	}
	if c.DeferDir == "" {
		return nil
	}
	rel, inside := relInside(c.DeferDir, c.DedupDir)
	if !inside || rel == deferral.ReservedDir {
		return nil
	}
	return fmt.Errorf("config: dedup.state_dir %s is inside defer.defer_dir %s: use %s or a directory outside defer_dir",
		c.DedupDir, c.DeferDir, filepath.Join(c.DeferDir, deferral.ReservedDir))
}

// relInside reports whether child lies strictly inside parent, and its
// relative path. Symbolic links in the existing parts of both paths are
// resolved first, so an alias cannot place the state inside the
// defer_dir unnoticed. Paths that cannot be made absolute count as not
// inside.
func relInside(parent, child string) (string, bool) {
	p, perr := resolvePath(parent)
	c, cerr := resolvePath(child)
	if perr != nil || cerr != nil {
		return "", false
	}
	rel, err := filepath.Rel(p, c)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// resolvePath returns the absolute form of p with symbolic links
// resolved in its longest existing prefix; the components that do not
// exist yet are appended as they are.
func resolvePath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rest := ""
	for dir := abs; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest), nil
		}
		if filepath.Dir(dir) == dir {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// TemplateList returns the template names quoted and comma separated,
// for error messages: "card", "card-serif", "plain", or "plain-serif".
func TemplateList() string {
	q := make([]string, len(render.Names))
	for i, n := range render.Names {
		q[i] = strconv.Quote(n)
	}
	return strings.Join(q[:len(q)-1], ", ") + ", or " + q[len(q)-1]
}

// keyError formats a validation error naming the config file, the line
// (when known), and the key. Callers must never pass the value of
// mailgun.api_key in msg.
func keyError(path string, line int, key, msg string) error {
	if line > 0 {
		return fmt.Errorf("config: %s: line %d: %s: %s", path, line, key, msg)
	}
	return fmt.Errorf("config: %s: %s: %s", path, key, msg)
}

// parseSize parses a byte size: a positive integer with an optional
// (case-insensitive) K, M, G, KB, MB, GB, or B suffix. A bare number is
// bytes.
func parseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	if t == "" {
		return 0, errors.New("empty size")
	}
	mult := int64(1)
	for _, suf := range []struct {
		s string
		m int64
	}{
		{"KB", 1 << 10}, {"MB", 1 << 20}, {"GB", 1 << 30},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30},
		{"B", 1},
	} {
		if strings.HasSuffix(t, suf.s) {
			mult = suf.m
			t = strings.TrimSpace(t[:len(t)-len(suf.s)])
			break
		}
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt64/mult {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * mult, nil
}
