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

func restoreFlagSpecs(a *restoreArgs) []flagSpec {
	return []flagSpec{
		stringFlag(&a.output, "output", "./restore.zip", "Output path (ZIP file for -format zip, directory for -format dir)",
			withPlaceholder("<path>"), withCompleter("_files")),
		stringFlag(&a.format, "format", "", "Restore format: zip or dir (default: auto from -output)",
			withPlaceholder("<zip|dir>")),
		boolFlag(&a.dryRun, "dry-run", false, "Show what would be restored without writing output"),
		stringFlag(&a.pathFilter, "path", "", "Restore only the given file or subtree (e.g. Documents/report.pdf or Documents/)",
			withPlaceholder("<path>")),
	}
}

func newRestoreFlagSet() (*flag.FlagSet, *restoreArgs) {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	a := &restoreArgs{}
	a.g = addGlobalFlags(fs, repoCommandGroups)
	bindFlags(fs, restoreFlagSpecs(a))
	return fs, a
}

func parseRestoreArgs(args []string) (*restoreArgs, error) {
	fs, a := newRestoreFlagSet()
	if err := parseFlags(fs, args); err != nil {
		return nil, err
	}
	a.format = strings.TrimSpace(strings.ToLower(a.format))
	a.snapshotRef = "latest"
	if fs.NArg() > 0 {
		a.snapshotRef = fs.Arg(0)
	}
	return a, nil
}

func runRestore(r *runner, ctx context.Context) int {
	a, err := parseRestoreArgs(r.args)
	if err != nil {
		return r.parseError(err)
	}
	format, err := resolveRestoreFormat(a.format, a.output)
	if err != nil {
		return r.fail("%v", err)
	}
	a.format = format

	if err := r.openClient(ctx, a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	restoreOpts := buildRestoreOpts(a)

	return execRestore(r, ctx, a, restoreOpts)
}

func execRestore(r *runner, ctx context.Context, a *restoreArgs, opts []cloudstic.RestoreOption) int {
	if a.dryRun {
		result, err := r.client.Restore(ctx, io.Discard, a.snapshotRef, opts...)
		if err != nil {
			return r.fail("Restore failed: %v", err)
		}
		if a.g.jsonEnabled() {
			return r.writeJSON(result)
		}
		printRestoreSummary(r.out, result, "")
		return 0
	}

	if a.format == "dir" {
		result, err := r.client.RestoreToDir(ctx, a.output, a.snapshotRef, opts...)
		if err != nil {
			return r.fail("Restore failed: %v", err)
		}
		if a.g.jsonEnabled() {
			return r.writeJSON(result)
		}
		printRestoreSummary(r.out, result, a.output)
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
	if a.g.jsonEnabled() {
		return r.writeJSON(result)
	}
	printRestoreSummary(r.out, result, a.output)
	return 0
}

func buildRestoreOpts(a *restoreArgs) []cloudstic.RestoreOption {
	var restoreOpts []cloudstic.RestoreOption
	if a.dryRun {
		restoreOpts = append(restoreOpts, engine.WithRestoreDryRun())
	}
	if a.g.verbose {
		restoreOpts = append(restoreOpts, engine.WithRestoreVerbose())
	}
	if a.pathFilter != "" {
		restoreOpts = append(restoreOpts, engine.WithRestorePath(a.pathFilter))
	}
	return restoreOpts
}

func printRestoreSummary(out io.Writer, result *engine.RestoreResult, output string) {
	if result.DryRun {
		_, _ = fmt.Fprintf(out, "\nRestore dry run complete. Snapshot: %s\n", result.SnapshotRef)
		_, _ = fmt.Fprintf(out, "  Files: %d, Dirs: %d\n", result.FilesWritten, result.DirsWritten)
		_, _ = fmt.Fprintf(out, "  Estimated size: %s\n", formatBytes(result.BytesWritten))
		return
	}
	_, _ = fmt.Fprintf(out, "\nRestore complete. Snapshot: %s\n", result.SnapshotRef)
	_, _ = fmt.Fprintf(out, "  Files: %d, Dirs: %d", result.FilesWritten, result.DirsWritten)
	if result.Warnings > 0 {
		_, _ = fmt.Fprintf(out, ", Warnings: %d", result.Warnings)
	}
	if result.Errors > 0 {
		_, _ = fmt.Fprintf(out, ", Errors: %d", result.Errors)
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "  Output: %s (%s)\n", output, formatBytes(result.BytesWritten))
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
