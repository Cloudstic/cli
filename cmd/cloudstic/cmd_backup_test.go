package main

import (
	"context"
	"strings"
	"testing"
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

	src, err := initSource(context.Background(), "local:"+tmpDir, false, "", "", "", "", "", a.skipMode, a.skipFlags, a.skipXattrs, a.xattrNamespaces, g, nil)
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

	src, err := initSource(context.Background(), "local:"+tmpDir, false, "", "", "", "", "", a.skipMode, a.skipFlags, a.skipXattrs, a.xattrNamespaces, g, nil)
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

	src, err := initSource(context.Background(), "local:"+tmpDir, false, "test-uuid-123", "", "", "", "", a.skipMode, a.skipFlags, a.skipXattrs, a.xattrNamespaces, g, nil)
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

	src, err := initSource(context.Background(), "local:"+tmpDir, false, "", "", "", "", "", a.skipMode, a.skipFlags, a.skipXattrs, a.xattrNamespaces, g, nil)
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

	_, err := initSource(context.Background(), "invalid-source:/", false, "", "", "", "", "", a.skipMode, a.skipFlags, a.skipXattrs, a.xattrNamespaces, g, nil)
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
