---
title: Environment variables
description: Every configuration setting as a MAILFIRE_* environment variable, for CI pipelines, containers, and scheduled jobs.
weight: 30
icon: world
---

Every Mailfire configuration setting can also be supplied through an environment variable. This suits CI pipelines,
containers, and scheduled jobs where writing a configuration file is inconvenient.

## Precedence

From highest to lowest:

1. Command-line options, such as `--from`, `--template`, `--template-file`, `--timeout`, `--cooldown`, and `--locale`
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
| `MAILFIRE_GMAIL_CREDENTIALS` | `[gmail] credentials` | Owner-only Gmail OAuth token file. Default: `~/.config/mailfire/gmail-credentials.json` (Windows: `%APPDATA%\mailfire\gmail-credentials.json`). |
| `MAILFIRE_GMAIL_TIMEOUT` | `[gmail] timeout` | Positive Gmail API/token request timeout. Default: `30s`. |
| `MAILFIRE_SENDING_DOMAIN` | `[sending] domain` | Mailgun sending domain. Required from some source. |
| `MAILFIRE_SENDING_PROVIDER` | `[sending] provider` | `mailgun` (default) or `smtp`. |
| `MAILFIRE_SENDING_FROM` | `[sending] from` | Default sender, such as `My Name <user@example.com>`. |
| `MAILFIRE_SENDING_TEMPLATE` | `[sending] template` | `card`, `card-serif`, `plain`, or `plain-serif`. Default: `card`. |
| `MAILFIRE_SENDING_TEMPLATE_FILE` | `[sending] template_file` | Path to a custom template. |
| `MAILFIRE_SENDING_TEMP_DIR` | `[sending] temp_dir` | Directory for temporary message files and spilled `mailfire run` logs. Default: the system temporary directory. See [Temporary files](configuration.md#temporary-files). |
| `MAILFIRE_SMTP_HOST` | `[smtp] host` | SMTP server hostname. Required when provider is `smtp`. |
| `MAILFIRE_SMTP_PORT` | `[smtp] port` | SMTP submission port, a number from 1 to 65535. Required when provider is `smtp`. |
| `MAILFIRE_SMTP_SECURITY` | `[smtp] security` | `tls` (implicit TLS), `starttls` (required STARTTLS), or `none` (explicit local plaintext relay). |
| `MAILFIRE_SMTP_USERNAME` | `[smtp] username` | Optional SMTP username; must be paired with password. |
| `MAILFIRE_SMTP_PASSWORD` | `[smtp] password` | Optional SMTP password; never included in output or queue data. |
| `MAILFIRE_SMTP_TIMEOUT` | `[smtp] timeout` | Positive connection/transaction timeout, such as `15s` or `1m`. |
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
| `MAILFIRE_RUN_LOCALE` | `[run] locale` | Date locale of `mailfire run` reports, such as `de_DE` or `en_GB`. Default: `LC_ALL`, `LC_TIME`, or `LANG` (Windows: the user's locale), else `en_US`. `--locale` overrides it. |

Two developer variables are not configuration settings and have no file equivalent:
`MAILFIRE_GMAIL_CLIENT_ID` and `MAILFIRE_GMAIL_CLIENT_SECRET` replace Mailfire's built-in Google OAuth client, for
testing with your own Google Cloud project. Normal use does not need them.

Paths accept a leading `~`; relative paths use the current working directory. Mailfire does not expand other
environment variables inside values; let your shell do that.

See the [annotated example configuration](configuration.md#annotated-example) for a detailed description of each setting.

## SMTP submission

Set `MAILFIRE_SENDING_PROVIDER=smtp`, then provide `MAILFIRE_SMTP_HOST`,
`MAILFIRE_SMTP_PORT`, and `MAILFIRE_SMTP_SECURITY`. Security is explicit:
`tls` is implicit TLS (typically port 465), `starttls` requires STARTTLS
(typically port 587), and `none` is only for an intentionally configured
unauthenticated local relay. Mailfire never downgrades a failed or unavailable
STARTTLS session to plaintext and never transmits a configured password over
plaintext.

`MAILFIRE_SMTP_USERNAME` and `MAILFIRE_SMTP_PASSWORD` are optional but must
be set together. A password/app password is appropriate for ordinary SMTP
submission. Gmail commonly requires an app password or OAuth; an ordinary
Google-account password is not a supported shortcut. Gmail OAuth support is
available as its own provider: set `MAILFIRE_SENDING_PROVIDER=gmail` and run
`mailfire auth login --provider gmail` (see the [Gmail guide](../guides/gmail.md)).

`MAILFIRE_SMTP_TIMEOUT` covers connection setup, TLS, authentication, and the
SMTP transaction. `--timeout` overrides it for a single command. A successful
SMTP transaction means the submission server accepted the message, not that it
was finally delivered to every recipient.

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
  any provider settings.
- `mailfire auth` likewise reads only the `[gmail]` settings and `MAILFIRE_GMAIL_*` variables, so Gmail can be
  authorized before the rest of the configuration exists.

## Configuration without a file

When no configuration file is found and at least one `MAILFIRE_*` variable is set, Mailfire uses the environment
and the defaults only. The selected provider's required settings must then come from the environment. For
Mailgun:

```shell
export MAILFIRE_MAILGUN_API_KEY="<your-api-key>"
export MAILFIRE_MAILGUN_REGION="eu"
export MAILFIRE_SENDING_DOMAIN="example.com"
export MAILFIRE_SENDING_FROM="My Name <user@example.com>"

mailfire validate
mailfire --to user@example.com --subject "Build finished" --message "All tests passed."
```

For SMTP, select the provider and its server; keep the password in your secret store:

```shell
export MAILFIRE_SENDING_PROVIDER="smtp"
export MAILFIRE_SENDING_DOMAIN="example.com"
export MAILFIRE_SMTP_HOST="smtp.example.com"
export MAILFIRE_SMTP_PORT="587"
export MAILFIRE_SMTP_SECURITY="starttls"
export MAILFIRE_SMTP_USERNAME="submission-user"
export MAILFIRE_SMTP_PASSWORD="<app-password>"
```

For Gmail, `MAILFIRE_SENDING_PROVIDER=gmail` is enough after `mailfire auth login --provider gmail`.

On Windows PowerShell, use `$env:MAILFIRE_MAILGUN_API_KEY = "<your-api-key>"`. Variable names are not
case-sensitive on Windows.

A file selected with `-c` or `--config` must exist and be valid, even when the environment supplies every setting.

## Validation and diagnostics

Invalid values are rejected before anything is sent. Errors name the variable that supplied the value, for example:

```text
mailfire: config: environment variable MAILFIRE_MAILGUN_REGION: invalid value "uk": must be "us" or "eu"
```

A missing required setting names both the configuration key and its variable. `mailfire validate` reports whether a
configuration file was used and where the provider settings, domain, sender, and credentials came from. Credentials
(API key, SMTP password, Gmail tokens) are never shown in diagnostics, logs, JSON output, or deferred messages; a
configuration file line containing one is reported without its value.

## Security

Environment variables are visible to the process and its children, and on some systems to other processes running
as the same user. Use your CI system's secret storage for `MAILFIRE_MAILGUN_API_KEY` and `MAILFIRE_SMTP_PASSWORD`,
and avoid printing the environment in job logs.
