package main

import (
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestPrintRestoreSummary_Normal(t *testing.T) {
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}

	r.printRestoreSummary(&cloudstic.RestoreResult{
		SnapshotRef:  "abc123",
		FilesWritten: 42,
		DirsWritten:  7,
		BytesWritten: 2 * 1024 * 1024,
		Errors:       0,
		DryRun:       false,
	}, "/tmp/restore.zip")

	got := out.String()
	for _, want := range []string{"Restore complete.", "abc123", "42", "7", "2.0 MiB", "/tmp/restore.zip"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output, got:\n%s", want, got)
		}
	}
}

func TestPrintRestoreSummary_WithErrors(t *testing.T) {
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}

	r.printRestoreSummary(&cloudstic.RestoreResult{
		SnapshotRef:  "xyz",
		FilesWritten: 10,
		DirsWritten:  2,
		BytesWritten: 512,
		Errors:       3,
	}, "out.zip")

	got := out.String()
	if !strings.Contains(got, "Errors: 3") {
		t.Errorf("expected 'Errors: 3' in output, got:\n%s", got)
	}
}

func TestPrintRestoreSummary_DryRun(t *testing.T) {
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}

	r.printRestoreSummary(&cloudstic.RestoreResult{
		SnapshotRef:  "dry123",
		FilesWritten: 5,
		DirsWritten:  1,
		BytesWritten: 1024,
		DryRun:       true,
	}, "")

	got := out.String()
	if !strings.Contains(got, "dry run complete") {
		t.Errorf("expected 'dry run complete', got:\n%s", got)
	}
	if !strings.Contains(got, "Estimated size") {
		t.Errorf("expected 'Estimated size', got:\n%s", got)
	}
	if strings.Contains(got, "Archive:") {
		t.Errorf("dry run should not show archive path, got:\n%s", got)
	}
}
