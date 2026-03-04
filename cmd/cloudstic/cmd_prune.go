package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
)

type pruneArgs struct {
	g      *globalFlags
	dryRun bool
}

func parsePruneArgs() *pruneArgs {
	fs := flag.NewFlagSet("prune", flag.ExitOnError)
	a := &pruneArgs{}
	a.g = addGlobalFlags(fs)
	dryRun := fs.Bool("dry-run", false, "Show what would be deleted without deleting")
	mustParse(fs)
	a.dryRun = *dryRun
	return a
}

func runPrune() {
	a := parsePruneArgs()

	ctx := context.Background()

	client, err := a.g.openClient()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	var pruneOpts []cloudstic.PruneOption
	if a.dryRun {
		pruneOpts = append(pruneOpts, engine.WithPruneDryRun())
	}
	if *a.g.verbose {
		pruneOpts = append(pruneOpts, engine.WithPruneVerbose())
	}
	result, err := client.Prune(ctx, pruneOpts...)
	if err != nil {
		fmt.Printf("Prune failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	printPruneStats(result)
}

// printPruneStats prints a prune result summary to stdout.
func printPruneStats(r *engine.PruneResult) {
	if r.DryRun {
		fmt.Printf("Prune dry run complete.\n")
		fmt.Printf("  Objects scanned:       %d\n", r.ObjectsScanned)
		fmt.Printf("  Objects would delete:  %d\n", r.ObjectsDeleted)
	} else {
		fmt.Printf("Prune complete.\n")
		fmt.Printf("  Objects scanned:  %d\n", r.ObjectsScanned)
		fmt.Printf("  Objects deleted:  %d\n", r.ObjectsDeleted)
		fmt.Printf("  Space reclaimed:  %s\n", formatBytes(r.BytesReclaimed))
	}
}
