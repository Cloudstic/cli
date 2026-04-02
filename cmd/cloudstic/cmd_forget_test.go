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

func TestValidateForgetArgs_FilterOnlyEnablesPolicyMode(t *testing.T) {
	args := &forgetArgs{
		filterTags: []string{"daily"},
		hasFilters: true,
	}

	if err := validateForgetArgs(args); err != nil {
		t.Fatalf("validateForgetArgs returned error: %v", err)
	}
	if !args.hasPolicy {
		t.Fatal("expected filter-only forget args to enable policy mode")
	}
}

func TestValidateForgetArgs_RequiresSelection(t *testing.T) {
	err := validateForgetArgs(&forgetArgs{})
	if err == nil {
		t.Fatal("expected error for empty forget args")
	}
	if !strings.Contains(err.Error(), "specify either <snapshot_id>") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateForgetArgs_RejectsSnapshotIDWithPolicyOrFilters(t *testing.T) {
	tests := []struct {
		name string
		args *forgetArgs
		want string
	}{
		{
			name: "keep_last",
			args: &forgetArgs{snapshotID: "abc123", keepLast: 1},
			want: "-keep-last",
		},
		{
			name: "tag_filter",
			args: &forgetArgs{snapshotID: "abc123", filterTags: []string{"daily"}},
			want: "-tag",
		},
		{
			name: "group_by",
			args: &forgetArgs{snapshotID: "abc123", groupBySet: true},
			want: "-group-by",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateForgetArgs(tt.args)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error to mention %q, got: %v", tt.want, err)
			}
		})
	}
}

func TestValidateForgetArgs_RejectsSnapshotIDWithAllFilterKinds(t *testing.T) {
	args := &forgetArgs{
		snapshotID:    "abc123",
		filterSource:  "local",
		filterAccount: "host",
		filterPath:    "/docs",
		groupBySet:    true,
	}

	err := validateForgetArgs(args)
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"-source", "-account", "-path", "-group-by"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to mention %q, got: %v", want, err)
		}
	}
}

func TestParseForgetArgs_FilterOnlySourceSetsPolicyAndGrouping(t *testing.T) {
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	os.Args = []string{
		"cloudstic", "forget",
		"--source", "local:/docs",
		"--account", "workstation",
		"--group-by", "source,path",
	}

	args := parseForgetArgs()

	if !args.hasFilters {
		t.Fatal("expected parseForgetArgs to detect filters")
	}
	if !args.hasPolicy {
		t.Fatal("expected filter-only parse to enable policy mode")
	}
	if !args.groupBySet {
		t.Fatal("expected explicit group-by to be recorded")
	}
	if args.filterSource != "local" {
		t.Fatalf("filterSource = %q, want %q", args.filterSource, "local")
	}
	if args.filterPath != "/docs" {
		t.Fatalf("filterPath = %q, want %q", args.filterPath, "/docs")
	}
	if args.filterAccount != "workstation" {
		t.Fatalf("filterAccount = %q, want %q", args.filterAccount, "workstation")
	}
	if args.groupBy != "source,path" {
		t.Fatalf("groupBy = %q, want %q", args.groupBy, "source,path")
	}
}

func TestParseForgetArgs_BareSourceKeywordDoesNotSetFilterPath(t *testing.T) {
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	os.Args = []string{"cloudstic", "forget", "--source", "local"}

	args := parseForgetArgs()

	if args.filterSource != "local" {
		t.Fatalf("filterSource = %q, want %q", args.filterSource, "local")
	}
	if args.filterPath != "" {
		t.Fatalf("filterPath = %q, want empty", args.filterPath)
	}
	if !args.hasFilters || !args.hasPolicy {
		t.Fatalf("expected bare source filter to enable filter-only policy mode: %+v", args)
	}
}

func TestPrintForgetUsage(t *testing.T) {
	var out strings.Builder

	printForgetUsage(&out)

	got := out.String()
	for _, want := range []string{
		"Usage: cloudstic forget [options] <snapshot_id>",
		"cloudstic forget --keep-last n",
		"cloudstic forget --tag X [--tag Y]",
		"cloudstic forget --source local:./docs",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected usage output to contain %q, got:\n%s", want, got)
		}
	}
}
