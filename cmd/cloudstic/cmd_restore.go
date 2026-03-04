package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
)

type restoreArgs struct {
	g           *globalFlags
	output      string
	dryRun      bool
	pathFilter  string
	snapshotRef string
}

func parseRestoreArgs() *restoreArgs {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	a := &restoreArgs{}
	a.g = addGlobalFlags(fs)
	output := fs.String("output", "./restore.zip", "Output ZIP file path")
	dryRun := fs.Bool("dry-run", false, "Show what would be restored without writing the archive")
	pathFilter := fs.String("path", "", "Restore only the given file or subtree (e.g. Documents/report.pdf or Documents/)")
	mustParse(fs)
	a.output = *output
	a.dryRun = *dryRun
	a.pathFilter = *pathFilter
	a.snapshotRef = "latest"
	if fs.NArg() > 0 {
		a.snapshotRef = fs.Arg(0)
	}
	return a
}

func runRestore() {
	a := parseRestoreArgs()

	ctx := context.Background()

	client, err := a.g.openClient()
	if err != nil {
		fmt.Printf("Failed to init store: %v\n", err)
		os.Exit(1)
	}

	var restoreOpts []cloudstic.RestoreOption
	if a.dryRun {
		restoreOpts = append(restoreOpts, engine.WithRestoreDryRun())
	}
	if *a.g.verbose {
		restoreOpts = append(restoreOpts, engine.WithRestoreVerbose())
	}
	if a.pathFilter != "" {
		restoreOpts = append(restoreOpts, engine.WithRestorePath(a.pathFilter))
	}

	if a.dryRun {
		result, err := client.Restore(ctx, io.Discard, a.snapshotRef, restoreOpts...)
		if err != nil {
			fmt.Printf("Restore failed: %v\n", err)
			os.Exit(1)
		}
		printRestoreSummary(result, "")
		return
	}

	f, err := os.Create(a.output)
	if err != nil {
		fmt.Printf("Failed to create output file: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = f.Close() }()

	result, err := client.Restore(ctx, f, a.snapshotRef, restoreOpts...)
	if err != nil {
		_ = os.Remove(a.output)
		fmt.Printf("Restore failed: %v\n", err)
		os.Exit(1)
	}
	printRestoreSummary(result, a.output)
}

// printRestoreSummary prints the restore result to stdout.
func printRestoreSummary(result *engine.RestoreResult, output string) {
	if result.DryRun {
		fmt.Printf("\nRestore dry run complete. Snapshot: %s\n", result.SnapshotRef)
		fmt.Printf("  Files: %d, Dirs: %d\n", result.FilesWritten, result.DirsWritten)
		fmt.Printf("  Estimated size: %s\n", formatBytes(result.BytesWritten))
		return
	}
	fmt.Printf("\nRestore complete. Snapshot: %s\n", result.SnapshotRef)
	fmt.Printf("  Files: %d, Dirs: %d", result.FilesWritten, result.DirsWritten)
	if result.Errors > 0 {
		fmt.Printf(", Errors: %d", result.Errors)
	}
	fmt.Println()
	fmt.Printf("  Archive: %s (%s)\n", output, formatBytes(result.BytesWritten))
}
