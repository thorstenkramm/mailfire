---
title: Markdown, alerts, and images
description: Write rich emails in Markdown, with tables, GitHub-style alerts, and local images that are attached and shown inline.
weight: 20
icon: file-text
---

With `--md`, Mailfire renders the body as Markdown, wraps it in the selected [template](templates.md), and inlines the
template's CSS, so the email looks the same in Gmail, Outlook, Apple Mail, and mobile clients. A plain-text alternative
is derived from the same source.

```shell
mailfire --md --to ops@example.com --subject "Backup report" --file report.md
```

## Supported Markdown

Mailfire follows CommonMark with the common GitHub extensions:

- headings, emphasis, lists, block quotes, code spans, and fenced code blocks
- tables and strikethrough
- automatic links for bare URLs
- typographic quotes and dashes
- GitHub-style alerts (below)

Raw HTML in Markdown is not passed through; use `--html` for trusted HTML fragments.

## Alerts

Markdown supports [GitHub-style alerts](https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/basic-writing-and-formatting-syntax#alerts):

```markdown
> [!NOTE]
> The next full backup runs on Sunday at 02:00.

> [!TIP]
> List the snapshots of a single host with `restic snapshots --host files01`.

> [!IMPORTANT]
> Restore tests are due this month.

> [!WARNING]
> files01 timed out on the first attempt and was backed up on the second.

> [!CAUTION]
> Snapshots older than 90 days will be deleted on Monday.
```

As on GitHub, the marker must be on its own line, and alerts only work at the top level, not inside lists or other
quotes. Each template gives them its own look; see the [template screenshots](templates.md#screenshots). In the
plain-text part, an alert appears as a quote that starts with its type, for example `> NOTE`.

## Images

Local images referenced in Markdown are attached and displayed inline:

```markdown
![Disk usage over the last 30 days](charts/disk.png)
```

Relative paths are resolved from the directory of the `--file` body (or the current directory for stdin and
`--message`). Remote image URLs are left unchanged. Many email clients block remote images until the reader allows
them, so attach charts and screenshots as local files.

## Preheader and footer

Two options add common email elements to `--md` and `--html` bodies:

- `--preheader <text>` sets the inbox preview text that clients show next to the subject. It is hidden in the email
  itself.
- `--footer <path>` appends a trusted HTML fragment as a footer, for example an unsubscribe note or a legal line.

```shell
mailfire --md --preheader "12 hosts, all green" --footer footer.html \
  --to ops@example.com --subject "Nightly backup" --file report.md
```
