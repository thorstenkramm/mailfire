---
title: Writing emails
description: Choose a body format, supply the body from stdin, a file, or the command line, add attachments, and address recipients.
weight: 10
icon: pencil
---

## Body formats

| Flag | Body format |
|------|-------------|
| `--txt` | Plain text (default) |
| `--md` | Markdown rendered as HTML using the selected [template](templates.md) |
| `--html` | HTML fragment wrapped in the selected template |
| `--html-raw` | Complete HTML document sent unchanged |

Choose one format per email. All HTML formats include a plain-text alternative derived from the body, so the message
reads well in every client.

- `--md` supports tables, GitHub-style alerts, and local images; see
  [Markdown, alerts, and images](markdown.md).
- `--html` takes a trusted HTML fragment. Existing inline styles survive; the template's CSS is inlined around them.
- `--html-raw` is for complete documents you built yourself: no template, no CSS inlining, no changes.

## The message body

Supply the body through one of these sources:

- Piped standard input (stdin)
- A regular file with `--file <path>`
- An explicit string with `--message <text>` (aliases `--m` and `-m`)

```shell
df -h | mailfire --to ops@example.com --subject "Disk usage on $(hostname)"
mailfire --md --to ops@example.com --subject "Weekly report" --file report.md
mailfire --to ops@example.com --subject "Deploy finished" --message "Version 2.4.1 is live."
```

`--file` and `--message` are mutually exclusive, even with empty values, and either one ignores stdin. Mailfire never
reads interactively from terminal stdin. Windows (CRLF) line endings are normalized, except for `--html-raw`, which is
sent byte-identical.

## Attachments

```shell
mailfire --to user@example.com --subject "Your file" --message "See attached" --attach report.pdf
```

Repeat `--attach` to include more files. An empty body is allowed only when an attachment is supplied; for an
attachment-only email from a terminal, omit `--message`. Piped input is still read when supplied. Attachments must be
regular files; a leading `~` is expanded. The content type is detected from the file's content.

Mailfire streams an encoded message through a temporary file rather than holding the attachment in its heap. When large
attachments are sent frequently, choose a disk-backed `[sending] temp_dir` if the system temporary directory is a
RAM-backed `tmpfs`; see [Temporary files](../reference/configuration.md#temporary-files).

## Recipients and sender

Use repeatable `--to`, `--cc`, and `--bcc` flags:

```shell
mailfire --to alice@example.com --to "Bob Example <bob@example.com>" \
  --cc team@example.com --bcc audit@example.com --subject "Status" --message "All green."
```

Addresses can be plain (`user@example.com`) or include a display name (`"Name <user@example.com>"`). Quote display
names when passing them to the shell. Comma-separated recipient lists are not supported, because commas are valid in
display names. Bcc recipients receive the message but never appear in its headers.

The sender comes from `from` in the `[sending]` section of the [configuration](../reference/configuration.md), or from
`--from`, which overrides it. If neither is set, `--from` is required.

Sender and recipient addresses must have dotted DNS domains with valid, nonempty labels and no leading or trailing
hyphens. Quoted names and local parts, plus addressing, and Unicode addresses are supported. These are syntax checks:
Mailfire does not perform DNS lookups or guarantee delivery. Invalid addresses are rejected locally and are never
deferred.

## Subject

`--subject` is required to send. It may contain any Unicode text, but no line breaks: a line break in a header is
never legitimate and could be used to inject headers, so Mailfire refuses it.
