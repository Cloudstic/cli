package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
)

// command is a node in the CLI command tree. The same type models both leaves
// (`backup`, `check`) and groups (`store`, `key`), so nesting is uniform and
// dispatch, usage, and shell completion can all walk one structure.
//
// A leaf carries run and, usually, flags. A group carries children and no run
// of its own: dispatchGroup handles subcommand lookup, usage, and errors for
// every group in one place.
type command struct {
	// name is the token typed on the command line, e.g. "backup" or "new".
	name string
	// summary is the one-line description shown in usage and completion.
	summary string
	// children are the nested commands of a group. Empty for a leaf.
	children []command
	// flags builds this command's complete flag set from its declaration.
	// Every leaf has one; groups do not.
	flags func() commandFlags
	// positionals describe and bind the command's positional arguments.
	positionals []positionalSpec
	// run executes a leaf command. Nil for groups.
	run func(r *runner, ctx context.Context) int
	// hidden keeps internal commands out of usage and completion output.
	hidden bool
	// usageOnError prints a command-specific usage banner before a parse error.
	// Optional.
	usageOnError func(io.Writer)
	// help prints the command's own help, replacing the flag package's default
	// listing when -h is given. Optional; the seam for richer per-command help.
	help func(io.Writer)
}

// commandOpt customises a command at construction time.
type commandOpt func(*command)

// withUsageOnError prints a usage banner before a parse or validation error.
func withUsageOnError(print func(io.Writer)) commandOpt {
	return func(c *command) { c.usageOnError = print }
}

// withHelp installs a command-specific -h renderer.
func withHelp(print func(io.Writer)) commandOpt {
	return func(c *command) { c.help = print }
}

// asHidden keeps a command out of usage and completion output.
func asHidden() commandOpt {
	return func(c *command) { c.hidden = true }
}

// group declares a command that only dispatches to nested commands.
func group(name, summary string, children ...command) command {
	return command{name: name, summary: summary, children: children, run: nil}
}

// isGroup reports whether the command dispatches to children.
func (c command) isGroup() bool { return len(c.children) > 0 }

// lookupChild finds a nested command by name.
func (c command) lookupChild(name string) (command, bool) {
	for _, child := range c.children {
		if child.name == name {
			return child, true
		}
	}
	return command{}, false
}

// visibleChildren returns the children shown to users.
func (c command) visibleChildren() []command {
	var out []command
	for _, child := range c.children {
		if !child.hidden {
			out = append(out, child)
		}
	}
	return out
}

// execute runs a command, dispatching through any nested levels first. This is
// the single implementation of group dispatch: subcommand lookup, the usage
// message when no subcommand is given, and the unknown-subcommand error.
func (c command) execute(r *runner, ctx context.Context, path string) int {
	if !c.isGroup() {
		if c.run == nil {
			return r.fail("Command %s is not runnable", path)
		}
		return c.run(r, ctx)
	}

	if len(r.args) < 1 {
		if !r.jsonEnabled() {
			printGroupUsage(r.errOut, c, path)
		}
		return exitFailure
	}

	name := r.args[0]
	child, ok := c.lookupChild(name)
	if !ok {
		return r.fail("Unknown %s subcommand: %s", path, name)
	}
	return child.execute(r.withArgs(r.args[1:]), ctx, path+" "+name)
}

// printGroupUsage writes the subcommand listing for a group.
func printGroupUsage(w io.Writer, c command, path string) {
	_, _ = fmt.Fprintf(w, "Usage: cloudstic %s <subcommand> [options]\n\n", path)
	_, _ = fmt.Fprintln(w, "Subcommands:")
	width := 0
	for _, child := range c.visibleChildren() {
		if len(child.name) > width {
			width = len(child.name)
		}
	}
	for _, child := range c.visibleChildren() {
		_, _ = fmt.Fprintf(w, "  %-*s  %s\n", width, child.name, child.summary)
	}
}

// walk visits every command in the tree, passing the space-joined path of each.
func walk(commands []command, fn func(path string, c command)) {
	var visit func(prefix string, cmds []command)
	visit = func(prefix string, cmds []command) {
		for _, c := range cmds {
			path := c.name
			if prefix != "" {
				path = prefix + " " + c.name
			}
			fn(path, c)
			visit(path, c.children)
		}
	}
	visit("", commands)
}

// leafPaths returns the space-joined paths of every runnable command.
func leafPaths(commands []command) []string {
	var paths []string
	walk(commands, func(path string, c command) {
		if !c.isGroup() && !c.hidden {
			paths = append(paths, path)
		}
	})
	return paths
}

// commandInput is the declarative input surface of a runnable command.
type commandInput struct {
	flags       []flagSpec
	positionals []positionalSpec
}

// declareArgs builds a command's args struct together with its flags and
// positional arguments. The dispatcher supplies the global flag destination.
type declareArgs[T any] func(g *globalFlags) (*T, commandInput)

// parseInto builds and parses a command declaration into its args struct.
func parseInto[T any](name string, groups []flagGroup, declare declareArgs[T], args []string) (*T, error) {
	g := &globalFlags{}
	a, input := declare(g)
	cf := newCommandFlags(name, groups, g, input)
	if err := cf.parse(args); err != nil {
		return nil, err
	}
	return a, nil
}

// leaf declares a runnable command. Parsing, environment resolution, and
// positional binding are derived from the command's single input declaration.
func leaf[T any](
	name, summary string,
	groups []flagGroup,
	declare declareArgs[T],
	run func(r *runner, ctx context.Context, a *T) int,
	opts ...commandOpt,
) command {
	build := func() (*T, commandFlags) {
		g := &globalFlags{}
		a, input := declare(g)
		return a, newCommandFlags(name, groups, g, input)
	}

	_, declared := build()
	var c command
	c = command{
		name:        name,
		summary:     summary,
		positionals: declared.positionals,
		flags:       func() commandFlags { _, cf := build(); return cf },
		run: func(r *runner, ctx context.Context) int {
			if c.help != nil && hasHelpFlag(r.args) {
				c.help(r.out)
				return 0
			}
			a, err := parseInto(name, groups, declare, r.args)
			if err != nil {
				if c.usageOnError != nil && !r.jsonEnabled() && !errors.Is(err, flag.ErrHelp) {
					c.usageOnError(r.errOut)
				}
				return r.parseError(err)
			}
			return run(r, ctx, a)
		},
	}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// hasHelpFlag reports whether the arguments request help.
func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "-h" || arg == "--help" || arg == "help" {
			return true
		}
	}
	return false
}
