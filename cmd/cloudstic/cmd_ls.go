package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/jedib0t/go-pretty/v6/list"
)

type lsArgs struct {
	g          *globalFlags
	snapshotID string
}

func newLsFlagSet() (*flag.FlagSet, *lsArgs) {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	a := &lsArgs{}
	a.g = addGlobalFlags(fs, repoCommandGroups)
	return fs, a
}

func parseLsArgs(args []string) (*lsArgs, error) {
	fs, a := newLsFlagSet()
	if err := parseFlags(fs, args); err != nil {
		return nil, err
	}
	a.snapshotID = "latest"
	if fs.NArg() > 0 {
		a.snapshotID = fs.Arg(0)
	}
	return a, nil
}

func runLsSnapshot(r *runner, ctx context.Context) int {
	a, err := parseLsArgs(r.args)
	if err != nil {
		return r.parseError(err)
	}
	if err := r.openClient(ctx, a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	start := time.Now()
	lsOpts := buildLsOpts(a)

	result, err := r.client.LsSnapshot(ctx, a.snapshotID, lsOpts...)
	if err != nil {
		return r.fail("Ls failed: %v", err)
	}
	if a.g.jsonEnabled() {
		return r.writeJSON(result)
	}
	printLsResult(r.out, result, time.Since(start))
	return 0
}

func buildLsOpts(a *lsArgs) []cloudstic.LsSnapshotOption {
	var lsOpts []cloudstic.LsSnapshotOption
	if a.g.verbose {
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
