// Package config loads and validates mailfire's TOML configuration from
// the platform-specific search locations, with an explicit -c/--config
// override.
//
// The API key is a secret: it is never included in error messages, and the
// loader warns on stderr when the config file is readable by group/others
// (Unix only).
package config

import (
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Defaults per PRD FR-2.
const (
	DefaultLogLevel       = "information"
	DefaultMaxSize  int64 = 50 * 1024 * 1024 // 50M
	DefaultCooldown       = 5 * time.Minute
)

// Config is the validated, normalized mailfire configuration. Sizes are in
// bytes; paths have ~ expanded.
type Config struct {
	// Path is the file the configuration was loaded from.
	Path string

	// [mailgun]
	APIKey string // required; never log or include in errors
	Region string // "us" or "eu"

	// [sending]
	Domain string // required
	From   string // optional; valid mailbox ("user@example.com" or "Name <user@example.com>")

	// [logging]
	Logfile    string // optional; logging disabled when empty
	LogLevel   string // "error", "information" (default), or "debug"
	LogMaxSize int64  // default 50M

	// [defer]
	Defer          bool          // default true
	DeferDir       string        // default per-platform data dir
	DeferMaxSize   int64         // default 50M
	ResendCooldown time.Duration // default 5m
}

// rawConfig mirrors the TOML schema. Everything is optional at parse time;
// presence and validity are enforced in build.
type rawConfig struct {
	Mailgun struct {
		APIKey string `toml:"api_key"`
		Region string `toml:"region"`
	} `toml:"mailgun"`
	Sending struct {
		Domain string `toml:"domain"`
		From   string `toml:"from"`
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
	} `toml:"defer"`
}

// Load finds, parses, and validates the configuration. When flagPath is
// non-empty (the -c/--config flag), only that file is tried; otherwise the
// platform search order applies and the first existing file wins. A warning
// about overly permissive file permissions is written to stderr (Unix only).
func Load(flagPath string, stderr io.Writer) (*Config, error) {
	return load(flagPath, stderr, runtime.GOOS, os.Getenv)
}

func load(flagPath string, stderr io.Writer, goos string, getenv func(string) string) (*Config, error) {
	path, err := find(flagPath, goos, getenv)
	if err != nil {
		return nil, err
	}
	warnIfPermissive(goos, path, stderr)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}
	var raw rawConfig
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, parseError(path, err)
	}
	return build(path, &raw, goos, getenv)
}

// find resolves which config file to use.
func find(flagPath, goos string, getenv func(string) string) (string, error) {
	if flagPath != "" {
		p := expandPath(flagPath, getenv)
		if !fileExists(p) {
			return "", fmt.Errorf("config: %s: no such file", p)
		}
		return p, nil
	}
	paths := searchPaths(goos, getenv)
	for _, p := range paths {
		if fileExists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("config: no configuration file found (searched: %s)", strings.Join(paths, ", "))
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// parseError formats a TOML decode error with the file name and, when
// available, the line number.
func parseError(path string, err error) error {
	var derr *toml.DecodeError
	if errors.As(err, &derr) {
		row, _ := derr.Position()
		return fmt.Errorf("config: parsing %s: line %d: %s", path, row, derr.Error())
	}
	return fmt.Errorf("config: parsing %s: %w", path, err)
}

// build validates the raw config and applies defaults.
func build(path string, raw *rawConfig, goos string, getenv func(string) string) (*Config, error) {
	cfg := &Config{Path: path}

	cfg.APIKey = strings.TrimSpace(raw.Mailgun.APIKey)
	if cfg.APIKey == "" {
		return nil, keyErr(path, "mailgun.api_key", "is required and must not be empty")
	}

	cfg.Region = strings.ToLower(strings.TrimSpace(raw.Mailgun.Region))
	switch cfg.Region {
	case "":
		return nil, keyErr(path, "mailgun.region", `is required: must be "us" or "eu"`)
	case "us", "eu":
	default:
		return nil, keyErr(path, "mailgun.region", fmt.Sprintf("invalid value %q: must be \"us\" or \"eu\"", cfg.Region))
	}

	cfg.Domain = strings.TrimSpace(raw.Sending.Domain)
	if cfg.Domain == "" {
		return nil, keyErr(path, "sending.domain", "is required and must not be empty")
	}

	cfg.From = strings.TrimSpace(raw.Sending.From)
	if cfg.From != "" {
		if _, err := mail.ParseAddress(cfg.From); err != nil {
			return nil, keyErr(path, "sending.from", fmt.Sprintf("invalid address %q: expected \"user@example.com\" or \"Name <user@example.com>\"", cfg.From))
		}
	}

	cfg.Logfile = expandPath(strings.TrimSpace(raw.Logging.Logfile), getenv)
	cfg.LogLevel = raw.Logging.Level
	if cfg.LogLevel == "" {
		cfg.LogLevel = DefaultLogLevel
	}
	switch cfg.LogLevel {
	case "error", "information", "debug":
	default:
		return nil, keyErr(path, "logging.loglevel", fmt.Sprintf("invalid value %q: must be \"error\", \"information\", or \"debug\"", cfg.LogLevel))
	}

	var err error
	if cfg.LogMaxSize, err = sizeOrDefault(path, "logging.max_size", raw.Logging.MaxSize); err != nil {
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
	if cfg.DeferMaxSize, err = sizeOrDefault(path, "defer.max_size", raw.Defer.MaxSize); err != nil {
		return nil, err
	}
	cfg.ResendCooldown = DefaultCooldown
	if s := strings.TrimSpace(raw.Defer.ResendCooldown); s != "" {
		d, derr := time.ParseDuration(s)
		if derr != nil || d <= 0 {
			return nil, keyErr(path, "defer.resend_cooldown", fmt.Sprintf("invalid duration %q: use e.g. \"30s\", \"5m\", \"1h\"", s))
		}
		cfg.ResendCooldown = d
	}
	return cfg, nil
}

// keyErr formats a validation error naming the config file and key. It
// never includes the key's value when the key is mailgun.api_key.
func keyErr(path, key, msg string) error {
	return fmt.Errorf("config: %s: %s: %s", path, key, msg)
}

// sizeOrDefault parses a size string like "50M", returning DefaultMaxSize
// when empty.
func sizeOrDefault(path, key, s string) (int64, error) {
	if strings.TrimSpace(s) == "" {
		return DefaultMaxSize, nil
	}
	n, err := parseSize(s)
	if err != nil {
		return 0, keyErr(path, key, fmt.Sprintf("invalid size %q: use bytes or a K, M, or G suffix, e.g. \"50M\"", s))
	}
	return n, nil
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
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * mult, nil
}
