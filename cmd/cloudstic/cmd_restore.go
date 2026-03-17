package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
)

type restoreArgs struct {
	g           *globalFlags
	output      string
	format      string
	dryRun      bool
	pathFilter  string
	snapshotRef string
}

func parseRestoreArgs() *restoreArgs {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	a := &restoreArgs{}
	a.g = addGlobalFlags(fs)
	output := fs.String("output", "./restore.zip", "Output path (ZIP file for -format zip, directory for -format dir)")
	format := fs.String("format", "", "Restore format: zip or dir (default: auto from -output)")
	dryRun := fs.Bool("dry-run", false, "Show what would be restored without writing output")
	pathFilter := fs.String("path", "", "Restore only the given file or subtree (e.g. Documents/report.pdf or Documents/)")
	mustParse(fs)
	a.output = *output
	a.format = strings.TrimSpace(strings.ToLower(*format))
	a.dryRun = *dryRun
	a.pathFilter = *pathFilter
	a.snapshotRef = "latest"
	if fs.NArg() > 0 {
		a.snapshotRef = fs.Arg(0)
	}
	return a
}

func (r *runner) runRestore() int {
	a := parseRestoreArgs()
	format, err := resolveRestoreFormat(a.format, a.output)
	if err != nil {
		return r.fail("%v", err)
	}
	a.format = format

	if err := r.openClient(a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	restoreOpts := buildRestoreOpts(a)

	return r.execRestore(a, restoreOpts)
}

func (r *runner) execRestore(a *restoreArgs, opts []cloudstic.RestoreOption) int {
	ctx := context.Background()

	if a.dryRun {
		result, err := r.client.Restore(ctx, io.Discard, a.snapshotRef, opts...)
		if err != nil {
			return r.fail("Restore failed: %v", err)
		}
		r.printRestoreSummary(result, "")
		return 0
	}

	if a.format == "dir" {
		result, err := r.client.RestoreToDir(ctx, a.output, a.snapshotRef, opts...)
		if err != nil {
			return r.fail("Restore failed: %v", err)
		}
		r.printRestoreSummary(result, a.output)
		return 0
	}

	f, err := os.Create(a.output)
	if err != nil {
		return r.fail("Failed to create output file: %v", err)
	}
	defer func() { _ = f.Close() }()

	result, err := r.client.Restore(ctx, f, a.snapshotRef, opts...)
	if err != nil {
		_ = os.Remove(a.output)
		return r.fail("Restore failed: %v", err)
	}
	r.printRestoreSummary(result, a.output)
	return 0
}

func buildRestoreOpts(a *restoreArgs) []cloudstic.RestoreOption {
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
	return restoreOpts
}

func (r *runner) printRestoreSummary(result *engine.RestoreResult, output string) {
	if result.DryRun {
		_, _ = fmt.Fprintf(r.out, "\nRestore dry run complete. Snapshot: %s\n", result.SnapshotRef)
		_, _ = fmt.Fprintf(r.out, "  Files: %d, Dirs: %d\n", result.FilesWritten, result.DirsWritten)
		_, _ = fmt.Fprintf(r.out, "  Estimated size: %s\n", formatBytes(result.BytesWritten))
		return
	}
	_, _ = fmt.Fprintf(r.out, "\nRestore complete. Snapshot: %s\n", result.SnapshotRef)
	_, _ = fmt.Fprintf(r.out, "  Files: %d, Dirs: %d", result.FilesWritten, result.DirsWritten)
	if result.Warnings > 0 {
		_, _ = fmt.Fprintf(r.out, ", Warnings: %d", result.Warnings)
	}
	if result.Errors > 0 {
		_, _ = fmt.Fprintf(r.out, ", Errors: %d", result.Errors)
	}
	_, _ = fmt.Fprintln(r.out)
	_, _ = fmt.Fprintf(r.out, "  Output: %s (%s)\n", output, formatBytes(result.BytesWritten))
}

func resolveRestoreFormat(explicitFormat, output string) (string, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return "", fmt.Errorf("-output cannot be empty")
	}
	if explicitFormat != "" {
		switch explicitFormat {
		case "zip", "dir":
			return explicitFormat, nil
		default:
			return "", fmt.Errorf("invalid -format %q: expected zip or dir", explicitFormat)
		}
	}
	if strings.HasSuffix(strings.ToLower(output), ".zip") {
		return "zip", nil
	}
	return "dir", nil
}
