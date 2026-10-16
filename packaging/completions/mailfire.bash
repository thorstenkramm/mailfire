# bash completion for mailfire
#
# Install as /usr/share/bash-completion/completions/mailfire, or source it
# from ~/.bashrc:  source /path/to/mailfire.bash

_mailfire() {
    local cur prev sub opts
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev=""
    if [[ $COMP_CWORD -ge 1 ]]; then
        prev="${COMP_WORDS[COMP_CWORD-1]}"
    fi

    # Flags taking a path argument.
    case "$prev" in
        --file | --template-file | --footer | --attach | -c | --config)
            compopt -o filenames 2>/dev/null
            # shellcheck disable=SC2207
            COMPREPLY=($(compgen -f -- "$cur"))
            return 0
            ;;
        --template)
            # shellcheck disable=SC2207
            COMPREPLY=($(compgen -W "card card-serif plain plain-serif" -- "$cur"))
            return 0
            ;;
        # Flags taking a free-form value: nothing to complete.
        --to | --cc | --bcc | --from | --subject | --preheader | --message | --m | -m)
            return 0
            ;;
    esac

    # Without a subcommand, "send" is implied.
    sub="send"
    if [[ $COMP_CWORD -ge 2 ]]; then
        case "${COMP_WORDS[1]}" in
            send | validate | resend | version | export-templates) sub="${COMP_WORDS[1]}" ;;
        esac
    fi

    case "$sub" in
        send)
            opts="--to --cc --bcc --from --subject --txt --md --html --html-raw
                  --file --message --m -m --template --template-file --preheader --footer --attach --test --no-defer
                  -c --config -h --help"
            ;;
        validate)
            opts="-c --config -h --help"
            ;;
        resend)
            opts="--list --force -c --config -h --help"
            ;;
        version)
            opts="-h --help"
            ;;
        export-templates)
            if [[ $cur != -* || $prev == -- ]]; then
                compopt -o filenames 2>/dev/null
                # shellcheck disable=SC2207
                COMPREPLY=($(compgen -d -- "$cur"))
                return 0
            fi
            opts="-h --help"
            ;;
    esac

    if [[ $COMP_CWORD -eq 1 && $cur != -* ]]; then
        opts="send validate resend version export-templates"
    fi

    # shellcheck disable=SC2207
    COMPREPLY=($(compgen -W "$opts" -- "$cur"))
    return 0
}

complete -F _mailfire mailfire
