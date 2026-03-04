package main

import (
	"context"
	"flag"
	"fmt"
	"os"
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

func runLsSnapshot() {
	a := parseLsArgs()

	ctx := context.Background()

	client, err := a.g.openClient()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}
	start := time.Now()
	var lsOpts []cloudstic.LsSnapshotOption
	if *a.g.verbose {
		lsOpts = append(lsOpts, cloudstic.WithLsVerbose())
	}
	result, err := client.LsSnapshot(ctx, a.snapshotID, lsOpts...)
	if err != nil {
		fmt.Printf("Ls failed: %v\n", err)
		os.Exit(1)
	}

	printLsResult(result, time.Since(start))
}

// printLsResult prints the snapshot header, file tree, and entry count to stdout.
func printLsResult(result *engine.LsSnapshotResult, elapsed time.Duration) {
	fmt.Printf("Listing files for snapshot: %s (Created: %s)\n", result.Ref, result.Snapshot.Created)
	renderSnapshotTree(result)
	fmt.Printf("\n%d entries listed in %s\n", len(result.RefToMeta), elapsed.Round(time.Millisecond))
}

func renderSnapshotTree(result *engine.LsSnapshotResult) {
	l := list.NewWriter()
	l.SetOutputMirror(os.Stdout)
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
