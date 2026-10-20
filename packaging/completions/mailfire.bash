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
        --file | --template-file | --footer | --attach | --preview | --report-template | -c | --config)
            compopt -o filenames 2>/dev/null
            # shellcheck disable=SC2207
            COMPREPLY=($(compgen -f -- "$cur"))
            return 0
            ;;
        --provider)
            # shellcheck disable=SC2207
            COMPREPLY=($(compgen -W "gmail" -- "$cur"))
            return 0
            ;;
        --on)
            # shellcheck disable=SC2207
            COMPREPLY=($(compgen -W "always failure" -- "$cur"))
            return 0
            ;;
        --template)
            # shellcheck disable=SC2207
            COMPREPLY=($(compgen -W "card card-serif plain plain-serif" -- "$cur"))
            return 0
            ;;
        # Flags taking a free-form value: nothing to complete.
        --to | --cc | --bcc | --from | --subject | --preheader | --message | --m | -m | --timeout | --dedup-key | --cooldown | --port | --name | --max-runtime | --success-codes | --tail | --locale)
            return 0
            ;;
    esac

    # Without a subcommand, "send" is implied.
    sub="send"
    if [[ $COMP_CWORD -ge 2 ]]; then
        case "${COMP_WORDS[1]}" in
            send | run | validate | auth | resend | queue | version | export-templates) sub="${COMP_WORDS[1]}" ;;
        esac
    fi

    case "$sub" in
        send)
            opts="--to --cc --bcc --from --subject --txt --md --html --html-raw
                  --file --message --m -m --template --template-file --preheader --footer --attach --test --no-defer
                  --timeout --dedup-key --cooldown --preview --json -c --config -h --help"
            ;;
        run)
            # Flags come first; from the command on, complete commands and files.
            local i
            for ((i = 2; i < COMP_CWORD; i++)); do
                case "${COMP_WORDS[i]}" in
                    --) break ;;
                    --to | --cc | --bcc | --from | --subject | --name | --on | --max-runtime | --success-codes | \
                        --tail | --locale | --report-template | --template | --template-file | --preheader | --footer | \
                        --timeout | --dedup-key | --cooldown | --preview | -c | --config) ((i++)) ;;
                    -*) ;;
                    *) break ;;
                esac
            done
            if [[ $i -lt $COMP_CWORD || ($cur != -* && $COMP_CWORD -gt 2) ]]; then
                compopt -o default 2>/dev/null
                # shellcheck disable=SC2207
                COMPREPLY=($(compgen -c -- "$cur"))
                return 0
            fi
            opts="--to --cc --bcc --from --subject --name --on --max-runtime --success-codes --tail --tee
                  --no-attach-logs --locale --report-template --template --template-file --preheader --footer
                  --test --no-defer --timeout --dedup-key --cooldown --preview --json -c --config -h --help"
            ;;
        validate)
            opts="--timeout --json -c --config -h --help"
            ;;
        auth)
            if [[ $COMP_CWORD -eq 2 && $cur != -* ]]; then
                opts="login status refresh logout"
            elif [[ ${COMP_WORDS[2]} == login ]]; then
                opts="--provider --no-browser --port --manual --timeout -c --config -h --help"
            else
                opts="--provider --timeout --json -c --config -h --help"
            fi
            ;;
        resend)
            opts="--list --force --timeout --json -c --config -h --help"
            ;;
        queue)
            if [[ $COMP_CWORD -eq 2 && $cur != -* ]]; then
                opts="list show delete stats"
            elif [[ ${COMP_WORDS[2]} == show ]]; then
                opts="--raw --json -c --config -h --help"
            else
                opts="--json -c --config -h --help"
            fi
            ;;
        version)
            opts="--json -h --help"
            ;;
        export-templates)
            if [[ $cur != -* || $prev == -- ]]; then
                compopt -o filenames 2>/dev/null
                # shellcheck disable=SC2207
                COMPREPLY=($(compgen -d -- "$cur"))
                return 0
            fi
            opts="--json -h --help"
            ;;
    esac

    if [[ $COMP_CWORD -eq 1 && $cur != -* ]]; then
        opts="send run validate auth resend queue version export-templates"
    fi

    # shellcheck disable=SC2207
    COMPREPLY=($(compgen -W "$opts" -- "$cur"))
    return 0
}

complete -F _mailfire mailfire
