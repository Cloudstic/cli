package main

import (
	"context"
	"flag"
)

// command describes one CLI command. The registry below is the single source
// of truth for the command surface: dispatch (main.go), the usage listing
// (usage.go), and the shell-completion command lists (completion.go) are all
// derived from it, so adding a command is one declaration rather than three
// edits that can silently drift apart.
type command struct {
	// name is the token typed on the command line, e.g. "backup".
	name string
	// summary is the one-line description shown in usage and zsh completion.
	summary string
	// subcommands lists the nested verbs for grouped commands such as
	// "key list" or "store new". Empty for leaf commands.
	subcommands []subcommand
	// flags builds this command's flag set together with its own flag
	// specifications, from a single construction. The flag set covers the
	// global groups the command opts into plus its own flags; the specs cover
	// only its own, and carry the metadata (env, secret, completer) that usage
	// and shell completion are generated from. Nil for grouped commands, whose
	// subcommands still build flags inline.
	flags func() (*flag.FlagSet, []flagSpec)
	// positional describes the command's positional arguments for shell
	// completion, as zsh _arguments specs (e.g. ":snapshot ID:"). Empty when
	// the command takes none.
	positional []string
	// run executes the command.
	run func(r *runner, ctx context.Context) int
	// hidden keeps internal commands out of usage and completion output.
	hidden bool
}

// subcommand describes one nested verb of a grouped command.
//
// Subcommands currently declare only their name and summary. Their flag sets
// are still built inline inside each run function, so they are not yet
// introspectable; extracting them is follow-up work tracked separately.
type subcommand struct {
	name    string
	summary string
}

// commandRegistry declares every command the CLI accepts. Order determines the
// order commands appear in `cloudstic help`.
func commandRegistry() []command {
	return []command{
		{
			name:    "init",
			summary: "Initialize a new repository (must run before first backup)",
			flags: func() (*flag.FlagSet, []flagSpec) {
				b, _ := newInitFlagSet()
				return b.set, b.own
			},
			run: runInit,
		},
		{
			name:    "backup",
			summary: "Create a new backup snapshot from a source",
			flags: func() (*flag.FlagSet, []flagSpec) {
				b, _ := newBackupFlagSet()
				return b.set, b.own
			},
			run: runBackup,
		},
		{
			name:    "auth",
			summary: "Manage reusable cloud auth entries",
			subcommands: []subcommand{
				{name: "new", summary: "Create or update a reusable cloud auth entry"},
				{name: "list", summary: "List auth entries from profiles.yaml"},
				{name: "show", summary: "Show one auth entry"},
				{name: "login", summary: "Run OAuth login flow for one auth entry"},
			},
			run: runAuth,
		},
		{
			name:    "store",
			summary: "Manage store entries in profiles.yaml",
			subcommands: []subcommand{
				{name: "new", summary: "Create or update a store entry in profiles.yaml"},
				{name: "list", summary: "List configured stores"},
				{name: "show", summary: "Show one store and its configuration"},
				{name: "verify", summary: "Verify one store's credentials and connectivity"},
				{name: "init", summary: "Initialize a configured store by reference"},
			},
			run: runStore,
		},
		{
			name:    "source",
			summary: "Discover source candidates for onboarding",
			subcommands: []subcommand{
				{name: "discover", summary: "Discover local source candidates for onboarding"},
			},
			run: runSource,
		},
		{
			name:    "setup",
			summary: "Guided setup and onboarding flows",
			subcommands: []subcommand{
				{name: "workstation", summary: "Guide workstation onboarding and profile scaffolding"},
			},
			run: runSetup,
		},
		{
			name:    "tui",
			summary: "Launch the interactive terminal dashboard",
			flags: func() (*flag.FlagSet, []flagSpec) {
				b, _ := newTUIFlagSet()
				return b.set, b.own
			},
			run: runTUI,
		},
		{
			name:    "profile",
			summary: "Manage backup profiles",
			subcommands: []subcommand{
				{name: "new", summary: "Create or update a backup profile in profiles.yaml"},
				{name: "list", summary: "List stores, auth entries, and backup profiles"},
				{name: "show", summary: "Show one profile and resolved store/auth references"},
			},
			run: runProfile,
		},
		{
			name:    "restore",
			summary: "Restore files from a backup snapshot",
			flags: func() (*flag.FlagSet, []flagSpec) {
				b, _ := newRestoreFlagSet()
				return b.set, b.own
			},
			positional: []string{":snapshot ID:"},
			run:        runRestore,
		},
		{
			name:    "list",
			summary: "List all backup snapshots in the repository",
			flags: func() (*flag.FlagSet, []flagSpec) {
				b, _ := newListFlagSet()
				return b.set, b.own
			},
			run: runList,
		},
		{
			name:    "ls",
			summary: "List files within a specific snapshot",
			flags: func() (*flag.FlagSet, []flagSpec) {
				b, _ := newLsFlagSet()
				return b.set, b.own
			},
			positional: []string{":snapshot ID:"},
			run:        runLsSnapshot,
		},
		{
			name:    "prune",
			summary: "Remove unused data chunks from the repository",
			flags: func() (*flag.FlagSet, []flagSpec) {
				b, _ := newPruneFlagSet()
				return b.set, b.own
			},
			run: runPrune,
		},
		{
			name:    "forget",
			summary: "Remove a specific snapshot from history",
			flags: func() (*flag.FlagSet, []flagSpec) {
				b, _ := newForgetFlagSet()
				return b.set, b.own
			},
			positional: []string{":snapshot ID:"},
			run:        runForget,
		},
		{
			name:    "diff",
			summary: "Compare two snapshots or a snapshot against latest",
			flags: func() (*flag.FlagSet, []flagSpec) {
				b, _ := newDiffFlagSet()
				return b.set, b.own
			},
			positional: []string{":snapshot 1:", ":snapshot 2:"},
			run:        runDiff,
		},
		{
			name:    "break-lock",
			summary: "Remove a stale repository lock left by a crashed process",
			flags: func() (*flag.FlagSet, []flagSpec) {
				b, _ := newBreakLockFlagSet()
				return b.set, b.own
			},
			run: runBreakLock,
		},
		{
			name:    "key",
			summary: "Manage encryption key slots",
			subcommands: []subcommand{
				{name: "list", summary: "List all encryption key slots in the repository"},
				{name: "add-recovery", summary: "Generate a 24-word recovery key for an encrypted repository"},
				{name: "passwd", summary: "Change the repository password"},
			},
			run: runKey,
		},
		{
			name:    "check",
			summary: "Verify repository integrity (reference chain, objects, data)",
			flags: func() (*flag.FlagSet, []flagSpec) {
				b, _ := newCheckFlagSet()
				return b.set, b.own
			},
			positional: []string{":snapshot ID:"},
			run:        runCheck,
		},
		{
			name:    "cat",
			summary: "Display raw JSON content of repository objects",
			flags: func() (*flag.FlagSet, []flagSpec) {
				b, _ := newCatFlagSet()
				return b.set, b.own
			},
			positional: []string{"*:object key:"},
			run:        runCat,
		},
		{
			name:    "completion",
			summary: "Generate shell completion scripts (bash, zsh, fish)",
			run:     func(r *runner, _ context.Context) int { return runCompletion(r) },
		},
		{
			name:    "__complete",
			summary: "Internal dynamic completion helper",
			run:     runCompletionQuery,
			hidden:  true,
		},
	}
}

// lookupCommand resolves a command by name, including its aliases.
func lookupCommand(name string) (command, bool) {
	for _, c := range commandRegistry() {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// visibleCommands returns the registry entries that appear in user-facing
// usage and completion output.
func visibleCommands() []command {
	var out []command
	for _, c := range commandRegistry() {
		if !c.hidden {
			out = append(out, c)
		}
	}
	return out
}

// commandNames returns the visible command names, plus the built-in verbs that
// are handled directly by dispatch rather than by a registry entry.
func commandNames() []string {
	names := make([]string, 0, len(visibleCommands())+2)
	for _, c := range visibleCommands() {
		names = append(names, c.name)
	}
	return append(names, "version", "help")
}

// usageCommandRows renders the registry as the rows shown under the usage
// COMMANDS heading, expanding grouped commands into "<group> <sub>" entries.
func usageCommandRows() [][2]string {
	var rows [][2]string
	for _, c := range visibleCommands() {
		if len(c.subcommands) == 0 {
			rows = append(rows, [2]string{c.name, c.summary})
			continue
		}
		for _, sub := range c.subcommands {
			rows = append(rows, [2]string{c.name + " " + sub.name, sub.summary})
		}
	}
	return rows
}

// flagNamesOf returns every flag name a command registers, globals included.
func flagNamesOf(c command) []string {
	fs, _ := c.flags()
	return flagNames(fs)
}

// ownSpecsOf returns a command's own flag specifications, excluding the global
// groups it opts into.
func ownSpecsOf(c command) []flagSpec {
	_, specs := c.flags()
	return specs
}

// flagNames returns the sorted flag names declared by fs.
func flagNames(fs *flag.FlagSet) []string {
	var names []string
	fs.VisitAll(func(f *flag.Flag) {
		names = append(names, f.Name)
	})
	return names
}

// isBoolFlag reports whether f is a boolean flag, which shells must not treat
// as consuming a following value.
func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}
