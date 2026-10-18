# JSON output reference

Add `--json` to `send`, `resend` (including `--list`), `queue list|show|delete|stats`, `validate`,
`auth status|refresh|logout`, `version`, or `export-templates` to get a machine-readable result. The documents are
the same for every sending provider (Mailgun, SMTP, Gmail); provider-specific details are noted below. This page documents schema version 1.

## Rules

- Each invocation prints exactly one JSON document, on a single line, to stdout. It is not JSON Lines; a batch
  (`resend`) is one document with an array and a summary.
- Nothing else is printed to stdout. Diagnostics, warnings (for example, about configuration file permissions or
  expired deferred messages), and progress details go to stderr in human-readable form.
- Exit codes are the same as without `--json`, and `exit_code` in the document always equals the process exit code.
- Failures are reported as JSON too, including usage and configuration errors, whenever the command line contains a
  `--json` (or `--json=true`) flag, even if it cannot be parsed otherwise. `--json` counts only where Mailfire reads it
  as a flag: not as the value of another flag (`--message --json`), not after a `--` terminator (a `--` that is the
  value of a flag, as in `--config --`, is no terminator), and for `send`, `validate`, and `version` not after the
  first positional argument. An unknown flag is assumed to take no value. For an unknown subcommand, whose flags are
  unknown, any `--json` before `--` counts.
- Help is always text: `--help` with `--json` prints the usual help and exits with 0.
- `--json` cannot be combined with `send --preview`, which writes HTML instead of a result (usage error, exit 1).
- Timestamps are RFC 3339 strings in UTC with second precision, for example `"2026-10-17T09:15:00Z"`.
- Optional fields are omitted when they do not apply; they are never `null`. Arrays are present whenever the
  command produced data, also when it failed (for example, a partial `resend`), and may be empty.
- Credentials are never included: no Mailgun API key, SMTP password, or Gmail token. Message bodies and attachments are never included, except the stored request in
  `queue show --raw --json`. Recipients, subjects, and error messages are included as they are; error messages from
  the providers are redacted like everywhere else in Mailfire.
- Strings are valid UTF-8 with standard JSON escaping; `<`, `>`, and `&` are not escaped.

## Common fields

Every document has these fields:

| Field | Type | Description |
|---|---|---|
| `schema_version` | number | `1` |
| `command` | string | `send`, `resend`, `queue list`, `queue show`, `queue delete`, `queue stats`, `validate`, `auth status`, `auth refresh`, `auth logout`, `auth` (usage errors), `version`, `export-templates`; `queue` when the queue subcommand is missing or unknown; `mailfire` for an unknown subcommand |
| `outcome` | string | Result of the command; values per command below. `failed` whenever the command could not do its job |
| `exit_code` | number | The process exit code |
| `error` | object | Present if and only if `exit_code` is not 0 |

The `error` object:

| Field | Type | Description |
|---|---|---|
| `code` | string | Stable identifier, see [error codes](#error-codes) |
| `message` | string | Human-readable explanation; wording may change between releases |

If a command fails before it produces data (usage error, configuration error, unreadable `defer_dir`), the document
contains only the common fields:

```json
{"schema_version":1,"command":"send","outcome":"failed","exit_code":1,"error":{"code":"usage_error","message":"missing required flag: --to"}}
```

## Error codes

| Code | Exit code | Meaning |
|---|---|---|
| `usage_error` | 1 | Invalid invocation or input: unknown flag or subcommand, missing or conflicting flags, invalid address, unreadable body, attachment, or footer |
| `unknown_id` | 1 | A deferred message ID given to `resend`, `queue show`, or `queue delete` does not exist |
| `export_failed` | 1 | `export-templates` could not write the templates, for example because they already exist |
| `config_error` | 2 | Configuration file or variable missing or invalid, unusable logfile; `validate`: domain unknown to Mailgun; Gmail without a usable OAuth client; `resend`: a message's provider is not configured or its account changed (the message is kept untouched, exit 5) |
| `dedup_error` | 2 (`send`), 5 (`resend`) | Notification deduplication state cannot be used; nothing was sent |
| `auth_failed` | 3 | Credentials rejected: Mailgun API key (401/403), SMTP authentication (535), Gmail not logged in or authorization revoked (run `mailfire auth login --provider gmail`) |
| `temporary_failure` | 4 or 5 | Retryable failure (network error, timeout, HTTP 429 or 5xx, SMTP 4xx). Exit 4 when the message was deferred (or for `validate`), 5 when it was not (`--no-defer`, `defer = false`, `--test`) |
| `rejected` | 5 | The provider rejected the message permanently: Mailgun or Gmail 4xx, SMTP 5xx (for example a rejected recipient; nothing was delivered to anyone) |
| `send_failed` | 5 | Other non-retryable sending failure, for example an interrupted request, missing STARTTLS, or an untrusted TLS certificate |
| `defer_failed` | 5 | Sending failed and the message could not be stored in `defer_dir`; it is lost |
| `queue_error` | 5 | `defer_dir` or a stored message cannot be read, locked, or deleted |

## send

| Field | Type | Description |
|---|---|---|
| `provider` | string | `mailgun`, `smtp`, or `gmail`; omitted when the configuration could not be loaded |
| `outcome` | string | `sent`, `accepted` (test mode: Mailgun validated the request, nothing was delivered), `skipped` (duplicate notification), `deferred`, or `failed` |
| `test_mode` | boolean | `true` with `--test` |
| `message_id` | string | With `sent` and `accepted`: Mailgun's message ID, Gmail's message ID, or for SMTP the Message-ID header Mailfire generated (SMTP servers return no reliable ID) |
| `deferred_id` | string | ID of the stored message; with `deferred`. Use it with `mailfire resend <id>` or `mailfire queue show <id>` |
| `skip` | object | With `skipped`, see below |

`skip` fields: `reason` (`cooldown`, `queued`, or `in_doubt`), `dedup_key`, `queued_id` (with `queued`: the deferred
message), `since` (with `cooldown`: when the earlier notification was sent; with `in_doubt`: when the interrupted
attempt started), and `eligible_at` (with `cooldown` and `in_doubt`).

`sent` means the provider accepted the message. For SMTP that is the submission server's 250 reply, not final
delivery to the recipient's mailbox. `--test` is a usage error (`usage_error`, exit 1) with SMTP and Gmail, which have
no validation-only mode.

```json
{"schema_version":1,"command":"send","outcome":"sent","exit_code":0,"test_mode":false,"message_id":"<20261017091500.1@mg.example.com>"}
{"schema_version":1,"command":"send","outcome":"deferred","exit_code":4,"error":{"code":"temporary_failure","message":"mailgun: send message: status 503: Service Unavailable"},"test_mode":false,"deferred_id":"20261017T091500.123456789-9f3ab2c17e4d88aa"}
{"schema_version":1,"command":"send","outcome":"skipped","exit_code":0,"test_mode":false,"skip":{"reason":"cooldown","dedup_key":"disk-full-db1","since":"2026-10-17T09:00:00Z","eligible_at":"2026-10-17T10:00:00Z"}}
```

## resend

| Field | Type | Description |
|---|---|---|
| `outcome` | string | `ok` (no failures), `partial` (some messages sent, others failed), or `failed` |
| `messages` | array | One record per processed message, in processing order |
| `summary` | object | `sent`, `skipped`, `failed`, and `expired` counts |
| `error` | object | With a non-zero exit code: the error of the most severe failure (ranked as the exit code: 3, 5, 4) |

Message record:

| Field | Type | Description |
|---|---|---|
| `id` | string | Deferred message ID |
| `provider` | string | Provider the message was deferred through and is resent through |
| `outcome` | string | `sent`, `skipped`, `failed`, or `expired` (removed for exceeding `max_age`; this can include messages that were not selected) |
| `reason` | string | With `skipped`: `not_eligible` (cooldown or backoff), `permanently_failed` (retry with `--force <id>`), or `gone` (removed by another process). With `failed` when nothing was attempted: `provider_unavailable` (the message's provider is not configured) or `account_mismatch` (a different SMTP account or Google account is configured now). Such messages are kept with unchanged retry state and are retried normally once the configuration is restored |
| `message_id` | string | With `sent` |
| `next_retry` | string | When the message becomes eligible; with `not_eligible` and with failures of code `temporary_failure` |
| `warning` | string | With `sent`, when the message could not be removed from `defer_dir` and might be sent again |
| `error` | object | With `failed` |

```json
{"schema_version":1,"command":"resend","outcome":"partial","exit_code":4,"error":{"code":"temporary_failure","message":"mailgun: send message: status 503: failure"},"messages":[{"id":"20261015T090000.000000001-aa","outcome":"sent","message_id":"<ok@mg.example.com>"},{"id":"20261015T100000.000000001-bb","outcome":"failed","next_retry":"2026-10-17T09:25:00Z","error":{"code":"temporary_failure","message":"mailgun: send message: status 503: failure"}},{"id":"20261017T091400.000000001-cc","outcome":"skipped","reason":"not_eligible","next_retry":"2026-10-17T09:19:00Z"}],"summary":{"sent":1,"skipped":1,"failed":1,"expired":0}}
```

With nothing to resend, `messages` is empty and `outcome` is `ok`.

## resend --list and queue list

`resend --list --json` prints the same document as `queue list --json`, with `command` set to `resend`.

| Field | Type | Description |
|---|---|---|
| `messages` | array | Deferred messages, oldest first; may be empty |

Message record (also used by `queue show`):

| Field | Type | Description |
|---|---|---|
| `id` | string | Deferred message ID |
| `provider` | string | `mailgun` (also for messages stored by versions before provider support), `smtp`, or `gmail` |
| `deferred_at` | string | When the message was deferred |
| `to` | array of strings | Recipients |
| `subject` | string | Subject |
| `size` | number | Size of the stored request in bytes |
| `attempts` | number | Failed resend attempts |
| `last_attempt` | string | Latest failed resend attempt; omitted if never resent |
| `last_error` | string | Latest error; omitted if none recorded |
| `status` | string | `pending`, `failed` (permanently rejected, excluded from automatic retries), or `expired` (older than `max_age`) |
| `next_retry` | string | With `pending`: when the message becomes eligible; a time in the past means it is eligible now |

```json
{"schema_version":1,"command":"queue list","outcome":"ok","exit_code":0,"messages":[{"id":"20261015T090000.000000001-aa","provider":"mailgun","deferred_at":"2026-10-15T09:00:00Z","to":["ops@example.com"],"subject":"Disk full","size":812,"attempts":2,"last_attempt":"2026-10-17T09:00:00Z","last_error":"mailgun: send message: status 503: failure","status":"pending","next_retry":"2026-10-17T09:20:00Z"}]}
```

## queue show

| Field | Type | Description |
|---|---|---|
| `message` | object | The message record described above |
| `raw` | object | Only with `--raw`: `content_type` (Mailgun: the multipart Content-Type; SMTP and Gmail: `message/rfc822`) and `body_base64` (the stored request, base64-encoded because it may be binary) |

`--raw` is the only way to get message content in JSON. The stored request contains the message and its attachments,
never credentials. For SMTP the stored message has no Bcc header; the envelope recipients, Bcc included, are kept in
the queue metadata only.

## queue delete

| Field | Type | Description |
|---|---|---|
| `outcome` | string | `ok`, `partial` (some messages deleted before an error), or `failed` |
| `deleted` | array of strings | IDs deleted |
| `not_deleted` | array of strings | Requested IDs that were not deleted |
| `unknown_ids` | array of strings | With `unknown_id`: the unknown IDs. Nothing was deleted |

## queue stats

`stats` object: `messages`, `failed`, `expired`, `eligible`, `total_size` (bytes), and `attempts` (failed resend
attempts, all messages); optionally `oldest_id`, `oldest_deferred_at`, `last_attempt`, and `next_retry` (earliest
retry of a pending message).

## validate

| Field | Type | Description |
|---|---|---|
| `checks` | array | Checks performed, in order; a failed check is the last one |
| `domain` | object | Mailgun only, when all checks passed: `name` and `state` as reported by Mailgun |

Check record: `name`, `ok` (boolean), and `message`. Names: `config_file`, `mandatory_keys`, then the provider check:
`api_key_and_domain` (Mailgun), `smtp_connection` (SMTP: connection, TLS negotiation, and authentication; no message
is sent and no recipient is probed), or `gmail_authorization` (Gmail: the stored authorization is usable, refreshed if
nearly expired; nothing is sent).

```json
{"schema_version":1,"command":"validate","outcome":"failed","exit_code":3,"error":{"code":"auth_failed","message":"mailgun rejected the API key: mailgun: validate domain: status 401: Forbidden"},"checks":[{"name":"config_file","ok":true,"message":"config file found and parses as valid TOML: /home/me/.config/mailfire/mailfire.conf"},{"name":"mandatory_keys","ok":true,"message":"mandatory keys present and well-formed (region \"eu\" from config file, domain \"example.com\" from config file, api key from config file)"},{"name":"api_key_and_domain","ok":false,"message":"mailgun rejected the API key: mailgun: validate domain: status 401: Forbidden"}]}
```

## auth status, auth refresh, auth logout

| Field | Type | Description |
|---|---|---|
| `provider` | string | `gmail` |
| `credential_file` | string | The credential file used (never its content) |
| `logged_in` | boolean | `status` only |
| `expires_at` | string | `status` and `refresh`: when the current access token expires (it is renewed automatically) |

`auth login` is interactive and has no JSON output.

```json
{"schema_version":1,"command":"auth refresh","outcome":"ok","exit_code":0,"provider":"gmail","credential_file":"/home/me/.config/mailfire/gmail-credentials.json","expires_at":"2026-10-17T10:15:00Z"}
```

## version

Fields: `version`, `commit`, and `build_date`.

```json
{"schema_version":1,"command":"version","outcome":"ok","exit_code":0,"version":"1.4.0","commit":"a1b2c3d","build_date":"2026-10-15T08:00:00Z"}
```

## export-templates

Field: `directory`, the target folder with a leading `~` expanded.

## Examples with jq

```shell
# Send and capture the message ID; fail the script on any error.
id=$(mailfire --json --to ops@example.com --subject "Backup done" --message "ok" | jq -er .message_id)

# Distinguish outcomes in a monitoring script.
result=$(mailfire --json --dedup-key disk-db1 --to ops@example.com --subject "Disk full" --file report.txt)
case $(jq -r .outcome <<<"$result") in
  sent | skipped) ;;
  deferred) echo "queued as $(jq -r .deferred_id <<<"$result")" ;;
  *) echo "failed: $(jq -r .error.code <<<"$result")" >&2 ;;
esac

# Resend and report failed messages.
mailfire resend --json | jq -r '.messages[] | select(.outcome == "failed") | "\(.id) \(.error.code)"'

# Count deferred messages and permanently rejected ones.
mailfire queue stats --json | jq '.stats | {messages, failed}'

# IDs of permanently rejected messages, for review.
mailfire queue list --json | jq -r '.messages[] | select(.status == "failed") | .id'

# Alert on authentication problems only.
mailfire validate --json | jq -e '.error.code != "auth_failed"' >/dev/null || echo "credentials rejected"
```

## Compatibility

`schema_version` is 1. Within a schema version:

- Existing fields keep their name, type, and meaning, and error codes keep their meaning.
- New fields, new error codes, new `outcome`, `status`, or `reason` values, and new commands may be added. Ignore
  unknown fields and handle unknown values, for example by treating an unknown error code by its exit code.
- Field order is not significant, and `message` texts may change.

Removing or renaming a field, changing its type or meaning, or changing the meaning of an error code requires a new
`schema_version`. Check `schema_version` if your script depends on exact semantics.
