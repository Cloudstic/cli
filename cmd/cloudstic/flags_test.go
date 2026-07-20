package main

import (
	"flag"
	"reflect"
	"testing"
)

func TestReorderArgs_PreservesTerminator(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "")
	var positional []string

	cf := commandFlags{set: fs, positionals: []positionalSpec{remainingPositionals(&positional, "argument")}}
	if err := cf.parse([]string{"object", "-json", "--", "-no-prompt"}); err != nil {
		t.Fatalf("parseFlags() error = %v", err)
	}
	if !*jsonOutput {
		t.Fatal("expected -json to be parsed before the positional argument")
	}
	if got, want := fs.Args(), []string{"object", "-no-prompt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Args() = %q, want %q", got, want)
	}
}

func TestParseCatArgs_TerminatedFlagShapedKey(t *testing.T) {
	args, err := parseInto("cat", repoCommandGroups, declareCatArgs, []string{"--", "-weirdly-named-file"})
	if err != nil {
		t.Fatalf("parseCatArgs() error = %v", err)
	}
	if got, want := args.keys, []string{"-weirdly-named-file"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %q, want %q", got, want)
	}
}

func TestParseHelpers_ReturnErrors(t *testing.T) {
	if _, err := parseInto("cat", repoCommandGroups, declareCatArgs, []string{"-unknown"}); err == nil {
		t.Fatal("parseCatArgs() expected an unknown-flag error")
	}
	if _, err := parseInto("diff", repoCommandGroups, declareDiffArgs, []string{"only-one-snapshot"}); err == nil {
		t.Fatal("parseDiffArgs() expected a validation error")
	}
	forget, err := parseInto("forget", repoCommandGroups, declareForgetArgs, nil)
	if err != nil {
		t.Fatalf("parse forget: %v", err)
	}
	if err := prepareForgetArgs(forget); err == nil {
		t.Fatal("parseForgetArgs() expected a validation error")
	}
}

func TestGlobalFlagScans_StopAtTerminator(t *testing.T) {
	args := []string{"--", "-no-prompt"}
	if hasGlobalFlag(args, "no-prompt") {
		t.Fatal("hasGlobalFlag() treated a positional argument as a flag")
	}

	g := &globalFlags{}
	var positional []string
	cf := newCommandFlags("test", repoCommandGroups, g, commandInput{
		positionals: []positionalSpec{remainingPositionals(&positional, "argument")},
	})
	if err := cf.parse([]string{"--", "-store"}); err != nil {
		t.Fatalf("parseFlags() error = %v", err)
	}
	if g.flagProvided("store") {
		t.Fatal("globalFlags.flagProvided() treated a positional argument as a flag")
	}
}

func TestParseFlags_AcceptsNoPromptForNestedCommands(t *testing.T) {
	g := &globalFlags{}
	if err := newCommandFlags("nested", nil, g, commandInput{}).parse([]string{"-no-prompt"}); err != nil {
		t.Fatalf("parseFlags() error = %v", err)
	}
}

func TestParseErrorExitCode_HelpSucceeds(t *testing.T) {
	r := newRunner(nil)
	if code := r.parseError(flag.ErrHelp); code != 0 {
		t.Fatalf("parseError(flag.ErrHelp) = %d, want 0", code)
	}
}

func TestRunnerWithArgs_DoesNotMutateParent(t *testing.T) {
	parent := newRunner([]string{"store", "list"})
	child := parent.withArgs([]string{"list", "-no-prompt"})

	if got, want := parent.args, []string{"store", "list"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parent args = %q, want %q", got, want)
	}
	if got, want := child.args, []string{"list", "-no-prompt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("child args = %q, want %q", got, want)
	}
	if !child.noPrompt {
		t.Fatal("child runner did not inherit prompt control from its arguments")
	}
}
