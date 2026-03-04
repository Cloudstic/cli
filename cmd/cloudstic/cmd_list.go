package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	cloudstic "github.com/cloudstic/cli"
)

type listArgs struct {
	g *globalFlags
}

func parseListArgs() *listArgs {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	a := &listArgs{g: addGlobalFlags(fs)}
	mustParse(fs)
	return a
}

func runList() {
	a := parseListArgs()

	ctx := context.Background()

	client, err := a.g.openClient()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}
	var listOpts []cloudstic.ListOption
	if *a.g.verbose {
		listOpts = append(listOpts, cloudstic.WithListVerbose())
	}
	result, err := client.List(ctx, listOpts...)
	if err != nil {
		fmt.Printf("List failed: %v\n", err)
		os.Exit(1)
	}

	printListResult(result)
}

// printListResult prints the snapshot count and table to stdout.
func printListResult(result *cloudstic.ListResult) {
	fmt.Printf("%d snapshots\n", len(result.Snapshots))
	renderSnapshotTable(result.Snapshots, nil)
}
