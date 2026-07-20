package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	cloudstic "github.com/cloudstic/cli"
)

type diffArgs struct {
	g     *globalFlags
	snap1 string
	snap2 string
}

func newDiffFlagSet() (boundFlagSet, *diffArgs) {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	a := &diffArgs{}
	g, globalSpecs := addGlobalFlags(fs, repoCommandGroups)
	a.g = g
	return boundFlagSet{set: fs, global: globalSpecs}, a
}

func parseDiffArgs(args []string) (*diffArgs, error) {
	b, a := newDiffFlagSet()
	if err := parseFlags(b, args); err != nil {
		return nil, err
	}
	if b.set.NArg() < 2 {
		return nil, fmt.Errorf("two snapshot IDs are required")
	}
	a.snap1 = b.set.Arg(0)
	a.snap2 = b.set.Arg(1)
	return a, nil
}

func runDiff(r *runner, ctx context.Context) int {
	a, err := parseDiffArgs(r.args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if !r.jsonEnabled() {
			_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic diff [options] <snapshot_id1> <snapshot_id2>")
			_, _ = fmt.Fprintln(r.errOut, "       cloudstic diff [options] <snapshot_id1> latest")
		}
		return r.parseError(err)
	}
	if err := r.openClient(ctx, a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	diffOpts := buildDiffOpts(a)

	result, err := r.client.Diff(ctx, a.snap1, a.snap2, diffOpts...)
	if err != nil {
		return r.fail("Diff failed: %v", err)
	}
	if a.g.jsonEnabled() {
		return r.writeJSON(result)
	}
	printDiffResult(r.out, result)
	return 0
}

func buildDiffOpts(a *diffArgs) []cloudstic.DiffOption {
	var diffOpts []cloudstic.DiffOption
	if a.g.verbose {
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
		runDiff, withFlags(func() boundFlagSet { b, _ := newDiffFlagSet(); return b }),
		withPositional(":snapshot 1:", ":snapshot 2:"))
}
