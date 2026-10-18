---
title: Local preview
description: Render the exact HTML body of an email to a file, without credentials, recipients, or network access.
weight: 40
icon: desktop
---

`--preview <path>` renders the email body to an HTML file instead of sending it. Use it to check a template, footer,
or Markdown layout before mailing anyone:

```shell
mailfire --md --template-file ./my-templates/card.html --footer footer.html \
  --subject "Report" --file report.md --preview report.html
```

The preview uses the same Markdown rendering, template, preheader, footer, and CSS inlining as a send. Open the file in
a browser and reload it after each change.

## Preview or test mode?

| | `--preview` | `--test` |
|---|---|---|
| Contacts the provider | No | Yes, Mailgun validates the request (Mailgun only) |
| Needs a configuration and credentials | No | Yes |
| Needs `--to` and `--subject` | No | Yes |
| Output | Rendered HTML body | Mailgun message ID |

SMTP and Gmail have no validation-only mode: `--test` is rejected with them instead of delivering a real email.

## What a preview needs

A preview needs no configuration file, provider settings, recipients, sender, or subject. Only the template selection
is read from a configuration file, `MAILFIRE_SENDING_TEMPLATE`, or `MAILFIRE_SENDING_TEMPLATE_FILE`; other settings
and variables are ignored. Supplied addresses and subjects are still checked. Nothing is sent, deferred,
deduplicated, or logged.

- `--md` and `--html` produce the rendered HTML part. Local Markdown images are embedded as `data:` URLs, so the
  preview is one portable file; remote image URLs are unchanged. A send attaches local images instead.
- `--txt` shows the escaped body in a `<pre>` block.
- `--html-raw` writes the supplied document unchanged.

## Output file

An existing output file is replaced, keeping its permissions, but only after rendering succeeded; a failed preview
leaves the previous file untouched. A symbolic link as output is followed; a link to a missing file is refused and left
unchanged. Mailfire refuses an output that is one of the inputs (body file or standard input redirected from a file,
footer, template, Markdown image, or configuration file), including through symbolic or hard links.

Use `--preview -` to write only the HTML to stdout; diagnostics go to stderr. To write a file named `-`, use `./-`.

## Options that do not apply

`--attach`, `--test`, `--no-defer`, `--timeout`, `--dedup-key`, and `--cooldown` only affect sending and are rejected
with `--preview`. `--json` is rejected too, because a preview writes HTML, not a command result. Plain-text parts,
headers, and MIME/EML export are not part of the preview.
