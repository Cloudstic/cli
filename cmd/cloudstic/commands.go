package main

import "flag"

// commandRegistry is the ordered list of top-level commands. Each command is
// declared in its own cmd_<name>.go file, next to the code that implements it;
// this list only fixes the order they appear in `cloudstic help`.
//
// Dispatch (main.go), the usage listing (usage.go), and the shell-completion
// command lists (completion.go) are all derived from this tree, so adding a
// command means adding one declaration and one entry here.
func commandRegistry() []command {
	return []command{
		initCommand(),
		backupCommand(),
		authCommand(),
		storeCommand(),
		sourceCommand(),
		setupCommand(),
		tuiCommand(),
		profileCommand(),
		restoreCommand(),
		listCommand(),
		lsCommand(),
		findCommand(),
		pruneCommand(),
		forgetCommand(),
		diffCommand(),
		copyCommand(),
		breakLockCommand(),
		keyCommand(),
		checkCommand(),
		catCommand(),
		completionCommand(),
		completeCommand(),
	}
}

// lookupCommand resolves a top-level command by name.
func lookupCommand(name string) (command, bool) {
	for _, c := range commandRegistry() {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// visibleCommands returns the top-level commands shown to users.
func visibleCommands() []command {
	var out []command
	for _, c := range commandRegistry() {
		if !c.hidden {
			out = append(out, c)
		}
	}
	return out
}

// commandNames returns the visible top-level command names, plus the built-in
// verbs handled directly by dispatch rather than by a registry entry.
func commandNames() []string {
	names := make([]string, 0, len(visibleCommands())+2)
	for _, c := range visibleCommands() {
		names = append(names, c.name)
	}
	return append(names, "version", "help")
}

// usageCommandRows renders the tree as the rows shown under the usage COMMANDS
// heading, expanding groups into "<group> <sub>" entries.
func usageCommandRows() [][2]string {
	var rows [][2]string
	for _, c := range visibleCommands() {
		if !c.isGroup() {
			rows = append(rows, [2]string{c.name, c.summary})
			continue
		}
		for _, child := range c.visibleChildren() {
			rows = append(rows, [2]string{c.name + " " + child.name, child.summary})
		}
	}
	return rows
}

// flagNamesOf returns every flag name a command registers, globals included.
func flagNamesOf(c command) []string {
	if c.flags == nil {
		return nil
	}
	return c.flags().names()
}

// ownSpecsOf returns a command's own flag specifications, excluding the global
// groups it opts into.
func ownSpecsOf(c command) []flagSpec {
	if c.flags == nil {
		return nil
	}
	return c.flags().ownSpecs()
}

// flagNames returns the flag names declared by fs, in lexical order.
func flagNames(fs *flag.FlagSet) []string {
	var names []string
	fs.VisitAll(func(f *flag.Flag) {
		names = append(names, f.Name)
	})
	return names
}
