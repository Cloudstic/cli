package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
)

type pruneArgs struct {
	g      *globalFlags
	dryRun bool
}

func pruneFlagSpecs(a *pruneArgs) []flagSpec {
	return []flagSpec{
		boolFlag(&a.dryRun, "dry-run", false, "Show what would be deleted without deleting"),
	}
}

func newPruneFlagSet() (boundFlagSet, *pruneArgs) {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	a := &pruneArgs{}
	g, globalSpecs := addGlobalFlags(fs, repoCommandGroups)
	a.g = g
	own := pruneFlagSpecs(a)
	bindFlags(fs, own)
	return boundFlagSet{set: fs, global: globalSpecs, own: own}, a
}

func parsePruneArgs(args []string) (*pruneArgs, error) {
	b, a := newPruneFlagSet()
	if err := parseFlags(b, args); err != nil {
		return nil, err
	}
	return a, nil
}

func runPrune(r *runner, ctx context.Context) int {
	a, err := parsePruneArgs(r.args)
	if err != nil {
		return r.parseError(err)
	}
	if err := r.openClient(ctx, a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	pruneOpts := buildPruneOpts(a)

	result, err := r.client.Prune(ctx, pruneOpts...)
	if err != nil {
		return r.fail("Prune failed: %v", err)
	}
	if a.g.jsonEnabled() {
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
	if a.g.verbose {
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
	return leaf("prune", "Remove unused data chunks from the repository",
		runPrune, withFlags(func() boundFlagSet { b, _ := newPruneFlagSet(); return b }))
}
