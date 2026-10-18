---
title: Deferred delivery
description: When sending fails temporarily, Mailfire stores the message and mailfire resend retries it later with backoff.
weight: 80
icon: refresh
---

If sending fails because of a network error, a temporary server error (HTTP 5xx, SMTP 4xx), or rate limiting, and
deferral is enabled (the default), the composed message is stored in `defer_dir` and Mailfire exits with code `4`.
Run `mailfire resend`, for example from cron, to retry:

```cron
# Retry deferred messages every five minutes.
*/5 * * * * /usr/local/bin/mailfire resend
```

Use `--no-defer`, or `defer = false` in `[defer]`, to fail immediately instead. Authentication errors and permanent
rejections are never deferred: retrying them could not succeed.

## Retry timing

A deferred message becomes eligible for `mailfire resend` once `resend_cooldown` (default `5m`) has passed. Each
failed retry doubles the wait, measured from the latest attempt and capped at one hour; a `resend_cooldown` longer
than one hour is used unchanged. `mailfire resend --force` retries immediately.

Messages the provider rejects permanently stay in `defer_dir`, marked as failed, and are skipped by later runs. Retry
one with `mailfire resend --force <id>`, or remove it with `mailfire queue delete <id>`. The stored message itself is
never changed; only its retry information is.

## Inspect the queue

```shell
mailfire queue list           # each message with attempts and next retry
mailfire queue show <id>      # recipients, subject, age, attempts, last error
mailfire queue stats          # totals
mailfire queue delete <id>    # remove messages
```

`list`, `show`, and `stats` never send, change, or remove anything. `queue delete` deletes nothing if any of the given
IDs is unknown.

## Limits and expiry

Messages are kept until they are sent or deleted, subject to `max_size` (default `50M`), which drops the oldest
messages when exceeded. Set `max_age` in `[defer]` (for example `"168h"`) to remove older messages automatically
whenever a message is deferred or `mailfire resend` runs; each removal is reported on stderr and in the logfile.
`mailfire resend` checks the age again right before each retry, so a message that exceeds `max_age` during a run is
removed rather than sent.

## Providers and accounts

A deferred message is resent through the provider and account it was originally sent through — the Mailgun domain,
the SMTP username and server, or the Google account — using that provider's current credentials, even if the default
provider changed. Credentials are never stored with deferred messages. If that provider is no longer configured, or a
different account is configured, the message is not attempted (`blocked`); it stays queued unchanged and is resent
normally once the configuration is restored.

## Duplicates

Concurrent Mailfire processes coordinate through a lock file in `defer_dir`, so overlapping `resend` and
`queue delete` runs cannot send or delete the same message twice. Exactly-once delivery is still not guaranteed: if a
request times out or the connection drops after the provider accepted the message (for SMTP, a lost final reply
after the message data), it stays deferred and a later `resend` may deliver it again.

## Exit codes of resend

`mailfire resend` returns the most severe outcome of all attempts, ranked `3`, `5`, `4`, `0`: an authentication error
outranks a permanent failure, which outranks a retryable one. A message that fails with `4` stays deferred. See
[Exit codes](../reference/exit-codes.md).
