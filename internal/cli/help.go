package cli

import "strings"

const moreHelp = "\nMore help: https://github.com/thorstenkramm/mailfire\n"

// usageText is the top-level help printed by "mailfire --help". It must
// match, word for word, the help block under "full usage documentation"
// in README.md and PRD §7; TestUsageMatchesREADME pins them together.
const usageText = `mailfire — send email from the command line via Mailgun, Gmail, or SMTP

Usage:
  mailfire [send] [flags]     compose and send an email (default)
  mailfire validate           check configuration and provider credentials
  mailfire auth login|status|refresh|logout --provider gmail
                              manage Gmail OAuth authorization
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
  --test             Mailgun test mode: validate only (rejected for SMTP and Gmail)
  --no-defer         fail immediately instead of deferring on error
  --timeout <dur>    per-request timeout (e.g. 15s, 1m); overrides provider timeout
  --dedup-key <key>  skip repeats of this notification within the cooldown
  --cooldown <dur>   suppression window for --dedup-key (default 1h)
  --preview <path>   write the rendered HTML to <path> (- for stdout); send nothing
  --json             print the result as one JSON document (conflicts with --preview)
  -c, --config <p>   use this config file
  -h, --help         show help
` + moreHelp

// sendUsage is printed by "mailfire send --help" and on send usage errors.
// Its flags section is cut from usageText so the two cannot drift apart.
var sendUsage = `Usage:
  mailfire [send] [flags]

Compose and send an email. Read piped stdin unless --file or --message is given.
Terminal stdin is never read. An empty body requires at least one attachment.
Sender and recipient domains must be dotted DNS names (no DNS lookup).

With --preview, render the body locally instead of sending: no provider
credentials, recipients, sender, or subject are needed, and nothing is sent,
deferred, deduplicated, or logged. Local Markdown images are embedded.

` + usageText[strings.Index(usageText, "Flags (send):"):]

// resendUsage is printed by "mailfire resend --help" and on resend usage
// errors.
const resendUsage = `Usage:
  mailfire resend [--list | --force | <id>...]

Retry deferred messages. Without arguments, every eligible deferred message
is retried; with <id>..., only those messages. A message becomes eligible
resend_cooldown after it was deferred; each failed retry doubles the wait,
up to one hour. Permanently rejected messages are kept but only retried
with --force <id>. The timeout is per request: retrying several messages
may take longer than a single timeout.

Flags (resend):
  --list             show deferred messages (ID, date, to, subject, size)
  --force            ignore the cooldown and backoff; with <id>, also retry
                     permanently rejected messages
  --timeout <dur>    per-request timeout (e.g. 15s, 1m); overrides provider timeout
  --json             print the result (or the --list listing) as one JSON document
  -c, --config <p>   use this config file
  -h, --help         show help
` + moreHelp

// queueUsage is printed by "mailfire queue --help" and on queue usage
// errors.
const queueUsage = `Usage:
  mailfire queue list                 list deferred messages and retry state
  mailfire queue show [--raw] <id>    show one message's metadata
  mailfire queue delete <id>...       delete the given messages
  mailfire queue stats                show queue totals

Inspect and delete deferred messages. list, show, and stats never send,
change, or remove messages. delete removes nothing if any ID is unknown.
Use mailfire resend to retry.

Flags (queue):
  --raw              show: print the stored request (message content)
  --json             print the result as one JSON document (with --raw, the
                     stored request is included base64-encoded)
  -c, --config <p>   use this config file
  -h, --help         show help
` + moreHelp

// validateUsage is printed by "mailfire validate --help" and on validate
// usage errors.
const validateUsage = `Usage:
  mailfire validate [flags]

Check the configuration (config file and MAILFIRE_* environment variables)
and the selected provider credentials without sending mail.

Flags (validate):
  --timeout <dur>    per-request timeout (e.g. 15s, 1m); overrides provider timeout
  --json             print the result as one JSON document
  -c, --config <p>   use this config file
  -h, --help         show help
` + moreHelp

// versionUsage is printed by "mailfire version --help" and on version
// usage errors.
const versionUsage = `Usage:
  mailfire version

Show version, commit, and build date.

Flags (version):
  --json             print the result as one JSON document
  -h, --help         show help
` + moreHelp

// authUsage is printed by "mailfire auth --help" and on auth usage errors.
const authUsage = `Usage:
  mailfire auth login --provider gmail [--no-browser] [--port <port>] [--manual]
  mailfire auth status --provider gmail
  mailfire auth refresh --provider gmail [--timeout <dur>]
  mailfire auth logout --provider gmail

Authorize Mailfire to send through Gmail (send-only access). login opens the
consent page in a browser; refresh and sends never do. On a headless host use
--no-browser and forward the callback port over SSH, or --manual when
forwarding is unavailable.

refresh exchanges the refresh token even when the cached token is still valid,
without sending mail; schedule it monthly for infrequent senders. logout
removes only the local credentials; revoke access in your Google Account.

Flags (auth):
  --provider gmail   provider to authorize (required)
  --no-browser       login: print the consent URL instead of opening a browser
  --port <port>      login: fixed callback port (for SSH forwarding)
  --manual           login: paste the callback URL (requires --port)
  --timeout <dur>    token request timeout (e.g. 15s); overrides gmail.timeout
  --json             status, refresh, logout: print the result as one JSON document
  -c, --config <p>   use this config file
  -h, --help         show help
` + moreHelp
