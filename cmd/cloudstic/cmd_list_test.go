package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
)

func TestRunList_Success(t *testing.T) {
	args := []string{}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		listResult: &cloudstic.ListResult{
			Snapshots: []engine.SnapshotEntry{
				{Ref: "snapshot/abc", Snap: core.Snapshot{Seq: 1, Created: "2024-01-01"}},
				{Ref: "snapshot/def", Snap: core.Snapshot{Seq: 2, Created: "2024-01-02"}},
			},
		},
	}}

	runList(r.withArgs(args), context.Background())

	got := out.String()
	if !strings.Contains(got, "2 snapshots") {
		t.Errorf("expected '2 snapshots' in output, got:\n%s", got)
	}
}

func TestRunList_JSON(t *testing.T) {
	args := []string{"-json"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		listResult: &cloudstic.ListResult{
			Snapshots: []engine.SnapshotEntry{
				{Ref: "snapshot/abc", Snap: core.Snapshot{Seq: 1, Created: "2024-01-01"}},
			},
		},
	}}

	if exit := runList(r.withArgs(args), context.Background()); exit != 0 {
		t.Fatalf("runList() exit = %d, want 0", exit)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("json unmarshal: %v\noutput:\n%s", err, out.String())
	}
	if _, ok := got["Snapshots"]; !ok {
		t.Fatalf("expected Snapshots key in JSON output, got: %v", got)
	}
}

func TestRunList_Empty(t *testing.T) {
	args := []string{}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		listResult: &cloudstic.ListResult{Snapshots: nil},
	}}

	runList(r.withArgs(args), context.Background())

	if !strings.Contains(out.String(), "0 snapshots") {
		t.Errorf("expected '0 snapshots', got: %s", out.String())
	}
}

func TestRunList_Group(t *testing.T) {
	args := []string{"-group"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		listResult: &cloudstic.ListResult{
			Snapshots: []engine.SnapshotEntry{
				{
					Ref: "snapshot/abc",
					Snap: core.Snapshot{
						Seq: 1, Created: "2024-01-01",
						Source: &core.SourceInfo{Type: "gdrive", Account: "a@b.com", Path: "/", DriveName: "My Drive"},
					},
				},
				{
					Ref: "snapshot/def",
					Snap: core.Snapshot{
						Seq: 2, Created: "2024-01-02",
						Source: &core.SourceInfo{Type: "local", Account: "host", Path: "/data"},
					},
				},
			},
		},
	}}

	runList(r.withArgs(args), context.Background())

	got := out.String()
	if !strings.Contains(got, "2 snapshots") {
		t.Errorf("expected '2 snapshots', got:\n%s", got)
	}
	if !strings.Contains(got, "──") {
		t.Errorf("expected group headers with ──, got:\n%s", got)
	}
}
