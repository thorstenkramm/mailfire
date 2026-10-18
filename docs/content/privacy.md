---
title: Privacy policy
description: What Mailfire does with your data, including the Google user data it accesses when you send through Gmail.
---

**Effective date:** October 18, 2026

Mailfire is an open-source command-line program that sends email you compose, through a provider you configure:
Mailgun, an SMTP server, or your Gmail account. It runs entirely on your own computer. The Mailfire project operates
no servers that receive, store, or process your data, and Mailfire contains no analytics, telemetry, or tracking.

## Google user data

When you choose the [Gmail provider](guides/gmail.md) and sign in with `mailfire auth login --provider gmail`,
Mailfire asks Google for two permissions:

| Scope | Why Mailfire requests it |
|---|---|
| `https://www.googleapis.com/auth/gmail.send` | To send the email messages you tell Mailfire to send, from your Gmail or Google Workspace account. |
| `openid` | To obtain Google's opaque account identifier, so a message queued for a later retry is never sent from a different account after you sign in again. |

### What Mailfire accesses

- **Sending only.** Mailfire calls a single Gmail API method, `users.messages.send`, with a message you composed.
  It cannot and does not read, search, list, modify, or delete the email in your mailbox, your contacts, or your
  calendar. Google's response to a send contains only the identifiers of the sent message.
- **Account identifier.** From the sign-in, Mailfire keeps only Google's opaque account identifier (the `sub` value).
  It does not request or store your name, email address, or profile picture.
- **Tokens.** Google issues an access token and a refresh token, which allow Mailfire to send on your behalf.

### How the data is stored

All of this data stays on your computer:

- The tokens, their expiry time, and the account identifier are saved in a credential file, by default
  `~/.config/mailfire/gmail-credentials.json` (on Windows, `%APPDATA%\mailfire\gmail-credentials.json`). On Linux
  and macOS, the file is readable only by your user account.
- If a send fails temporarily, the composed message and the account identifier are kept in Mailfire's local queue
  until `mailfire resend` delivers it or you delete it.
- If you enable Mailfire's optional send log, each attempt is recorded locally with its recipients, subject,
  status, and message ID. Credentials and tokens are never written to the log.

Mailfire sends your data only to Google, over encrypted connections, and only to sign you in, refresh the access
token, and send your messages.

### How the data is used and shared

Mailfire uses Google user data only to provide its user-facing feature: sending the email you ask it to send. The
Mailfire project and its developer never receive this data, and it is not shared with, transferred to, or sold to
anyone. It is not used for advertising, profiling, or to train AI or machine-learning models, and no human reads
it.

Mailfire's use and transfer of information received from Google APIs will adhere to the
[Google API Services User Data Policy](https://developers.google.com/terms/api-services-user-data-policy),
including the Limited Use requirements.

### Retention and deletion

Your data stays on your computer until you remove it:

- `mailfire auth logout --provider gmail` deletes the local credential file.
- To also revoke Mailfire's access on Google's side, remove Mailfire under
  [Third-party apps & services](https://myaccount.google.com/permissions) in your Google account. After that, the
  stored tokens can no longer be used to send email.
- `mailfire queue delete` removes queued messages, and you can delete the send log file at any time.

Uninstalling Mailfire does not delete these files; remove them as described above.

## Mailgun and SMTP

With Mailgun or SMTP, Mailfire sends your messages and credentials only to the Mailgun API or the SMTP server you
configure. Those services process your messages under their own privacy policies. Your API key and passwords stay
in your configuration file or environment variables on your computer.

## This website

This documentation site is a static website hosted on GitHub Pages. It sets no cookies and includes no analytics or
third-party scripts. Your color theme choice is saved in your browser's local storage and never leaves your
browser. GitHub, as the host, may process technical data such as IP addresses to deliver the site; see
[GitHub's privacy statement](https://docs.github.com/en/site-policy/privacy-policies/github-general-privacy-statement).

## Changes

Changes to this policy are published on this page with a new effective date. The full history is available in the
[project repository](https://github.com/thorstenkramm/mailfire/commits/main/docs/content/privacy.md).

## Contact

For questions about this policy or your data, email [privacy@system42.io](mailto:privacy@system42.io).
