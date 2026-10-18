# Environment variables

Every Mailfire configuration setting can also be supplied through an environment variable. This suits CI pipelines,
containers, and scheduled jobs where writing a configuration file is inconvenient.

## Precedence

From highest to lowest:

1. Command-line options, such as `--from`, `--template`, `--template-file`, `--timeout`, and `--cooldown`
2. `MAILFIRE_*` environment variables
3. The configuration file
4. Built-in defaults

Environment variables override individual settings; the remaining settings still come from the configuration file
or the defaults. When a command-line option supplies a setting, its environment and file values are ignored and not
validated, so an invalid lower-priority value does not fail that run. One exception: setting both
`MAILFIRE_SENDING_TEMPLATE` and `MAILFIRE_SENDING_TEMPLATE_FILE` to nonempty values is always a configuration error,
even with `--template` or `--template-file` (see [Template selection](#template-selection)).

## Variables

Each variable is named `MAILFIRE_<SECTION>_<KEY>`, using the uppercased TOML section and key names.
There are no shorter aliases. Values use the same syntax and validation as in the configuration file, without
TOML quoting.

| Variable | Setting | Value |
| --- | --- | --- |
| `MAILFIRE_MAILGUN_API_KEY` | `[mailgun] api_key` | Mailgun API key. Required from some source. |
| `MAILFIRE_MAILGUN_REGION` | `[mailgun] region` | `us` or `eu`. Required from some source. |
| `MAILFIRE_MAILGUN_TIMEOUT` | `[mailgun] timeout` | Positive duration, such as `15s` or `1m`. Default: `30s`. |
| `MAILFIRE_SENDING_DOMAIN` | `[sending] domain` | Mailgun sending domain. Required from some source. |
| `MAILFIRE_SENDING_FROM` | `[sending] from` | Default sender, such as `My Name <user@example.com>`. |
| `MAILFIRE_SENDING_TEMPLATE` | `[sending] template` | `card`, `card-serif`, `plain`, or `plain-serif`. Default: `card`. |
| `MAILFIRE_SENDING_TEMPLATE_FILE` | `[sending] template_file` | Path to a custom template. |
| `MAILFIRE_LOGGING_LOGFILE` | `[logging] logfile` | Log file path. Logging is disabled unless set. |
| `MAILFIRE_LOGGING_LOGLEVEL` | `[logging] loglevel` | `error`, `information`, or `debug`. Default: `information`. |
| `MAILFIRE_LOGGING_MAX_SIZE` | `[logging] max_size` | Bytes or a `K`, `M`, or `G` suffix. Default: `50M`. |
| `MAILFIRE_DEFER_DEFER` | `[defer] defer` | `true` or `false` (case-insensitive). Default: `true`. |
| `MAILFIRE_DEFER_DEFER_DIR` | `[defer] defer_dir` | Directory for deferred messages. Default: platform-specific. |
| `MAILFIRE_DEFER_MAX_SIZE` | `[defer] max_size` | Bytes or a `K`, `M`, or `G` suffix. Default: `50M`. |
| `MAILFIRE_DEFER_RESEND_COOLDOWN` | `[defer] resend_cooldown` | Positive duration. Default: `5m`. |
| `MAILFIRE_DEFER_MAX_AGE` | `[defer] max_age` | Positive duration, such as `168h`. Default: none (messages do not expire). |
| `MAILFIRE_DEDUP_STATE_DIR` | `[dedup] state_dir` | Directory for notification deduplication state. Default: platform-specific. |
| `MAILFIRE_DEDUP_COOLDOWN` | `[dedup] cooldown` | Positive duration. Default: `1h`. `--cooldown` overrides it. |

Paths accept a leading `~`; relative paths use the current working directory. Mailfire does not expand other
environment variables inside values; let your shell do that.

See [mailfire.example.conf](../mailfire.example.conf) for a detailed description of each setting.

## Empty and unset variables

A variable that is empty or contains only whitespace is treated as unset. It never clears a value from the
configuration file or a default. To disable a boolean setting, set it explicitly, for example
`MAILFIRE_DEFER_DEFER=false`.

Unknown `MAILFIRE_*` variables are ignored.

## Template selection

`template` and `template_file` form a single selection:

- A nonempty `MAILFIRE_SENDING_TEMPLATE` or `MAILFIRE_SENDING_TEMPLATE_FILE` replaces the configuration file's
  template selection of either kind.
- Setting both variables to nonempty values is always a configuration error, also when `--template` or
  `--template-file` is given: the conflicting environment is reported rather than silently ignored.
- `--template` or `--template-file` overrides the selection from a single variable, even if its value is invalid,
  such as an unknown template name.
- `send --preview` reads only these two variables; all other `MAILFIRE_*` variables are ignored, so it works without
  the required Mailgun settings.

## Configuration without a file

When no configuration file is found and at least one `MAILFIRE_*` variable is set, Mailfire uses the environment
and the defaults only. The required settings must then come from the environment:

```shell
export MAILFIRE_MAILGUN_API_KEY="<your-api-key>"
export MAILFIRE_MAILGUN_REGION="eu"
export MAILFIRE_SENDING_DOMAIN="example.com"
export MAILFIRE_SENDING_FROM="My Name <user@example.com>"

mailfire validate
mailfire --to user@example.com --subject "Build finished" --message "All tests passed."
```

On Windows PowerShell, use `$env:MAILFIRE_MAILGUN_API_KEY = "<your-api-key>"`. Variable names are not
case-sensitive on Windows.

A file selected with `-c` or `--config` must exist and be valid, even when the environment supplies every setting.

## Validation and diagnostics

Invalid values are rejected before anything is sent. Errors name the variable that supplied the value, for example:

```text
mailfire: config: environment variable MAILFIRE_MAILGUN_REGION: invalid value "uk": must be "us" or "eu"
```

A missing required setting names both the configuration key and its variable. `mailfire validate` reports whether a
configuration file was used and where the region, domain, sender, and API key came from. The API key itself is
never shown in diagnostics or logs.

## Security

Environment variables are visible to the process and its children, and on some systems to other processes running
as the same user. Use your CI system's secret storage for `MAILFIRE_MAILGUN_API_KEY` and avoid printing the
environment in job logs.
