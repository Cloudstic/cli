package main

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/engine"
)

func TestInitSource_Local_ExtendedOptions(t *testing.T) {
	tmpDir := t.TempDir()
	a := &backupArgs{
		skipMode:        true,
		skipFlags:       true,
		skipXattrs:      true,
		xattrNamespaces: "user.,com.apple.",
	}
	g := &globalFlags{}

	src, err := initSource(context.Background(), initSourceOptions{sourceURI: "local:" + tmpDir, skipMode: a.skipMode, skipFlags: a.skipFlags, skipXattrs: a.skipXattrs, xattrNamespaces: a.xattrNamespaces, globalFlags: g})
	if err != nil {
		t.Fatalf("initSource failed: %v", err)
	}
	if src == nil {
		t.Fatal("expected non-nil source")
	}

	// Verify info reflects local source.
	info := src.Info()
	if info.Type != "local" {
		t.Errorf("expected source type 'local', got %q", info.Type)
	}
}

func TestInitSource_Local_NoExtendedOptions(t *testing.T) {
	tmpDir := t.TempDir()
	a := &backupArgs{}
	g := &globalFlags{}

	src, err := initSource(context.Background(), initSourceOptions{sourceURI: "local:" + tmpDir, skipMode: a.skipMode, skipFlags: a.skipFlags, skipXattrs: a.skipXattrs, xattrNamespaces: a.xattrNamespaces, globalFlags: g})
	if err != nil {
		t.Fatalf("initSource failed: %v", err)
	}
	if src == nil {
		t.Fatal("expected non-nil source")
	}
}

func TestInitSource_Local_VolumeUUID(t *testing.T) {
	tmpDir := t.TempDir()
	a := &backupArgs{}
	g := &globalFlags{}

	src, err := initSource(context.Background(), initSourceOptions{sourceURI: "local:" + tmpDir, volumeUUID: "test-uuid-123", skipMode: a.skipMode, skipFlags: a.skipFlags, skipXattrs: a.skipXattrs, xattrNamespaces: a.xattrNamespaces, globalFlags: g})
	if err != nil {
		t.Fatalf("initSource failed: %v", err)
	}
	info := src.Info()
	if info.Identity != "test-uuid-123" {
		t.Errorf("expected Identity 'test-uuid-123', got %q", info.Identity)
	}
}

func TestInitSource_Local_XattrNamespacesParsing(t *testing.T) {
	tmpDir := t.TempDir()
	a := &backupArgs{
		xattrNamespaces: "user.,com.apple.",
	}
	g := &globalFlags{}

	src, err := initSource(context.Background(), initSourceOptions{sourceURI: "local:" + tmpDir, skipMode: a.skipMode, skipFlags: a.skipFlags, skipXattrs: a.skipXattrs, xattrNamespaces: a.xattrNamespaces, globalFlags: g})
	if err != nil {
		t.Fatalf("initSource failed: %v", err)
	}
	if src == nil {
		t.Fatal("expected non-nil source")
	}
}

func TestInitSource_UnsupportedType(t *testing.T) {
	a := &backupArgs{}
	g := &globalFlags{}

	_, err := initSource(context.Background(), initSourceOptions{sourceURI: "invalid-source:/", skipMode: a.skipMode, skipFlags: a.skipFlags, skipXattrs: a.skipXattrs, xattrNamespaces: a.xattrNamespaces, globalFlags: g})
	if err == nil {
		t.Fatal("expected error for unsupported source type")
	}
	if !strings.Contains(err.Error(), "unknown source scheme") {
		t.Errorf("expected 'unknown source scheme' error, got: %v", err)
	}
}

func TestParseXattrNamespacePrefixes(t *testing.T) {
	got := parseXattrNamespacePrefixes("user., com.apple., ,security.,")
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
	printUsage()
}

func TestBuildBackupOpts_IgnoreEmptySnapshot(t *testing.T) {
	a := &backupArgs{ignoreEmpty: true, g: newTestGlobalFlags()}
	opts := buildBackupOpts(a, nil)
	if len(opts) != 1 {
		t.Fatalf("len(opts)=%d want 1", len(opts))
	}
}

func TestPrintBackupSummary_EmptySnapshotIgnored(t *testing.T) {
	var out strings.Builder
	r := &runner{out: &out}

	r.printBackupSummary(&engine.RunResult{
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
