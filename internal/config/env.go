package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// env.go implements the MAILFIRE_* environment overlay (task 28). Every
// variable maps to exactly one configuration key, named
// MAILFIRE_<SECTION>_<KEY> with the TOML names uppercased. The overlay is
// applied to the parsed (or empty) raw config before validation, so
// precedence is: command-line flags > environment > config file >
// defaults, and environment-only operation works when the required
// settings (mailgun.api_key, mailgun.region, sending.domain) come from
// the environment.
//
// A variable that is unset, empty, or all whitespace is treated as
// unset: it never clears a value from the config file or a default.

// envBinding maps one MAILFIRE_* variable to a rawConfig field.
type envBinding struct {
	env string // e.g. MAILFIRE_MAILGUN_API_KEY
	key string // e.g. mailgun.api_key
	set func(raw *rawConfig, value string)
}

// envBindings is the complete, explicit list of string-valued
// environment overrides. defer.defer (a boolean) and the template
// name/file pair are handled separately in applyEnv. Adding a
// configuration key means adding a row here and documenting the variable
// in docs/content/reference/environment.md and docs/mailfire.1.
var envBindings = []envBinding{
	{"MAILFIRE_MAILGUN_API_KEY", "mailgun.api_key", func(r *rawConfig, v string) { r.Mailgun.APIKey = v }},
	{"MAILFIRE_MAILGUN_REGION", "mailgun.region", func(r *rawConfig, v string) { r.Mailgun.Region = v }},
	{"MAILFIRE_MAILGUN_TIMEOUT", "mailgun.timeout", func(r *rawConfig, v string) { r.Mailgun.Timeout = &v }},
	{"MAILFIRE_SENDING_DOMAIN", "sending.domain", func(r *rawConfig, v string) { r.Sending.Domain = v }},
	{"MAILFIRE_SENDING_PROVIDER", "sending.provider", func(r *rawConfig, v string) { r.Sending.Provider = v }},
	{"MAILFIRE_GMAIL_CREDENTIALS", "gmail.credentials", func(r *rawConfig, v string) { r.Gmail.Credentials = v }},
	{"MAILFIRE_GMAIL_TIMEOUT", "gmail.timeout", func(r *rawConfig, v string) { r.Gmail.Timeout = &v }},
	{"MAILFIRE_SMTP_HOST", "smtp.host", func(r *rawConfig, v string) { r.SMTP.Host = v }},
	{"MAILFIRE_SMTP_SECURITY", "smtp.security", func(r *rawConfig, v string) { r.SMTP.Security = v }},
	{"MAILFIRE_SMTP_USERNAME", "smtp.username", func(r *rawConfig, v string) { r.SMTP.Username = v }},
	{"MAILFIRE_SMTP_PASSWORD", "smtp.password", func(r *rawConfig, v string) { r.SMTP.Password = v }},
	{"MAILFIRE_SMTP_TIMEOUT", "smtp.timeout", func(r *rawConfig, v string) { r.SMTP.Timeout = &v }},
	{"MAILFIRE_SENDING_FROM", "sending.from", func(r *rawConfig, v string) { r.Sending.From = v }},
	{"MAILFIRE_SENDING_TEMP_DIR", "sending.temp_dir", func(r *rawConfig, v string) { r.Sending.TempDir = v }},
	{"MAILFIRE_LOGGING_LOGFILE", "logging.logfile", func(r *rawConfig, v string) { r.Logging.Logfile = v }},
	{"MAILFIRE_LOGGING_LOGLEVEL", "logging.loglevel", func(r *rawConfig, v string) { r.Logging.Level = v }},
	{"MAILFIRE_LOGGING_MAX_SIZE", "logging.max_size", func(r *rawConfig, v string) { r.Logging.MaxSize = v }},
	{"MAILFIRE_DEFER_DEFER_DIR", "defer.defer_dir", func(r *rawConfig, v string) { r.Defer.Dir = v }},
	{"MAILFIRE_DEFER_MAX_SIZE", "defer.max_size", func(r *rawConfig, v string) { r.Defer.MaxSize = v }},
	{"MAILFIRE_DEFER_RESEND_COOLDOWN", "defer.resend_cooldown", func(r *rawConfig, v string) { r.Defer.ResendCooldown = v }},
	{"MAILFIRE_DEFER_MAX_AGE", "defer.max_age", func(r *rawConfig, v string) { r.Defer.MaxAge = v }},
	{"MAILFIRE_DEDUP_STATE_DIR", "dedup.state_dir", func(r *rawConfig, v string) { r.Dedup.StateDir = v }},
	{"MAILFIRE_DEDUP_COOLDOWN", "dedup.cooldown", func(r *rawConfig, v string) { r.Dedup.Cooldown = v }},
	{"MAILFIRE_RUN_LOCALE", "run.locale", func(r *rawConfig, v string) { r.Run.Locale = v }},
}

// Variables handled outside envBindings.
const (
	envDeferBool    = "MAILFIRE_DEFER_DEFER"
	envTemplate     = "MAILFIRE_SENDING_TEMPLATE"
	envTemplateFile = "MAILFIRE_SENDING_TEMPLATE_FILE"
	envSMTPPort     = "MAILFIRE_SMTP_PORT"
)

// envNames returns every documented MAILFIRE_* variable.
func envNames() []string {
	names := make([]string, 0, len(envBindings)+4)
	for _, b := range envBindings {
		names = append(names, b.env)
	}
	return append(names, envDeferBool, envTemplate, envTemplateFile, envSMTPPort)
}

// envActive reports whether any documented MAILFIRE_* variable carries a
// value. It gates environment-only operation: with no config file and no
// variables, Load keeps reporting "no configuration file found".
func envActive(getenv func(string) string) bool {
	for _, n := range envNames() {
		if strings.TrimSpace(getenv(n)) != "" {
			return true
		}
	}
	return false
}

// EnvActive reports whether any MAILFIRE_* environment variable supplies
// configuration. The validate subcommand uses it to recognize an
// environment-only setup.
func EnvActive() bool { return envActive(os.Getenv) }

// applyEnv overlays the MAILFIRE_* variables onto raw. It returns the
// config keys whose values came from the environment, mapped to the
// variable that supplied them, so build can name the source of an
// invalid value instead of a misleading file line.
func applyEnv(raw *rawConfig, getenv func(string) string) (map[string]string, error) {
	sources := make(map[string]string)
	get := func(name string) string { return strings.TrimSpace(getenv(name)) }

	for _, b := range envBindings {
		if v := get(b.env); v != "" {
			b.set(raw, v)
			sources[b.key] = b.env
		}
	}

	// defer.defer is a *bool in rawConfig (TOML-decoded), so the
	// environment value is parsed here. Only the TOML spellings are
	// accepted, case-insensitively; anything else is a configuration
	// error naming the variable.
	if v := get(envDeferBool); v != "" {
		b, err := parseEnvBool(v)
		if err != nil {
			return nil, fmt.Errorf("config: environment variable %s: %w", envDeferBool, err)
		}
		raw.Defer.Defer = &b
		sources["defer.defer"] = envDeferBool
	}

	// smtp.port is a TOML integer, so the environment value is parsed
	// here and a malformed one names the variable.
	if v := get(envSMTPPort); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("config: environment variable %s: invalid port %q: must be a number between 1 and 65535", envSMTPPort, v)
		}
		raw.SMTP.Port = n
		sources["smtp.port"] = envSMTPPort
	}

	if err := applyTemplateEnv(raw, getenv, sources); err != nil {
		return nil, err
	}
	return sources, nil
}

// applyTemplateEnv overlays MAILFIRE_SENDING_TEMPLATE and
// MAILFIRE_SENDING_TEMPLATE_FILE onto raw and records them in sources.
// Template name and file form a single selection across sources (task
// 28 decision): a nonempty environment choice replaces the file's
// selection of either kind; setting both variables is an error. An
// explicit --template/--template-file still wins, because the CLI
// overrides the loaded config. LoadRender applies only this overlay.
func applyTemplateEnv(raw *rawConfig, getenv func(string) string, sources map[string]string) error {
	tmpl, tmplFile := strings.TrimSpace(getenv(envTemplate)), strings.TrimSpace(getenv(envTemplateFile))
	if tmpl != "" && tmplFile != "" {
		return fmt.Errorf("config: environment variables %s and %s are mutually exclusive: set only one", envTemplate, envTemplateFile)
	}
	if tmpl != "" {
		raw.Sending.Template, raw.Sending.TemplateFile = tmpl, ""
		sources["sending.template"] = envTemplate
	}
	if tmplFile != "" {
		raw.Sending.TemplateFile, raw.Sending.Template = tmplFile, ""
		sources["sending.template_file"] = envTemplateFile
	}
	return nil
}

// parseEnvBool accepts exactly the TOML boolean spellings,
// case-insensitively.
func parseEnvBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean %q: use \"true\" or \"false\"", s)
}
