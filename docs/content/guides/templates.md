---
title: Templates
description: Choose one of four built-in email templates, or export and customize them, or write your own.
weight: 30
icon: thumbnails
---

`--md` and `--html` bodies are wrapped in one of four built-in templates. There are two layouts, `card` and `plain`, and
each comes in a sans-serif and a serif variant.

| Template      | Layout                                           | Font       |
|---------------|--------------------------------------------------|------------|
| `card`        | white card, 600 px wide, centered on a gray page | sans-serif |
| `card-serif`  | white card, 600 px wide, centered on a gray page | serif      |
| `plain`       | no background, left aligned, up to 640 px wide   | sans-serif |
| `plain-serif` | no background, left aligned, up to 640 px wide   | serif      |

`card` is the default. To use another template for a single email, pass `--template`:

```shell
mailfire --md --template plain-serif --to user@example.com --subject "Nightly backup finished" --file report.md
```

To change the default, set `template` in the `[sending]` section of the [configuration file](../reference/configuration.md):

```toml
[sending]
template = "plain"
```

All templates support dark mode and small screens, and they render in Outlook.

## Custom templates

Export editable, self-contained copies of all four built-ins (plus `job-report.md`, the
[job report template](#job-report-template) of `mailfire run`):

```shell
mailfire export-templates ./my-templates
mailfire --md --template-file ./my-templates/card.html --to user@example.com --subject "Report" --file report.md
```

The export command creates the folder and refuses to overwrite existing template files.
Edit the HTML and CSS (including `.content table`, `th` and `td` rules), or write your own file.
Custom templates use Go `text/template` syntax. Available fields are `.Subject`, `.Lang`
(defaults to `en`), `.Preheader`, `.Body` and `.Footer`. Escape plain text with `{{html .Subject}}`
and `{{html .Preheader}}`; insert HTML with `{{.Body}}` and `{{.Footer}}`.
Use `{{with .Footer}}...{{.}}...{{end}}` for optional blocks. Outlook conditional comments
are preserved, and CSS is inlined just as for built-in templates.

While editing, render a local preview instead of sending test emails. It needs no provider credentials:

```shell
mailfire --md --template-file ./my-templates/card.html --file report.md --preview report.html
```

See [Local preview](preview.md) for details.

To configure a custom default:

```toml
[sending]
template_file = "./my-templates/card.html"
```

`template_file` and `template` are mutually exclusive in config, as are `--template-file`
and `--template` on the command line. Either flag overrides the configured default.
Paths support `~`; relative paths are resolved from the current working directory.
The flag requires `--md` or `--html`; configured defaults apply only to those formats.

## Job report template

`mailfire run` (see [Monitor scheduled jobs](jobs.md)) generates its body from a second kind of template: a
Markdown content template, rendered with Go `text/template` and then wrapped in the layout template like any
`--md` body. `export-templates` writes the built-in one as `job-report.md`. Folders exported by earlier versions lack
it; export into a new folder to get it, since the export never overwrites existing templates.

```shell
mailfire export-templates ./my-templates
mailfire run --report-template ./my-templates/job-report.md --to ops@example.com -- /usr/local/bin/backup.sh
mailfire run --report-template ./my-templates/job-report.md --preview report.html -- /usr/local/bin/backup.sh
```

The template is checked before the job runs: a syntax error or an unknown field is a usage error. The fields are a
stable contract; new fields may be added, existing ones are not renamed or removed:

| Field | Value |
|---|---|
| `.Emoji` | ✅ success, 🔴 failure, ⏱️ timeout, ⚠️ interrupted |
| `.Job` | Job name: `--name`, or the command's file name |
| `.JobFull` | Full command line, shell-quoted |
| `.Status` | `success`, `failure`, `timeout`, or `interrupted` |
| `.StatusWord` | `succeeded`, `failed`, `timed out`, or `interrupted` |
| `.Host`, `.WorkDir` | Host name and working directory |
| `.Start`, `.End` | Start and end time, localized |
| `.Duration` | Runtime, such as `3m 12s` |
| `.ExitCode` | The job's exit code (number) |
| `.ExitDetail` | `killed by signal 9 (KILL)`, `could not start: <reason>`, or empty |
| `.Tail` | The `--tail` line count (number) |
| `.StdoutTail`, `.StderrTail` | The last lines of each stream, cleaned for display; empty for an empty stream or `--tail 0` |
| `.StdoutBytes`, `.StderrBytes` | Bytes the job wrote to each stream (number) |
| `.StdoutOmitted`, `.StderrOmitted` | Bytes the 5 MiB log cap dropped from each attachment (number) |
| `.Logs` | Attached log file names, comma separated; empty when nothing is attached |

The values are inserted as they are. They become Markdown, so a job name with Markdown characters can change the
formatting; the built-in template puts the command and the output in code spans and fenced blocks.

## Fonts

The fonts come with the operating system, so nothing is downloaded. Each font list starts with fonts that are installed
by default on macOS/iOS, Windows, Android and Linux. Arial and Times New Roman are only used if none of the others is
available.

- **Sans-serif:** system-ui, -apple-system, Segoe UI, Roboto, Helvetica Neue, Noto Sans, Liberation Sans, Arial
- **Serif:** Charter, Bitstream Charter, Sitka Text, Cambria, Georgia, Times New Roman

## Alerts

In Markdown bodies you can use GitHub-style alerts. Each template gives them its own look, as shown in the screenshots
below:

```markdown
> [!NOTE]
> The next full backup runs on Sunday at 02:00.

> [!TIP]
> List the snapshots of a single host with `restic snapshots --host files01`.

> [!IMPORTANT]
> Restore tests are due this month. Pick a host and restore at least one directory.

> [!WARNING]
> files01 timed out on the first attempt and was backed up on the second.

> [!CAUTION]
> Snapshots older than 90 days will be deleted on Monday.
```

As on GitHub, the marker must be on its own line, and alerts only work at the top level, not inside lists or other
quotes. In the plain-text part of the email, an alert appears as a quote that starts with its type, e.g. `> NOTE`.

## Screenshots

All screenshots show the same message, sent with `--md`, `--preheader` and `--footer`.

### card

![The card template: a white card with sans-serif text on a gray page, with a table and five colored alerts](/images/templates/card.png)

### card-serif

![The card-serif template: a white card with serif text on a warm gray page, alerts with small uppercase titles](/images/templates/card-serif.png)

### plain

![The plain template: left-aligned sans-serif text on white, alerts with a colored left bar](/images/templates/plain.png)

### plain-serif

![The plain-serif template: left-aligned serif text on white, alerts with italic titles](/images/templates/plain-serif.png)
