package main

import (
	"context"
	"flag"
	"fmt"
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

func parseLsArgs() *lsArgs {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	a := &lsArgs{}
	a.g = addGlobalFlags(fs)
	mustParse(fs)
	a.snapshotID = "latest"
	if fs.NArg() > 0 {
		a.snapshotID = fs.Arg(0)
	}
	return a
}

func (r *runner) runLsSnapshot(ctx context.Context) int {
	a := parseLsArgs()
	if err := r.openClient(ctx, a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	start := time.Now()
	lsOpts := buildLsOpts(a)

	result, err := r.client.LsSnapshot(context.Background(), a.snapshotID, lsOpts...)
	if err != nil {
		return r.fail("Ls failed: %v", err)
	}
	r.printLsResult(result, time.Since(start))
	return 0
}

func buildLsOpts(a *lsArgs) []cloudstic.LsSnapshotOption {
	var lsOpts []cloudstic.LsSnapshotOption
	if *a.g.verbose {
		lsOpts = append(lsOpts, cloudstic.WithLsVerbose())
	}
	return lsOpts
}

func (r *runner) printLsResult(result *engine.LsSnapshotResult, elapsed time.Duration) {
	_, _ = fmt.Fprintf(r.out, "Listing files for snapshot: %s (Created: %s)\n", result.Ref, result.Snapshot.Created)
	r.renderSnapshotTree(result)
	_, _ = fmt.Fprintf(r.out, "\n%d entries listed in %s\n", len(result.RefToMeta), elapsed.Round(time.Millisecond))
}

func (r *runner) renderSnapshotTree(result *engine.LsSnapshotResult) {
	l := list.NewWriter()
	l.SetOutputMirror(r.out)
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
