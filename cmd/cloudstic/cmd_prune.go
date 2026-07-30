package main

import (
	"context"
	"fmt"
	"io"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
)

type pruneArgs struct {
	*globalFlags
	dryRun bool
}

func declarePruneArgs(g *globalFlags) (*pruneArgs, commandInput) {
	a := &pruneArgs{globalFlags: g}
	return a, commandInput{flags: []flagSpec{
		boolFlag(&a.dryRun, "dry-run", false, "Show what would be deleted without deleting"),
	}}
}

func runPrune(r *runner, ctx context.Context, a *pruneArgs, cfg clientConfig) int {
	if err := r.openClient(ctx, cfg); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	pruneOpts := buildPruneOpts(a)

	result, err := r.client.Prune(ctx, pruneOpts...)
	if err != nil {
		return r.fail("Prune failed: %v", err)
	}
	if a.jsonEnabled() {
		return r.writeJSON(result)
	}
	_, _ = fmt.Fprintln(r.out)
	printPruneStats(r.out, result)
	return 0
}

func buildPruneOpts(a *pruneArgs) []cloudstic.PruneOption {
	var pruneOpts []cloudstic.PruneOption
	if a.dryRun {
		pruneOpts = append(pruneOpts, engine.WithPruneDryRun())
	}
	if a.verbose {
		pruneOpts = append(pruneOpts, engine.WithPruneVerbose())
	}
	return pruneOpts
}

func printPruneStats(out io.Writer, res *cloudstic.PruneResult) {
	if res.DryRun {
		_, _ = fmt.Fprintf(out, "Prune dry run complete.\n")
		_, _ = fmt.Fprintf(out, "  Objects scanned:       %d\n", res.ObjectsScanned)
		_, _ = fmt.Fprintf(out, "  Objects would delete:  %d\n", res.ObjectsDeleted)
	} else {
		_, _ = fmt.Fprintf(out, "Prune complete.\n")
		_, _ = fmt.Fprintf(out, "  Objects scanned:  %d\n", res.ObjectsScanned)
		_, _ = fmt.Fprintf(out, "  Objects deleted:  %d\n", res.ObjectsDeleted)
		_, _ = fmt.Fprintf(out, "  Space reclaimed:  %s\n", formatBytes(res.BytesReclaimed))
	}
}

// pruneCommand declares the `prune` command.
func pruneCommand() command {
	return repoLeaf("prune", "Remove unused data chunks from the repository",
		repoCommandGroups, declarePruneArgs, runPrune)
}
