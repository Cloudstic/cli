package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"
)

// These compatibility helpers keep focused command tests concise while the
// production path uses the recursive registry dispatcher directly.
func runAuth(r *runner, ctx context.Context) int {
	return dispatchCommand(r, ctx, rootCommand("auth"))
}

func runStore(r *runner, ctx context.Context) int {
	return dispatchCommand(r, ctx, rootCommand("store"))
}

func runProfile(r *runner, ctx context.Context) int {
	return dispatchCommand(r, ctx, rootCommand("profile"))
}

func runSource(r *runner, ctx context.Context) int {
	return dispatchCommand(r, ctx, rootCommand("source"))
}

func runSetup(r *runner, ctx context.Context) int {
	return dispatchCommand(r, ctx, rootCommand("setup"))
}

func TestCommandRegistryIsValid(t *testing.T) {
	seenPaths := make(map[string]bool)
	seenRoots := make(map[string]bool)
	for _, root := range commandRegistry() {
		if seenRoots[root.name] {
			t.Errorf("duplicate root command %q", root.name)
		}
		seenRoots[root.name] = true
		for _, alias := range root.aliases {
			if seenRoots[alias] {
				t.Errorf("duplicate command name or alias %q", alias)
			}
			seenRoots[alias] = true
		}
	}
	for _, command := range publicLeafCommands() {
		if seenPaths[command.path()] {
			t.Errorf("duplicate command path %q", command.path())
		}
		seenPaths[command.path()] = true
		if command.summary == "" {
			t.Errorf("public command %q has no summary", command.path())
		}
		if command.run == nil {
			t.Errorf("leaf command %q has no handler", command.path())
		}
		if len(command.argumentValues) != 0 && command.argumentName == "" {
			t.Errorf("command %q has positional values without an argument name", command.path())
		}
		seenFlags := make(map[string]bool)
		for _, spec := range command.effectiveFlags() {
			if seenFlags[spec.name] {
				t.Errorf("command %q has duplicate flag %q", command.path(), spec.name)
			}
			seenFlags[spec.name] = true
			if spec.description == "" {
				t.Errorf("command %q flag %q has no description", command.path(), spec.name)
			}
			if spec.completion != completionNone && !spec.takesValue() {
				t.Errorf("command %q boolean flag %q has a value completer", command.path(), spec.name)
			}
			if len(spec.completionValues) != 0 && !spec.takesValue() {
				t.Errorf("command %q boolean flag %q has completion values", command.path(), spec.name)
			}
			if len(spec.completionValues) != 0 && spec.completion != completionNone {
				t.Errorf("command %q flag %q mixes static and dynamic completion", command.path(), spec.name)
			}
		}
	}
}

func TestCommandRegistryFlagsMatchParsers(t *testing.T) {
	for _, command := range publicLeafCommands() {
		if command.flagProbe == nil {
			continue
		}
		t.Run(strings.ReplaceAll(command.path(), " ", "_"), func(t *testing.T) {
			actual := probeCommandFlags(t, command)
			want := make(map[string]bool)
			for _, spec := range command.effectiveFlags() {
				want[spec.name] = spec.takesValue()
			}
			if !reflect.DeepEqual(actual, want) {
				t.Errorf("flag parity mismatch\nactual: %#v\nwant:   %#v", actual, want)
			}
		})
	}
}

func probeCommandFlags(t *testing.T, command *commandSpec) map[string]bool {
	t.Helper()
	var observed *flag.FlagSet
	previous := flagSetObserver
	flagSetObserver = func(fs *flag.FlagSet) {
		observed = fs
		fs.SetOutput(io.Discard)
	}
	t.Cleanup(func() { flagSetObserver = previous })
	r := &runner{args: []string{"-h"}, out: io.Discard, errOut: io.Discard, noPrompt: true}
	command.flagProbe(r, context.Background())
	if observed == nil {
		t.Fatalf("command did not parse a FlagSet")
	}
	result := make(map[string]bool)
	observed.VisitAll(func(f *flag.Flag) {
		boolean := false
		if boolValue, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
			boolean = boolValue.IsBoolFlag()
		}
		result[f.Name] = !boolean
	})
	return result
}

func TestUsageListsPublicLeaves(t *testing.T) {
	var out bytes.Buffer
	printUsage(&out)
	usage := out.String()
	commandsSection := usage
	if _, after, ok := strings.Cut(usage, "COMMANDS"); ok {
		commandsSection = after
	}
	if before, _, ok := strings.Cut(commandsSection, "GLOBAL OPTIONS"); ok {
		commandsSection = before
	}
	for _, command := range publicLeafCommands() {
		if !strings.Contains(usage, command.path()) {
			t.Errorf("usage missing public command %q", command.path())
		}
	}
	for _, hidden := range []string{"__complete", "--version", "--help"} {
		if strings.Contains(commandsSection, hidden) {
			t.Errorf("usage contains hidden command or alias %q", hidden)
		}
	}
	for _, required := range []string{"store init", "version"} {
		if !strings.Contains(usage, required) {
			t.Errorf("usage missing newly visible command %q", required)
		}
	}
}

func TestUsageFlagsAndEnvironmentComeFromRegistries(t *testing.T) {
	var out bytes.Buffer
	printUsage(&out)
	usage := out.String()

	for _, spec := range globalFlagSpecs {
		if !strings.Contains(usage, "-"+spec.name) {
			t.Errorf("usage missing global flag -%s", spec.name)
		}
	}
	for _, command := range publicLeafCommands() {
		for _, spec := range command.flags {
			if !strings.Contains(usage, "-"+spec.name) {
				t.Errorf("usage missing %q flag -%s", command.path(), spec.name)
			}
		}
		for _, note := range command.notes {
			if !strings.Contains(usage, note) {
				t.Errorf("usage missing %q note %q", command.path(), note)
			}
		}
	}
	for _, variable := range environmentVariables() {
		if variable.Documented && !strings.Contains(usage, variable.Name) {
			t.Errorf("usage missing documented environment variable %s", variable.Name)
		}
	}

	_, storeNew, ok := strings.Cut(usage, "store new")
	if !ok {
		t.Fatal("usage missing store new details")
	}
	storeNew, _, _ = strings.Cut(storeNew, "store list")
	for _, line := range strings.Split(storeNew, "\n") {
		if strings.Contains(line, "-s3-region") && strings.Contains(line, "CLOUDSTIC_S3_REGION") {
			t.Error("store new incorrectly advertises an environment binding owned by the global flag spec")
		}
	}
}

func TestNestedRegistryDispatch(t *testing.T) {
	var errOut bytes.Buffer
	r := &runner{args: []string{"wat"}, out: io.Discard, errOut: &errOut}
	if code := runCmd(r, context.Background(), "auth"); code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(errOut.String(), "Unknown auth subcommand: wat") {
		t.Fatalf("unexpected error: %q", errOut.String())
	}
}
