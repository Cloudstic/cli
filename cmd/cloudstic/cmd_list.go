package main

import (
	"context"
	"flag"
	"fmt"

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

func (r *runner) runList() int {
	a := parseListArgs()
	if err := r.openClient(a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	listOpts := buildListOpts(a)

	result, err := r.client.List(context.Background(), listOpts...)
	if err != nil {
		return r.fail("List failed: %v", err)
	}
	r.printListResult(result, *a.group)
	return 0
}

func buildListOpts(a *listArgs) []cloudstic.ListOption {
	var listOpts []cloudstic.ListOption
	if *a.g.verbose {
		listOpts = append(listOpts, cloudstic.WithListVerbose())
	}
	return listOpts
}

func (r *runner) printListResult(result *cloudstic.ListResult, group bool) {
	_, _ = fmt.Fprintf(r.out, "%d snapshots\n", len(result.Snapshots))
	if group {
		r.renderGroupedSnapshotTables(result.Snapshots)
	} else {
		r.renderSnapshotTable(result.Snapshots, nil)
	}
}
