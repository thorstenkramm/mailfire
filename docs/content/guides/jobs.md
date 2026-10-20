---
title: Monitor scheduled jobs
description: Wrap cron, systemd, and Task Scheduler jobs with mailfire run to get an email report of every run, or only of failures.
weight: 95
icon: clock
---

`mailfire run` runs a command, captures its output, and emails a report: status, start and end time, duration, exit
code, the last lines of stdout and stderr, and the full logs as attachments. It is a drop-in wrapper: it exits with the
job's exit code, so the scheduler still sees the job's result.

```shell
mailfire run --to ops@example.com /usr/local/bin/my-backup.sh
mailfire run --to ops@example.com --on failure -- rsync -a /data /mnt/nas
mailfire run --to ops@example.com --name nightly-db -- sh -c 'pg_dump db | gzip > /backup/db.gz'
```

The subject of the first example is `✅ my-backup.sh succeeded on db1`; a failure reads
`🔴 my-backup.sh failed on db1`.

## Arguments

Mailfire's flags come first. The first argument that is not a flag, or the argument after `--`, is the command, and
everything after it belongs to the command, unparsed — like `sudo`, `timeout`, or `env`. A Mailfire flag after the
command is passed to the job:

```shell
mailfire run --to ops@example.com -- printf '%s\n' --to   # prints "--to"
```

Use `--` whenever the command itself starts with a dash or you want to be explicit.

The command runs directly, without a shell, with an empty standard input and Mailfire's environment and working
directory. For pipes, redirections, or variables, run a shell explicitly and name the job with `--name`:

```shell
mailfire run --to ops@example.com --name nightly-db -- sh -c 'pg_dump db | gzip > /backup/db.gz'
```

## What is checked before the job starts

Everything that does not depend on the job's result is checked first: flags, the configuration, recipients, the
sender, the subject and report templates, the locale, and the selected provider's settings. A problem there is a usage
error (exit 1) or a configuration error (exit 2), and the job does **not** run. Provider problems that show only when
sending — a rejected API key, an unreachable server — are reported after the job, with the exit codes below.

The configuration, report template, layout template, and footer are loaded before the job starts and reused for its
report. Edits to those files during a long-running job apply to the next run. Gmail access tokens are refreshed when
the report is sent, if needed.

`--test` (Mailgun only) and `--preview` affect only the email: the job always runs for real.

## Exit codes

- The job's exit code, so schedulers still see the job's result. A command that cannot be started exits with `127`
  (not found) or `126` (not executable), as in shells; a job killed by signal *n* with `128+n`.
- If the job succeeded but the report could not be sent or was deferred, Mailfire's own code (`2`–`5`) applies.
  A failed job keeps its exit code even if reporting also fails. See [Exit codes](../reference/exit-codes.md).

## Report only failures

`--on failure` sends a report only when the job did not succeed: it failed, timed out, was interrupted, or could not
be started. A successful run sends nothing.

```cron
*/15 * * * * mailfire run --to ops@example.com --on failure -- /usr/local/bin/check-replication
```

Some programs use nonzero exit codes for success; `--success-codes 0,24` treats rsync's "files vanished" code as
success. The report's status changes; Mailfire's exit code is still the job's.

A job that fails every 15 minutes would send a report every 15 minutes. Add a [deduplication](deduplication.md) key to
report a persistent failure once per cooldown:

```cron
*/15 * * * * mailfire run --to ops@example.com --on failure --dedup-key replication --cooldown 6h -- /usr/local/bin/check-replication
```

## Long-running jobs

`--max-runtime 2h` stops a job that runs too long and reports status `timeout`. On Linux and macOS, Mailfire sends
SIGTERM to the job's process group and SIGKILL 5 seconds later; on Windows it terminates the process tree.

When Mailfire itself receives SIGINT or SIGTERM (for example when systemd stops the unit), it forwards the signal to
the job's process group, waits for the job, and still sends the report, with status `interrupted`.

## The report

The body shows the command, host, working directory, start and end time, duration, and exit code, followed by the last
`--tail` lines (default 10) of stdout and of stderr. Sections of empty streams are omitted. For display, terminal color
codes are removed, progress lines overwritten with `\r` show their final state, invalid UTF-8 is replaced, and each line
is cut at 1,000 characters.

The logs are attached as `<job>.stdout.log` and `<job>.stderr.log` when they are not empty. Each log is capped at 5
MiB: the first and last 2.5 MiB are kept, with a `[... N bytes omitted ...]` marker in between, so the message stays
well below provider limits. `--no-attach-logs` disables the attachments.

Output is captured safely however much the job prints: at most 10 KiB per stream is held in memory, the rest goes to an
owner-only temporary file that never grows beyond the cap and is removed when Mailfire exits (on Linux and macOS it
has no directory entry at all, so not even a crash leaves it behind).

`--tee` also copies the job's output live to Mailfire's stdout and stderr, for example to keep it in the systemd
journal. With `--json`, both streams go to stderr, so stdout carries only the [JSON document](../reference/json-output.md#run).

### Subject

The default subject is `{emoji} {job} {status} on {host}`. `--subject` takes your own template with these
placeholders:

| Placeholder | Value |
|---|---|
| `{emoji}` | ✅ success, 🔴 failure, ⏱️ timeout, ⚠️ interrupted |
| `{job}` | Job name: `--name`, or the command's file name |
| `{jobfull}` | The full command line, shell-quoted |
| `{status}` | `succeeded`, `failed`, `timed out`, or `interrupted` |
| `{duration}` | Runtime, such as `850ms`, `4s`, `3m 12s`, `2h 5m`, or `1d 3h` |
| `{exitcode}` | The job's exit code |
| `{host}` | Host name |

`{{` and `}}` produce literal braces. An unknown placeholder is a usage error, reported before the job runs. Braces
are used instead of `%`, because `%` is special in a crontab: it ends the command and starts its standard input.

### Dates

Start and end times use the local time zone. Their month and weekday names and date order follow a locale, for example
`So., 18. Okt. 2026 17:22:10 CEST` (`de_DE`), `Sun, Oct 18, 2026 17:22:10 CEST` (`en_US`), or
`Sun, 18 Oct 2026 17:22:10 CEST` (`en_GB`). Only dates are localized; all other texts stay English.

The locale is chosen in this order:

1. `--locale de_DE`
2. `locale` in the `[run]` section of the configuration, or `MAILFIRE_RUN_LOCALE`
3. The environment: `LC_ALL`, `LC_TIME`, `LANG` (the first that is set; `C` and `POSIX` count as unset); on Windows the
   user's locale
4. `en_US`

`de_DE`, `de-DE`, `de_DE.UTF-8`, and `de_DE@euro` are accepted; a bare language such as `de` selects its main locale.
An unsupported locale from `--locale` or the configuration is an error; one from the environment silently falls back
to `en_US`.

Cron, launchd, and Task Scheduler often start jobs without `LANG`, so a job can show different dates in a terminal test
than in its scheduled run. Set the locale in the configuration file to avoid that:

```toml
[run]
locale = "de_DE"
```

### Templates

The report body comes from a Markdown content template, which the selected layout template (`--template`,
`--template-file`, or the configuration) wraps like any other email. Export the built-in report template with
`mailfire export-templates`, edit `job-report.md`, and select it with `--report-template`; see
[Templates](templates.md#job-report-template). Use `--preview report.html` to run the job and write the rendered report
to a file instead of sending it.

## Scheduler examples

### cron

```cron
MAILTO=""
0 2 * * * /usr/local/bin/mailfire run --to ops@example.com --name backup -- /usr/local/bin/backup.sh
```

`MAILTO=""` stops cron from mailing the job's output itself; Mailfire reports it.

### systemd

```ini
# /etc/systemd/system/backup.service
[Service]
Type=oneshot
ExecStart=/usr/local/bin/mailfire run --to ops@example.com --tee --name backup -- /usr/local/bin/backup.sh
```

`--tee` keeps the job's output in the journal. A timer unit starts the service; systemd sees the job's exit code.

### Windows Task Scheduler

Use `mailfire.exe` as the program and pass the job as arguments:

```text
Program:   C:\Program Files\mailfire\mailfire.exe
Arguments: run --to ops@example.com --on failure -- C:\Jobs\backup.ps1
```

`.exe` and `.com` files run directly, `.bat` and `.cmd` files through `cmd /c`, and `.ps1` scripts through
`powershell -NoProfile -ExecutionPolicy Bypass -File`. Windows output with CRLF line endings is normalized. Windows has
no signals: `--max-runtime` and interruption terminate the job's process tree.

## Secrets in logs

The job's output is emailed. If it can contain secrets — tokens, connection strings, customer data — limit what is
sent: `--tail 0` shows no output in the body, and `--no-attach-logs` attaches nothing. The status, exit code, and
times are still reported.
