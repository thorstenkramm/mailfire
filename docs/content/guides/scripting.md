---
title: Scripting and automation
description: Use Mailfire from shell scripts, cron, and CI with stable exit codes, JSON output, environment configuration, and a send log.
weight: 100
icon: code
---

Mailfire is designed to run unattended: it never prompts, never reads interactively from a terminal, and reports
every outcome through its exit code.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success, including a repeat skipped by `--dedup-key` |
| `1` | Usage error |
| `2` | Configuration error |
| `3` | Authentication error |
| `4` | Send failed, message deferred for `mailfire resend` |
| `5` | Send failed, message not deferred |

```shell
mailfire --to ops@example.com --subject "Backup failed" --file backup.log
rc=$?
case $rc in
  0) ;;                                   # sent (or skipped as a repeat)
  4) logger "mail deferred; mailfire resend will retry" ;;
  *) logger "mail failed with exit code $rc" ;;
esac
```

See [Exit codes](../reference/exit-codes.md) for the details of each command.

## JSON output

Add `--json` to `send`, `resend` (including `--list`), `queue list|show|delete|stats`, `validate`,
`auth status|refresh|logout`, `version`, or `export-templates` to get exactly one JSON document on stdout instead of
the human-readable output:

```shell
mailfire --json --to ops@example.com --subject "Backup done" --message "ok" | jq -r .message_id
mailfire resend --json | jq '.summary'
mailfire queue list --json | jq -r '.messages[] | select(.status == "failed") | .id'
```

Every document has `schema_version`, `command`, `outcome`, and `exit_code`; failures add an `error` object with a
stable `code` such as `auth_failed`. Diagnostics and warnings stay on stderr, exit codes are unchanged, and `--help`
always prints text. Credentials and message content are never included, except the stored request with
`queue show --raw --json`. The [JSON output reference](../reference/json-output.md) documents every field, error
code, and the compatibility guarantees.

## Configuration without a file

In CI pipelines and containers, supply the configuration through `MAILFIRE_*` environment variables instead of a
file:

```shell
export MAILFIRE_MAILGUN_API_KEY="<your-api-key>"   # from your CI secret store
export MAILFIRE_MAILGUN_REGION="eu"
export MAILFIRE_SENDING_DOMAIN="example.com"
export MAILFIRE_SENDING_FROM="CI <ci@example.com>"
mailfire --to dev@example.com --subject "Build finished" --message "All tests passed."
```

See [Environment variables](../reference/environment.md) for every variable and the precedence rules.

## Cron

Cron runs with a minimal environment and `PATH`. Use the full path of the binary (`command -v mailfire` prints it),
and pass `-c` when the configuration is not in a default location:

```cron
# Daily report at 07:00; retry deferred messages every five minutes.
0 7 * * *   /usr/local/bin/mailfire -c /etc/mailfire/mailfire.conf --md --to ops@example.com --subject "Daily report" --file /var/reports/daily.md
*/5 * * * * /usr/local/bin/mailfire -c /etc/mailfire/mailfire.conf resend
```

Pair recurring alerts with [`--dedup-key`](deduplication.md) so a persistent problem is reported once per cooldown.

## Timeouts

Provider requests time out after 30 seconds by default. Set `timeout` in the provider's section (`[mailgun]`,
`[smtp]`, or `[gmail]`), or pass `--timeout <duration>` (for example `15s` or `1m`) on `send`, `resend`, and
`validate`; the flag overrides the setting. A `resend` run retrying several messages may take longer than a single
timeout.

## Send log

Logging is disabled unless `logfile` is set in `[logging]`. Every sending attempt — successful or not, including test
sends and resends — then appends one JSON line. The fields depend on `loglevel`:

| Level | Fields |
|---|---|
| `error` | `timestamp`, `message_id`, `to`, `status` — failed attempts only |
| `information` (default) | `timestamp`, `message_id`, `to`, `subject`, `status` |
| `debug` | also `from`, `size`, and the provider's `response` |

`status` is the provider's reply code (HTTP status, or the SMTP reply code), or `0` when no response was received.
Test sends carry `"test": true`. Deferred messages dropped because the queue exceeded `max_size`, or removed after
`max_age`, are logged at every level with `"event": "defer_drop"` or `"defer_expire"`. Credentials never reach the
log.

When the file exceeds `max_size` (default `50M`), the oldest lines are removed; the newest entry is always kept.

```toml
[logging]
logfile = "~/logs/mailfire.log"
loglevel = "information"
max_size = "50M"
```
