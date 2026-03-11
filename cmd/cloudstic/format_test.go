package main

import (
	"strings"
	"testing"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
)

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TiB"},
	}
	for _, c := range cases {
		got := formatBytes(c.in)
		if got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRenderSnapshotTable_Empty(t *testing.T) {
	r := &runner{out: &strings.Builder{}, errOut: &strings.Builder{}}
	// Should not panic with empty entries.
	r.renderSnapshotTable(nil, nil)
}

func TestRenderSnapshotTable_WithEntries(t *testing.T) {
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}

	entries := []engine.SnapshotEntry{
		{
			Ref: "snapshot/abc123",
			Snap: core.Snapshot{
				Seq:     1,
				Created: "2024-01-01T00:00:00Z",
				Tags:    []string{"daily"},
				Source: &core.SourceInfo{
					Type:    "local",
					Account: "user",
					Path:    "/home/user",
				},
			},
		},
	}
	r.renderSnapshotTable(entries, nil)

	got := out.String()
	if !strings.Contains(got, "abc123") {
		t.Errorf("expected output to contain snapshot hash, got:\n%s", got)
	}
	if !strings.Contains(got, "local") {
		t.Errorf("expected output to contain source type, got:\n%s", got)
	}
}

func TestRenderSnapshotTable_WithReasons(t *testing.T) {
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}

	ref := "snapshot/def456"
	entries := []engine.SnapshotEntry{
		{Ref: ref, Snap: core.Snapshot{Seq: 1, Created: time.Now().Format(time.RFC3339)}},
	}
	reasons := map[string]string{ref: "keep-last"}
	r.renderSnapshotTable(entries, reasons)

	got := out.String()
	if !strings.Contains(got, "keep-last") {
		t.Errorf("expected reasons column in output, got:\n%s", got)
	}
	if !strings.Contains(strings.ToUpper(got), "REASONS") {
		t.Errorf("expected 'REASONS' header in output, got:\n%s", got)
	}
}

func TestRenderSnapshotTable_VolumeLabel(t *testing.T) {
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}

	entries := []engine.SnapshotEntry{
		{
			Ref: "snapshot/abc",
			Snap: core.Snapshot{
				Seq:     1,
				Created: "2024-01-01T00:00:00Z",
				Source: &core.SourceInfo{
					Type:        "gdrive",
					Account:     "user@gmail.com",
					Path:        "/",
					VolumeLabel: "My Drive",
				},
			},
		},
	}
	r.renderSnapshotTable(entries, nil)

	got := out.String()
	if !strings.Contains(got, "gdrive (My Drive)") {
		t.Errorf("expected 'gdrive (My Drive)' in Source column, got:\n%s", got)
	}
	if !strings.Contains(got, "user@gmail.com") {
		t.Errorf("expected account in Account column, got:\n%s", got)
	}
}

func TestSourceGroupKey(t *testing.T) {
	tests := []struct {
		name   string
		source *core.SourceInfo
		want   string
	}{
		{"nil source", nil, ""},
		{
			"local with UUID",
			&core.SourceInfo{Type: "local", Account: "host", Path: ".", VolumeUUID: "UUID-1"},
			"local\x00UUID-1\x00.",
		},
		{
			"gdrive no UUID",
			&core.SourceInfo{Type: "gdrive", Account: "user@gmail.com", Path: "/"},
			"gdrive\x00user@gmail.com\x00/",
		},
		{
			"shared drive with UUID",
			&core.SourceInfo{Type: "gdrive", Account: "user@gmail.com", Path: "/", VolumeUUID: "drive-123"},
			"gdrive\x00drive-123\x00/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sourceGroupKey(tt.source)
			if got != tt.want {
				t.Errorf("sourceGroupKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSourceGroupLabel(t *testing.T) {
	tests := []struct {
		name   string
		source *core.SourceInfo
		want   string
	}{
		{"nil source", nil, "(unknown source)"},
		{
			"local no label",
			&core.SourceInfo{Type: "local", Account: "macbook", Path: "/data"},
			"local · macbook · /data",
		},
		{
			"gdrive with label",
			&core.SourceInfo{Type: "gdrive", Account: "user@gmail.com", Path: "/", VolumeLabel: "My Drive"},
			"gdrive (My Drive) · user@gmail.com · /",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sourceGroupLabel(tt.source)
			if got != tt.want {
				t.Errorf("sourceGroupLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderGroupedSnapshotTables(t *testing.T) {
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}

	entries := []engine.SnapshotEntry{
		{
			Ref: "snapshot/aaa",
			Snap: core.Snapshot{
				Seq: 1, Created: "2024-01-01T00:00:00Z",
				Source: &core.SourceInfo{Type: "gdrive", Account: "user@gmail.com", Path: "/", VolumeLabel: "My Drive"},
			},
		},
		{
			Ref: "snapshot/bbb",
			Snap: core.Snapshot{
				Seq: 2, Created: "2024-01-02T00:00:00Z",
				Source: &core.SourceInfo{Type: "local", Account: "macbook", Path: "."},
			},
		},
		{
			Ref: "snapshot/ccc",
			Snap: core.Snapshot{
				Seq: 3, Created: "2024-01-03T00:00:00Z",
				Source: &core.SourceInfo{Type: "gdrive", Account: "user@gmail.com", Path: "/", VolumeLabel: "My Drive"},
			},
		},
	}

	r.renderGroupedSnapshotTables(entries)

	got := out.String()
	// Should have two group headers.
	if strings.Count(got, "──") != 2 {
		t.Errorf("expected 2 group headers, got:\n%s", got)
	}
	if !strings.Contains(got, "gdrive (My Drive) · user@gmail.com · / (2 snapshots)") {
		t.Errorf("expected gdrive group header with 2 snapshots, got:\n%s", got)
	}
	if !strings.Contains(got, "local · macbook · . (1 snapshots)") {
		t.Errorf("expected local group header with 1 snapshot, got:\n%s", got)
	}
	// Both snapshot hashes should appear.
	if !strings.Contains(got, "aaa") || !strings.Contains(got, "bbb") || !strings.Contains(got, "ccc") {
		t.Errorf("expected all snapshot hashes in output, got:\n%s", got)
	}
}
