package main

import (
	"context"
)

type tuiArgs struct {
	profilesFile string
}

func declareTUIArgs(_ *globalFlags) (*tuiArgs, commandInput) {
	// tui opts into no global groups; it reads only its own -profiles-file.
	a := &tuiArgs{}
	return a, commandInput{flags: []flagSpec{
		stringFlag(&a.profilesFile, "profiles-file", defaultProfilesPathNoCreate(), "Path to profiles YAML file",
			withEnv("CLOUDSTIC_PROFILES_FILE"), withPlaceholder("<path>"), withCompleter("_files")),
	}}
}

func runTUI(r *runner, ctx context.Context, args *tuiArgs) int {
	if !r.canPrompt() {
		return r.fail("cloudstic tui requires an interactive terminal")
	}
	return runTUIProgram(r, ctx, args.profilesFile)
}

// tuiCommand declares the `tui` command.
func tuiCommand() command {
	return leaf("tui", "Launch the interactive terminal dashboard",
		nil, declareTUIArgs, runTUI)
}
