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
	for _, want := range []string{"Restore complete.", "abc123", "42", "7", "2.0 MiB", "Output:", "/tmp/restore.zip"} {
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

func TestPrintRestoreSummary_WithWarnings(t *testing.T) {
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}

	r.printRestoreSummary(&cloudstic.RestoreResult{
		SnapshotRef:  "warn",
		FilesWritten: 1,
		DirsWritten:  1,
		BytesWritten: 64,
		Warnings:     2,
	}, "out")

	if !strings.Contains(out.String(), "Warnings: 2") {
		t.Fatalf("expected warnings in output, got:\n%s", out.String())
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
	if strings.Contains(got, "Output:") {
		t.Errorf("dry run should not show output path, got:\n%s", got)
	}
}

func TestResolveRestoreFormat(t *testing.T) {
	tests := []struct {
		name      string
		format    string
		output    string
		want      string
		wantError bool
	}{
		{name: "explicit zip", format: "zip", output: "ignored", want: "zip"},
		{name: "explicit dir", format: "dir", output: "ignored", want: "dir"},
		{name: "invalid explicit", format: "tar", output: "out.tar", wantError: true},
		{name: "empty output", format: "", output: "   ", wantError: true},
		{name: "auto zip", format: "", output: "out.zip", want: "zip"},
		{name: "auto zip uppercase", format: "", output: "OUT.ZIP", want: "zip"},
		{name: "auto dir", format: "", output: "./restored", want: "dir"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveRestoreFormat(tc.format, tc.output)
			if tc.wantError {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRestoreFormat: %v", err)
			}
			if got != tc.want {
				t.Fatalf("format=%q want=%q", got, tc.want)
			}
		})
	}
}
