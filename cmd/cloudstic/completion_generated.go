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
	placeholderCommandNames     = "@@COMMAND_NAMES@@"
	placeholderGlobalFlags      = "@@GLOBAL_FLAGS@@"
	placeholderGlobalValueFlags = "@@GLOBAL_VALUE_FLAGS@@"
	placeholderZshCommands      = "@@ZSH_COMMANDS@@"
	placeholderFishCommands     = "@@FISH_COMMANDS@@"
)

// globalFlagSet builds a flag set containing only the global flags, for
// introspection by completion generation and drift tests.
func globalFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("global", flag.ContinueOnError)
	addGlobalFlags(fs, allGlobalGroups)
	return fs
}

// globalFlagNames returns every global flag name in declaration order.
func globalFlagNames() []string {
	return flagNames(globalFlagSet())
}

// globalValueFlagNames returns the global flags that take a value, i.e. every
// non-boolean flag. Shells use this to know which flags consume the next word.
func globalValueFlagNames() []string {
	var names []string
	globalFlagSet().VisitAll(func(f *flag.Flag) {
		if !isBoolFlag(f) {
			names = append(names, f.Name)
		}
	})
	return names
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
		placeholderGlobalValueFlags, dashPrefixed(globalValueFlagNames(), "|"),
		placeholderZshCommands, zshCommandLines(),
		placeholderFishCommands, fishCommandLines(),
		placeholderBashCmdFlags, bashCommandFlagCases(),
		placeholderZshCmdFlags, zshCommandFlagCases(),
		placeholderFishCmdFlags, fishCommandFlagLines(),
		placeholderZshGlobalFlags, zshGlobalFlagLines(),
		placeholderFishGlobalFlags, fishGlobalFlagLines(),
	).Replace(script)
}

// Per-command flag entries for the shell scripts are generated from each
// command's own flag specifications. Grouped commands (auth/store/profile/key)
// still build their flags inline and keep their hand-written blocks.
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

// bashCommandFlagCases renders one `case` arm per command listing its own flags.
func bashCommandFlagCases() string {
	var b strings.Builder
	for _, c := range generatedFlagCommands() {
		names := dashPrefixed(specNames(ownSpecsOf(c)), " ")
		fmt.Fprintf(&b, "        %s)\n            cmd_flags=%q ;;\n", c.name, names)
	}
	return strings.TrimRight(b.String(), "\n")
}

// zshCommandFlagCases renders one `_arguments` arm per command. Each flag
// contributes its description, value placeholder, and completion function.
func zshCommandFlagCases() string {
	var b strings.Builder
	for _, c := range generatedFlagCommands() {
		fmt.Fprintf(&b, "        %s)\n            _arguments $global_flags", c.name)
		for _, s := range ownSpecsOf(c) {
			fmt.Fprintf(&b, " \\\n                '%s'", zshFlagEntry(s))
		}
		for _, p := range c.positional {
			fmt.Fprintf(&b, " \\\n                '%s'", p)
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
