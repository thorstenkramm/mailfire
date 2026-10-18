---
title: Mailgun
description: The default provider. Send through the Mailgun HTTP API with an API key and a verified sending domain.
weight: 50
icon: bolt
---

Mailgun is the default provider: a configuration without `provider` in `[sending]` sends through the
[Mailgun HTTP API](https://documentation.mailgun.com/).

## Configuration

```toml
[mailgun]
api_key = "<your-api-key>"
region = "eu"          # "us" or "eu", where your Mailgun account is hosted
# timeout = "30s"      # per-request timeout; --timeout overrides it

[sending]
domain = "example.com" # the sending domain configured in Mailgun
from = "My Name <user@example.com>"
```

The API key is a secret: it is sent only in the request's `Authorization` header and never appears in error messages,
logs, JSON output, or deferred messages — even if a server echoes it back. Keep it out of the file with
`MAILFIRE_MAILGUN_API_KEY`; see [Environment variables](../reference/environment.md).

## Check the setup

```shell
mailfire validate
```

`validate` checks the configuration file and settings, then asks Mailgun to confirm the API key and the sending
domain (`GET /v3/domains/{domain}`). Nothing is sent. It exits with `3` when the key is rejected, `2` when Mailgun does
not know the domain, and `4` when Mailgun cannot be reached reliably.

## Test mode

```shell
mailfire --test --to user@example.com --subject "Test" --message "Hello"
```

`--test` sends the complete request with Mailgun's test mode: Mailgun validates it but delivers nothing, and Mailfire
says so explicitly. Test sends are never deferred and never take part in
[deduplication](deduplication.md).

## Failures

| Mailgun response | Result |
|---|---|
| 401 or 403 | Authentication error, exit `3`; nothing is deferred |
| Other 4xx | Permanent rejection, exit `5`; nothing is deferred |
| 429, 5xx, network error, timeout | Deferred for `mailfire resend`, exit `4` |

See [Deferred delivery](deferred-delivery.md) for retries.
