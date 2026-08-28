package main

import (
	"context"
)

type tuiArgs struct {
	*globalFlags
	profilesFile string
}

func declareTUIArgs(g *globalFlags) (*tuiArgs, commandInput) {
	// tui opts into no global *groups*; it reads only its own -profiles-file
	// and the base flags every command gets, which is where -config-dir lives.
	a := &tuiArgs{globalFlags: g}
	return a, commandInput{flags: []flagSpec{
		profilesFileFlag(&a.profilesFile, g),
	}}
}

func runTUI(r *runner, ctx context.Context, a *tuiArgs) int {
	if !r.canPrompt() {
		return r.fail("cloudstic tui requires an interactive terminal")
	}
	return runTUIProgram(r, ctx, a.profilesFile, a.configDir)
}

// tuiCommand declares the `tui` command.
func tuiCommand() command {
	return leaf("tui", "Launch the interactive terminal dashboard",
		nil, declareTUIArgs, runTUI)
}
