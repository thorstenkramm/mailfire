#compdef mailfire
#
# zsh completion for mailfire
#
# Install as _mailfire in a directory on $fpath (for example
# /usr/share/zsh/site-functions/_mailfire), then run: autoload -U compinit && compinit

_mailfire_send() {
    _arguments -S \
        '*--to=[recipient, repeatable]:address:' \
        '*--cc=[cc recipient, repeatable]:address:' \
        '*--bcc=[bcc recipient, repeatable]:address:' \
        '--from=[sender, overrides config]:address:' \
        '--subject=[subject line]:subject:' \
        '(--md --html --html-raw)--txt[body is plain text (default)]' \
        '(--txt --html --html-raw)--md[body is Markdown]' \
        '(--txt --md --html-raw)--html[body is an HTML fragment]' \
        '(--txt --md --html)--html-raw[body is a complete HTML document]' \
        '(--message --m -m)--file=[read body from file]:file:_files' \
        '(--file)--message=[explicit body]:text:' \
        '(--file)--m=[explicit body]:text:' \
        '(--file)-m[explicit body]:text:' \
        '(--template-file)--template=[email template]:template:(card card-serif plain plain-serif)' \
        '(--template)--template-file=[custom HTML template]:file:_files' \
        '--preheader=[inbox preview text]:text:' \
        '--footer=[HTML fragment appended as footer]:file:_files' \
        '(--preview)*--attach=[attach a file, repeatable]:file:_files' \
        '(--preview)--test[Mailgun test mode; rejected for SMTP and Gmail]' \
        '(--preview)--no-defer[fail immediately instead of deferring on error]' \
        '(--preview)--timeout=[per-request timeout, overrides provider timeout]:duration:' \
        '(--preview)--dedup-key=[skip repeats of this notification within the cooldown]:key:' \
        '(--preview)--cooldown=[suppression window for --dedup-key]:duration:' \
        '(--attach --test --no-defer --timeout --dedup-key --cooldown --json)--preview=[write the rendered HTML instead of sending]:file:_files' \
        '(--preview)--json[print the result as one JSON document]' \
        '(-c --config)'{-c,--config=}'[use this config file]:file:_files' \
        '(-h --help)'{-h,--help}'[show help]'
}

_mailfire_validate() {
    _arguments -S \
        '--json[print the result as one JSON document]' \
        '--timeout=[per-request timeout, overrides provider timeout]:duration:' \
        '(-c --config)'{-c,--config=}'[use this config file]:file:_files' \
        '(-h --help)'{-h,--help}'[show help]'
}

_mailfire_auth() {
    local -a auth_subcommands
    auth_subcommands=(
        'login:authorize Gmail in a browser'
        'status:show whether Gmail is authorized'
        'refresh:refresh the Gmail authorization without sending'
        'logout:remove the local Gmail credentials'
    )
    if (( CURRENT == 2 )); then
        _describe -t commands 'auth command' auth_subcommands
        return
    fi
    case $words[2] in
        login)
            _arguments -S \
                '--provider=[provider to authorize]:provider:(gmail)' \
                '--no-browser[print the consent URL instead of opening a browser]' \
                '--port=[fixed local callback port]:port:' \
                '--manual[paste the callback URL (requires --port)]' \
                '--timeout=[token request timeout]:duration:' \
                '(-c --config)'{-c,--config=}'[use this config file]:file:_files' \
                '(-h --help)'{-h,--help}'[show help]'
            ;;
        *)
            _arguments -S \
                '--provider=[provider]:provider:(gmail)' \
                '--timeout=[token request timeout]:duration:' \
                '--json[print the result as one JSON document]' \
                '(-c --config)'{-c,--config=}'[use this config file]:file:_files' \
                '(-h --help)'{-h,--help}'[show help]'
            ;;
    esac
}

_mailfire_resend() {
    _arguments -S \
        '(--force)--list[list deferred messages without sending]' \
        '(--list)--force[ignore the cooldown and backoff]' \
        '--json[print the result as one JSON document]' \
        '--timeout=[per-request timeout, overrides provider timeout]:duration:' \
        '(-c --config)'{-c,--config=}'[use this config file]:file:_files' \
        '(-h --help)'{-h,--help}'[show help]' \
        '*:deferred message id:'
}

_mailfire_queue() {
    local -a queue_subcommands
    queue_subcommands=(
        'list:list deferred messages and retry state'
        'show:show the metadata of one message'
        'delete:delete the given messages'
        'stats:show queue totals'
    )
    if (( CURRENT == 2 )); then
        _describe -t commands 'queue command' queue_subcommands
        return
    fi
    case $words[2] in
        show)
            _arguments -S \
                '--raw[print the stored request]' \
                '--json[print the result as one JSON document]' \
                '(-c --config)'{-c,--config=}'[use this config file]:file:_files' \
                '(-h --help)'{-h,--help}'[show help]' \
                ':deferred message id:'
            ;;
        delete)
            _arguments -S \
                '--json[print the result as one JSON document]' \
                '(-c --config)'{-c,--config=}'[use this config file]:file:_files' \
                '(-h --help)'{-h,--help}'[show help]' \
                '*:deferred message id:'
            ;;
        *)
            _arguments -S \
                '--json[print the result as one JSON document]' \
                '(-c --config)'{-c,--config=}'[use this config file]:file:_files' \
                '(-h --help)'{-h,--help}'[show help]'
            ;;
    esac
}

_mailfire_version() {
    _arguments -S \
        '--json[print the result as one JSON document]' \
        '(-h --help)'{-h,--help}'[show help]'
}

_mailfire_export-templates() {
    _arguments -S \
        '--json[print the result as one JSON document]' \
        '(-h --help)'{-h,--help}'[show help]' \
        '1:export folder:_files -/'
}

_mailfire() {
    local -a subcommands
    subcommands=(
        'send:compose and send an email (default)'
        'validate:check configuration and provider credentials'
        'auth:manage Gmail authorization'
        'resend:retry deferred messages'
        'queue:inspect and delete deferred messages'
        'version:show version information'
        'export-templates:export editable built-in templates'
    )

    if (( CURRENT == 2 )) && [[ $words[2] != -* ]]; then
        _describe -t commands 'mailfire command' subcommands
        return
    fi

    local sub=send
    if (( CURRENT > 2 )); then
        case $words[2] in
            send|validate|auth|resend|queue|version|export-templates) sub=$words[2] ;;
        esac
    fi

    # Drop the subcommand so _arguments sees only its own flags.
    if (( CURRENT > 2 )) && [[ $sub == $words[2] ]]; then
        words[2]=()
        (( CURRENT-- ))
    fi
    _mailfire_$sub
}

_mailfire "$@"
