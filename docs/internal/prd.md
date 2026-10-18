# mailfire — Product Requirements Document

**Status:** Draft **Date:** 2026-10-15 **Author:** Thorsten Kramm

## 1. Overview

`mailfire` is a command-line client for the Mailgun HTTP API. It sends email
directly from the shell — from scripts, cron jobs, CI pipelines, or by hand —
in plain text, Markdown, or HTML. HTML output is wrapped in a built-in,
email-client-safe base template that works in Outlook, dark mode, and on
mobile.

**Vision:** `sendmail` for the API era. One static binary, one config file,
pipable stdin, done.

## 2. Goals and non-goals

### Goals

- Send email from the command line via the Mailgun API with minimal ceremony.
- Accept the message body on stdin, from a file (`--file`), or an explicit string (`--message`, aliases `--m` and `-m`).
- Render Markdown to a polished, responsive, dark-mode-aware HTML email.
- Support attachments, multiple recipients (to/cc/bcc), and custom senders.
- Defer failed sending attempts for later retry — never silently lose mail.
- Validate configuration and API key without sending a message.
- Run on Linux, macOS, and Windows as a single static binary.

### Non-goals

- Receiving, parsing, or forwarding email.
- Bulk/newsletter sending with list management, templates per recipient,
  or tracking/analytics (use Mailgun's own tooling for that).
- SMTP server or SMTP relay functionality.
- A GUI or TUI.

## 3. Target users and use cases

- **Sysadmins** piping cron output, backup reports, or alerts into email:
  `restic backup ... 2>&1 | mailfire --md --to ops@example.com --subject "Backup report"`.
- **Developers** sending notifications from scripts and CI jobs.
- **Power users** who want to fire off a nicely formatted email without
  opening a mail client.

## 4. Technology

| Concern            | Choice                                                                                                                                                                 |
|--------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Language           | Go (single static binary, strong template/HTTP stdlib)                                                                                                                 |
| Markdown rendering | [goldmark](https://github.com/yuin/goldmark) (CommonMark + tables)                                                                                                     |
| CSS inlining       | [go-premailer](https://github.com/vanng822/go-premailer) — inline the template's `<style>` block before sending so clients that strip `<style>` still render correctly |
| Base template      | `text/template` (not `html/template`, which strips the `<!--[if mso]>` comments Outlook needs); template embedded via `embed.FS`                                       |
| Config format      | TOML, parsed with [pelletier/go-toml](https://github.com/pelletier/go-toml)                                                                                            |
| CLI                | stdlib `flag` with subcommand dispatch                                                                                                                                 |
| Mailgun API        | v3, `POST /v3/{domain}/messages` (multipart/form-data)                                                                                                                 |

Mailgun base URL by region:

- `us` → `https://api.mailgun.net`
- `eu` → `https://api.eu.mailgun.net`

Authentication: HTTP Basic Auth, user `api`, password = API key.

## 5. Functional requirements

### FR-1 — Sending

Synopsis: `mailfire [send] [flags]` (no subcommand defaults to `send`).

- Body is read from nonterminal stdin unless `--file <path>` or `--message <text>` is explicitly given. These flags are mutually exclusive even with empty values; both ignore stdin. Body files must be regular files, checked before opening to reject FIFOs without blocking.
- Never read interactive terminal stdin. Empty bodies are allowed only with attachments; otherwise fail locally with `mail body missing: use stdin, --file or --message`. Piped stdin is still consumed with attachments. Attachment-only requests include an empty text field when there is no HTML body.
- Validate every sender and recipient mailbox before dispatch: require a dotted DNS domain with valid nonempty labels, no leading/trailing hyphens. Preserve quoted display names/local parts, plus addresses and Unicode support. No DNS/network validation; syntax validity does not promise delivery. Usage errors are never deferred.
- Exactly one body format per invocation:
    - `--txt` — plain text (default)
    - `--md` — Markdown, rendered to HTML and wrapped in the base template
    - `--html` — HTML fragment, wrapped in the base template
    - `--html-raw` — full HTML document, sent as-is
- Required: at least one `--to`, and `--subject`.
- Recipients: `--to`, `--cc`, `--bcc`, each repeatable, each accepting
  `user@example.com` or `Name <user@example.com>`. **(Decision Q1)**
  Flags are repeatable only — comma-separated lists are *not* supported,
  because display names may legitimately contain commas
  (`"Doe, John <john@x.com>"`).
- `--from` overrides the configured default sender.
- `--attach <path>` is repeatable; attachments are sent as multipart parts.
- `--preheader <text>` sets the hidden inbox preview text (HTML formats only).
- `--footer <path>` appends an HTML fragment as the template footer (HTML formats only).
- For `--md` and `--html` a multipart/alternative message is generated with
  both the rendered HTML and a plain-text part derived from the source.
- Local images referenced in Markdown — `![alt](path/to/image.png)` — are
  attached as inline images and referenced via `cid:` (MIME
  multipart/related), so they display inline in the mail client.
  **(Decision Q4)** Remote image URLs (`https://...`) are left untouched.
- `--test` sends with Mailgun's test mode (`o:testmode=yes`): the API
  accepts and validates the request but delivers nothing. Allows safe
  end-to-end verification of config and payload. **(Decision Q5)**
  Deferred `resend` never uses test mode.
- **(Decision Q2)** There is deliberately *no* `--dry-run` flag; rendering
  is verified with `--test` or by sending to oneself.
- On success, print the Mailgun message ID to stdout and exit 0.

### FR-2 — Configuration

`mailfire` reads TOML config from the first existing location, in order:

| Platform    | Locations (first hit wins)                                                                     |
|-------------|------------------------------------------------------------------------------------------------|
| Linux/macOS | `./.mailfire.conf`, `$HOME/.config/mailfire/mailfire.conf`, `/etc/mailfire/mailfire.conf`      |
| Windows     | `.\.mailfire.conf`, `%APPDATA%\mailfire\mailfire.conf`, `%PROGRAMDATA%\mailfire\mailfire.conf` |

`--config <path>` overrides the search.

Schema (see README for the annotated example):

- `[mailgun]` `api_key` (required), `region` (`us`|`eu`, required)
- `[sending]` `domain` (required), `from` (optional; then `--from` is mandatory),
  `template` (`card`|`card-serif`|`plain`|`plain-serif`, default `card`; see FR-7)
- `[logging]` `logfile` (optional; logging disabled without it),
  `loglevel` (`error`|`information`|`debug`, default `information`),
  `max_size` (default `50M`)
- `[defer]` `defer` (default `true`), `defer_dir` (default per-platform data
  dir, e.g. `$HOME/.local/share/mailfire` or `%LOCALAPPDATA%\mailfire`),
  `max_size` (default `50M`), `resend_cooldown` (default `5m`; see FR-4)

Security:

- Warn on startup if the config file is readable by group/others (permissions more permissive than `0600`).
- The API key must never appear in logs, error messages, or the logfile.

### FR-3 — Logging

- Disabled by default; enabled by setting `[logging].logfile`.
- One JSON object per line (JSONL), one per sending attempt.
- Fields per level:
    - `error`: timestamp, message ID, to, status code — failures only
    - `information`: timestamp, message ID, to, subject, status code
    - `debug`: timestamp, message ID, from, to, subject, message size,
      status code, API response body
- The logfile is truncated in place when `max_size` is exceeded: the oldest
  lines are dropped, never the newest.

### FR-4 — Deferral and resend

- If sending fails (network error, 5xx, rate limit) and `defer = true`,
  the fully composed message (MIME + metadata JSON) is stored in
  `defer_dir` and `mailfire` exits with a distinct exit code (see §8).
- With `defer = false` or `--no-defer`, failure is reported and nothing is
  stored.
- `mailfire resend` retries all deferred messages oldest-first; a message is
  removed from `defer_dir` only after successful delivery.
- **(Decision Q3)** `resend` applies a cooldown: messages deferred less than
  `resend_cooldown` ago (default `5m`, configurable in `[defer]`) are
  skipped, to avoid hammering Mailgun during an outage or rapid duplicate
  retries from cron. `resend --force` ignores the cooldown.
- `mailfire resend --list` shows deferred messages (ID, date, to, subject,
  size) without sending.
- `mailfire resend <id>...` retries specific messages.
- When `defer_dir` exceeds `max_size`, the oldest unsent messages are
  dropped; each drop is logged (if logging is enabled) and reported on
  stderr.

### FR-5 — Validation

`mailfire validate` checks, in order, and reports each step:

1. Config file found and parses as valid TOML.
2. All mandatory keys present and well-formed (region ∈ {us, eu},
   domain non-empty, from address valid under the shared mailbox syntax check).
3. API key and domain valid against Mailgun, via
   `GET /v3/domains/{domain}` (Mailgun has no dedicated ping route; this
   endpoint authenticates the key and confirms the domain in one call).

Exit 0 if all checks pass, non-zero otherwise (see §8).

### FR-6 — Version

`mailfire version` prints name, version, commit, and build date, e.g.
`mailfire 0.3.1 (commit a1b2c3d, built 2026-10-15)`. Version is injected at
build time via `-ldflags -X`.

### FR-7 — Templates

- Ships four embedded templates rendered with `text/template`: two layouts,
  `templates/card.html` (600 px white card centered on a gray page) and
  `templates/plain.html` (no background, left aligned, flexible width up to
  640 px), each in a sans-serif and a serif variant: `card` (default),
  `card-serif`, `plain`, `plain-serif`. Shared head and styles live in
  `templates/head.html`. Screenshots: `docs/content/guides/templates.md`.
- The template is chosen per message with `--template <name>`, else by
  `[sending] template`, else `card`. `--template` requires `--md` or `--html`.
- Font stacks start with fonts preinstalled on macOS/iOS, Windows, Android
  and Linux (system-ui/Segoe UI/Roboto, Charter/Sitka/Cambria); Arial and
  Times New Roman are only last-resort fallbacks.
- Markdown supports GitHub-style alerts (`> [!NOTE]`, `[!TIP]`,
  `[!IMPORTANT]`, `[!WARNING]`, `[!CAUTION]`) at the top level, rendered as
  one-cell tables styled by each template; the text part shows them as
  `> NOTE` quotes.
- Template fields: `Lang`, `Subject`, `Preheader`, `Body`, `Footer`.
- Must render correctly in Outlook (MSO conditionals, table layout,
  600/640 px column), dark mode (`prefers-color-scheme`), and small screens (≤ 600 px media query).
- The `<style>` block is inlined with go-premailer before sending; the block
  itself remains for clients and media queries that need it.

## 6. Non-functional requirements

- **Distribution:** single static binary per platform; no runtime deps.
- **Performance:** cold start < 100 ms; sending dominated by API latency.
- **Reliability:** a failed send must never lose the message when deferral
  is enabled.
- **Security:** API key only on disk in the config file (advised `0600`),
  never in logs, arguments of spawned processes, or error output.
- **Privacy:** no telemetry, no third-party calls besides Mailgun.

## 7. CLI reference

```text
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

## 8. Error handling and exit codes

| Code | Meaning                                                                |
|------|------------------------------------------------------------------------|
| 0    | Success (sent, validated, resent, or info shown)                       |
| 1    | Usage error (missing flags, unknown subcommand, conflicting formats)   |
| 2    | Configuration error (missing file, bad TOML, missing/invalid keys)     |
| 3    | Authentication error (API key rejected: 401/403 from Mailgun)          |
| 4    | Send failed and message was **deferred** for later resend              |
| 5    | Send failed and was **not** deferred (`--no-defer` or `defer = false`) |

Errors are written to stderr in plain language, with the failing step named (e.g.
`config: parsing /etc/mailfire/mailfire.conf: line 12: ...`).

## 9. Milestones

### Linux distribution requirements (task 22)

- Publish stable GitHub release DEB/RPM packages to the public Cloudsmith repository
  `thorstenkramm/mailfire`, for amd64 and arm64, without rebuilding the assets.
- Verify GitHub SHA-256 checksums before upload; preserve original-hash provenance
  when Cloudsmith signs RPMs. Never overwrite an existing version with different input.
- Provide signed APT/DNF installation, native architecture smoke tests, and manual
  retry of a partially published release. Downloads require no credentials.
- Homebrew distribution is macOS-only; its implementation is a later part of task 22.

### Delivery milestones

1. **MVP:** `send` with `--txt`, config loading, Mailgun API call, exit codes.
2. **Formats:** `--md`, `--html`, `--html-raw`, template rendering, CSS
   inlining, multipart/alternative, attachments, preheader, footer.
3. **Robustness:** logging with levels and truncation, deferral, `resend`.
4. **Polish:** `validate`, `version`, Windows support, packaging (Homebrew, Scoop, .deb/.rpm), man page, shell
   completions.

## 10. Decisions log

Resolved 2026-10-15; the outcomes are integrated into the requirements above.

| # | Question | Decision |
| --- | --- | --- |
| Q1 | Comma-separated recipient lists in `--to`/`--cc`/`--bcc`? | **No** — flags are repeatable only; commas are legal in display names (FR-1) |
| Q2 | `--dry-run` flag printing the composed MIME without sending? | **No** — use `--test` or send to yourself instead (FR-1) |
| Q3 | Backoff in `resend` to avoid hammering Mailgun during outages? | **Yes** — cooldown, default `5m`, configurable via `[defer].resend_cooldown`, overridable with `--force` (FR-4) |
| Q4 | Inline images via `cid:` for local files in Markdown? | **Yes** — `![...](local.png)` is attached and inlined via multipart/related (FR-1) |
| Q5 | Mailgun test mode (`o:testmode=yes`) via `--test`? | **Yes** (FR-1) |
