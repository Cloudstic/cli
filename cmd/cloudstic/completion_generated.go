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
	).Replace(script)
}
