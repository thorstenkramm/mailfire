---
title: Notification deduplication
description: Send a repeated monitoring alert only once per cooldown with --dedup-key, across sends and retries.
weight: 90
icon: bell
---

Monitoring checks and cron jobs often report the same problem again and again. Pass `--dedup-key <key>` to send a
notification only once per cooldown:

```shell
mailfire --dedup-key disk-full-db1 --cooldown 30m \
  --to ops@example.com --subject "Disk full on db1" --message "df: 98%"
```

## What counts as a repeat

A notification is identified by the key, the sending domain, and all recipients (to, cc, and bcc, compared
case-insensitively in any order). Different keys, domains, or recipients never interfere. A repeat is skipped with exit
code `0` and a `skipped: ...` line on stdout that says when it becomes eligible again, while:

- **the same notification was delivered within its cooldown.** The cooldown is set by `--cooldown`, else `cooldown` in
  `[dedup]`, default `1h`. The delivered send's value applies, and skipped repeats do not extend it, so a persistent
  problem is reported once per cooldown.
- **the same notification is still deferred,** even if it was rejected permanently. The first deferred message is
  kept; no duplicate is queued. Retry it with `mailfire resend --force <id>` or remove it with
  `mailfire queue delete <id>`.
- **an attempt was interrupted,** for example by a crash, less than a minute ago and may have been delivered.
  Mailfire then prefers a possible duplicate over a lost alert.

Only a delivery starts the cooldown, by `send` or by a successful `mailfire resend` of the deferred message. Failed
and deferred sends do not, and `--test` sends neither check nor start it. If the system clock moves backward, affected
cooldowns end immediately. A concurrent send of the same notification waits until the first one's outcome is
recorded.

To send a repeat anyway, omit `--dedup-key`. `mailfire resend --force` is unrelated: it ignores the retry wait of
deferred messages, not the notification cooldown.

## State

The state is stored in `state_dir` in `[dedup]` (default `$HOME/.local/share/mailfire/dedup`, or
`%LOCALAPPDATA%\mailfire\dedup` on Windows), independently of the deferral settings. Inside `defer_dir`, only its
subdirectory `dedup` may be used (symbolic links are resolved for this check). Expired entries are removed
automatically.

Corrupt or unreadable state is an error and nothing is sent: `send` exits with code `2`. `mailfire resend` keeps a
deferred notification whose state it cannot use (unsafe `state_dir`, corrupt or unreadable state, a lock failure)
queued without sending it, reports it as failed, and exits with code `5`. Remove the named file to reset that
notification.
