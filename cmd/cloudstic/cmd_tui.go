package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
)

type tuiArgs struct {
	profilesFile string
}

func tuiCommandSpec() *commandSpec {
	command := leaf("tui", "Launch the interactive terminal dashboard", "", runTUI, profilesFileFlag())
	command.flagProbe = probeTUIFlags
	return command
}

func probeTUIFlags(r *runner, _ context.Context) int {
	_, _ = parseTUIArgs(r.args)
	return 0
}

func parseTUIArgs(args []string) (*tuiArgs, error) {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	a := &tuiArgs{}
	fs.StringVar(&a.profilesFile, "profiles-file", defaultProfilesPathNoCreate(), "Path to profiles YAML file")
	if err := parseFlags(fs, args, tuiCommandSpec()); err != nil {
		return nil, err
	}
	return a, nil
}

func printTUIUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: cloudstic tui [options]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Launch the interactive terminal dashboard.")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Options:")
	_, _ = fmt.Fprintf(w, "  -profiles-file <path>  Path to profiles YAML file (default %s)\n", defaultProfilesPathNoCreate())
}

func runTUI(r *runner, ctx context.Context) int {
	for _, arg := range r.args {
		if arg == "-h" || arg == "--help" || arg == "help" {
			printTUIUsage(r.out)
			return 0
		}
	}

	args, err := parseTUIArgs(r.args)
	if err != nil {
		return r.parseError(err)
	}
	if !r.canPrompt() {
		return r.fail("cloudstic tui requires an interactive terminal")
	}

	dashboard, err := tuiBuildDashboard(ctx, args.profilesFile)
	if err != nil {
		return r.fail("Failed to build TUI dashboard: %v", err)
	}
	return newTUISession(r, args.profilesFile, dashboard).run(ctx)
}
