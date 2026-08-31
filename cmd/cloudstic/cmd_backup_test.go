package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestSplitCommaList(t *testing.T) {
	got := splitCommaList("user., com.apple., ,security.,")
	want := []string{"user.", "com.apple.", "security."}
	if len(got) != len(want) {
		t.Fatalf("len=%d want=%d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want=%q", i, got[i], want[i])
		}
	}
}

func TestPrintUsage_Smoke(t *testing.T) {
	// Verify printUsage doesn't panic.
	printUsage(io.Discard)
}

func TestRunBackup_JSON(t *testing.T) {
	tmpDir := t.TempDir()
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		backupResult: &cloudstic.BackupResult{
			SnapshotHash: "abc123",
			SnapshotRef:  "snapshot/abc123",
			FilesNew:     3,
		},
	}}

	args := []string{"-source", "local:" + tmpDir, "-json"}
	if code := backupCommand().execute(r.withArgs(args), context.Background(), "backup"); code != 0 {
		t.Fatalf("runBackup() exit = %d, want 0", code)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("json unmarshal: %v\noutput:\n%s", err, out.String())
	}
	if got["SnapshotHash"] != "abc123" {
		t.Fatalf("expected SnapshotHash=abc123 in JSON output, got: %v", got)
	}
}

func TestPrintBackupSummary_EmptySnapshotIgnored(t *testing.T) {
	var out strings.Builder
	r := &runner{out: &out}

	printBackupSummary(r.out, &cloudstic.BackupResult{
		Root:                 "node/abc",
		FilesUnmodified:      1,
		Duration:             2,
		EmptySnapshotIgnored: true,
	})

	got := out.String()
	if !strings.Contains(got, "No new snapshot created; nothing changed") {
		t.Fatalf("missing empty snapshot message:\n%s", got)
	}
	if strings.Contains(got, "saved") {
		t.Fatalf("unexpected snapshot saved line:\n%s", got)
	}
}
