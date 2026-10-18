<p align="center"><img src="docs/logo.png" alt="mailfire logo: an orange flame with a cut-out envelope, next to the word mailfire" width="640"></p>

# Mailfire

[![CI](https://github.com/thorstenkramm/mailfire/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/thorstenkramm/mailfire/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/thorstenkramm/mailfire)](https://github.com/thorstenkramm/mailfire/releases/latest)
[![Go version](https://img.shields.io/github/go-mod/go-version/thorstenkramm/mailfire)](https://github.com/thorstenkramm/mailfire/blob/main/go.mod)
[![License](https://img.shields.io/github/license/thorstenkramm/mailfire)](LICENSE)

Send plain-text, Markdown, and HTML email from the command line through the Mailgun API.

- Four built-in email templates, plus support for custom templates
- Attachments, inline Markdown images, and GitHub-style alerts
- Test mode to validate requests without delivering email
- Local HTML preview without credentials or network access
- Logging and deferred delivery with retry support
- Opt-in deduplication of repeated notifications
- JSON output for scripts and monitoring

[Installation](#installation) · [Quick start](#quick-start) · [Features and usage](#features-and-usage) ·
[Configuration](#configuration) · [Command reference](#command-reference)

[![The four built-in email templates: card, card-serif, plain, and plain-serif](docs/templates/overview.webp)](templates.md#screenshots)

## Installation

Every [GitHub release](https://github.com/thorstenkramm/mailfire/releases) includes binaries for Linux and macOS
(amd64 and arm64), Windows (amd64), DEB and RPM packages, and a `checksums.txt` file.

### macOS: Homebrew

Install on Apple Silicon or Intel Macs using [Homebrew](https://brew.sh):

```shell
brew tap thorstenkramm/tap
brew trust --cask thorstenkramm/tap/mailfire
brew install --cask mailfire
mailfire version
```

The cask includes the binary, man page, and Bash/Zsh completions.

The macOS binary is not Apple-notarized. If Gatekeeper blocks it and you trust this release,
remove the quarantine attribute from the installed binary, then retry:

```shell
xattr -d com.apple.quarantine "$(realpath "$(command -v mailfire)")"
mailfire version
```

To update:

```shell
brew update
brew upgrade --cask mailfire
```

### Linux: Cloudsmith repositories

Install and update Mailfire through our public Cloudsmith repository (amd64 and arm64).
No Cloudsmith account or API key is needed.

Follow the repository setup instructions for your distribution:

- [Debian / Ubuntu (APT)](docs/linux-repositories.md#debian--ubuntu)
- [Fedora / RPM (DNF)](docs/linux-repositories.md#fedora--rpm)

The guide covers signing keys, repository configuration, package restrictions, updates, and removal.
If `apt update` reports `NO_PUBKEY B43413AD5FA82F43`, see
[Manual setup and recovery](docs/linux-repositories.md#manual-setup-and-recovery).

### Manual download

Download the archive or package for your platform from the
[release page](https://github.com/thorstenkramm/mailfire/releases/latest) and verify its checksum against `checksums.txt`.
On Linux, use `sha256sum -c --ignore-missing checksums.txt`; on macOS, use `shasum -a 256 <file>`
and compare the result with the corresponding entry.

For **Debian / Ubuntu**, install the downloaded DEB package (replace `<version>` and select your architecture):

```shell
sudo dpkg -i mailfire_<version>_linux_amd64.deb
```

For **Fedora / RHEL**, use the RPM package instead:

```shell
sudo rpm -i mailfire_<version>_linux_amd64.rpm
```

For a **binary archive**, extract it and put `mailfire` (or `mailfire.exe` on Windows) in a directory on your `PATH`.
The macOS Gatekeeper workaround above also applies to downloaded binaries.

Packages include the man page and Bash/Zsh completions. Archives include these under `man/` and `completions/`.
To install completions manually, source `completions/mailfire.bash` from `~/.bashrc`, or save
`completions/mailfire.zsh` as `_mailfire` in a directory on your Zsh `fpath`.
In a source checkout, these files are under `packaging/completions/`.

Scoop publishing is planned for a later release.

### Build from source

With Go 1.27.2 or newer installed, run `make build` from a source checkout.

## Quick start

You need a Mailgun API key and a sending domain configured in Mailgun.

### 1. Configure Mailfire

Create `.mailfire.conf` in your current directory with your API key, region (`us` or `eu`), sending domain, and sender:

```toml
[mailgun]
api_key = "<your-api-key>"
region = "eu"

[sending]
domain = "example.com"
from = "My Name <user@example.com>"
```

On Linux and macOS, restrict access to your credentials:

```shell
chmod 600 .mailfire.conf
```

For all available settings, see [mailfire.example.conf](mailfire.example.conf).
Copy it to `.mailfire.conf` if you prefer to start with the annotated example.

### 2. Validate your setup

```shell
mailfire validate
mailfire --test --to user@example.com --subject "Test" --message "Hello"
```

Replace the recipient with your own address. `--test` asks Mailgun to validate the request without delivering it.
The output explicitly says that nothing was delivered.

### 3. Send an email

```shell
mailfire --md --to user@example.com --subject "Have a nice day" <<EOF
# This is an email

It is written in Markdown and delivered as HTML.

[Mailfire](https://github.com/thorstenkramm/mailfire)
EOF
```

This example uses Bash/Zsh heredoc syntax. On other shells, use `--file` or `--message` instead.
On success, Mailfire prints the Mailgun message ID.

## Features and usage

### Email formats

| Flag | Body format |
|------|-------------|
| `--txt` | Plain text (default) |
| `--md` | Markdown rendered as HTML using the selected template |
| `--html` | HTML fragment wrapped in the selected template |
| `--html-raw` | Complete HTML document sent unchanged |

Choose one format per email. All HTML formats include a plain-text alternative derived from the body.

### Email body and attachments

Supply the body through one of these sources:

- Piped standard input (stdin)
- A regular file with `--file <path>`
- An explicit string with `--message <text>` (aliases `--m` and `-m`)

`--file` and `--message` are mutually exclusive, even with empty values. Either option ignores stdin.
Mailfire never reads interactively from terminal stdin. An empty body is allowed only when an attachment is supplied.

```shell
mailfire --to user@example.com --subject "Your file" --message "See attached" --attach report.pdf
```

Repeat `--attach` to include more files. For an attachment-only email from a terminal, omit `--message`.
Piped input is still read when supplied.

### Templates

For `--md` and `--html`, choose from `card` (default), `card-serif`, `plain`, and `plain-serif`:

```shell
mailfire --md --template plain --to user@example.com --subject "Report" --file report.md
```

To customize a built-in template, export it and edit its HTML and CSS:

```shell
mailfire export-templates ./my-templates
mailfire --md --template-file ./my-templates/card.html --to user@example.com --subject "Report" --file report.md
```

You can also write your own template. `--template` and `--template-file` are mutually exclusive.
Set `template` or `template_file` in the `[sending]` configuration section to change the default;
either command-line option overrides it.

`--preheader <text>` sets the hidden inbox preview text. `--footer <path>` appends an HTML fragment as a footer.
Both options require `--md` or `--html`.

See the [template guide](templates.md) for custom template syntax, styling, and screenshots.

### Markdown alerts and inline images

Markdown supports [GitHub-style alerts](https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/basic-writing-and-formatting-syntax#alerts):
`> [!NOTE]`, `> [!TIP]`, `> [!IMPORTANT]`, `> [!WARNING]`, and `> [!CAUTION]`.

Local images referenced in Markdown (`![alt](path/to/image.png)`) are attached and displayed inline.
Remote image URLs are left unchanged.

### Local preview

`--preview <path>` renders the email body to an HTML file instead of sending it. Use it to check a template, footer,
or Markdown layout before mailing anyone:

```shell
mailfire --md --template-file ./my-templates/card.html --footer footer.html \
  --subject "Report" --file report.md --preview report.html
```

The preview uses the same Markdown rendering, template, preheader, footer, and CSS inlining as a send. Preview and
`--test` differ:

| | `--preview` | `--test` |
|---|---|---|
| Contacts Mailgun | No | Yes, Mailgun validates the request |
| Needs a configuration and API key | No | Yes |
| Needs `--to` and `--subject` | No | Yes |
| Output | Rendered HTML body | Mailgun message ID |

A preview needs no configuration file, Mailgun settings, recipients, sender, or subject. Only the template selection
is read from a configuration file, `MAILFIRE_SENDING_TEMPLATE`, or `MAILFIRE_SENDING_TEMPLATE_FILE`; other settings
and variables are ignored. Supplied addresses and subjects are
still checked. Nothing is sent, deferred, deduplicated, or logged.

- `--md` and `--html` produce the rendered HTML part. Local Markdown images are embedded as `data:` URLs, so the
  preview is one portable file; remote image URLs are unchanged. A send attaches local images instead.
- `--txt` shows the escaped body in a `<pre>` block.
- `--html-raw` writes the supplied document unchanged.

An existing output file is replaced, keeping its permissions, but only after rendering succeeded; a failed preview
leaves the previous file untouched. A symbolic link as output is followed; a link to a missing file is refused and left
unchanged. Mailfire refuses an output that is one of the inputs (body file or standard input redirected from a file,
footer, template, Markdown image, or configuration file), including through symbolic or hard links.
Use `--preview -` to write only the HTML to stdout; diagnostics go to stderr. To write a file named `-`, use `./-`.

`--attach`, `--test`, `--no-defer`, `--timeout`, `--dedup-key`, and `--cooldown` only affect sending and are rejected
with `--preview`. `--json` is rejected too, because a preview writes HTML, not a command result. Plain-text parts, headers, and MIME/EML export are not part of the preview.

### Recipients

Use repeatable `--to`, `--cc`, and `--bcc` flags. Addresses can be plain (`user@example.com`) or include a
display name (`"Name <user@example.com>"`). Quote display names when passing them to the shell.
Comma-separated recipient lists are not supported because commas are valid in display names.

Sender and recipient addresses must have dotted DNS domains with valid, nonempty labels and no leading or trailing
hyphens. Quoted names and local parts, plus addressing, and Unicode addresses are supported.
These are syntax checks; Mailfire does not perform DNS lookups or guarantee delivery.
Invalid addresses are rejected locally and are never deferred.

## Configuration

See [mailfire.example.conf](mailfire.example.conf) for all settings, defaults, and examples, including
custom templates, logging, and deferred delivery. The [quick start](#quick-start) shows the minimum setup.

On Linux and macOS, Mailfire checks these locations in order and uses the first existing file:

1. `./.mailfire.conf` — current directory
2. `$HOME/.config/mailfire/mailfire.conf` — user configuration
3. `/etc/mailfire/mailfire.conf` — system-wide configuration

On Windows:

1. `.\.mailfire.conf` — current directory
2. `%APPDATA%\mailfire\mailfire.conf` — user configuration
3. `%PROGRAMDATA%\mailfire\mailfire.conf` — system-wide configuration

Use `-c <path>` or `--config <path>` to select a different file.

Every setting can also be supplied through a `MAILFIRE_<SECTION>_<KEY>` environment variable, such as
`MAILFIRE_MAILGUN_API_KEY` or `MAILFIRE_DEFER_MAX_SIZE`. Command-line options override environment variables, which
override the configuration file. Empty variables are ignored. Without a configuration file, the environment must supply
the API key, region, and domain. See the [environment variable reference](docs/environment.md).

Mailgun requests time out after 30 seconds by default (per request, covering connection setup, upload, and
response reading). Change this with `timeout` in the `[mailgun]` section, or with `--timeout <duration>`
(for example `15s` or `1m`) on `send`, `resend`, and `validate`; the flag overrides the configuration.
A `resend` run retrying several messages may take longer than a single timeout.

Anyone who can read the configuration file can use its API key. On Linux and macOS, Mailfire warns if the file
is accessible by group or others; restrict access with `chmod 600 <path>`.
On Windows, configure access through NTFS permissions; the Unix permission check does not apply.

## Command reference

```text
$ mailfire --help
mailfire — send email from the command line via the Mailgun API

Usage:
  mailfire [send] [flags]     compose and send an email (default)
  mailfire validate           check config and Mailgun credentials
  mailfire resend [--list | --force | <id>...]
                              retry deferred messages
  mailfire queue list|show|delete|stats
                              inspect and delete deferred messages
  mailfire version            show version information
  mailfire export-templates <folder>  export editable built-in templates

Flags (send):
  --to <addr>        recipient, repeatable (required to send)
  --cc <addr>        cc recipient, repeatable
  --bcc <addr>       bcc recipient, repeatable
  --from <addr>      sender, overrides config ("Name <user@example.com>")
  --subject <text>   subject line (required to send)
  --txt              body is plain text (default)
  --md               body is Markdown, rendered into the template
  --html             body is an HTML fragment, wrapped in the template
  --html-raw         body is a complete HTML document, sent as-is
  --file <path>      read body from file instead of stdin
  --message <text>   explicit body; aliases --m, -m (conflicts with --file)
  --template <name>  HTML template: card, card-serif, plain, plain-serif
  --template-file <path>  custom HTML template (conflicts with --template)
  --preheader <txt>  inbox preview text (HTML formats only)
  --footer <path>    HTML fragment appended as footer (HTML formats only)
  --attach <path>    attach a file, repeatable
  --test             use Mailgun test mode: validate the request, deliver nothing
  --no-defer         fail immediately instead of deferring on error
  --timeout <dur>    per-request timeout (e.g. 15s, 1m); overrides mailgun.timeout
  --dedup-key <key>  skip repeats of this notification within the cooldown
  --cooldown <dur>   suppression window for --dedup-key (default 1h)
  --preview <path>   write the rendered HTML to <path> (- for stdout); send nothing
  --json             print the result as one JSON document (conflicts with --preview)
  -c, --config <p>   use this config file
  -h, --help         show help

More help: https://github.com/thorstenkramm/mailfire
```

Every subcommand accepts `-h`/`--help` and prints its own usage and flags.

### Subcommands

- `mailfire send` composes and sends an email. This is the default when no subcommand is given.
- `mailfire validate` checks the configuration file, required settings, and whether Mailgun accepts the API key and domain.
  It reports which settings came from environment variables without showing the API key.
- `mailfire resend` retries deferred messages, oldest first. Messages are removed only after a successful send.
  Messages that are not yet eligible for a retry are skipped (see [Deferred delivery](#deferred-delivery));
  `--force` ignores the wait. `--list` shows deferred messages without sending, and `mailfire resend <id>...`
  retries specific messages.
- `mailfire queue` inspects and deletes deferred messages without sending: `queue list` shows each message with its
  attempt count and next retry, `queue show <id>` shows one message's recipients, subject, age, attempts, last error,
  and next retry (`--raw` prints the stored request instead), `queue stats` shows totals, and
  `queue delete <id>...` deletes the given messages. If any ID is unknown, `queue delete` deletes nothing.
- `mailfire export-templates <folder>` exports all four built-in templates as editable files. It creates the folder
  if needed and refuses to overwrite existing template files. No configuration or credentials are required.
- `mailfire version` prints the version, commit, and build date.

### Deferred delivery

If sending fails because of a network error, a Mailgun server error, or rate limiting, and `defer = true`
(the default), the composed message is stored in `defer_dir` and Mailfire exits with code 4.
Run `mailfire resend` (for example, from cron) to retry. Use `--no-defer` or `defer = false` to fail immediately instead.

A deferred message becomes eligible for `mailfire resend` once `resend_cooldown` (default `5m`) has passed. Each
failed retry doubles the wait, measured from the latest attempt and capped at one hour; a `resend_cooldown` longer
than one hour is used unchanged. `--force` retries immediately. Messages that Mailgun rejects permanently stay in
`defer_dir`, marked as failed, and are skipped by later runs; retry one with `mailfire resend --force <id>` or remove
it with `mailfire queue delete <id>`. The stored message itself is never changed; only its retry information is.

Messages are kept until they are sent or deleted, subject to `max_size`. Set `max_age` in `[defer]` (for example
`"168h"`) to remove older messages automatically whenever a message is deferred or `mailfire resend` runs; each
removal is reported on stderr and in the logfile. `mailfire resend` checks the age again right before each retry, so
a message that exceeds `max_age` during a run is removed rather than sent. `mailfire queue list`, `show`, and `stats`
never remove anything.

Concurrent Mailfire processes coordinate through a lock file in `defer_dir`, so overlapping `resend` and
`queue delete` runs cannot send or delete the same message twice. Exactly-once delivery is still not guaranteed:
if a request times out or the connection drops after Mailgun accepted the message, it stays deferred and a later
`resend` may deliver it again.

### Notification deduplication

Monitoring checks and cron jobs often report the same problem again and again. Pass `--dedup-key <key>` to send a
notification only once per cooldown:

```shell
mailfire --dedup-key disk-full-db1 --cooldown 30m \
  --to ops@example.com --subject "Disk full on db1" --message "df: 98%"
```

A notification is identified by the key, the sending domain, and all recipients (to, cc, and bcc, compared
case-insensitively in any order); different keys, domains, or recipients never interfere. A repeat is skipped with
exit code 0 and a `skipped: ...` line on stdout that says when it becomes eligible again, while:

- the same notification was delivered within its cooldown. The cooldown is set by `--cooldown`, else `cooldown` in
  `[dedup]`, default `1h`; the delivered send's value applies, and skipped repeats do not extend it, so a persistent
  problem is reported once per cooldown.
- the same notification is still deferred, even if Mailgun rejected it permanently (retry it with
  `mailfire resend --force <id>` or remove it with `mailfire queue delete <id>`). The first deferred message is kept;
  no duplicate is queued.
- an attempt was interrupted, for example by a crash, less than a minute ago and may have been delivered. Mailfire
  then prefers a possible duplicate over a lost alert.

Only a delivery starts the cooldown, by `send` or by a successful `mailfire resend` of the deferred message. Failed
and deferred sends do not, and `--test` sends neither check nor start it. If the system clock moves backward, affected
cooldowns end immediately. A concurrent send of the same notification waits until the first one's outcome is recorded.

The state is stored in `state_dir` in `[dedup]` (default `$HOME/.local/share/mailfire/dedup`, or
`%LOCALAPPDATA%\mailfire\dedup` on Windows), independently of the deferral settings. Inside `defer_dir`, only its
subdirectory `dedup` may be used (symbolic links are resolved for this check). Expired entries are removed
automatically. Corrupt or unreadable state is an error and nothing is sent: `send` exits with code 2. `mailfire
resend` keeps a deferred notification whose state it cannot use (unsafe `state_dir`, corrupt or unreadable state, a
lock failure) queued without sending it, reports it as failed, and exits with code 5. Remove the named file to reset
that notification.

To send a repeat anyway, omit `--dedup-key`. `mailfire resend --force` is unrelated: it ignores the retry wait of
deferred messages, not the notification cooldown.

### JSON output

Add `--json` to `send`, `resend` (including `--list`), `queue list|show|delete|stats`, `validate`, `version`, or
`export-templates` to get exactly one JSON document on stdout instead of the human-readable output:

```shell
mailfire --json --to ops@example.com --subject "Backup done" --message "ok" | jq -r .message_id
mailfire resend --json | jq '.summary'
mailfire queue list --json | jq -r '.messages[] | select(.status == "failed") | .id'
```

Every document has `schema_version`, `command`, `outcome`, and `exit_code`; failures add an `error` object with a
stable `code` such as `auth_failed`. Diagnostics and warnings stay on stderr, exit codes are unchanged, and
`--help` always prints text. API keys and message content are never included, except the stored request with
`queue show --raw --json`. See the [JSON output reference](docs/json-output.md) for the schema, all fields and
error codes, and compatibility guarantees.

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success (sent, validated, resent, templates exported, or information shown) |
| 1 | Usage error (missing flags, unknown subcommand, conflicting formats) |
| 2 | Configuration error (missing file, invalid TOML, missing or invalid keys) |
| 3 | Authentication error (API key rejected: 401/403 from Mailgun) |
| 4 | Send failed and message was deferred for a later retry |
| 5 | Send failed and was not deferred (`--no-defer` or `defer = false`) |

Exit code 5 is also used when Mailgun rejects a message permanently (any other 4xx response), when a
`--test` send fails with a retryable error (test sends are never deferred), and when the message cannot be stored
in `defer_dir`. `mailfire validate` exits with 4 when Mailgun cannot be reached reliably (retry later), and with 2
when Mailgun does not know the domain. `mailfire resend` returns the most severe outcome of all attempts,
ranked 3, 5, 4, 0; a message that fails with 4 stays deferred. `mailfire queue` exits with 1 for an unknown
message ID and with 5 when `defer_dir` cannot be read or a message cannot be deleted. A send skipped by
`--dedup-key` exits with 0; unusable deduplication state exits with 2. `--preview` exits with 1 when rendering or
writing the output fails, and with 2 for an invalid configuration file or template setting.

Errors are written to stderr and identify the failing step.

### Further documentation

- [Template guide and screenshots](templates.md)
- [Example configuration](mailfire.example.conf)
- [Environment variable reference](docs/environment.md)
- [JSON output reference](docs/json-output.md)
- [Linux package repositories](docs/linux-repositories.md)
- [Man page](docs/mailfire.1): run `man mailfire` after a package installation, or `man ./docs/mailfire.1` in a source checkout
- [Detailed requirements](docs/prd.md)
