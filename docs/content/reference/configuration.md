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

1. Command-line options, such as `--from`, `--template`, `--timeout`, and `--cooldown`
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

Only the selected provider's section must be complete. Keep the sections of providers you used earlier while
deferred messages for them are queued: they are resent through their original provider.

## File permissions

Anyone who can read the configuration file can use its credentials (API key or SMTP password). On Linux and macOS,
Mailfire warns if the file is accessible by group or others; restrict access with `chmod 600 <path>`. On Windows,
configure access through NTFS permissions; the Unix permission check does not apply.

Prefer environment variables, or your secret manager, for credentials on shared systems.

## Annotated example

This is [`mailfire.example.conf`](https://github.com/thorstenkramm/mailfire/blob/main/mailfire.example.conf) from the
repository. Copy it to `.mailfire.conf` to start with the annotated example.

{{< example-config >}}
