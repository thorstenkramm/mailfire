package cli

import "strings"

const moreHelp = "\nMore help: https://github.com/thorstenkramm/mailfire\n"

// usageText is the top-level help printed by "mailfire --help". It must
// match, word for word, the help block under "full usage documentation"
// in README.md and PRD §7; TestUsageMatchesREADME pins them together.
const usageText = `mailfire — send email from the command line via the Mailgun API

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
` + moreHelp

// sendUsage is printed by "mailfire send --help" and on send usage errors.
// Its flags section is cut from usageText so the two cannot drift apart.
var sendUsage = `Usage:
  mailfire [send] [flags]

Compose and send an email. Read piped stdin unless --file or --message is given.
Terminal stdin is never read. An empty body requires at least one attachment.
Sender and recipient domains must be dotted DNS names (no DNS lookup).

` + usageText[strings.Index(usageText, "Flags (send):"):]

// resendUsage is printed by "mailfire resend --help" and on resend usage
// errors.
const resendUsage = `Usage:
  mailfire resend [--list | --force | <id>...]

Retry deferred messages. Without arguments, every deferred message older
than the resend cooldown is retried; with <id>..., only those messages.

Flags (resend):
  --list             show deferred messages (ID, date, to, subject, size)
  --force            resend even messages younger than the cooldown
  -c, --config <p>   use this config file
  -h, --help         show help
` + moreHelp

// validateUsage is printed by "mailfire validate --help" and on validate
// usage errors.
const validateUsage = `Usage:
  mailfire validate [flags]

Check the config file and the Mailgun credentials without sending mail.

Flags (validate):
  -c, --config <p>   use this config file
  -h, --help         show help
` + moreHelp

// versionUsage is printed by "mailfire version --help" and on version
// usage errors.
const versionUsage = `Usage:
  mailfire version

Show version, commit, and build date.

Flags (version):
  -h, --help         show help
` + moreHelp
