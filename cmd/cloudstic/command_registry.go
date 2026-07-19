package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

type commandRun func(*runner, context.Context) int

type completionKind string
type flagSection string

const (
	completionNone        completionKind = ""
	completionFile        completionKind = "file"
	completionProfile     completionKind = "profile"
	completionAuth        completionKind = "auth"
	flagSectionEncryption flagSection    = "encryption"
)

type flagSpec struct {
	name             string
	description      string
	value            string
	completion       completionKind
	completionValues []string
	environment      []environmentSpec
	section          flagSection
}

type environmentSpec struct {
	name       string
	sensitive  bool
	documented bool
}

func environment(name string) environmentSpec {
	return environmentSpec{name: name, documented: true}
}

func sensitiveEnvironment(name string) environmentSpec {
	spec := environment(name)
	spec.sensitive = true
	return spec
}

func hiddenEnvironment(name string) environmentSpec {
	return environmentSpec{name: name}
}

func boolFlag(name, description string) flagSpec {
	return flagSpec{name: name, description: description}
}

func valueFlag(name, value, description string, completion completionKind) flagSpec {
	return flagSpec{name: name, value: value, description: description, completion: completion}
}

func (f flagSpec) takesValue() bool { return f.value != "" }

func (f flagSpec) withEnvironment(environment ...environmentSpec) flagSpec {
	f.environment = environment
	return f
}

func (f flagSpec) withCompletionValues(values ...string) flagSpec {
	f.completionValues = values
	return f
}

func (f flagSpec) inSection(section flagSection) flagSpec {
	f.section = section
	return f
}

type commandSpec struct {
	name            string
	aliases         []string
	summary         string
	args            string
	hidden          bool
	usesGlobalFlags bool
	run             commandRun
	flags           []flagSpec
	notes           []string
	argumentName    string
	argumentValues  []string
	children        []*commandSpec
	flagProbe       commandRun
	parent          *commandSpec
}

func commandRegistry() []*commandSpec {
	return []*commandSpec{
		initCommandSpec(),
		backupCommandSpec(),
		authCommandSpec(),
		storeCommandSpec(),
		sourceCommandSpec(),
		setupCommandSpec(),
		tuiCommandSpec(),
		profileCommandSpec(),
		restoreCommandSpec(),
		listCommandSpec(),
		lsCommandSpec(),
		pruneCommandSpec(),
		forgetCommandSpec(),
		diffCommandSpec(),
		breakLockCommandSpec(),
		keyCommandSpec(),
		checkCommandSpec(),
		catCommandSpec(),
		completionCommandSpec(),
		versionCommandSpec(),
		helpCommandSpec(),
		completionQueryCommandSpec(),
	}
}

func (c *commandSpec) path() string {
	if c.parent == nil {
		return c.name
	}
	return c.parent.path() + " " + c.name
}

func (c *commandSpec) effectiveFlags() []flagSpec {
	byName := make(map[string]flagSpec)
	if c.usesGlobalFlags {
		for _, f := range globalFlagSpecs {
			byName[f.name] = f
		}
	} else if c.flagProbe != nil {
		// parseFlags installs -no-prompt for every real FlagSet.
		byName["no-prompt"] = globalFlagByName("no-prompt")
	}
	for _, f := range c.flags {
		byName[f.name] = f
	}
	result := make([]flagSpec, 0, len(byName))
	for _, f := range byName {
		result = append(result, f)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].name < result[j].name })
	return result
}

func (c *commandSpec) flag(name string) (flagSpec, bool) {
	for _, spec := range c.effectiveFlags() {
		if spec.name == name {
			return spec, true
		}
	}
	return flagSpec{}, false
}

func globalFlagByName(name string) flagSpec {
	for _, spec := range globalFlagSpecs {
		if spec.name == name {
			return spec
		}
	}
	return flagSpec{name: name}
}

func leaf(name, summary, args string, run commandRun, flags ...flagSpec) *commandSpec {
	return &commandSpec{name: name, summary: summary, args: args, run: run, flagProbe: run, flags: flags}
}

func leafAliases(name string, aliases []string, summary, args string, run commandRun, flags ...flagSpec) *commandSpec {
	c := leaf(name, summary, args, run, flags...)
	c.aliases = aliases
	return c
}

func (c *commandSpec) withGlobalFlags() *commandSpec {
	c.usesGlobalFlags = true
	return c
}

func (c *commandSpec) withNotes(notes ...string) *commandSpec {
	c.notes = notes
	return c
}

func (c *commandSpec) withArgumentValues(name string, values ...string) *commandSpec {
	c.argumentName = name
	c.argumentValues = values
	return c
}

func group(name, summary string, children ...*commandSpec) *commandSpec {
	c := &commandSpec{name: name, summary: summary, children: children}
	for _, child := range children {
		child.parent = c
	}
	return c
}

func (c *commandSpec) hide() *commandSpec {
	c.hidden = true
	return c
}

func (c *commandSpec) withoutFlagProbe() *commandSpec {
	c.flagProbe = nil
	return c
}

func findCommand(commands []*commandSpec, name string) *commandSpec {
	for _, command := range commands {
		if command.name == name {
			return command
		}
		for _, alias := range command.aliases {
			if alias == name {
				return command
			}
		}
	}
	return nil
}

func rootCommand(name string) *commandSpec { return findCommand(commandRegistry(), name) }

func dispatchCommand(r *runner, ctx context.Context, command *commandSpec) int {
	if len(command.children) == 0 {
		return command.run(r, ctx)
	}
	if len(r.args) == 0 {
		printGroupUsage(r.errOut, command)
		return exitFailure
	}
	child := findCommand(command.children, r.args[0])
	if child == nil {
		return r.fail("Unknown %s subcommand: %s", command.path(), r.args[0])
	}
	return dispatchCommand(r.withArgs(r.args[1:]), ctx, child)
}

func printGroupUsage(out io.Writer, command *commandSpec) {
	_, _ = fmt.Fprintf(out, "Usage: cloudstic %s <subcommand> [options]\n\n", command.path())
	_, _ = fmt.Fprintln(out, "Available subcommands:")
	for _, child := range command.children {
		if !child.hidden {
			_, _ = fmt.Fprintf(out, "  %-14s %s\n", child.name, child.summary)
		}
	}
}

func publicLeafCommands() []*commandSpec {
	var result []*commandSpec
	var walk func([]*commandSpec)
	walk = func(commands []*commandSpec) {
		for _, command := range commands {
			if command.hidden {
				continue
			}
			if len(command.children) != 0 {
				walk(command.children)
				continue
			}
			result = append(result, command)
		}
	}
	walk(commandRegistry())
	return result
}

func publicRootCommands() []*commandSpec {
	commands := commandRegistry()
	result := make([]*commandSpec, 0, len(commands))
	for _, command := range commands {
		if !command.hidden {
			result = append(result, command)
		}
	}
	return result
}

func commandWords(commands []*commandSpec) string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		if !command.hidden {
			names = append(names, command.name)
		}
	}
	return strings.Join(names, " ")
}

func flagWords(flags []flagSpec) string {
	words := make([]string, 0, len(flags))
	for _, spec := range flags {
		words = append(words, "-"+spec.name)
	}
	return strings.Join(words, " ")
}

func valueFlagWords(flags []flagSpec) string {
	words := make([]string, 0, len(flags))
	for _, spec := range flags {
		if spec.takesValue() {
			words = append(words, "-"+spec.name)
		}
	}
	return strings.Join(words, " ")
}

// flagSetObserver is used by registry parity tests to inspect the real FlagSet
// immediately before parsing. Production code leaves it nil.
var flagSetObserver func(*flag.FlagSet)
