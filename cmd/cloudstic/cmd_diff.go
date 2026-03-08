package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	cloudstic "github.com/cloudstic/cli"
)

type diffArgs struct {
	g     *globalFlags
	snap1 string
	snap2 string
}

func parseDiffArgs() *diffArgs {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	a := &diffArgs{}
	a.g = addGlobalFlags(fs)
	mustParse(fs)
	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "Usage: cloudstic diff [options] <snapshot_id1> <snapshot_id2>")
		fmt.Fprintln(os.Stderr, "       cloudstic diff [options] <snapshot_id1> latest")
		os.Exit(1)
	}
	a.snap1 = fs.Arg(0)
	a.snap2 = fs.Arg(1)
	return a
}

func (r *runner) runDiff() int {
	a := parseDiffArgs()
	if err := r.openClient(a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	diffOpts := buildDiffOpts(a)

	result, err := r.client.Diff(context.Background(), a.snap1, a.snap2, diffOpts...)
	if err != nil {
		return r.fail("Diff failed: %v", err)
	}
	r.printDiffResult(result)
	return 0
}

func buildDiffOpts(a *diffArgs) []cloudstic.DiffOption {
	var diffOpts []cloudstic.DiffOption
	if *a.g.verbose {
		diffOpts = append(diffOpts, cloudstic.WithDiffVerbose())
	}
	return diffOpts
}

func (r *runner) printDiffResult(result *cloudstic.DiffResult) {
	_, _ = fmt.Fprintf(r.out, "Diffing %s vs %s\n", result.Ref1, result.Ref2)
	for _, c := range result.Changes {
		_, _ = fmt.Fprintf(r.out, "%s %s\n", c.Type, c.Path)
	}
}
