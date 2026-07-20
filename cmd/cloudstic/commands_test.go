package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRegistryEntriesAreWellFormed guards the registry's own invariants.
func TestRegistryEntriesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	walk(commandRegistry(), func(path string, c command) {
		if c.name == "" {
			t.Errorf("command at %q has an empty name", path)
		}
		if seen[path] {
			t.Errorf("duplicate command path %q", path)
		}
		seen[path] = true
		if c.summary == "" {
			t.Errorf("command %q has no summary", path)
		}
		switch {
		case c.isGroup() && c.run != nil:
			t.Errorf("group %q must not declare a run function", path)
		case !c.isGroup() && c.run == nil:
			t.Errorf("leaf %q has no run function", path)
		}
	})
}

// TestUsageListsEveryRegisteredCommand ensures the usage output cannot drift
// from the registry, since both are now derived from it.
func TestUsageListsEveryRegisteredCommand(t *testing.T) {
	var buf bytes.Buffer
	printUsage(&buf)
	got := buf.String()

	for _, path := range leafPaths(visibleCommands()) {
		if !strings.Contains(got, path) {
			t.Errorf("usage output is missing command %q", path)
		}
	}

	for _, c := range commandRegistry() {
		if c.hidden && strings.Contains(got, c.name) {
			t.Errorf("usage output should not mention hidden command %q", c.name)
		}
	}
}

// completionScripts renders each shell script once for the drift checks below.
func completionScripts(t *testing.T) map[string]string {
	t.Helper()
	scripts := map[string]string{}
	for name, gen := range map[string]func(w *bytes.Buffer){
		"bash": func(w *bytes.Buffer) { completionBash(w) },
		"zsh":  func(w *bytes.Buffer) { completionZsh(w) },
		"fish": func(w *bytes.Buffer) { completionFish(w) },
	} {
		var buf bytes.Buffer
		gen(&buf)
		scripts[name] = buf.String()
	}
	return scripts
}

// TestCompletionListsEveryRegisteredCommand is the drift guard: a command added
// to the registry but missing from any shell script fails here. This is the
// regression test for `check` and `store`, which were previously absent from
// the hand-maintained bash/zsh/fish command lists.
func TestCompletionListsEveryRegisteredCommand(t *testing.T) {
	for shell, script := range completionScripts(t) {
		for _, c := range visibleCommands() {
			if !strings.Contains(script, c.name) {
				t.Errorf("%s completion is missing command %q", shell, c.name)
			}
		}
	}

	// Hidden commands must not be offered to the user. The scripts still
	// *invoke* `cloudstic __complete` to fetch dynamic values, so assert on the
	// generated list of offered names rather than the whole script.
	for _, c := range commandRegistry() {
		if !c.hidden {
			continue
		}
		for _, offered := range commandNames() {
			if offered == c.name {
				t.Errorf("hidden command %q must not be offered for completion", c.name)
			}
		}
	}
}

// TestCompletionGlobalFlagsMatchFlagSet ensures the completion global-flag
// lists match the flags actually registered by addGlobalFlags.
func TestCompletionGlobalFlagsMatchFlagSet(t *testing.T) {
	scripts := completionScripts(t)

	for _, name := range globalFlagNames() {
		if !strings.Contains(scripts["bash"], "-"+name) {
			t.Errorf("bash completion is missing global flag -%s", name)
		}
	}

	// The bash global_flags list must be exactly the registered global flags:
	// no extras, no omissions, no duplicates.
	line := ""
	for _, l := range strings.Split(scripts["bash"], "\n") {
		if strings.Contains(l, "local global_flags=") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("could not find global_flags declaration in bash completion")
	}
	got := strings.Fields(strings.Trim(strings.SplitN(line, "=", 2)[1], `"`))
	want := strings.Fields(dashPrefixed(globalFlagNames(), " "))
	if len(got) != len(want) {
		t.Errorf("bash global_flags has %d entries, registry has %d", len(got), len(want))
	}
	seen := map[string]bool{}
	for _, f := range got {
		if seen[f] {
			t.Errorf("bash global_flags lists %s more than once", f)
		}
		seen[f] = true
	}
	for _, f := range want {
		if !seen[f] {
			t.Errorf("bash global_flags is missing %s", f)
		}
	}
}

// TestCompletionValueFlagsExcludeBooleans ensures boolean flags are never
// listed as value-taking, which would make shells swallow the next word.
func TestCompletionValueFlagsExcludeBooleans(t *testing.T) {
	valueFlags := map[string]bool{}
	for _, n := range globalValueFlagNames() {
		valueFlags[n] = true
	}
	fs := globalFlagSet()
	for _, name := range globalFlagNames() {
		f := fs.Lookup(name)
		if isBoolFlag(f) && valueFlags[name] {
			t.Errorf("boolean flag -%s must not be listed as value-taking", name)
		}
		if !isBoolFlag(f) && !valueFlags[name] {
			t.Errorf("value flag -%s is missing from the value-taking list", name)
		}
	}
}

// TestCommandFlagSetsAreIntrospectable ensures every command that declares a
// flag-set builder produces a usable, non-empty flag set. This is what lets the
// completion drift checks reason about real flags rather than a hand-copied list.
func TestCommandFlagSetsAreIntrospectable(t *testing.T) {
	for _, c := range commandRegistry() {
		if c.flags == nil {
			continue
		}
		fs := c.flags().set
		if fs == nil {
			t.Errorf("command %q returned a nil flag set", c.name)
			continue
		}
		if len(flagNames(fs)) == 0 {
			t.Errorf("command %q declared a flag set with no flags", c.name)
		}
	}
}

// TestCommandFlagsAppearInBashCompletion checks that every flag a command
// actually declares is offered by the bash completion for that command.
func TestCommandFlagsAppearInBashCompletion(t *testing.T) {
	script := completionScripts(t)["bash"]
	globals := map[string]bool{}
	for _, n := range globalFlagNames() {
		globals[n] = true
	}

	for _, c := range commandRegistry() {
		if c.flags == nil || c.hidden {
			continue
		}
		for _, name := range flagNamesOf(c) {
			if globals[name] {
				continue // covered by the global_flags list
			}
			if !strings.Contains(script, "-"+name) {
				t.Errorf("bash completion for %q is missing flag -%s", c.name, name)
			}
		}
	}
}

// allDeclaredSpecs collects every flag specification the CLI declares.
func allDeclaredSpecs() []flagSpec {
	specs := globalFlagSpecsFor(&globalFlags{}, allGlobalGroups)
	for _, c := range commandRegistry() {
		if c.flags != nil {
			specs = append(specs, ownSpecsOf(c)...)
		}
	}
	return specs
}

// TestDeclaredCompletersExistInZsh catches a completer named in a flag spec
// that no zsh helper actually defines — a silent completion breakage that is
// invisible until a user tabs on that flag.
func TestDeclaredCompletersExistInZsh(t *testing.T) {
	script := completionScripts(t)["zsh"]
	for _, s := range allDeclaredSpecs() {
		switch s.completer {
		case "", "_files": // builtin or none
			continue
		}
		if !strings.Contains(script, s.completer+"() {") {
			t.Errorf("flag -%s declares completer %q, but no such zsh function is defined", s.name, s.completer)
		}
	}
}

// TestDeclaredCompletersHaveFishMapping ensures every completer has an explicit
// fish translation, so dynamic value suggestions are not silently dropped.
func TestDeclaredCompletersHaveFishMapping(t *testing.T) {
	for _, s := range allDeclaredSpecs() {
		if s.completer == "" || s.isBool {
			continue
		}
		if got := fishValueSpec(s.completer); got == " -x" && s.completer != "" {
			t.Errorf("flag -%s declares completer %q with no fish mapping; it will lose value suggestions", s.name, s.completer)
		}
	}
}

// TestGeneratedCommandFlagsMatchSpecs is the per-command drift guard: each
// generated shell entry must offer exactly the flags the command declares.
func TestGeneratedCommandFlagsMatchSpecs(t *testing.T) {
	scripts := completionScripts(t)
	for _, c := range commandRegistry() {
		if c.flags == nil || c.hidden {
			continue
		}
		for _, s := range ownSpecsOf(c) {
			for shell, script := range scripts {
				// fish spells long options as "-l name"; bash and zsh use "-name".
				token := "-" + s.name
				if shell == "fish" {
					token = "-l " + s.name
				}
				if !strings.Contains(script, token) {
					t.Errorf("%s completion for %q is missing flag -%s", shell, c.name, s.name)
				}
			}
		}
	}
}

// TestGlobalFlagsGeneratedForAllShells ensures the zsh global_flags array and
// the fish global completions are derived from the global specs, so a new
// global flag cannot be offered by one shell and missed by another. fish
// previously declared only 3 of the ~29 global flags.
func TestGlobalFlagsGeneratedForAllShells(t *testing.T) {
	scripts := completionScripts(t)
	specs := globalSpecs()
	if len(specs) == 0 {
		t.Fatal("no global specs declared")
	}

	for _, s := range specs {
		if !strings.Contains(scripts["zsh"], "'-"+s.name+"[") {
			t.Errorf("zsh global_flags is missing -%s", s.name)
		}
		if !strings.Contains(scripts["fish"], "complete -c cloudstic -o "+s.name+" -l "+s.name) {
			t.Errorf("fish global completions are missing -%s", s.name)
		}
	}

	// The zsh array must contain exactly the declared globals, no extras.
	block := scripts["zsh"]
	start := strings.Index(block, "global_flags=(")
	if start < 0 {
		t.Fatal("zsh global_flags array not found")
	}
	end := strings.Index(block[start:], "\n    )")
	entries := strings.Count(block[start:start+end], "\n        '-")
	if entries != len(specs) {
		t.Errorf("zsh global_flags has %d entries, specs declare %d", entries, len(specs))
	}
}
