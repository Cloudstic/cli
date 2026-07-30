package main

import (
	"context"
	"fmt"
	"io"

	cloudstic "github.com/cloudstic/cli"
)

type listArgs struct {
	*globalFlags
	group bool
}

func declareListArgs(g *globalFlags) (*listArgs, commandInput) {
	a := &listArgs{globalFlags: g}
	return a, commandInput{flags: []flagSpec{
		boolFlag(&a.group, "group", false, "Group snapshots by source identity"),
	}}
}

func runList(r *runner, ctx context.Context, a *listArgs) int {
	cfg, err := resolveClientConfig(a.globalFlags)
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}
	if err := r.openClient(ctx, cfg); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	listOpts := buildListOpts(a)

	result, err := r.client.List(ctx, listOpts...)
	if err != nil {
		return r.fail("List failed: %v", err)
	}
	if a.jsonEnabled() {
		return r.writeJSON(result)
	}
	printListResult(r.out, result, a.group)
	return 0
}

func buildListOpts(a *listArgs) []cloudstic.ListOption {
	var listOpts []cloudstic.ListOption
	if a.verbose {
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

// listCommand declares the `list` command.
func listCommand() command {
	return leaf("list", "List all backup snapshots in the repository",
		repoCommandGroups, declareListArgs, runList)
}
