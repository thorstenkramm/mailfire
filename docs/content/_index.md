---
title: Mailfire
description: Send plain-text, Markdown, and HTML email from the command line through Mailgun, Gmail, or SMTP.
headline: Email from the command line, done right.
lead: >-
  Mailfire sends plain-text, Markdown, and HTML email from scripts, cron jobs, CI pipelines, and your terminal —
  through Mailgun, Gmail, or any SMTP server. Beautiful templates, attachments, retries, and JSON output included.
note: A single binary for Linux, macOS, and Windows. Open source, MIT licensed.
terminal: |-
  $ mailfire --md --to ops@example.com --subject "Nightly backup" <<EOF
  # Backup finished

  All **12 hosts** completed in 4m 31s.

  > [!WARNING]
  > files01 needed a second attempt.
  EOF
  <20261018T091500.3ad5358f96c70a25@example.com>
featuresTitle: Everything a notification needs
features:
  - icon: file-text
    title: Markdown that looks great
    text: Write Markdown or HTML and Mailfire renders it into a responsive template that works in Outlook, dark mode, and on mobile — with a plain-text alternative.
    link: /guides/markdown
  - icon: server
    title: Three ways to send
    text: Use the Mailgun API, your Gmail account with send-only OAuth, or standard SMTP submission to any provider or local relay.
    link: /guides/smtp
  - icon: refresh
    title: Never lose a message
    text: Network errors and outages defer the message instead of dropping it. `mailfire resend` retries with backoff from cron.
    link: /guides/deferred-delivery
  - icon: bell
    title: No alert storms
    text: Opt-in deduplication sends a repeated monitoring alert once per cooldown, across sends and retries.
    link: /guides/deduplication
  - icon: code
    title: Built for scripts
    text: Stable exit codes, one JSON document per command, and environment-only configuration for CI and containers.
    link: /guides/scripting
  - icon: desktop
    title: Preview before you send
    text: Render the exact HTML body to a file without credentials or network access, and iterate on templates offline.
    link: /guides/preview
templatesTitle: Four templates, or your own
templatesText: >-
  `card`, `card-serif`, `plain`, and `plain-serif` cover reports, alerts, and newsletters. Export them, edit the HTML
  and CSS, and Mailfire inlines the styles so every client renders them the same.
providersTitle: Pick your provider
providers:
  - link: /guides/mailgun
  - link: /guides/smtp
  - link: /guides/gmail
gmail:
  title: How Mailfire uses your Gmail account
  text: >-
    With the Gmail provider, Mailfire asks Google only for permission to send email (`gmail.send`) and for an opaque
    account identifier (`openid`), not your name or email address. It sends the messages you tell it to send — it never reads, lists, or changes the mail in
    your mailbox. Your sign-in tokens are stored only on your own computer and never reach the Mailfire project or
    anyone else.
installTitle: Install in seconds
install:
  - name: macOS
    code: |-
      brew tap thorstenkramm/tap
      brew trust --cask thorstenkramm/tap/mailfire
      brew install --cask mailfire
    link: /getting-started/installation
  - name: Debian / Ubuntu
    code: |-
      # Add the signed, pinned repository first (see the Linux guide), then:
      sudo apt install mailfire
    link: /getting-started/linux-packages
  - name: Fedora / RPM
    code: |-
      # Add the signed repository first (see the Linux guide), then:
      sudo dnf install mailfire
    link: /getting-started/linux-packages
  - name: From source
    code: |-
      git clone https://github.com/thorstenkramm/mailfire.git
      cd mailfire && make build
    link: /getting-started/installation
---
