package main

import (
	"context"
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
	// flags builds this command's flag set together with its own flag
	// specifications, from a single construction. Nil when the command takes
	// no flags of its own.
	flags func() boundFlagSet
	// positional describes the command's positional arguments for shell
	// completion, as zsh _arguments specs (e.g. ":snapshot ID:").
	positional []string
	// run executes a leaf command. Nil for groups.
	run func(r *runner, ctx context.Context) int
	// hidden keeps internal commands out of usage and completion output.
	hidden bool
}

// commandOpt customises a command at construction time.
type commandOpt func(*command)

// withFlags attaches the command's flag-set builder.
func withFlags(build func() boundFlagSet) commandOpt {
	return func(c *command) { c.flags = build }
}

// withPositional describes the command's positional arguments for completion.
func withPositional(specs ...string) commandOpt {
	return func(c *command) { c.positional = specs }
}

// asHidden keeps a command out of usage and completion output.
func asHidden() commandOpt {
	return func(c *command) { c.hidden = true }
}

// leaf declares a command that does work.
func leaf(name, summary string, run func(r *runner, ctx context.Context) int, opts ...commandOpt) command {
	c := command{name: name, summary: summary, run: run}
	for _, opt := range opts {
		opt(&c)
	}
	return c
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
