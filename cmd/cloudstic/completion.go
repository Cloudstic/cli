package main

import (
	"context"
	"fmt"
	"io"
)

type completionArgs struct{ shell string }

func declareCompletionArgs(_ *globalFlags) (*completionArgs, commandInput) {
	a := &completionArgs{}
	return a, commandInput{positionals: []positionalSpec{requiredPositional(&a.shell, "shell", "_cloudstic_shells")}}
}

func runCompletion(r *runner, _ context.Context, a *completionArgs) int {
	switch a.shell {
	case "bash":
		completionBash(r.out)
	case "zsh":
		completionZsh(r.out)
	case "fish":
		completionFish(r.out)
	default:
		return r.fail("Unsupported shell: %s\nAvailable shells: bash, zsh, fish", a.shell)
	}
	return 0
}

// completionBash writes a bash completion script to w.
func completionBash(w io.Writer) {
	_, _ = fmt.Fprint(w, renderCompletion(`# bash completion for cloudstic

_cloudstic_query() {
    local kind="$1"
    local cur="$2"
    shift 2
    cloudstic __complete -- "$kind" "$cur" "$@" 2>/dev/null
}

_cloudstic() {
    local cur prev words cword
    _init_completion || return

    local commands="@@COMMAND_NAMES@@"

    local global_flags="@@GLOBAL_FLAGS@@"

    # Complete a value flag's argument, wherever the flag appears. Generated
    # from the flag declarations, one arm per completer.
    case "$prev" in
@@BASH_FLAG_VALUES@@
    esac

    # Identify the subcommand
    local cmd=""
    local i
    for ((i=1; i < cword; i++)); do
        case "${words[i]}" in
            -*)
                # skip flags and their values
                case "${words[i]}" in
                    @@VALUE_FLAGS@@)
                        ((i++)) ;;
                esac
                ;;
            *)
                cmd="${words[i]}"
                break
                ;;
        esac
    done

    # Complete subcommand
    if [[ -z "$cmd" ]]; then
        COMPREPLY=($(compgen -W "$commands" -- "$cur"))
        return
    fi

    # Complete flags per subcommand
    local cmd_flags=""
    case "$cmd" in
@@BASH_CMD_FLAGS@@
    esac

    if [[ -z "$cur" || "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "$cmd_flags $global_flags" -- "$cur"))
        return
    fi
}

complete -F _cloudstic cloudstic
`))
}

// completionZsh writes a zsh completion script to w.
func completionZsh(w io.Writer) {
	_, _ = fmt.Fprint(w, renderCompletion(`#compdef cloudstic

# zsh completion for cloudstic

_cloudstic_query() {
    local kind="$1"
    shift
    local -a prior_words
    prior_words=("${words[@]:2:$(( CURRENT - 2 ))}")
    cloudstic __complete -- "$kind" "$PREFIX" "${prior_words[@]}" 2>/dev/null
}

_cloudstic_dynamic_values() {
    local kind="$1"
    local label="$2"
    local -a values
    values=(${(f)"$(_cloudstic_query "$kind")"})
    _describe -t "$kind" "$label" values
}

_cloudstic_profile_names() {
    _cloudstic_dynamic_values profile-names 'profile'
}

_cloudstic_auth_names() {
    _cloudstic_dynamic_values auth-names 'auth entry'
}

_cloudstic_store_prefixes() {
    local -a values
    values=('local:' 's3:' 'b2:' 'sftp://')
    compadd -Q -S '' -X 'store URI prefix' -- "${values[@]}"
}

_cloudstic_shells() {
    local -a values
    values=('bash' 'zsh' 'fish')
    compadd -Q -X 'shell' -- "${values[@]}"
}

_cloudstic_source_prefixes() {
    local -a values
    values=('local:' 'sftp://' 'gdrive' 'gdrive-changes' 'onedrive' 'onedrive-changes')
    compadd -Q -S '' -X 'source URI prefix' -- "${values[@]}"
}

_cloudstic_find_types() {
    local -a values
    values=('f:file' 'd:folder')
    _describe -t types 'entry type' values
}

_cloudstic() {
    local -a commands
    commands=(
@@ZSH_COMMANDS@@
    )

    local prev_word="${words[CURRENT-1]}"

    local -a global_flags
    global_flags=(
@@ZSH_GLOBAL_FLAGS@@
    )
    # Check if a subcommand has been given
    local cmd
    local -i i=2
    while (( i < CURRENT )); do
        case "${words[i]}" in
            -*)
                # Skip flags with values
                case "${words[i]}" in
                    @@VALUE_FLAGS@@)
                        (( i++ )) ;;
                esac
                ;;
            *)
                cmd="${words[i]}"
                break
                ;;
        esac
        (( i++ ))
    done

    if [[ -z "$cmd" ]]; then
        case "$prev_word" in
            @@VALUE_FLAGS@@)
                _arguments $global_flags
                return
                ;;
        esac
        _describe -t commands 'cloudstic command' commands
        _arguments $global_flags
        return
    fi

    case "$cmd" in
@@ZSH_CMD_FLAGS@@
@@ZSH_GROUPS@@
    esac
}

compdef _cloudstic cloudstic
`))
}

// completionFish writes a fish completion script to w.
func completionFish(w io.Writer) {
	_, _ = fmt.Fprint(w, renderCompletion(`# fish completion for cloudstic

function __fish_cloudstic_query
    set -l kind $argv[1]
    cloudstic __complete -- $kind (commandline -ct) (commandline -opc) 2>/dev/null
end

# Disable file completions by default
complete -c cloudstic -f

# Subcommands
@@FISH_COMMANDS@@

# Global flags (available for all subcommands)
@@FISH_GLOBAL_FLAGS@@

# Flags for each top-level command
@@FISH_CMD_FLAGS@@

# Subcommands of grouped commands, and their flags
@@FISH_GROUPS@@

# completion
complete -c cloudstic -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish' -d 'Shell type'
`))
}

// completionCommand declares the `completion` command.
func completionCommand() command {
	return leaf("completion", "Generate shell completion scripts (bash, zsh, fish)",
		nil, declareCompletionArgs, runCompletion)
}
