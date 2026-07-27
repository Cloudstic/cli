package main

import (
	"flag"
	"fmt"
	"strings"
)

// The shell completion scripts embed a handful of lists that must match the
// command registry and the real flag definitions. Rather than maintaining them
// by hand in three shell dialects, the scripts carry placeholders that are
// substituted here at generation time.
const (
	placeholderCommandNames = "@@COMMAND_NAMES@@"
	placeholderGlobalFlags  = "@@GLOBAL_FLAGS@@"
	placeholderZshCommands  = "@@ZSH_COMMANDS@@"
	placeholderFishCommands = "@@FISH_COMMANDS@@"
)

// globalFlagSet builds a flag set containing only the global flags, for
// introspection by completion generation and drift tests.
func globalFlagSet() *flag.FlagSet {
	return newCommandFlags("global", allGlobalGroups, &globalFlags{}, commandInput{}).set
}

// globalFlagNames returns every global flag name in declaration order.
func globalFlagNames() []string {
	return flagNames(globalFlagSet())
}

// dashPrefixed renders names as shell flag tokens joined by sep.
func dashPrefixed(names []string, sep string) string {
	dashed := make([]string, 0, len(names))
	for _, n := range names {
		dashed = append(dashed, "-"+n)
	}
	return strings.Join(dashed, sep)
}

// completionCommandNames lists the commands offered as the first argument.
func completionCommandNames() string {
	return strings.Join(commandNames(), " ")
}

// zshCommandLines renders the registry as zsh `name:description` entries.
func zshCommandLines() string {
	var b strings.Builder
	for _, c := range visibleCommands() {
		fmt.Fprintf(&b, "        '%s:%s'\n", c.name, escapeZsh(c.summary))
	}
	fmt.Fprintf(&b, "        '%s:%s'\n", "version", "Print version information")
	fmt.Fprintf(&b, "        '%s:%s'", "help", "Show usage information")
	return b.String()
}

// fishCommandLines renders the registry as fish subcommand completions.
func fishCommandLines() string {
	var b strings.Builder
	for _, c := range visibleCommands() {
		fmt.Fprintf(&b, "complete -c cloudstic -n __fish_use_subcommand -a %s -d '%s'\n", c.name, escapeFish(c.summary))
	}
	fmt.Fprintf(&b, "complete -c cloudstic -n __fish_use_subcommand -a %s -d '%s'\n", "version", "Print version information")
	fmt.Fprintf(&b, "complete -c cloudstic -n __fish_use_subcommand -a %s -d '%s'", "help", "Show usage information")
	return b.String()
}

// escapeZsh escapes a description for a single-quoted zsh `name:desc` entry.
func escapeZsh(s string) string {
	s = strings.ReplaceAll(s, `'`, `'\''`)
	return strings.ReplaceAll(s, ":", `\:`)
}

// escapeFish escapes a description for a single-quoted fish argument.
func escapeFish(s string) string {
	return strings.ReplaceAll(s, `'`, `\'`)
}

// renderCompletion substitutes the generated lists into a shell script template.
func renderCompletion(script string) string {
	return strings.NewReplacer(
		placeholderCommandNames, completionCommandNames(),
		placeholderGlobalFlags, dashPrefixed(globalFlagNames(), " "),
		placeholderValueFlags, dashPrefixed(allValueFlagNames(), "|"),
		placeholderZshCommands, zshCommandLines(),
		placeholderFishCommands, fishCommandLines(),
		placeholderBashCmdFlags, bashCommandCases(),
		placeholderZshCmdFlags, zshCommandFlagCases(),
		placeholderFishCmdFlags, fishCommandFlagLines(),
		placeholderZshGlobalFlags, zshGlobalFlagLines(),
		placeholderFishGlobalFlags, fishGlobalFlagLines(),
		placeholderZshGroups, zshGroupBlocks(),
		placeholderFishGroups, fishGroupLines(),
	).Replace(script)
}

// Per-command flag entries for top-level shell completion are generated from
// each command's own flag and positional specifications. The shell templates
// still own nested command dispatch; their leaves use the same declarations.
const (
	placeholderBashCmdFlags = "@@BASH_CMD_FLAGS@@"
	placeholderZshCmdFlags  = "@@ZSH_CMD_FLAGS@@"
	placeholderFishCmdFlags = "@@FISH_CMD_FLAGS@@"
)

// generatedFlagCommands returns the commands whose flags are introspectable.
func generatedFlagCommands() []command {
	var out []command
	for _, c := range visibleCommands() {
		if c.flags != nil {
			out = append(out, c)
		}
	}
	return out
}

// bashCommandCases renders the whole `case "$cmd"` body: one arm per top-level
// leaf, one dispatch arm per group, and a catch-all. Generating the entire body
// means a command cannot be listed twice, or left behind when it is renamed.
func bashCommandCases() string {
	var b strings.Builder
	for _, c := range visibleCommands() {
		if c.isGroup() {
			b.WriteString(bashGroupArm(c))
			continue
		}
		fmt.Fprintf(&b, "        %s)\n", c.name)
		// A positional with a fixed value set completes those values directly.
		if words := leadingPositionalWords(c); words != "" {
			fmt.Fprintf(&b, "            COMPREPLY=($(compgen -W %q -- \"$cur\"))\n            return ;;\n", words)
			continue
		}
		fmt.Fprintf(&b, "            cmd_flags=%q ;;\n", dashPrefixed(specNames(ownSpecsOf(c)), " "))
	}
	b.WriteString("        *)\n            cmd_flags=\"\" ;;\n")
	return strings.TrimRight(b.String(), "\n")
}

// leadingPositionalWords returns the literal completion words for a command's
// first positional argument, when that positional has a fixed value set.
func leadingPositionalWords(c command) string {
	if len(c.positionals) == 0 {
		return ""
	}
	return bashCompleterWords(c.positionals[0].completer)
}

// bashGroupArm renders one group's dispatch arm: offer the subcommand names
// until one is chosen, then that subcommand's own flags.
func bashGroupArm(g command) string {
	var b strings.Builder
	fmt.Fprintf(&b, "        %s)\n", g.name)
	fmt.Fprintf(&b, "            local %s_sub=\"\"\n            local j\n", g.name)
	b.WriteString("            for ((j=i+1; j < cword; j++)); do\n")
	b.WriteString("                case \"${words[j]}\" in\n                    -*) ;;\n")
	fmt.Fprintf(&b, "                    *) %s_sub=\"${words[j]}\"; break ;;\n", g.name)
	b.WriteString("                esac\n            done\n")
	fmt.Fprintf(&b, "            if [[ -z \"$%s_sub\" ]]; then\n", g.name)
	fmt.Fprintf(&b, "                COMPREPLY=($(compgen -W %q -- \"$cur\"))\n                return\n            fi\n",
		strings.Join(childNames(g), " "))
	fmt.Fprintf(&b, "            case \"$%s_sub\" in\n", g.name)
	for _, child := range g.visibleChildren() {
		fmt.Fprintf(&b, "                %s)\n                    cmd_flags=%q ;;\n",
			child.name, dashPrefixed(specNames(ownSpecsOf(child)), " "))
	}
	b.WriteString("                *)\n                    cmd_flags=\"\" ;;\n            esac\n            ;;\n")
	return b.String()
}

// zshRebaseWords re-bases zsh's $words and $CURRENT on the subcommand found at
// index variable idx, so that _arguments sees the subcommand as the command
// word. Without it, _arguments counts the subcommand name — and any global flag
// typed before it — as positional arguments the command never declared, decides
// every argument is already supplied, and offers nothing at all.
//
// It has to be emitted inline in each arm rather than factored into a shell
// helper: zsh binds the completion special parameters per function scope, so a
// helper's assignments do not reach the _arguments call in the caller.
func zshRebaseWords(indent, idx string) string {
	return fmt.Sprintf("%swords=(\"${(@)words[%s,-1]}\"); (( CURRENT -= %s - 1 ))\n", indent, idx, idx)
}

// zshCommandFlagCases renders one `_arguments` arm per command. Each flag
// contributes its description, value placeholder, and completion function.
func zshCommandFlagCases() string {
	var b strings.Builder
	for _, c := range generatedFlagCommands() {
		fmt.Fprintf(&b, "        %s)\n%s            _arguments $global_flags", c.name, zshRebaseWords("            ", "i"))
		for _, s := range ownSpecsOf(c) {
			fmt.Fprintf(&b, " \\\n                '%s'", zshFlagEntry(s))
		}
		for _, p := range c.positionals {
			fmt.Fprintf(&b, " \\\n                '%s'", p.zshSpec())
		}
		b.WriteString("\n            ;;\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// zshFlagEntry renders a single zsh _arguments specification for a flag.
func zshFlagEntry(s flagSpec) string {
	prefix := ""
	if s.repeatable {
		prefix = "*"
	}
	entry := fmt.Sprintf("%s-%s[%s]", prefix, s.name, escapeZshDesc(s.completionUsage()))
	if s.isBool {
		return entry
	}
	return entry + fmt.Sprintf(":%s:%s", zshValueName(s), s.completer)
}

// zshValueName is the value label shown while completing a flag's argument.
func zshValueName(s flagSpec) string {
	name := strings.Trim(s.placeholder, "<>")
	if name == "" {
		name = "value"
	}
	return name
}

// fishCommandFlagLines renders fish completions for each command's own flags.
func fishCommandFlagLines() string {
	var b strings.Builder
	for _, c := range generatedFlagCommands() {
		for _, s := range ownSpecsOf(c) {
			fmt.Fprintf(&b, "complete -c cloudstic -n '__fish_seen_subcommand_from %s' -o %s -l %s",
				c.name, s.name, s.name)
			if !s.isBool {
				b.WriteString(fishValueSpec(s.completer))
			}
			fmt.Fprintf(&b, " -d '%s'\n", escapeFish(s.completionUsage()))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// escapeZshDesc escapes a flag description for use inside zsh's [...] block.
func escapeZshDesc(s string) string {
	r := strings.NewReplacer(`'`, `'\''`, "[", `\[`, "]", `\]`, ":", `\:`)
	return r.Replace(s)
}

// fishValueSpec maps a declared completer to the fish flags that reproduce it,
// so dynamic value suggestions survive generation.
func fishValueSpec(completer string) string {
	switch completer {
	case "_files":
		return " -r -F"
	case "_cloudstic_profile_names":
		return " -x -a '(__fish_cloudstic_query profile-names)'"
	case "_cloudstic_auth_names":
		return " -x -a '(__fish_cloudstic_query auth-names)'"
	case "_cloudstic_store_prefixes":
		return " -x -a 'local: s3: b2: sftp://'"
	case "_cloudstic_source_prefixes":
		return " -x -a 'local: sftp:// gdrive gdrive-changes onedrive onedrive-changes'"
	case "_cloudstic_shells":
		return " -x -a 'bash zsh fish'"
	default:
		return " -x"
	}
}

const (
	placeholderZshGlobalFlags  = "@@ZSH_GLOBAL_FLAGS@@"
	placeholderFishGlobalFlags = "@@FISH_GLOBAL_FLAGS@@"
)

// globalSpecs returns every global flag specification, in declaration order.
func globalSpecs() []flagSpec {
	return globalFlagSpecsFor(&globalFlags{}, allGlobalGroups)
}

// zshGlobalFlagLines renders the zsh global_flags array from the specs.
func zshGlobalFlagLines() string {
	var b strings.Builder
	for i, s := range globalSpecs() {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "        '%s'", zshFlagEntry(s))
	}
	return b.String()
}

// fishGlobalFlagLines renders fish completions for the global flags. These
// apply to every command, so they carry no subcommand condition.
func fishGlobalFlagLines() string {
	var b strings.Builder
	for i, s := range globalSpecs() {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "complete -c cloudstic -o %s -l %s", s.name, s.name)
		if !s.isBool {
			b.WriteString(fishValueSpec(s.completer))
		}
		fmt.Fprintf(&b, " -d '%s'", escapeFish(s.completionUsage()))
	}
	return b.String()
}

// Nested (grouped) commands are generated from the tree too, so a group's
// subcommand list and each subcommand's flags come from the same declarations
// that dispatch and help use.
const (
	placeholderZshGroups  = "@@ZSH_GROUPS@@"
	placeholderFishGroups = "@@FISH_GROUPS@@"
	placeholderValueFlags = "@@VALUE_FLAGS@@"
)

// visibleGroups returns the top-level commands that dispatch to children.
func visibleGroups() []command {
	var out []command
	for _, c := range visibleCommands() {
		if c.isGroup() {
			out = append(out, c)
		}
	}
	return out
}

// childNames returns a group's visible subcommand names.
func childNames(c command) []string {
	var names []string
	for _, child := range c.visibleChildren() {
		names = append(names, child.name)
	}
	return names
}

// allValueFlagNames returns every non-boolean flag declared anywhere in the
// tree, global or command-specific. Shells use this to know which flags consume
// the following word, so it must cover the whole surface rather than a
// hand-kept subset.
func allValueFlagNames() []string {
	seen := map[string]bool{}
	var names []string
	add := func(specs []flagSpec) {
		for _, s := range specs {
			if s.isBool || seen[s.name] {
				continue
			}
			seen[s.name] = true
			names = append(names, s.name)
		}
	}
	add(globalFlagSpecsFor(&globalFlags{}, allGlobalGroups))
	walk(commandRegistry(), func(_ string, c command) {
		if c.flags != nil {
			add(c.flags().ownSpecs())
		}
	})
	return names
}

// zshGroupBlocks renders each group's subcommand descriptions and the
// _arguments entry for each subcommand.
func zshGroupBlocks() string {
	var b strings.Builder
	for _, g := range visibleGroups() {
		fmt.Fprintf(&b, "        %s)\n", g.name)
		fmt.Fprintf(&b, "            local -a %s_commands\n            %s_commands=(\n", g.name, g.name)
		for _, child := range g.visibleChildren() {
			fmt.Fprintf(&b, "                '%s:%s'\n", child.name, escapeZsh(child.summary))
		}
		b.WriteString("            )\n")
		fmt.Fprintf(&b, "            local %s_sub\n            local -i gi=$((i+1))\n", g.name)
		b.WriteString("            while (( gi < CURRENT )); do\n                case \"${words[gi]}\" in\n                    -*) ;;\n")
		fmt.Fprintf(&b, "                    *) %s_sub=\"${words[gi]}\"; break ;;\n", g.name)
		b.WriteString("                esac\n                (( gi++ ))\n            done\n")
		fmt.Fprintf(&b, "            if [[ -z \"$%s_sub\" ]]; then\n", g.name)
		fmt.Fprintf(&b, "                _describe -t %s-commands '%s subcommand' %s_commands\n                return\n            fi\n",
			g.name, g.name, g.name)
		fmt.Fprintf(&b, "            case \"$%s_sub\" in\n", g.name)
		for _, child := range g.visibleChildren() {
			fmt.Fprintf(&b, "                %s)\n%s                    _arguments $global_flags",
				child.name, zshRebaseWords("                    ", "gi"))
			for _, s := range ownSpecsOf(child) {
				fmt.Fprintf(&b, " \\\n                        '%s'", zshFlagEntry(s))
			}
			for _, p := range child.positionals {
				fmt.Fprintf(&b, " \\\n                        '%s'", p.zshSpec())
			}
			b.WriteString("\n                    ;;\n")
		}
		b.WriteString("                *)\n")
		b.WriteString(zshRebaseWords("                    ", "gi"))
		b.WriteString("                    _arguments $global_flags\n                    ;;\n            esac\n            ;;\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// fishGroupLines renders each group's subcommand offers and the flags of each
// subcommand, conditioned on the group and subcommand already being typed.
func fishGroupLines() string {
	var b strings.Builder
	for _, g := range visibleGroups() {
		siblings := strings.Join(childNames(g), " ")
		for _, child := range g.visibleChildren() {
			fmt.Fprintf(&b, "complete -c cloudstic -n '__fish_seen_subcommand_from %s; and not __fish_seen_subcommand_from %s' -a %s -d '%s'\n",
				g.name, siblings, child.name, escapeFish(child.summary))
		}
		for _, child := range g.visibleChildren() {
			for _, s := range ownSpecsOf(child) {
				fmt.Fprintf(&b, "complete -c cloudstic -n '__fish_seen_subcommand_from %s; and __fish_seen_subcommand_from %s' -l %s",
					g.name, child.name, s.name)
				if !s.isBool {
					b.WriteString(fishValueSpec(s.completer))
				}
				fmt.Fprintf(&b, " -d '%s'\n", escapeFish(s.completionUsage()))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// bashCompleterWords maps a declared completer to a literal bash word list,
// for the completers whose values are a fixed set. Dynamic completers are
// served by _cloudstic_query and file completion by _filedir, so they return
// an empty list here.
func bashCompleterWords(completer string) string {
	switch completer {
	case "_cloudstic_shells":
		return "bash zsh fish"
	case "_cloudstic_store_prefixes":
		return "local: s3: b2: sftp://"
	case "_cloudstic_source_prefixes":
		return "local: sftp:// gdrive gdrive-changes onedrive onedrive-changes"
	default:
		return ""
	}
}
