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
        '--template=[email template]:template:(card card-serif plain plain-serif)' \
        '--preheader=[inbox preview text]:text:' \
        '--footer=[HTML fragment appended as footer]:file:_files' \
        '*--attach=[attach a file, repeatable]:file:_files' \
        '--test[use Mailgun test mode]' \
        '--no-defer[fail immediately instead of deferring on error]' \
        '(-c --config)'{-c,--config=}'[use this config file]:file:_files' \
        '(-h --help)'{-h,--help}'[show help]'
}

_mailfire_validate() {
    _arguments -S \
        '(-c --config)'{-c,--config=}'[use this config file]:file:_files' \
        '(-h --help)'{-h,--help}'[show help]'
}

_mailfire_resend() {
    _arguments -S \
        '(--force)--list[list deferred messages without sending]' \
        '(--list)--force[ignore the resend cooldown]' \
        '(-c --config)'{-c,--config=}'[use this config file]:file:_files' \
        '(-h --help)'{-h,--help}'[show help]' \
        '*:deferred message id:'
}

_mailfire_version() {
    _arguments -S \
        '(-h --help)'{-h,--help}'[show help]'
}

_mailfire() {
    local -a subcommands
    subcommands=(
        'send:compose and send an email (default)'
        'validate:check config and Mailgun credentials'
        'resend:retry deferred messages'
        'version:show version information'
    )

    if (( CURRENT == 2 )) && [[ $words[2] != -* ]]; then
        _describe -t commands 'mailfire command' subcommands
        return
    fi

    local sub=send
    if (( CURRENT > 2 )); then
        case $words[2] in
            send|validate|resend|version) sub=$words[2] ;;
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
