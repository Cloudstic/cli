package main

import (
	"context"
)

type tuiArgs struct {
	profilesFile string
	bubbletea    bool
}

func declareTUIArgs(_ *globalFlags) (*tuiArgs, commandInput) {
	// tui opts into no global groups; it reads only its own -profiles-file.
	a := &tuiArgs{}
	return a, commandInput{flags: []flagSpec{
		stringFlag(&a.profilesFile, "profiles-file", defaultProfilesPathNoCreate(), "Path to profiles YAML file",
			withEnv("CLOUDSTIC_PROFILES_FILE"), withPlaceholder("<path>"), withCompleter("_files")),
		boolFlag(&a.bubbletea, "bubbletea", false,
			"Use the experimental Bubble Tea renderer (RFC 0012 Phase 2)",
			withEnv("CLOUDSTIC_TUI_BUBBLETEA")),
	}}
}

func runTUI(r *runner, ctx context.Context, args *tuiArgs) int {
	if !r.canPrompt() {
		return r.fail("cloudstic tui requires an interactive terminal")
	}

	dashboard, err := tuiBuildDashboard(ctx, args.profilesFile)
	if err != nil {
		return r.fail("Failed to build TUI dashboard: %v", err)
	}
	if args.bubbletea {
		return runBubbleTeaTUI(r, ctx, dashboard)
	}
	return newTUISession(r, args.profilesFile, dashboard).run(ctx)
}

// tuiCommand declares the `tui` command.
func tuiCommand() command {
	return leaf("tui", "Launch the interactive terminal dashboard",
		nil, declareTUIArgs, runTUI)
}
