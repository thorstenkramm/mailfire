---
title: SMTP
description: Send through any email provider's submission server or a local relay, with implicit TLS, STARTTLS, or an explicit plaintext relay.
weight: 60
icon: server
---

Set `provider = "smtp"` in `[sending]` to submit messages to an existing email provider's SMTP server or to a local
relay. No Mailgun account is needed.

## Configuration

```toml
[sending]
provider = "smtp"
domain = "example.com"
from = "My Name <user@example.com>"

[smtp]
host = "smtp.example.com"
port = 587
security = "starttls"
username = "submission-user"
password = "<app-password>"
```

| Setting | Description |
|---|---|
| `host` | The submission server. Required. |
| `port` | Usually `465` with `tls` and `587` with `starttls`. Required. |
| `security` | `tls` (implicit TLS), `starttls` (STARTTLS required), or `none` (plaintext, only for a trusted local relay). Required. |
| `username`, `password` | Optional; set both or neither. Never sent with `security = "none"`. |
| `timeout` | Optional; bounds the whole connection: connect, TLS, authentication, and submission. `--timeout` overrides it. |

Every setting has a `MAILFIRE_SMTP_*` variable, such as `MAILFIRE_SMTP_PASSWORD`, which keeps the password out of the
file; see [Environment variables](../reference/environment.md).

## TLS and security

- Certificates and host names are always verified. An untrusted or mismatched certificate fails the send; there is no
  option to skip verification.
- With `starttls`, Mailfire never falls back to plaintext: if the server does not offer STARTTLS, or the upgrade
  fails, the send fails.
- Credentials are only ever sent over an encrypted connection. Mailfire picks PLAIN, LOGIN, or CRAM-MD5, whichever the
  server offers.
- `security = "none"` must be chosen explicitly and cannot be combined with credentials.

For a local relay, make plaintext explicit and omit credentials:

```toml
[sending]
provider = "smtp"
domain = "example.com"

[smtp]
host = "127.0.0.1"
port = 2525
security = "none"
```

## Account setup

Most large providers, including Gmail and Microsoft 365, require an app password or OAuth for SMTP. An ordinary
account password is not a supported shortcut. For Gmail, the [Gmail provider](gmail.md) with OAuth avoids SMTP
passwords entirely.

## Check the setup

```shell
mailfire validate
```

`validate` connects, negotiates TLS, and authenticates — without sending a message or probing any recipient. SMTP has
no validation-only send mode, so `--test` is refused; use [`--preview`](preview.md) to check the rendering.

## Delivery and failures

On success, Mailfire prints the Message-ID it generated. SMTP servers do not return a reliable message ID, so this
header is what identifies the message; it stays the same if the message is deferred and retried.

A `250` reply means the server accepted the message, not that it reached the recipient's mailbox. If any recipient
is rejected, Mailfire aborts before sending the message data, so nobody receives a partial send; the error names the
recipient.

| Server response | Result |
|---|---|
| Authentication rejected (535) | Exit `3`; nothing is deferred |
| Temporary failure (4xx), connection error, timeout | Deferred for `mailfire resend`, exit `4` |
| Permanent failure (5xx), missing STARTTLS, untrusted certificate | Exit `5`; nothing is deferred |

Deferred SMTP messages keep their envelope (Bcc recipients included) and are resent to the same SMTP account. See
[Deferred delivery](deferred-delivery.md).
