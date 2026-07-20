package main

import (
	"context"
	"fmt"
	"io"

	cloudstic "github.com/cloudstic/cli"
)

type diffArgs struct {
	*globalFlags
	snap1 string
	snap2 string
}

func printDiffUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: cloudstic diff [options] <snapshot_id1> <snapshot_id2>")
	_, _ = fmt.Fprintln(w, "       cloudstic diff [options] <snapshot_id1> latest")
}

func declareDiffArgs(g *globalFlags) (*diffArgs, commandInput) {
	a := &diffArgs{globalFlags: g}
	return a, commandInput{positionals: []positionalSpec{
		requiredPositional(&a.snap1, "snapshot ID 1"),
		requiredPositional(&a.snap2, "snapshot ID 2"),
	}}
}

func runDiff(r *runner, ctx context.Context, a *diffArgs) int {
	if err := r.openClient(ctx, a.globalFlags); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	diffOpts := buildDiffOpts(a)

	result, err := r.client.Diff(ctx, a.snap1, a.snap2, diffOpts...)
	if err != nil {
		return r.fail("Diff failed: %v", err)
	}
	if a.jsonEnabled() {
		return r.writeJSON(result)
	}
	printDiffResult(r.out, result)
	return 0
}

func buildDiffOpts(a *diffArgs) []cloudstic.DiffOption {
	var diffOpts []cloudstic.DiffOption
	if a.verbose {
		diffOpts = append(diffOpts, cloudstic.WithDiffVerbose())
	}
	return diffOpts
}

func printDiffResult(out io.Writer, result *cloudstic.DiffResult) {
	_, _ = fmt.Fprintf(out, "Diffing %s vs %s\n", result.Ref1, result.Ref2)
	for _, c := range result.Changes {
		_, _ = fmt.Fprintf(out, "%s %s\n", c.Type, c.Path)
	}
}

// diffCommand declares the `diff` command.
func diffCommand() command {
	return leaf("diff", "Compare two snapshots or a snapshot against latest",
		repoCommandGroups, declareDiffArgs, runDiff, withUsageOnError(printDiffUsage))
}
