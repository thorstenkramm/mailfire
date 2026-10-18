---
title: Command line
description: Usage and flags of every Mailfire subcommand, exactly as mailfire --help prints them.
weight: 10
icon: code
---

The help texts on this page are checked against the program by the test suite, so they always match the installed
version.

## Overview

Without a subcommand, `send` is assumed: `mailfire --to ...` and `mailfire send --to ...` are the same. Every subcommand accepts `-h` or `--help` and prints its own usage and flags.

```text
$ mailfire --help
mailfire — send email from the command line via Mailgun, Gmail, or SMTP

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

More help: https://mailfire.system42.io
```

## send

Composes and sends an email; see [Writing emails](../guides/formats.md) and the other guides for each option.

```text
$ mailfire send --help
Usage:
  mailfire [send] [flags]

Compose and send an email. Read piped stdin unless --file or --message is given.
Terminal stdin is never read. An empty body requires at least one attachment.
Sender and recipient domains must be dotted DNS names (no DNS lookup).

With --preview, render the body locally instead of sending: no provider
credentials, recipients, sender, or subject are needed, and nothing is sent,
deferred, deduplicated, or logged. Local Markdown images are embedded.

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

More help: https://mailfire.system42.io
```

## validate

Checks the configuration file, required settings, and the selected provider without sending mail: Mailgun's API key and domain; the SMTP connection, TLS negotiation, and authentication (no recipient is probed); or the Gmail authorization. It reports which settings came from environment variables without showing credentials.

```text
$ mailfire validate --help
Usage:
  mailfire validate [flags]

Check the configuration (config file and MAILFIRE_* environment variables)
and the selected provider credentials without sending mail.

Flags (validate):
  --timeout <dur>    per-request timeout (e.g. 15s, 1m); overrides provider timeout
  --json             print the result as one JSON document
  -c, --config <p>   use this config file
  -h, --help         show help

More help: https://mailfire.system42.io
```

## auth

Manages the Gmail authorization; see [Gmail](../guides/gmail.md).

```text
$ mailfire auth --help
Usage:
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

More help: https://mailfire.system42.io
```

## resend

Retries deferred messages, oldest first. Messages are removed only after a successful send. Messages that are not yet eligible for a retry are skipped; `--force` ignores the wait. See [Deferred delivery](../guides/deferred-delivery.md).

```text
$ mailfire resend --help
Usage:
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

More help: https://mailfire.system42.io
```

## queue

Inspects and deletes deferred messages without sending. `queue show <id>` shows one message's recipients, subject, provider, age, attempts, last error, and next retry; `--raw` prints the stored request instead.

```text
$ mailfire queue --help
Usage:
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

More help: https://mailfire.system42.io
```

## export-templates

Exports all four built-in templates as editable files. It creates the folder if needed and refuses to overwrite existing template files. No configuration or credentials are required. See [Templates](../guides/templates.md#custom-templates).

```text
$ mailfire export-templates --help
Usage:
  mailfire export-templates [--json] <folder>

Export all built-in templates as editable HTML template files.
Creates the folder if needed; refuses to overwrite existing templates.

Flags (export-templates):
  --json             print the result as one JSON document
  -h, --help         show help

More help: https://mailfire.system42.io
```

## version

Prints the version, commit, and build date.

```text
$ mailfire version --help
Usage:
  mailfire version

Show version, commit, and build date.

Flags (version):
  --json             print the result as one JSON document
  -h, --help         show help

More help: https://mailfire.system42.io
```
