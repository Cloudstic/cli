package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/jedib0t/go-pretty/v6/list"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
)

type lsArgs struct {
	*globalFlags
	snapshotID string
}

func declareLsArgs(g *globalFlags) (*lsArgs, commandInput) {
	a := &lsArgs{globalFlags: g}
	return a, commandInput{positionals: []positionalSpec{
		optionalPositional(&a.snapshotID, "snapshot ID", "latest"),
	}}
}

func runLsSnapshot(r *runner, ctx context.Context, a *lsArgs) int {
	cfg, err := resolveClientConfig(a.globalFlags)
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}
	if err := r.openClient(ctx, cfg); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	start := time.Now()
	lsOpts := buildLsOpts(a)

	result, err := r.client.LsSnapshot(ctx, a.snapshotID, lsOpts...)
	if err != nil {
		return r.fail("Ls failed: %v", err)
	}
	if a.jsonEnabled() {
		return r.writeJSON(result)
	}
	printLsResult(r.out, result, time.Since(start))
	return 0
}

func buildLsOpts(a *lsArgs) []cloudstic.LsSnapshotOption {
	var lsOpts []cloudstic.LsSnapshotOption
	if a.verbose {
		lsOpts = append(lsOpts, cloudstic.WithLsVerbose())
	}
	return lsOpts
}

func printLsResult(out io.Writer, result *engine.LsSnapshotResult, elapsed time.Duration) {
	_, _ = fmt.Fprintf(out, "Listing files for snapshot: %s (Created: %s)\n", result.Ref, result.Snapshot.Created)
	renderSnapshotTree(out, result)
	_, _ = fmt.Fprintf(out, "\n%d entries listed in %s\n", len(result.RefToMeta), elapsed.Round(time.Millisecond))
}

func renderSnapshotTree(out io.Writer, result *engine.LsSnapshotResult) {
	l := list.NewWriter()
	l.SetOutputMirror(out)
	for _, rootRef := range result.RootRefs {
		appendTreeNode(l, rootRef, result.RefToMeta, result.ChildRefs)
	}
	l.Render()
}

func appendTreeNode(l list.Writer, ref string, refToMeta map[string]core.FileMeta, children map[string][]string) {
	meta := refToMeta[ref]

	label := meta.Name
	if meta.Type == core.FileTypeFile {
		label += fmt.Sprintf(" (%s)", formatBytes(meta.Size))
	}
	l.AppendItem(label)

	kids := children[ref]
	if len(kids) == 0 {
		return
	}

	sort.Slice(kids, func(i, j int) bool {
		return refToMeta[kids[i]].Name < refToMeta[kids[j]].Name
	})

	l.Indent()
	for _, childRef := range kids {
		appendTreeNode(l, childRef, refToMeta, children)
	}
	l.UnIndent()
}

// lsCommand declares the `ls` command.
func lsCommand() command {
	return leaf("ls", "List files within a specific snapshot",
		repoCommandGroups, declareLsArgs, runLsSnapshot)
}
