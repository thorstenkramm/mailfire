---
title: Configuration
description: Where Mailfire looks for its TOML configuration file, how settings combine, and every setting with its default.
weight: 20
icon: cog
---

Mailfire reads a [TOML](https://toml.io) configuration file. The [quick start](../getting-started/quick-start.md)
shows the minimum setup; the [annotated example](#annotated-example) below describes every setting.

## File locations

On Linux and macOS, Mailfire checks these locations in order and uses the first existing file:

1. `./.mailfire.conf` — current directory
2. `$HOME/.config/mailfire/mailfire.conf` — user configuration
3. `/etc/mailfire/mailfire.conf` — system-wide configuration

On Windows:

1. `.\.mailfire.conf` — current directory
2. `%APPDATA%\mailfire\mailfire.conf` — user configuration
3. `%PROGRAMDATA%\mailfire\mailfire.conf` — system-wide configuration

Use `-c <path>` or `--config <path>` to select a different file. A file selected this way must exist and be valid.

## Precedence

From highest to lowest:

1. Command-line options, such as `--from`, `--template`, `--timeout`, `--cooldown`, and `--locale`
2. `MAILFIRE_*` environment variables — see [Environment variables](environment.md)
3. The configuration file
4. Built-in defaults

Without a configuration file, the environment can supply everything: the selected provider's required settings must
then come from `MAILFIRE_*` variables.

## Sections

| Section | Purpose |
|---|---|
| `[sending]` | Provider selection (`mailgun`, `smtp`, or `gmail`), sending domain, default sender, and template |
| `[mailgun]` | Mailgun API key, region, and timeout — see [Mailgun](../guides/mailgun.md) |
| `[smtp]` | SMTP server, port, TLS mode, credentials, and timeout — see [SMTP](../guides/smtp.md) |
| `[gmail]` | Gmail credential file and timeout — see [Gmail](../guides/gmail.md) |
| `[logging]` | Optional JSON send log — see [Send log](../guides/scripting.md#send-log) |
| `[defer]` | Deferred delivery and retries — see [Deferred delivery](../guides/deferred-delivery.md) |
| `[dedup]` | Notification deduplication — see [Notification deduplication](../guides/deduplication.md) |
| `[run]` | Date locale of `mailfire run` reports — see [Monitor scheduled jobs](../guides/jobs.md) |

Only the selected provider's section must be complete. Keep the sections of providers you used earlier while
deferred messages for them are queued: they are resent through their original provider.

## File permissions

Anyone who can read the configuration file can use its credentials (API key or SMTP password). On Linux and macOS,
Mailfire warns if the file is accessible by group or others; restrict access with `chmod 600 <path>`. On Windows,
configure access through NTFS permissions; the Unix permission check does not apply.

Prefer environment variables, or your secret manager, for credentials on shared systems.

## Temporary files

While sending, Mailfire writes the encoded message to a temporary file and streams it to the provider. This keeps the
Mailfire process's memory use small even for large attachments. `mailfire run` also spills captured stdout and stderr
to temporary files after 10 KiB per stream; each stream is capped at 5 MiB.

By default these files use the system temporary directory: `$TMPDIR`, or `/tmp` when it is unset, on Unix; `%TMP%`
or `%TEMP%` on Windows. Set `[sending] temp_dir` (or `MAILFIRE_SENDING_TEMP_DIR`) to use another directory. A leading
`~` is expanded. Mailfire creates a missing directory with owner-only permissions only when its parent already exists;
it never creates a missing directory tree. Existing directory permissions are left unchanged.

On some Linux systems `/tmp` is a RAM-backed `tmpfs`. That means a message file — up to Gmail's 35 MB message limit —
occupies RAM for the send, and a job's captured logs can occupy up to 10 MiB for its lifetime. Check the effective
location with `findmnt -T "${TMPDIR:-/tmp}"` (look for `tmpfs`) or, on Linux, `stat -f -c %T "${TMPDIR:-/tmp}"`.
`/var/tmp/mailfire` or `~/.cache/mailfire` are common disk-backed alternatives. If it is on the same filesystem as
`[defer] defer_dir`, a failed send can move its message into the queue instead of copying it.

Temporary message files are removed after a send; stale files from a killed process are swept after 24 hours. On Unix,
spilled job logs are unlinked immediately and disappear even after a crash; Windows removes them when the job ends.

## Annotated example

This is [`mailfire.example.conf`](https://github.com/thorstenkramm/mailfire/blob/main/mailfire.example.conf) from the
repository. Copy it to `.mailfire.conf` to start with the annotated example.

{{< example-config >}}
