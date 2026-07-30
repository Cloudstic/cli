package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"
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
	// run executes a leaf command. Nil for groups. path is the complete command
	// path, including parent groups.
	run func(r *runner, ctx context.Context, path string) int
	// hidden keeps internal commands out of usage and completion output.
	hidden bool
}

// commandOpt customises a command at construction time.
type commandOpt func(*command)

// asHidden keeps a command out of usage and completion output.
func asHidden() commandOpt {
	return func(c *command) { c.hidden = true }
}

// group declares a command that only dispatches to nested commands.
func group(name, summary string, children ...command) command {
	return command{name: name, summary: summary, children: children}
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
		r = r.withUsage(func(w io.Writer) { printCommandSynopsis(w, c, path) })
		return c.run(r, ctx, path)
	}

	// Only consume help at the group level when it is the complete argument
	// list. A later help flag belongs to the selected child command.
	if len(r.args) == 1 && (r.args[0] == "-h" || r.args[0] == "--help" || r.args[0] == "help") {
		printCommandHelp(r.out, c, path)
		return 0
	}

	if len(r.args) < 1 {
		if !r.jsonEnabled() {
			printCommandHelp(r.errOut, c, path)
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
	return leafWith(name, summary, groups, declare,
		func(r *runner, ctx context.Context, a *T, _ *globalFlags) int {
			return run(r, ctx, a)
		}, opts...)
}

// repoLeaf declares a command that opens a repository.
//
// It resolves the client configuration once, after a successful parse, and hands
// it to run — so the two-step "resolve, then connect" happens in one place
// instead of once per command. Before this existed, every such command carried
// its own copy of the resolve call and of the failure message for it, which is
// eleven chances for them to drift and eleven error branches no test exercised.
//
// Resolution deliberately happens after parsing rather than before: it reads the
// profiles file, and `-h`, completion and a parse failure must not.
//
// Not every repository command fits. `backup` runs its profile loop N times with
// N configurations, one per profile, so a single value resolved before dispatch
// would be wrong for it — it uses leaf and resolves per iteration.
func repoLeaf[T any](
	name, summary string,
	groups []flagGroup,
	declare declareArgs[T],
	run func(r *runner, ctx context.Context, a *T, cfg clientConfig) int,
	opts ...commandOpt,
) command {
	return leafWith(name, summary, groups, declare,
		func(r *runner, ctx context.Context, a *T, g *globalFlags) int {
			cfg, err := resolveClientConfig(g)
			if err != nil {
				return r.fail("Failed to init store: %v", err)
			}
			return run(r, ctx, a, cfg)
		}, opts...)
}

// leafWith is the shared body of leaf and repoLeaf. invoke receives the parsed
// args together with the global flags they were parsed into, which is what lets
// repoLeaf resolve a configuration without every command having to hand its own
// flag struct back.
func leafWith[T any](
	name, summary string,
	groups []flagGroup,
	declare declareArgs[T],
	invoke func(r *runner, ctx context.Context, a *T, g *globalFlags) int,
	opts ...commandOpt,
) command {
	build := func() (*T, *globalFlags, commandFlags) {
		g := &globalFlags{}
		a, input := declare(g)
		return a, g, newCommandFlags(name, groups, g, input)
	}

	_, _, declared := build()
	var c command
	c = command{
		name:        name,
		summary:     summary,
		positionals: declared.positionals,
		flags:       func() commandFlags { _, _, cf := build(); return cf },
		run: func(r *runner, ctx context.Context, path string) int {
			a, g, cf := build()
			if commandHelpRequested(cf.set, r.args) {
				printCommandHelp(r.out, c, path)
				return 0
			}
			// The flag package's own error and usage output is discarded: the
			// dispatcher reports parse failures itself, prefixed with the
			// command's synopsis, so errors are not printed twice.
			cf.set.SetOutput(io.Discard)
			err := cf.parse(r.args)
			if err != nil {
				// Every parse failure shows the command's synopsis: it is
				// shorter and more useful than re-listing every flag.
				if !r.jsonEnabled() && !errors.Is(err, flag.ErrHelp) {
					printCommandSynopsis(r.errOut, c, path)
				}
				return r.parseError(err)
			}
			return invoke(r, ctx, a, g)
		},
	}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// commandHelpRequested recognizes help flags using the command's real flag
// set, so a literal "-h" can still be consumed as another flag's value or as
// a positional argument after "--".
func commandHelpRequested(fs *flag.FlagSet, args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return false
		}
		if len(arg) < 2 || arg[0] != '-' {
			continue
		}
		name := arg[1:]
		if name[0] == '-' {
			name = name[1:]
		}
		if before, _, ok := strings.Cut(name, "="); ok {
			name = before
		}
		if name == "h" || name == "help" {
			return true
		}
		f := fs.Lookup(name)
		if f == nil || strings.Contains(arg, "=") {
			continue
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
		}
	}
	return false
}
