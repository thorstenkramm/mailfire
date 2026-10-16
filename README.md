# Mailfire

Send beautiful email from the command line via the Mailgun API with ease.

## at a glance

`mailfire` is an API client for the Mailgun HTTP-API that enables you to send email from the command line.

```shell
mailfire --md --to user@example.com --subject "Have a nice day" <<EOF
# This is an Email

It is written in markdown. It's delivered as HTML.

[mailfire](https://github.com/thorstenkramm/mailfire)
EOF
```

The following formats are supported:
- `--txt`, plaintext, default 
- `--md`, Markdown rendered as HTML using a built-in template
- `--html`, HTML inserted into the built-in template, you don't have to care about the frame around your HTML
- `--html-raw`, raw HTML submitted, no base frame layout around it

The body is read from piped stdin, a regular file with `--file <path>`, or an explicit string with `--message <text>` (aliases `--m` and `-m`). File and message flags are mutually exclusive, even with empty values; either ignores stdin. Terminal stdin is never read. An empty body is allowed only with `--attach`; otherwise the error is `mail body missing: use stdin, --file or --message`. On success, the Mailgun message ID is printed.

For example: `mailfire --to user@example.com --subject "Your file" --message "See attached" --attach report.pdf`. Omit `--message` for an attachment-only mail from a terminal; piped input is still read when supplied.

Before sending, sender and all `--to`/`--cc`/`--bcc` mailboxes are checked for a dotted DNS domain with valid, nonempty labels (no leading/trailing hyphens). Quoted names/local parts, plus addresses and Unicode mailboxes are supported. These are syntax checks, without DNS lookups or delivery guarantees. Invalid input fails locally and is never deferred.

For `--md`, `--html`, and `--html-raw`, a multipart/alternative message with an HTML and a plain-text part
(derived from the body) is sent.
Local images referenced in Markdown (`![alt](path/to/image.png)`) are attached and shown inline;
remote image URLs are left untouched. `--preheader <text>` sets the hidden inbox preview text and
`--footer <path>` appends an HTML fragment as footer (both for `--md` and `--html` only).

`--md` and `--html` bodies are wrapped in one of four built-in templates: `card` (default), `card-serif`,
`plain` and `plain-serif`. Pick one per email with `--template <name>`, or change the default with
`template` in the `[sending]` section of the config file. Markdown supports
[GitHub-style alerts](https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/basic-writing-and-formatting-syntax#alerts)
(`> [!NOTE]`, `> [!TIP]`, `> [!IMPORTANT]`, `> [!WARNING]`, `> [!CAUTION]`).
See [templates.md](templates.md) for screenshots.

Sending attachments:
```shell
mailfire --to user@example.com --subject "The files" --attach ~/doc1.pdf --attach ~/image.png 
```

Recipients are given with repeatable `--to`, `--cc`, and `--bcc` flags (`user@example.com` or
`Name <user@example.com>`). Comma-separated lists are not supported, as commas are legal in display names.

Check your setup safely with `--test`: Mailgun accepts and validates the request but delivers nothing.
The message ID is then printed as `accepted in test mode, message id <id> (validated only, nothing was delivered)`.
```shell
mailfire --test --to user@example.com --subject "Test" <<< "Hello"
```

## Installation

Every [GitHub release](https://github.com/thorstenkramm/mailfire/releases) ships static binaries for
Linux, macOS (amd64 and arm64) and Windows (amd64), `.deb` and `.rpm` packages, and a `checksums.txt`.

**Homebrew** (macOS and Linux):

```shell
brew install thorstenkramm/tap/mailfire
```

**Scoop** (Windows):

```powershell
scoop bucket add thorstenkramm https://github.com/thorstenkramm/scoop-bucket
scoop install mailfire
```

**Debian / Ubuntu** and **Fedora / RHEL** (download the package from the release page):

```shell
sudo dpkg -i mailfire_<version>_linux_amd64.deb
sudo rpm -i mailfire_<version>_linux_amd64.rpm
```

**Binary**: download the archive for your platform, verify it against `checksums.txt` (`sha256sum -c
--ignore-missing checksums.txt`), and put `mailfire` into a directory on your `PATH`.

The packages install the man page (`man mailfire`) and bash/zsh completions. From an archive or
a source checkout, use `packaging/completions/mailfire.bash` (`source` it from `~/.bashrc`) and
`packaging/completions/mailfire.zsh` (save it as `_mailfire` in a directory on your `fpath`).

Or build from source with `make build` (Go required).

## Configuration

`mailfire` looks for its configuration in the following location in the given order. First hit wins.
- `./.mailfire.conf`, current directory
- `$HOME/.config/mailfire/mailfire.conf`, your home directory
- `/etc/mailfire/mailfire.conf`, system-wide configuration

On Windows:
- `.\.mailfire.conf`, current directory
- `%APPDATA%\mailfire\mailfire.conf`, your user profile
- `%PROGRAMDATA%\mailfire\mailfire.conf`, system-wide configuration

> [!WARNING]
> Using a system-wide configuration is usually insecure. Anyone can take the API key and use it anywhere.
> Use only with the utmost care!

On Linux and macOS, `mailfire` warns on startup when the config file is accessible by group or
others (permissions more permissive than `0600`); fix it with `chmod 600 <path>`. On Windows
this check is skipped — file access is governed by NTFS ACLs, not POSIX permission bits.

```toml
## mailfire configuration
[mailgun]
  ## Your API key, string, mandatory
  api_key = "<your-api-key>"

  ## mailgun region, string, mandatory
  ## It's either 'us' or 'eu'
  region = "eu"

[sending]
  ## Domain used for sending as configured on mailgun
  ## domain, string, mandatory
  domain = "example.com"

  ## From address, string, optional
  ## If not given, you must specify via --from for every email to be sent.
  ## Can be overwritten per email with --from
  ## Formats: "user@example.com" or "My Name <user@example.com>" 
  from = "My Name <user@example.com>"

  ## Email template for --md and --html, string, optional, default 'card'
  ## One of: card, card-serif, plain, plain-serif (see templates.md)
  ## Can be overwritten per email with --template
  template = "card"

[logging]
  ## Logfile, string, optional
  ## By default, logging is disabled
  ## If a logfile is given, all sending attempts and their status will be logged in JSON format
  #logfile = "~/logs/mailfire.log"
  ## On Windows (use a literal string so backslashes need no escaping):
  #logfile = 'C:\Users\me\AppData\Local\mailfire\mailfire.log'

  ## Loglevel, string, optional, default 'information'
  ## Supports:
  ## error: datetime, message-id, to, status code (only on errors, successful delivery is not logged)
  ## information: datetime, message-id, to, subject, status code
  ## debug: datetime, message-id, from, to, subject, message size, status code, api response body
  loglevel = "debug"

  ## To not flood your disk, logs are automatically truncated.
  ## If the max size is exceeded, the oldest log lines get removed.
  ## max_size, string, optional, default 50M
  max_size = "100M"

[defer]
  ## Failed sending attempts are by default deferred for later retrying.
  ## defer, boolean, default true
  defer = false

  ## Deferred email are stored by default in your home dir in $HOME/.local/share/mailfire
  ## (%LOCALAPPDATA%\mailfire on Windows)
  ## The directory is created on-the-fly
  ## defer_dir, path, string, optional
  defer_dir = "/var/tmp/mailfire/deferred"

  ## To not flood your disk, the defer_dir is automatically cleaned up.
  ## If the max size is exceeded, the oldest unsent emails get removed.
  ## max_size, string, optional, default 50M
  max_size = "100M"

  ## Cooldown before a deferred message is retried by 'mailfire resend'.
  ## Messages younger than this are skipped to avoid hammering Mailgun
  ## during an outage. Override per run with 'resend --force'.
  ## resend_cooldown, duration string, optional, default 5m
  resend_cooldown = "5m"
```

## full usage documentation

The help message explains full usage as follows:

```text
$ mailfire --help
mailfire — send email from the command line via the Mailgun API

Usage:
  mailfire [send] [flags]     compose and send an email (default)
  mailfire validate           check config and Mailgun credentials
  mailfire resend [--list | --force | <id>...]
                              retry deferred messages
  mailfire version            show version information

Flags (send):
  --to <addr>        recipient, repeatable (required)
  --cc <addr>        cc recipient, repeatable
  --bcc <addr>       bcc recipient, repeatable
  --from <addr>      sender, overrides config ("Name <user@example.com>")
  --subject <text>   subject line (required)
  --txt              body is plain text (default)
  --md               body is Markdown, rendered into the built-in template
  --html             body is an HTML fragment, wrapped in the template
  --html-raw         body is a complete HTML document, sent as-is
  --file <path>      read body from file instead of stdin
  --message <text>   explicit body; aliases --m, -m (conflicts with --file)
  --template <name>  HTML template: card, card-serif, plain, plain-serif
  --preheader <txt>  inbox preview text (HTML formats only)
  --footer <path>    HTML fragment appended as footer (HTML formats only)
  --attach <path>    attach a file, repeatable
  --test             use Mailgun test mode: validate the request, deliver nothing
  --no-defer         fail immediately instead of deferring on error
  -c, --config <p>   use this config file
  -h, --help         show help
```

Every subcommand accepts `-h`/`--help` and prints its own usage and flags.

## Subcommands

- `mailfire validate` checks, step by step, that the config file is found and valid TOML, that all
  mandatory keys are well-formed, and that the API key and domain are accepted by Mailgun. Mailgun
  has no ping route; `GET /v3/domains/{domain}` authenticates the key and confirms the domain in one call.
- `mailfire resend` retries deferred messages, oldest first. A message is removed only after successful
  delivery. Messages deferred less than `resend_cooldown` ago are skipped; `--force` ignores the cooldown.
  `--list` shows the deferred messages (ID, date, to, subject, size) without sending, and
  `mailfire resend <id>...` retries specific messages.
- `mailfire version` prints the version, commit, and build date.

## Deferral

If sending fails because of a network error, a Mailgun server error, or rate limiting, and `defer = true`
(the default), the composed message is stored in `defer_dir` and `mailfire` exits with code 4.
Run `mailfire resend` (for example from cron) to retry. Use `--no-defer` or `defer = false` to fail
immediately instead.

## Exit codes

| Code | Meaning                                                                |
|------|------------------------------------------------------------------------|
| 0    | Success (sent, validated, resent, or info shown)                       |
| 1    | Usage error (missing flags, unknown subcommand, conflicting formats)   |
| 2    | Configuration error (missing file, bad TOML, missing/invalid keys)     |
| 3    | Authentication error (API key rejected: 401/403 from Mailgun)          |
| 4    | Send failed and message was **deferred** for later resend              |
| 5    | Send failed and was **not** deferred (`--no-defer` or `defer = false`) |

Exit code 5 is also used when Mailgun rejects a message permanently (any other 4xx response), when a
`--test` send fails with a retryable error (test sends are never deferred), and when the message cannot be
stored in `defer_dir`. `mailfire validate` exits with 4 when Mailgun cannot be reached reliably (retry
later) and with 2 when Mailgun does not know the domain. `mailfire resend` returns the most severe outcome
of all attempts, ranked 3, 5, 4, 0; a message that fails with 4 stays deferred.

Errors are written to stderr in plain language, naming the failing step.

## Man page

A man page is available as [docs/mailfire.1](docs/mailfire.1); view it with `man ./docs/mailfire.1`.
The packages install it as `mailfire(1)`.

The requirements are specified in detail in [docs/prd.md](docs/prd.md).
