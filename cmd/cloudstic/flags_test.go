package main

import (
	"flag"
	"reflect"
	"testing"
)

func TestReorderArgs_PreservesTerminator(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "")

	if err := parseFlags(fs, []string{"object", "-json", "--", "-no-prompt"}, nil); err != nil {
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
	args, err := parseCatArgs([]string{"--", "-weirdly-named-file"})
	if err != nil {
		t.Fatalf("parseCatArgs() error = %v", err)
	}
	if got, want := args.keys, []string{"-weirdly-named-file"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %q, want %q", got, want)
	}
}

func TestParseHelpers_ReturnErrors(t *testing.T) {
	if _, err := parseCatArgs([]string{"-unknown"}); err == nil {
		t.Fatal("parseCatArgs() expected an unknown-flag error")
	}
	if _, err := parseDiffArgs([]string{"only-one-snapshot"}); err == nil {
		t.Fatal("parseDiffArgs() expected a validation error")
	}
	if _, err := parseForgetArgs(nil); err == nil {
		t.Fatal("parseForgetArgs() expected a validation error")
	}
}

func TestGlobalFlagScans_StopAtTerminator(t *testing.T) {
	args := []string{"--", "-no-prompt"}
	if hasGlobalFlag(args, "no-prompt") {
		t.Fatal("hasGlobalFlag() treated a positional argument as a flag")
	}

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	if err := parseFlags(fs, []string{"--", "-store"}, nil); err != nil {
		t.Fatalf("parseFlags() error = %v", err)
	}
	if g.valueSource("store") != valueSourceDefault {
		t.Fatal("globalFlags.valueSource() treated a positional argument as a flag")
	}
}

func TestParseFlags_AcceptsNoPromptForNestedCommands(t *testing.T) {
	fs := flag.NewFlagSet("nested", flag.ContinueOnError)
	if err := parseFlags(fs, []string{"-no-prompt"}, nil); err != nil {
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
