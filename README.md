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
- Logging and deferred delivery with retry support

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
  mailfire version            show version information
  mailfire export-templates <folder>  export editable built-in templates

Flags (send):
  --to <addr>        recipient, repeatable (required)
  --cc <addr>        cc recipient, repeatable
  --bcc <addr>       bcc recipient, repeatable
  --from <addr>      sender, overrides config ("Name <user@example.com>")
  --subject <text>   subject line (required)
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
  -c, --config <p>   use this config file
  -h, --help         show help

More help: https://github.com/thorstenkramm/mailfire
```

Every subcommand accepts `-h`/`--help` and prints its own usage and flags.

### Subcommands

- `mailfire send` composes and sends an email. This is the default when no subcommand is given.
- `mailfire validate` checks the configuration file, required settings, and whether Mailgun accepts the API key and domain.
- `mailfire resend` retries deferred messages, oldest first. Messages are removed only after a successful send.
  Messages deferred less than `resend_cooldown` ago are skipped; `--force` ignores the cooldown.
  `--list` shows deferred messages without sending, and `mailfire resend <id>...` retries specific messages.
- `mailfire export-templates <folder>` exports all four built-in templates as editable files. It creates the folder
  if needed and refuses to overwrite existing template files. No configuration or credentials are required.
- `mailfire version` prints the version, commit, and build date.

### Deferred delivery

If sending fails because of a network error, a Mailgun server error, or rate limiting, and `defer = true`
(the default), the composed message is stored in `defer_dir` and Mailfire exits with code 4.
Run `mailfire resend` (for example, from cron) to retry. Use `--no-defer` or `defer = false` to fail immediately instead.

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
ranked 3, 5, 4, 0; a message that fails with 4 stays deferred.

Errors are written to stderr and identify the failing step.

### Further documentation

- [Template guide and screenshots](templates.md)
- [Example configuration](mailfire.example.conf)
- [Linux package repositories](docs/linux-repositories.md)
- [Man page](docs/mailfire.1): run `man mailfire` after a package installation, or `man ./docs/mailfire.1` in a source checkout
- [Detailed requirements](docs/prd.md)
