# Mailfire

Send beautiful email from the command line via the Mailgun API with ease.

## at a glance

`mailfire` is an API client for the Mailgun HTTP-API that enables you to send email from the command line.

```shell
mailfire --md --to user@example.com --subject "Have a nice day" <<EOF
# This is an Email

It is written in markdown. It's delivered as HTML.

[mailfire](https://github.com/thorstenkramm/mailfire)
```

The following formats are supported:
- `--txt`, plaintext, default 
- `--md`, Markdown rendered as HTML using a built-in template
- `--html`, HTML inserted into the built-in template, you don't have to care about the frame around your HTML
- `--html-raw`, raw HTML submitted, no base frame layout around it

Sending attachments:
```shell
mailfire --to user@example.com --subject "The files" --attach ~/doc1.pdf --attach ~/image.png 
```

## Configuration

`mailfire` looks for its configuration in the following location in the given order. First hit wins.
- `./.mailfire.conf`, current directory
- `$HOME/.config/mailfire/mailfire.conf`, your home directory
- `/etc/mailfire/mailfire.conf`, system-wide configuration

On Windows:
- `.\.mailfire.conf`, current directory
- `%APPDATA%\mailfire\mailfire.conf`, your user profile
- `%PROGRAMDATA%\mailfire\mailfire.conf`, system-wide configuration

> [!WARNING]
> Using a system-wide configuration is usually insecure. Anyone can take the API key and use it anywhere.
> Use only with the utmost care!

On Linux and macOS, `mailfire` warns on startup when the config file is accessible by group or
others (permissions more permissive than `0600`); fix it with `chmod 600 <path>`. On Windows
this check is skipped — file access is governed by NTFS ACLs, not POSIX permission bits.

```toml
## mailfire configuration
[mailgun]
  ## Your API key, string, mandatory
  api_key = "<your-api-key>"

  ## mailgun region, string, mandatory
  ## It's either 'us' or 'eu'
  region = "eu"

[sending]
  ## Domain used for sending as configured on mailgun
  ## domain, string, mandatory
  domain = "example.com"

  ## From address, string, optional
  ## If not given, you must specify via --from for every email to be sent.
  ## Can be overwritten per email with --from
  ## Formats: "user@example" or "My Name <user@example.com>" 
  from = "My Name <user@example.com>"

[logging]
  ## Logfile, string, optional
  ## By default, logging is disabled
  ## If a logfile is given, all sending attempts and their status will be logged in JSON format
  #logfile = ~/logs/mailfire.log
  ## On Windows (use a literal string so backslashes need no escaping):
  #logfile = 'C:\Users\me\AppData\Local\mailfire\mailfire.log'

  ## Loglevel, string, optional, default 'information'
  ## Supports:
  ## error: datetime, message-id, to, status code (only on errors, successfull delivery is not logged) 
  ## information: datetime, message-id, to, subject, status code
  ## debug: datetime, message-id, from, to, subject, message size, status code, api response body
  loglevel = "debug"

  ## To not flood your disk, logs are automatically truncated.
  ## If the max size is exeeded, oldes logs get removed.
  ## max_size, string, optional, default 50M
  max_size = "100M"

[defer]
  ## Failed sending attempts are by default deferred for later retrying.
  ## defer, boolean, default true
  defer = false

  ## Deferred email are stored by default in your home dir in $HOME/.local/share/mailfire
  ## (%LOCALAPPDATA%\mailfire on Windows)
  ## The default defer_dir is created on-the-fly
  ## defer_dir, path to an existing directory, string, optional
  defer_dir = /var/tmp/mailfire/deferred

  ## To not flood your disk, the deferred_dir is automatically cleaned up.
  ## If the max size is exeeded, oldes unsent emails get removed.
  ## max_size, string, optional, default 50M
  max_size = "100M"

  ## Cooldown before a deferred message is retried by 'mailfire resend'.
  ## Messages younger than this are skipped to avoid hammering Mailgun
  ## during an outage. Override per run with 'resend --force'.
  ## resend_cooldown, duration string, optional, default 5m
  resend_cooldown = "5m"
```

## full usage documentation

The help message explains full usage as follows:

```text
$ mailfire --help
mailfire — send email from the command line via the Mailgun API

Usage:
  mailfire [send] [flags]     compose and send an email (default)
  mailfire validate           check config and Mailgun credentials
  mailfire resend [--list | --force | <id>...]
                              retry deferred messages
  mailfire version            show version information

Flags (send):
  --to <addr>        recipient, repeatable (required)
  --cc <addr>        cc recipient, repeatable
  --bcc <addr>       bcc recipient, repeatable
  --from <addr>      sender, overrides config ("Name <user@example.com>")
  --subject <text>   subject line (required)
  --txt              body is plain text (default)
  --md               body is Markdown, rendered into the built-in template
  --html             body is an HTML fragment, wrapped in the template
  --html-raw         body is a complete HTML document, sent as-is
  --file <path>      read body from file instead of stdin
  --preheader <txt>  inbox preview text (HTML formats only)
  --footer <path>    HTML fragment appended as footer (HTML formats only)
  --attach <path>    attach a file, repeatable
  --test             use Mailgun test mode: validate the request, deliver nothing
  --no-defer         fail immediately instead of deferring on error
  -c, --config <p>   use this config file
  -h, --help         show help
```

It needs:
- validation, to validate the config and the api key. Mailgun has no ping route;
  `GET /v3/domains/{domain}` authenticates the key and confirms the domain in one call.
- resending deferred messages
- showing the version

All three are covered as subcommands (`validate`, `resend`, `version`) and specified in detail in [tmp/prd.md](tmp/prd.md).