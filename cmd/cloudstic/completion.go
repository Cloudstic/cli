package main

import (
	"context"
	"fmt"
	"io"
	"strings"
)

func runCompletion(r *runner) int {
	if len(r.args) < 1 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic completion <shell>")
		_, _ = fmt.Fprintln(r.errOut)
		_, _ = fmt.Fprintln(r.errOut, "Available shells: bash, zsh, fish")
		_, _ = fmt.Fprintln(r.errOut)
		_, _ = fmt.Fprintln(r.errOut, "Setup:")
		_, _ = fmt.Fprintln(r.errOut, "  bash:  source <(cloudstic completion bash)")
		_, _ = fmt.Fprintln(r.errOut, "  zsh:   source <(cloudstic completion zsh)")
		_, _ = fmt.Fprintln(r.errOut, "  fish:  cloudstic completion fish | source")
		return 1
	}

	switch r.args[0] {
	case "bash":
		completionBash(r.out)
	case "zsh":
		completionZsh(r.out)
	case "fish":
		completionFish(r.out)
	default:
		return r.fail("Unsupported shell: %s\nAvailable shells: bash, zsh, fish", r.args[0])
	}
	return 0
}

func completionCommandSpec() *commandSpec {
	return leaf("completion", "Generate shell completion scripts", "<bash|zsh|fish>",
		func(r *runner, _ context.Context) int { return runCompletion(r) }).withoutFlagProbe().withArgumentValues(
		"shell", "bash", "zsh", "fish").withNotes(
		"Supported shells: bash, zsh, and fish.")
}

func completionBash(w io.Writer) {
	_, _ = fmt.Fprintf(w, `# bash completion for cloudstic

_cloudstic_query() {
    local kind="$1" cur="$2"
    shift 2
    cloudstic __complete "$kind" "$cur" "$@" 2>/dev/null
}

_cloudstic() {
    local cur prev words cword
    _init_completion || return

    local commands=%q
    local global_flags=%q
    local global_value_flags=%q
    local root=""
    local root_index=0
    local sub=""
    local path=""
    local cmd_flags=""
    local value_flags=""
    local i value_flag

    for ((i=1; i<cword; i++)); do
        if [[ "${words[i]}" == -* ]]; then
            for value_flag in $global_value_flags; do
                if [[ "${words[i]}" == "$value_flag" ]]; then
                    ((i++))
                    break
                fi
            done
            continue
        fi
        root="${words[i]}"
        root_index=$i
        break
    done

    if [[ -z "$root" ]]; then
`, commandWords(publicRootCommands()), flagWords(globalFlagSpecs), valueFlagWords(globalFlagSpecs))
	writeBashRootCompletionCases(w)
	_, _ = fmt.Fprint(w, `
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "$global_flags" -- "$cur"))
        else
            COMPREPLY=($(compgen -W "$commands" -- "$cur"))
        fi
        return
    fi

    path="$root"

    case "$root" in
`)
	for _, root := range publicRootCommands() {
		if len(root.children) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "        %s)\n", root.name)
		_, _ = fmt.Fprintf(w, "            sub=\"${words[root_index+1]}\"\n")
		_, _ = fmt.Fprintf(w, "            if [[ $cword -eq $((root_index+1)) ]]; then\n")
		_, _ = fmt.Fprintf(w, "                COMPREPLY=($(compgen -W %q -- \"$cur\")); return\n", commandWords(root.children))
		_, _ = fmt.Fprintf(w, "            fi\n")
		_, _ = fmt.Fprintf(w, "            path=\"$root $sub\" ;;\n")
	}
	_, _ = fmt.Fprint(w, `    esac

    case "$path" in
`)
	for _, command := range publicLeafCommands() {
		flags := command.effectiveFlags()
		_, _ = fmt.Fprintf(w, "        %q) cmd_flags=%q; value_flags=%q ;;\n", command.path(), flagWords(flags), valueFlagWords(flags))
	}
	_, _ = fmt.Fprint(w, `    esac

    case "$path:$prev" in
`)
	writeBashCompletionCases(w)
	_, _ = fmt.Fprint(w, "    esac\n\n")
	writeBashArgumentCases(w)
	_, _ = fmt.Fprint(w, `

    if [[ -n "$value_flags" ]]; then
        local value_flag
        for value_flag in $value_flags; do
            [[ "$prev" == "$value_flag" ]] && return
        done
    fi
    if [[ -z "$cur" || "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "$cmd_flags" -- "$cur"))
    fi
}

complete -F _cloudstic cloudstic
`)
}

func writeBashArgumentCases(w io.Writer) {
	_, _ = fmt.Fprintln(w, "    case \"$path:$((cword-root_index))\" in")
	for _, command := range publicLeafCommands() {
		if len(command.argumentValues) == 0 {
			continue
		}
		word := 1
		if command.parent != nil {
			word = 2
		}
		_, _ = fmt.Fprintf(w, "        %q) COMPREPLY=($(compgen -W %q -- \"$cur\")); return ;;\n",
			fmt.Sprintf("%s:%d", command.path(), word), strings.Join(command.argumentValues, " "))
	}
	_, _ = fmt.Fprintln(w, "    esac")
}

func writeBashRootCompletionCases(w io.Writer) {
	_, _ = fmt.Fprintln(w, "        case \"$prev\" in")
	for _, spec := range globalFlagSpecs {
		writeBashCompletionCase(w, "            ", "-"+spec.name, spec)
	}
	_, _ = fmt.Fprintln(w, "        esac")
}

func writeBashCompletionCases(w io.Writer) {
	for _, command := range publicLeafCommands() {
		for _, spec := range command.effectiveFlags() {
			writeBashCompletionCase(w, "        ", command.path()+":-"+spec.name, spec)
		}
	}
}

func writeBashCompletionCase(w io.Writer, indent, key string, spec flagSpec) {
	if len(spec.completionValues) != 0 {
		_, _ = fmt.Fprintf(w, "%s%q) COMPREPLY=($(compgen -W %q -- \"$cur\")); return ;;\n",
			indent, key, strings.Join(spec.completionValues, " "))
		return
	}
	switch spec.completion {
	case completionProfile:
		_, _ = fmt.Fprintf(w, "%s%q) COMPREPLY=($(compgen -W \"$(_cloudstic_query profile-names \"$cur\" \"${words[@]:1:$((cword-1))}\")\" -- \"$cur\")); return ;;\n", indent, key)
	case completionAuth:
		_, _ = fmt.Fprintf(w, "%s%q) COMPREPLY=($(compgen -W \"$(_cloudstic_query auth-names \"$cur\" \"${words[@]:1:$((cword-1))}\")\" -- \"$cur\")); return ;;\n", indent, key)
	case completionFile:
		_, _ = fmt.Fprintf(w, "%s%q) _filedir; return ;;\n", indent, key)
	}
}

func completionZsh(w io.Writer) {
	_, _ = fmt.Fprintf(w, `#compdef cloudstic

# zsh completion for cloudstic
_cloudstic_query() {
    local kind="$1"
    shift
    cloudstic __complete "$kind" "$PREFIX" "${words[@]:2:$(( CURRENT - 2 ))}" 2>/dev/null
}

_cloudstic_profile_names() { compadd ${(f)"$(_cloudstic_query profile-names)"}; }
_cloudstic_auth_names() { compadd ${(f)"$(_cloudstic_query auth-names)"}; }

_cloudstic() {
	local root="" sub="" path=""
	local -i root_index=0 i
	local value_flag
	local -a commands subcommands specs global_value_flags
	global_value_flags=(%s)

    commands=(
`, valueFlagWords(globalFlagSpecs))
	for _, command := range publicRootCommands() {
		_, _ = fmt.Fprintf(w, "        %s:%s\n", zshQuote(command.name), zshQuote(command.summary))
	}
	_, _ = fmt.Fprint(w, `    )

	for ((i=2; i<CURRENT; i++)); do
		if [[ "${words[i]}" == -* ]]; then
			for value_flag in $global_value_flags; do
				if [[ "${words[i]}" == "$value_flag" ]]; then
					((i++))
					break
				fi
			done
			continue
		fi
		root="${words[i]}"
		root_index=$i
		break
	done

	if [[ -z "$root" ]]; then
		specs=(
`)
	for _, spec := range globalFlagSpecs {
		_, _ = fmt.Fprintf(w, "            %s\n", zshQuote(zshFlagSpec(spec)))
	}
	_, _ = fmt.Fprint(w, `        )
		_describe -t commands 'cloudstic command' commands
		_arguments $specs
		return
	fi

	path="$root"

    case "$root" in
`)
	for _, root := range publicRootCommands() {
		if len(root.children) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "        %s)\n", root.name)
		_, _ = fmt.Fprint(w, "            sub=\"${words[root_index+1]}\"\n")
		_, _ = fmt.Fprint(w, "            subcommands=(\n")
		for _, child := range root.children {
			if !child.hidden {
				_, _ = fmt.Fprintf(w, "                %s:%s\n", zshQuote(child.name), zshQuote(child.summary))
			}
		}
		_, _ = fmt.Fprint(w, "            )\n")
		_, _ = fmt.Fprint(w, "            if (( CURRENT == root_index + 1 )); then _describe -t subcommands 'subcommand' subcommands; return; fi\n")
		_, _ = fmt.Fprint(w, "            path=\"$root $sub\" ;;\n")
	}
	_, _ = fmt.Fprint(w, `    esac

    case "$path" in
`)
	for _, command := range publicLeafCommands() {
		_, _ = fmt.Fprintf(w, "        %s)\n", zshQuote(command.path()))
		_, _ = fmt.Fprint(w, "            specs=(\n")
		for _, spec := range command.effectiveFlags() {
			_, _ = fmt.Fprintf(w, "                %s\n", zshQuote(zshFlagSpec(spec)))
		}
		if len(command.argumentValues) != 0 {
			_, _ = fmt.Fprintf(w, "                %s\n", zshQuote("1:"+command.argumentName+":("+strings.Join(command.argumentValues, " ")+")"))
		}
		_, _ = fmt.Fprint(w, "            ) ;;\n")
	}
	_, _ = fmt.Fprint(w, `    esac
    (( ${#specs} )) && _arguments $specs
}

compdef _cloudstic cloudstic
`)
}

func zshFlagSpec(spec flagSpec) string {
	result := "-" + spec.name + "[" + strings.ReplaceAll(spec.description, "]", "\\]") + "]"
	if !spec.takesValue() {
		return result
	}
	result += ":" + spec.value + ":"
	if len(spec.completionValues) != 0 {
		return result + "(" + strings.Join(spec.completionValues, " ") + ")"
	}
	switch spec.completion {
	case completionFile:
		return result + "_files"
	case completionProfile:
		return result + "_cloudstic_profile_names"
	case completionAuth:
		return result + "_cloudstic_auth_names"
	default:
		return result
	}
}

func zshQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func completionFish(w io.Writer) {
	_, _ = fmt.Fprint(w, `# fish completion for cloudstic
complete -c cloudstic -f

function __fish_cloudstic_query
    set -l kind $argv[1]
    cloudstic __complete $kind (commandline -ct) (commandline -opc)[2..-1] 2>/dev/null
end

`)
	for _, root := range publicRootCommands() {
		_, _ = fmt.Fprintf(w, "complete -c cloudstic -n __fish_use_subcommand -a %s -d %s\n", fishQuote(root.name), fishQuote(root.summary))
	}
	for _, spec := range globalFlagSpecs {
		writeFishFlag(w, "__fish_use_subcommand", spec)
	}
	for _, root := range publicRootCommands() {
		for _, child := range root.children {
			if child.hidden {
				continue
			}
			condition := fmt.Sprintf("__fish_seen_subcommand_from %s; and not __fish_seen_subcommand_from %s", root.name, commandWords(root.children))
			_, _ = fmt.Fprintf(w, "complete -c cloudstic -n %s -a %s -d %s\n", fishQuote(condition), fishQuote(child.name), fishQuote(child.summary))
		}
	}
	for _, command := range publicLeafCommands() {
		condition := fishCommandCondition(command)
		for _, spec := range command.effectiveFlags() {
			writeFishFlag(w, condition, spec)
		}
		if len(command.argumentValues) != 0 {
			_, _ = fmt.Fprintf(w, "complete -c cloudstic -n %s -a %s\n", fishQuote(condition), fishQuote(strings.Join(command.argumentValues, " ")))
		}
	}
}

func writeFishFlag(w io.Writer, condition string, spec flagSpec) {
	_, _ = fmt.Fprintf(w, "complete -c cloudstic -n %s -l %s", fishQuote(condition), spec.name)
	if spec.takesValue() {
		_, _ = fmt.Fprint(w, " -r")
	}
	if len(spec.completionValues) != 0 {
		_, _ = fmt.Fprintf(w, " -a %s", fishQuote(strings.Join(spec.completionValues, " ")))
	} else {
		switch spec.completion {
		case completionFile:
			_, _ = fmt.Fprint(w, " -F")
		case completionProfile:
			_, _ = fmt.Fprint(w, " -a '(__fish_cloudstic_query profile-names)'")
		case completionAuth:
			_, _ = fmt.Fprint(w, " -a '(__fish_cloudstic_query auth-names)'")
		}
	}
	_, _ = fmt.Fprintf(w, " -d %s\n", fishQuote(spec.description))
}

func fishCommandCondition(command *commandSpec) string {
	if command.parent == nil {
		return "__fish_seen_subcommand_from " + command.name
	}
	return "__fish_seen_subcommand_from " + command.parent.name + "; and __fish_seen_subcommand_from " + command.name
}

func fishQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "\\'") + "'"
}
