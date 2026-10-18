# TOML configuration rules

Use these rules when changing Mailfire configuration, configuration examples, or
the configuration loader.

## Source of truth

- Mailfire parses TOML with `github.com/pelletier/go-toml/v2`, not Viper.
- `internal/config/config.go` defines the schema, validation, and defaults.
- `internal/config/paths.go` defines config discovery, path expansion, and
  platform-specific defaults.
- `mailfire.example.conf` is the annotated, shareable configuration example.
  README.md contains only the minimal quick-start configuration and links to it.
- Configuration files use the `.conf` extension but contain TOML.
- Treat local `mailfire.conf` and `.mailfire.conf` files as user configuration.
  Do not overwrite them or copy their credentials into examples or documentation.

## Structure and style

- Use the existing top-level tables: `[mailgun]`, `[smtp]`, `[gmail]`, `[sending]`,
  `[logging]`, `[defer]`, and `[dedup]`. Add tables or keys only alongside implementation and documentation.
- Match `mailfire.example.conf`: table headers, settings, and comments are
  left-aligned; separate tables and related settings with blank lines.
- Use `# ` comments and `# key = value` for commented-out settings.
- Follow existing key names exactly. New keys use `snake_case`; do not rename
  established keys such as `logfile` and `loglevel` merely for style consistency.
- Keys must be unique within each table. The same key may legitimately appear in
  different tables, such as `[logging].max_size` and `[defer].max_size`.

## Values

- Use `=` for assignment and quote strings. Prefer double-quoted strings except
  where literal single-quoted strings make paths clearer.
- For Windows paths, use literal strings such as
  `'C:\Users\me\AppData\Local\mailfire\mailfire.log'`, or escape backslashes
  in double-quoted strings.
- Booleans are unquoted `true` and `false`. Use unquoted integers only for fields
  whose schema accepts integers.
- Sizes and durations are strings, for example `max_size = "50M"` and
  `resend_cooldown = "5m"`. Do not assume TOML numbers are accepted for these keys.
- Document leading `~` expansion and current-working-directory resolution of
  relative paths. Do not imply arbitrary environment-variable interpolation.
- Do not introduce arrays or nested tables unless the loader explicitly supports
  them for that setting.

## Examples and defaults

- Describe each setting's purpose, whether it is required or optional, its default
  (or absence of one), and any implemented command-line override.
- Use short, consistent comments. Explain platform differences where relevant.
- Show required fields with illustrative values. Use `<your-api-key>` for the
  API key, `<app-password>` for SMTP passwords, and reserved example domains such as
  `example.com`; never use real secrets.
- Keep optional overrides commented out unless needed to make the example useful,
  such as a sample sender. Preserve normal defaults rather than enabling debug
  logging or disabling deferral in the starter configuration.
- Explain that `sending.from` is optional only when `--from` is provided when sending.
- Do not enable both `sending.template` and `sending.template_file` in an example.
  Explain their mutual exclusion and command-line overrides.
- Do not document planned settings as available until implemented. Task files
  describe proposals, not the current schema.
- Every key has a `MAILFIRE_<SECTION>_<KEY>` environment override
  (`internal/config/env.go`). A new key needs its binding there and documentation in
  `docs/environment.md`, `docs/mailfire.1`, and, where relevant, README.md.
- In `mailfire.example.conf`, put `# Env: MAILFIRE_<SECTION>_<KEY>` with the exact
  variable from `envNames()` directly above each setting, one per key (never one
  line for a group such as `api_key` and `region`). The header states precedence
  (CLI > nonempty environment > file > defaults), that empty or whitespace-only
  variables count as unset, and value syntax (`true`/`false`, plain durations and
  sizes); any shell `export` example stays commented and uses placeholders.
- Note the template pair exception: both `MAILFIRE_SENDING_TEMPLATE` and
  `MAILFIRE_SENDING_TEMPLATE_FILE` nonempty is an error, even with a CLI override.

Example:

```toml
[sending]
# Required: the sending domain configured in Mailgun. No default.
# Env: MAILFIRE_SENDING_DOMAIN
domain = "example.com"

# Optional sender; no default. Supply --from if omitted.
# --from overrides this setting for a single send.
# Env: MAILFIRE_SENDING_FROM
from = "My Name <user@example.com>"

# Optional template for --md and --html. Default: "card".
# Either --template or --template-file overrides the configured selection.
# Env: MAILFIRE_SENDING_TEMPLATE
# template = "card"
```

## Validation and maintenance

- Validate examples with a TOML parser; syntax validity alone does not prove that
  keys, value types, or combinations are supported by Mailfire.
- For schema or loader changes, run `go test ./internal/config ./internal/cli`.
  Cover meaningful validation and precedence behavior in the existing test suite.
- The loader currently ignores unknown keys and tables. Do not rely on this to
  validate examples: check every documented key against the implemented schema.
- Keep `mailfire.example.conf`, README.md, and `docs/mailfire.1` consistent when
  configuration behavior changes. Update help and completions for related flags.
- Do not use `mailfire validate` as an offline syntax check: it also contacts Mailgun.
- No Taplo configuration is currently present. Do not assume `.taplo.toml` exists
  or require a Node-based formatter solely to edit a TOML example. Preserve the
  established formatting unless a formatter is deliberately introduced.
- Keep credentials out of diagnostics and logs. Document restrictive file access
  (`chmod 600` on Unix; appropriate NTFS permissions on Windows).

## Naming

- Use "Mailfire" in prose and `mailfire` for commands and file names.
- Use project URLs under `https://github.com/thorstenkramm/mailfire`.
