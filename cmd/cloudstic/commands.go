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
	// newFlagSet builds this command's flag set without parsing it, so tests
	// can introspect the real flags. Nil for commands that take no flags of
	// their own (grouped commands delegate to their subcommands).
	newFlagSet func() *flag.FlagSet
	// ownFlags returns this command's own flag specifications, excluding the
	// global groups it opts into. Shell completion is generated from these.
	// Nil for grouped commands, whose subcommands still build flags inline.
	ownFlags func() []flagSpec
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
			name:       "init",
			summary:    "Initialize a new repository (must run before first backup)",
			newFlagSet: func() *flag.FlagSet { fs, _ := newInitFlagSet(); return fs },
			ownFlags:   func() []flagSpec { return initFlagSpecs(&initArgs{}) },
			run:        runInit,
		},
		{
			name:       "backup",
			summary:    "Create a new backup snapshot from a source",
			newFlagSet: func() *flag.FlagSet { fs, _ := newBackupFlagSet(); return fs },
			ownFlags:   func() []flagSpec { return backupFlagSpecs(&backupArgs{}) },
			run:        runBackup,
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
			name:       "tui",
			summary:    "Launch the interactive terminal dashboard",
			newFlagSet: func() *flag.FlagSet { fs, _ := newTUIFlagSet(); return fs },
			ownFlags:   func() []flagSpec { return tuiFlagSpecs(&tuiArgs{}) },
			run:        runTUI,
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
			name:       "restore",
			summary:    "Restore files from a backup snapshot",
			newFlagSet: func() *flag.FlagSet { fs, _ := newRestoreFlagSet(); return fs },
			ownFlags:   func() []flagSpec { return restoreFlagSpecs(&restoreArgs{}) },
			positional: []string{":snapshot ID:"},
			run:        runRestore,
		},
		{
			name:       "list",
			summary:    "List all backup snapshots in the repository",
			newFlagSet: func() *flag.FlagSet { fs, _ := newListFlagSet(); return fs },
			ownFlags:   func() []flagSpec { return listFlagSpecs(&listArgs{}) },
			run:        runList,
		},
		{
			name:       "ls",
			summary:    "List files within a specific snapshot",
			newFlagSet: func() *flag.FlagSet { fs, _ := newLsFlagSet(); return fs },
			positional: []string{":snapshot ID:"},
			run:        runLsSnapshot,
		},
		{
			name:       "prune",
			summary:    "Remove unused data chunks from the repository",
			newFlagSet: func() *flag.FlagSet { fs, _ := newPruneFlagSet(); return fs },
			ownFlags:   func() []flagSpec { return pruneFlagSpecs(&pruneArgs{}) },
			run:        runPrune,
		},
		{
			name:       "forget",
			summary:    "Remove a specific snapshot from history",
			newFlagSet: func() *flag.FlagSet { fs, _ := newForgetFlagSet(); return fs },
			ownFlags:   func() []flagSpec { return forgetFlagSpecs(&forgetArgs{}) },
			positional: []string{":snapshot ID:"},
			run:        runForget,
		},
		{
			name:       "diff",
			summary:    "Compare two snapshots or a snapshot against latest",
			newFlagSet: func() *flag.FlagSet { fs, _ := newDiffFlagSet(); return fs },
			positional: []string{":snapshot 1:", ":snapshot 2:"},
			run:        runDiff,
		},
		{
			name:       "break-lock",
			summary:    "Remove a stale repository lock left by a crashed process",
			newFlagSet: func() *flag.FlagSet { fs, _ := newBreakLockFlagSet(); return fs },
			run:        runBreakLock,
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
			name:       "check",
			summary:    "Verify repository integrity (reference chain, objects, data)",
			newFlagSet: func() *flag.FlagSet { fs, _ := newCheckFlagSet(); return fs },
			ownFlags:   func() []flagSpec { return checkFlagSpecs(&checkArgs{}) },
			positional: []string{":snapshot ID:"},
			run:        runCheck,
		},
		{
			name:       "cat",
			summary:    "Display raw JSON content of repository objects",
			newFlagSet: func() *flag.FlagSet { fs, _ := newCatFlagSet(); return fs },
			ownFlags:   func() []flagSpec { return catFlagSpecs(&catArgs{}) },
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
