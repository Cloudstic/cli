package main

import (
	"os"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
)

func TestRunList_Success(t *testing.T) {
	os.Args = []string{"cloudstic", "list"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		listResult: &cloudstic.ListResult{
			Snapshots: []engine.SnapshotEntry{
				{Ref: "snapshot/abc", Snap: core.Snapshot{Seq: 1, Created: "2024-01-01"}},
				{Ref: "snapshot/def", Snap: core.Snapshot{Seq: 2, Created: "2024-01-02"}},
			},
		},
	}}

	r.runList()

	got := out.String()
	if !strings.Contains(got, "2 snapshots") {
		t.Errorf("expected '2 snapshots' in output, got:\n%s", got)
	}
}

func TestRunList_Empty(t *testing.T) {
	os.Args = []string{"cloudstic", "list"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		listResult: &cloudstic.ListResult{Snapshots: nil},
	}}

	r.runList()

	if !strings.Contains(out.String(), "0 snapshots") {
		t.Errorf("expected '0 snapshots', got: %s", out.String())
	}
}

func TestRunList_Group(t *testing.T) {
	os.Args = []string{"cloudstic", "list", "-group"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		listResult: &cloudstic.ListResult{
			Snapshots: []engine.SnapshotEntry{
				{
					Ref: "snapshot/abc",
					Snap: core.Snapshot{
						Seq: 1, Created: "2024-01-01",
						Source: &core.SourceInfo{Type: "gdrive", Account: "a@b.com", Path: "/", VolumeLabel: "My Drive"},
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

	r.runList()

	got := out.String()
	if !strings.Contains(got, "2 snapshots") {
		t.Errorf("expected '2 snapshots', got:\n%s", got)
	}
	if !strings.Contains(got, "──") {
		t.Errorf("expected group headers with ──, got:\n%s", got)
	}
}
