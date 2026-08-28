package main

import (
	"strings"
	"testing"
	"time"

	cloudstic "github.com/cloudstic/cli"
)

func sampleCopyResult() *cloudstic.CopyResult {
	source := &cloudstic.SourceInfo{Type: "local", Path: "/Users/ada/Documents"}
	return &cloudstic.CopyResult{
		Copied: []cloudstic.CopiedSnapshot{{
			SourceRef: "snapshot/410b18a2c0ffee00",
			DestRef:   "snapshot/a1b2c3d4deadbeef",
			Created:   "2026-04-01T18:15:03Z",
			Source:    source,
		}},
		Skipped: []cloudstic.SkippedSnapshot{{
			SourceRef: "snapshot/4e5d5487feedface",
			DestRef:   "snapshot/e5f6a7b8badc0de1",
			Created:   "2026-03-30T16:22:11Z",
			Source:    source,
			Reason:    "already copied",
		}},
		BytesRead:    4_509_715_660,
		BytesWritten: 1_181_116_006,
		Duration:     92 * time.Second,
	}
}

func TestPrintCopyResultGolden(t *testing.T) {
	// Timestamps render in local time, so pin the zone rather than the machine.
	t.Setenv("TZ", "UTC")
	time.Local = time.UTC

	var out strings.Builder
	printCopyBanner(&out, "local:/tmp/cloudstic-src", "s3:dest-bucket/prod", false)
	printCopyResult(&out, sampleCopyResult())

	assertGolden(t, "print_copy_result", out.String())
}

func TestPrintCopyResultDryRunGolden(t *testing.T) {
	t.Setenv("TZ", "UTC")
	time.Local = time.UTC

	result := sampleCopyResult()
	result.DryRun = true
	// A dry run cannot know destination refs: they name objects not yet written.
	result.Copied[0].DestRef = ""

	var out strings.Builder
	printCopyBanner(&out, "local:/tmp/cloudstic-src", "s3:dest-bucket/prod", true)
	printCopyResult(&out, result)

	assertGolden(t, "print_copy_dry_run", out.String())
}

func TestPrintCopyResultEmptySelection(t *testing.T) {
	var out strings.Builder
	printCopyResult(&out, &cloudstic.CopyResult{DryRun: true})

	if !strings.Contains(out.String(), "no snapshots selected") {
		t.Errorf("an empty selection should say so explicitly, got:\n%s", out.String())
	}
}

func TestPrintCopyBannerNamesBothRepositories(t *testing.T) {
	var out strings.Builder
	printCopyBanner(&out, "local:/seed", "s3:prod/backups", false)

	got := out.String()
	// The destination is the one thing a rerun cannot undo, so both ends have
	// to be visible before the first write.
	if !strings.Contains(got, "local:/seed") || !strings.Contains(got, "s3:prod/backups") {
		t.Errorf("banner does not name both repositories:\n%s", got)
	}
}

func TestPluralSnapshots(t *testing.T) {
	for n, want := range map[int]string{0: "0 snapshots", 1: "1 snapshot", 2: "2 snapshots"} {
		if got := plural(n, "snapshot"); got != want {
			t.Errorf("plural(%d, snapshot) = %q, want %q", n, got, want)
		}
	}
}
