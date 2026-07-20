package main

import (
	"context"
	"fmt"
	"io"
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

func printTUIUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: cloudstic tui [options]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Launch the interactive terminal dashboard.")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Options:")
	_, _ = fmt.Fprintf(w, "  -profiles-file <path>  Path to profiles YAML file (default %s)\n", defaultProfilesPathNoCreate())
}

func runTUI(r *runner, ctx context.Context, args *tuiArgs) int {
	if !r.canPrompt() {
		return r.fail("cloudstic tui requires an interactive terminal")
	}

	dashboard, err := tuiBuildDashboard(ctx, args.profilesFile)
	if err != nil {
		return r.fail("Failed to build TUI dashboard: %v", err)
	}
	return newTUISession(r, args.profilesFile, dashboard).run(ctx)
}

// tuiCommand declares the `tui` command.
func tuiCommand() command {
	return leaf("tui", "Launch the interactive terminal dashboard",
		nil, declareTUIArgs, runTUI, withHelp(printTUIUsage))
}
