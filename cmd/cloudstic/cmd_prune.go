package main

import (
	"context"
	"flag"
	"fmt"

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

func (r *runner) runPrune() int {
	a := parsePruneArgs()
	if err := r.openClient(a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	pruneOpts := buildPruneOpts(a)

	result, err := r.client.Prune(context.Background(), pruneOpts...)
	if err != nil {
		return r.fail("Prune failed: %v", err)
	}
	_, _ = fmt.Fprintln(r.out)
	r.printPruneStats(result)
	return 0
}

func buildPruneOpts(a *pruneArgs) []cloudstic.PruneOption {
	var pruneOpts []cloudstic.PruneOption
	if a.dryRun {
		pruneOpts = append(pruneOpts, engine.WithPruneDryRun())
	}
	if *a.g.verbose {
		pruneOpts = append(pruneOpts, engine.WithPruneVerbose())
	}
	return pruneOpts
}

func (r *runner) printPruneStats(res *cloudstic.PruneResult) {
	if res.DryRun {
		_, _ = fmt.Fprintf(r.out, "Prune dry run complete.\n")
		_, _ = fmt.Fprintf(r.out, "  Objects scanned:       %d\n", res.ObjectsScanned)
		_, _ = fmt.Fprintf(r.out, "  Objects would delete:  %d\n", res.ObjectsDeleted)
	} else {
		_, _ = fmt.Fprintf(r.out, "Prune complete.\n")
		_, _ = fmt.Fprintf(r.out, "  Objects scanned:  %d\n", res.ObjectsScanned)
		_, _ = fmt.Fprintf(r.out, "  Objects deleted:  %d\n", res.ObjectsDeleted)
		_, _ = fmt.Fprintf(r.out, "  Space reclaimed:  %s\n", formatBytes(res.BytesReclaimed))
	}
}
