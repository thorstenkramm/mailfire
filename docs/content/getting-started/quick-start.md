---
title: Quick start
description: Configure a sending provider, check the setup, and send your first Markdown email.
weight: 20
icon: play-circle
---

You need one of:

- a Mailgun API key and a sending domain configured in Mailgun (shown on this page),
- a Gmail account — see [Gmail](../guides/gmail.md), or
- an SMTP submission server, from your email provider or a local relay — see [SMTP](../guides/smtp.md).

## 1. Configure Mailfire

Create `.mailfire.conf` in your current directory with your API key, region (`us` or `eu`), sending domain, and
sender:

```toml
[mailgun]
api_key = "<your-api-key>"
region = "eu"

[sending]
domain = "example.com"
from = "My Name <user@example.com>"
```

On Linux and macOS, restrict access to your credentials:

```shell
chmod 600 .mailfire.conf
```

Mailfire also finds its configuration in your user and system configuration directories, and every setting can come
from an environment variable instead. See [Configuration](../reference/configuration.md) for all locations and
settings.

## 2. Check your setup

```shell
mailfire validate
mailfire --test --to user@example.com --subject "Test" --message "Hello"
```

`validate` checks the configuration and asks Mailgun to confirm the API key and domain — without sending anything.
Replace the recipient with your own address: `--test` asks Mailgun to validate the full request without delivering
it, and the output says explicitly that nothing was delivered.

> [!TIP]
> SMTP and Gmail have no validation-only mode, so `--test` is refused with them. Use `mailfire validate` to check the
> connection and credentials, and [`--preview`](../guides/preview.md) to check the rendering.

## 3. Send an email

```shell
mailfire --md --to user@example.com --subject "Have a nice day" <<EOF
# This is an email

It is written in Markdown and delivered as HTML.

[Mailfire](https://mailfire.system42.io)
EOF
```

This example uses Bash/Zsh heredoc syntax; on other shells, use `--file` or `--message` instead. On success, Mailfire
prints the message ID (Mailgun's or Gmail's, or for SMTP the Message-ID it generated) and exits with code `0`.

## Where next

- [Writing emails](../guides/formats.md): formats, attachments, and recipients
- [Templates](../guides/templates.md): change the look, or bring your own
- [Deferred delivery](../guides/deferred-delivery.md): what happens when sending fails
- [Scripting and automation](../guides/scripting.md): exit codes, JSON output, and cron
