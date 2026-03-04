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
		fmt.Println("Usage: cloudstic diff [options] <snapshot_id1> <snapshot_id2>")
		fmt.Println("       cloudstic diff [options] <snapshot_id1> latest")
		os.Exit(1)
	}
	a.snap1 = fs.Arg(0)
	a.snap2 = fs.Arg(1)
	return a
}

func runDiff() {
	a := parseDiffArgs()

	ctx := context.Background()

	client, err := a.g.openClient()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}
	var diffOpts []cloudstic.DiffOption
	if *a.g.verbose {
		diffOpts = append(diffOpts, cloudstic.WithDiffVerbose())
	}
	result, err := client.Diff(ctx, a.snap1, a.snap2, diffOpts...)
	if err != nil {
		fmt.Printf("Diff failed: %v\n", err)
		os.Exit(1)
	}

	printDiffResult(result)
}

// printDiffResult prints the diff header and per-file changes to stdout.
func printDiffResult(result *cloudstic.DiffResult) {
	fmt.Printf("Diffing %s vs %s\n", result.Ref1, result.Ref2)
	for _, c := range result.Changes {
		fmt.Printf("%s %s\n", c.Type, c.Path)
	}
}
