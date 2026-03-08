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
