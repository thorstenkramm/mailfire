---
title: Gmail
description: Send through your Gmail or Google Workspace account with a one-time browser sign-in and send-only access.
weight: 70
icon: mail
---

Mailfire can send through the Gmail API of a personal Gmail or Google Workspace account. You do not need SMTP
settings, an app password, or your own Google Cloud project.

> [!IMPORTANT] Availability
> Mailfire's Google app is awaiting Google's verification. Until it is verified, only accounts registered as testers
> can sign in. Developers can use their own Desktop OAuth client by setting `MAILFIRE_GMAIL_CLIENT_ID` and
> `MAILFIRE_GMAIL_CLIENT_SECRET`.

## Set up

Select the provider in your configuration file and authorize Mailfire once:

```toml
[sending]
provider = "gmail"
from = "My Name <me@gmail.com>"
```

```sh
mailfire auth login --provider gmail
mailfire validate
```

On Google's consent page, tick the checkbox to let Mailfire **send email on your behalf**: Google lists each permission
separately, and without this one the login fails with an explanation and nothing is saved.

`auth login` opens Google's consent page in your browser and waits for the redirect to a temporary local address
(`http://127.0.0.1:<port>/oauth2/callback`). The flow uses PKCE and a random state value; requests to the local
address that do not carry this state are refused.

Mailfire requests two scopes:

- `gmail.send` — send email as you. Mailfire cannot read, list, or delete your mail.
- `openid` — only an opaque, stable account ID. Mailfire uses it to make sure a deferred message is never resent
  from a different Google account after you sign in again. It includes no name, email address, or other profile data.

The access token, its expiry, the refresh token, and the account ID are stored in an owner-only (`0600`) file:
`~/.config/mailfire/gmail-credentials.json`, or `%APPDATA%\mailfire\gmail-credentials.json` on Windows. Change it with
`credentials` in `[gmail]` or `MAILFIRE_GMAIL_CREDENTIALS`. Do not commit or copy this file: the refresh token is a
credential. Writes are atomic and locked, so concurrent sends and refreshes cannot corrupt it.

`mailfire auth status --provider gmail` shows whether credentials exist; `mailfire auth logout --provider gmail`
removes the local file. Logging out does not revoke Mailfire's access at Google; do that at
<https://myaccount.google.com/permissions>.

The [privacy policy](../privacy.md) describes how Mailfire handles your Google user data.

## Token lifetime

Access tokens usually last about one hour. Mailfire refreshes them automatically before a send when they expire
within five minutes. Sends, resends, and refreshes never open a browser or prompt; if the authorization is gone, they
fail with exit code 3 and name the login command.

Refresh tokens are long-lived, not permanent. Google can expire a refresh token that has not been used for six
months, and revocation, password or security changes, Workspace policy, or a time-limited grant can require another
browser login at any time.

## SSH and headless servers

Run login on the server with `--no-browser` and a fixed port. Mailfire prints the consent URL and an SSH command:

```sh
mailfire auth login --provider gmail --no-browser --port 8765
```

On the computer with your browser, forward the port, then open the printed URL there:

```sh
ssh -L 8765:127.0.0.1:8765 user@server
```

Google redirects your browser to `127.0.0.1:8765`, and the tunnel delivers the callback to Mailfire on the server.
Google's device-code flow does not support Gmail scopes, and Mailfire does not use the deprecated copy-and-paste
out-of-band flow.

If the server forbids port forwarding, use `--manual --port 8765`. After consent, the browser fails to load the
callback page; copy the complete URL from its address bar and paste it into Mailfire. This is still the loopback flow
with PKCE and state validation. Never copy refresh tokens through chat or shell history.

## Keep authorization alive

For infrequent senders, schedule a real token refresh monthly. It exchanges the refresh token even when the cached
access token is still valid, and it does not send mail, read mail, alter the queue, or consume notification
cooldowns:

```cron
# Refresh Gmail authorization on the first day of each month at 09:15.
# Replace the path with the output of: command -v mailfire
15 9 1 * * /usr/local/bin/mailfire auth refresh --provider gmail
```

- On Apple Silicon with Homebrew the path is usually `/opt/homebrew/bin/mailfire`.
- Run the job as the same OS user, with the same configuration, as your sends. Cron has a minimal environment and
  `PATH`; pass `-c /path/to/mailfire.conf` if your configuration is not in a default location.
- On Windows, create a Task Scheduler task that runs `mailfire auth refresh --provider gmail` monthly under the same
  user account.
- Mailfire's credential file needs no unlocked desktop keychain, so it works on headless systems.
- Monitor the job's exit status through an independent channel (not by Gmail mail alone): 0 refreshed, 3 not
  authorized (log in again), 4 temporary failure (retry later), 2 configuration problem.

Refreshing prevents inactivity expiry only while it succeeds. It cannot prevent revocation, password-related
invalidation, account-policy changes, time-limited grants, or other Google authorization failures. External OAuth
apps left in Google's "Testing" status issue refresh tokens for Gmail scopes that expire after seven days; refreshing
does not extend that limit.

## Sending behavior and limits

- **Sender:** Gmail sends only as the authenticated account or one of its configured "Send mail as" aliases. If
  `from` is neither, Gmail replaces it with the account's address; Mailfire cannot check aliases without access
  to your Gmail settings, which it does not request.
- **Bcc:** Gmail takes the recipients, Bcc included, from the message and removes the Bcc header before delivery, so
  recipients do not see Bcc addresses.
- **Limits:** messages may be at most 25 MB. Gmail enforces daily sending limits per account (lower for new and
  free accounts); exceeding them, and other temporary errors (HTTP 429 and 5xx), defer the message for
  `mailfire resend`.
- **Test mode:** Gmail has no Mailgun-style validation-only mode. `--test` is rejected (exit 1) rather than
  delivering real email; use `mailfire validate` to check the authorization and `--preview` to check rendering.
- **Deferred messages** keep the Google account ID. They are resent only from the same account; after a login with a
  different account they are kept but not sent (`blocked (account mismatch)`) until the original account is
  authorized again.
