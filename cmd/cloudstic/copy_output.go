package main

import (
	"fmt"
	"io"
	"time"

	cloudstic "github.com/cloudstic/cli"
)

// printCopyBanner names both repositories before anything is written.
//
// A copy can write a great deal of history into the wrong place, and the
// destination is the one thing an operator cannot undo by rerunning. Printing
// both ends first makes a mistyped -store visible while it is still cheap.
func printCopyBanner(out io.Writer, sourceURI, destURI string, dryRun bool) {
	_, _ = fmt.Fprintf(out, "copying from %s\n", sourceURI)
	_, _ = fmt.Fprintf(out, "            to %s\n", destURI)
	if dryRun {
		_, _ = fmt.Fprintln(out, "dry run: nothing will be written")
	}
}

// printCopySelection reports what a dry run would do.
func printCopySelection(out io.Writer, result *cloudstic.CopyResult) {
	if len(result.Copied) == 0 && len(result.Skipped) == 0 {
		_, _ = fmt.Fprintln(out, "\nno snapshots selected")
		return
	}
	_, _ = fmt.Fprintln(out)
	for _, snap := range result.Skipped {
		_, _ = fmt.Fprintf(out, "skipping snapshot %s, already copied as snapshot %s\n",
			shortSnapshotRef(snap.SourceRef), shortSnapshotRef(snap.DestRef))
	}
	for _, snap := range result.Copied {
		_, _ = fmt.Fprintf(out, "would copy snapshot %s%s\n",
			shortSnapshotRef(snap.SourceRef), copySnapshotSuffix(snap.Source, snap.Created))
	}
}

// printCopyResult renders the outcome of a completed run.
func printCopyResult(out io.Writer, result *cloudstic.CopyResult) {
	if result.DryRun {
		printCopyPending(out, result)
		return
	}

	_, _ = fmt.Fprintln(out)
	for _, snap := range result.Skipped {
		_, _ = fmt.Fprintf(out, "skipping snapshot %s, already copied as snapshot %s\n",
			shortSnapshotRef(snap.SourceRef), shortSnapshotRef(snap.DestRef))
	}
	for _, snap := range result.Copied {
		_, _ = fmt.Fprintf(out, "snapshot %s%s\n",
			shortSnapshotRef(snap.SourceRef), copySnapshotSuffix(snap.Source, snap.Created))
		_, _ = fmt.Fprintf(out, "snapshot %s saved\n", shortSnapshotRef(snap.DestRef))
	}

	_, _ = fmt.Fprintf(out, "\ncopied %s, skipped %s (read %s, wrote %s) in %s\n",
		plural(len(result.Copied), "snapshot"),
		plural(len(result.Skipped), "snapshot"),
		formatBytes(result.BytesRead),
		formatBytes(result.BytesWritten),
		result.Duration.Round(time.Millisecond),
	)
}

// printCopyPending is the dry-run summary: what a real run would do.
func printCopyPending(out io.Writer, result *cloudstic.CopyResult) {
	printCopySelection(out, result)
	if len(result.Copied) == 0 && len(result.Skipped) == 0 {
		return
	}
	_, _ = fmt.Fprintf(out, "\nwould copy %s, skip %s\n",
		plural(len(result.Copied), "snapshot"), plural(len(result.Skipped), "snapshot"))
}

func copySnapshotSuffix(source *cloudstic.SourceInfo, created string) string {
	label := ""
	if source != nil {
		if source.Path != "" {
			label = fmt.Sprintf(" of [%s:%s]", source.Type, source.Path)
		} else {
			label = fmt.Sprintf(" of [%s]", source.Type)
		}
	}
	if at, err := time.Parse(time.RFC3339, created); err == nil {
		return label + " at " + at.Local().Format("2006-01-02 15:04:05 -0700")
	}
	return label
}
