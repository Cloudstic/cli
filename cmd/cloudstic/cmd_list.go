package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	cloudstic "github.com/cloudstic/cli"
)

type listArgs struct {
	g     *globalFlags
	group *bool
}

func parseListArgs() *listArgs {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	a := &listArgs{
		g:     addGlobalFlags(fs),
		group: fs.Bool("group", false, "Group snapshots by source identity"),
	}
	mustParse(fs)
	return a
}

func runList(r *runner, ctx context.Context) int {
	a := parseListArgs()
	if err := r.openClient(ctx, a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	listOpts := buildListOpts(a)

	result, err := r.client.List(ctx, listOpts...)
	if err != nil {
		return r.fail("List failed: %v", err)
	}
	if a.g.jsonEnabled() {
		return r.writeJSON(result)
	}
	printListResult(r.out, result, *a.group)
	return 0
}

func buildListOpts(a *listArgs) []cloudstic.ListOption {
	var listOpts []cloudstic.ListOption
	if a.g.verbose {
		listOpts = append(listOpts, cloudstic.WithListVerbose())
	}
	return listOpts
}

func printListResult(out io.Writer, result *cloudstic.ListResult, group bool) {
	_, _ = fmt.Fprintf(out, "%d snapshots\n", len(result.Snapshots))
	if group {
		renderGroupedSnapshotTables(out, result.Snapshots)
	} else {
		renderSnapshotTable(out, result.Snapshots, nil)
	}
}
