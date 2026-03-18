package main

import (
	"context"
	"os"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
)

func TestRunForget_SingleSnapshot(t *testing.T) {
	os.Args = []string{"cloudstic", "forget", "abc123"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		forgetResult: &cloudstic.ForgetResult{Prune: nil},
	}}

	r.runForget(context.Background())

	if !strings.Contains(out.String(), "Snapshot removed.") {
		t.Errorf("expected 'Snapshot removed.', got:\n%s", out.String())
	}
}

func TestRunForget_SingleSnapshot_WithPruneResult(t *testing.T) {
	os.Args = []string{"cloudstic", "forget", "--prune", "abc123"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		forgetResult: &cloudstic.ForgetResult{
			Prune: &cloudstic.PruneResult{
				ObjectsScanned: 10,
				ObjectsDeleted: 2,
				BytesReclaimed: 1024,
			},
		},
	}}

	r.runForget(context.Background())

	got := out.String()
	if !strings.Contains(got, "Snapshot removed.") {
		t.Errorf("expected 'Snapshot removed.', got:\n%s", got)
	}
	if !strings.Contains(got, "Prune complete.") {
		t.Errorf("expected prune stats, got:\n%s", got)
	}
}

func TestRunForget_Policy_NoRemove(t *testing.T) {
	os.Args = []string{"cloudstic", "forget", "--keep-last", "1"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		policyResult: &cloudstic.PolicyResult{
			Groups: []engine.PolicyGroupResult{
				{
					Key:    engine.GroupKey{Source: "source", Account: "account", Path: "path"},
					Keep:   []engine.KeepReason{{Entry: engine.SnapshotEntry{Ref: "snapshot/keep1", Snap: core.Snapshot{Seq: 1}}, Reasons: []string{"keep-last"}}},
					Remove: nil,
				},
			},
		},
	}}

	r.runForget(context.Background())

	got := out.String()
	if !strings.Contains(got, "No snapshots to remove") {
		t.Errorf("expected 'No snapshots to remove', got:\n%s", got)
	}
}

func TestRunForget_Policy_WithRemoval(t *testing.T) {
	os.Args = []string{"cloudstic", "forget", "--keep-last", "1"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		policyResult: &cloudstic.PolicyResult{
			Groups: []engine.PolicyGroupResult{
				{
					Key:    engine.GroupKey{Source: "local", Account: "user"},
					Keep:   []engine.KeepReason{{Entry: engine.SnapshotEntry{Ref: "snapshot/keep1", Snap: core.Snapshot{Seq: 2}}, Reasons: []string{"keep-last"}}},
					Remove: []engine.SnapshotEntry{{Ref: "snapshot/old1", Snap: core.Snapshot{Seq: 1}}},
				},
			},
		},
	}}

	r.runForget(context.Background())

	got := out.String()
	if !strings.Contains(got, "1 snapshots have been removed") {
		t.Errorf("expected removal count, got:\n%s", got)
	}
}

func TestRunForget_Policy_DryRun(t *testing.T) {
	os.Args = []string{"cloudstic", "forget", "--keep-last", "1", "--dry-run"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		policyResult: &cloudstic.PolicyResult{
			Groups: []engine.PolicyGroupResult{
				{
					Key:    engine.GroupKey{Source: "local", Account: "user"},
					Remove: []engine.SnapshotEntry{{Ref: "snapshot/old1", Snap: core.Snapshot{Seq: 1}}},
				},
			},
		},
	}}

	r.runForget(context.Background())

	got := out.String()
	if !strings.Contains(got, "would remove") {
		t.Errorf("expected 'would remove' (dry run), got:\n%s", got)
	}
	if !strings.Contains(got, "dry run") {
		t.Errorf("expected 'dry run' in summary, got:\n%s", got)
	}
}
