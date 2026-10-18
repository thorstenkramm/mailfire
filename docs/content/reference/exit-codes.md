---
title: Exit codes
description: What each exit code means, for every command.
weight: 50
icon: list
---

| Code | Meaning |
|------|---------|
| `0` | Success: sent, validated, resent, templates exported, or information shown |
| `1` | Usage error: missing flags, unknown subcommand, conflicting formats |
| `2` | Configuration error: missing file, invalid TOML, missing or invalid settings |
| `3` | Authentication error: Mailgun API key or SMTP credentials rejected; Gmail not authorized |
| `4` | Send failed, and the message was deferred for a later `mailfire resend` |
| `5` | Send failed, and the message was not deferred |

Errors are written to stderr in plain language and name the failing step. With `--json`, the document's `exit_code`
always equals the process exit code; see [JSON output](json-output.md).

## send

- `5` is also used when the provider rejects a message permanently (another Mailgun or Gmail 4xx response, an SMTP
  5xx reply such as a rejected recipient, missing STARTTLS, or an untrusted certificate), when deferral is off
  (`--no-defer` or `defer = false`), when a `--test` send fails with a retryable error (test sends are never
  deferred), and when the message cannot be stored in `defer_dir`.
- A send skipped by `--dedup-key` exits with `0`; unusable deduplication state exits with `2`.
- `--test` with SMTP or Gmail is a usage error (`1`): those providers have no validation-only mode.
- `--preview` exits with `1` when rendering or writing the output fails, and with `2` for an invalid configuration
  file or template setting.

## validate

`4` when the provider cannot be reached reliably (retry later), `3` when credentials are rejected, and `2` when
Mailgun does not know the domain.

## resend

`mailfire resend` returns the most severe outcome of all attempts, ranked `3`, `5`, `4`, `0`. A message that fails
with `4` stays deferred. A message whose provider is not configured, or whose account changed, is kept unchanged and
reported with `5`.

## queue

`1` for an unknown message ID, `5` when `defer_dir` cannot be read or a message cannot be deleted.

## auth

`3` when Gmail is not authorized (not logged in, authorization revoked or expired, consent denied), `4` for a transient
token-endpoint failure or timeout, `2` for a configuration problem, `5` for other failures such as writing the
credential file.
