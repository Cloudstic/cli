package main

import (
	"context"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
)

func TestRunForget_SingleSnapshot(t *testing.T) {
	args := []string{"abc123"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		forgetResult: &cloudstic.ForgetResult{Prune: nil},
	}}

	forgetCommand().execute(r.withArgs(args), context.Background(), "forget")

	if !strings.Contains(out.String(), "Snapshot removed.") {
		t.Errorf("expected 'Snapshot removed.', got:\n%s", out.String())
	}
}

func TestRunForget_SingleSnapshot_WithPruneResult(t *testing.T) {
	args := []string{"--prune", "abc123"}
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

	forgetCommand().execute(r.withArgs(args), context.Background(), "forget")

	got := out.String()
	if !strings.Contains(got, "Snapshot removed.") {
		t.Errorf("expected 'Snapshot removed.', got:\n%s", got)
	}
	if !strings.Contains(got, "Prune complete.") {
		t.Errorf("expected prune stats, got:\n%s", got)
	}
}

func TestRunForget_Policy_NoRemove(t *testing.T) {
	args := []string{"--keep-last", "1"}
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

	forgetCommand().execute(r.withArgs(args), context.Background(), "forget")

	got := out.String()
	if !strings.Contains(got, "No snapshots to remove") {
		t.Errorf("expected 'No snapshots to remove', got:\n%s", got)
	}
}

func TestRunForget_Policy_WithRemoval(t *testing.T) {
	args := []string{"--keep-last", "1"}
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

	forgetCommand().execute(r.withArgs(args), context.Background(), "forget")

	got := out.String()
	if !strings.Contains(got, "1 snapshots have been removed") {
		t.Errorf("expected removal count, got:\n%s", got)
	}
}

func TestRunForget_Policy_DryRun(t *testing.T) {
	args := []string{"--keep-last", "1", "--dry-run"}
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

	forgetCommand().execute(r.withArgs(args), context.Background(), "forget")

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

	args := []string{"--source", "local:/docs",
		"--account", "workstation",
		"--group-by", "source,path",
	}

	parsed, err := parseInto("forget", repoCommandGroups, declareForgetArgs, args)
	if err != nil {
		t.Fatalf("parseForgetArgs() error = %v", err)
	}
	if err := prepareForgetArgs(parsed); err != nil {
		t.Fatalf("prepareForgetArgs() error = %v", err)
	}

	if !parsed.hasFilters {
		t.Fatal("expected parseForgetArgs to detect filters")
	}
	if !parsed.hasPolicy {
		t.Fatal("expected filter-only parse to enable policy mode")
	}
	if !parsed.groupBySet {
		t.Fatal("expected explicit group-by to be recorded")
	}
	if parsed.filterSource != "local" {
		t.Fatalf("filterSource = %q, want %q", parsed.filterSource, "local")
	}
	if parsed.filterPath != "/docs" {
		t.Fatalf("filterPath = %q, want %q", parsed.filterPath, "/docs")
	}
	if parsed.filterAccount != "workstation" {
		t.Fatalf("filterAccount = %q, want %q", parsed.filterAccount, "workstation")
	}
	if parsed.groupBy != "source,path" {
		t.Fatalf("groupBy = %q, want %q", parsed.groupBy, "source,path")
	}
}

func TestParseForgetArgs_BareSourceKeywordDoesNotSetFilterPath(t *testing.T) {

	args := []string{"--source", "local"}

	parsed, err := parseInto("forget", repoCommandGroups, declareForgetArgs, args)
	if err != nil {
		t.Fatalf("parseForgetArgs() error = %v", err)
	}
	if err := prepareForgetArgs(parsed); err != nil {
		t.Fatalf("prepareForgetArgs() error = %v", err)
	}

	if parsed.filterSource != "local" {
		t.Fatalf("filterSource = %q, want %q", parsed.filterSource, "local")
	}
	if parsed.filterPath != "" {
		t.Fatalf("filterPath = %q, want empty", parsed.filterPath)
	}
	if !parsed.hasFilters || !parsed.hasPolicy {
		t.Fatalf("expected bare source filter to enable filter-only policy mode: %+v", parsed)
	}
}

func TestForgetValidationPrintsDerivedUsage(t *testing.T) {
	var out strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &out}
	if code := forgetCommand().execute(r, context.Background(), "forget"); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if want := "Usage: cloudstic forget [options] [snapshot_id]"; !strings.Contains(out.String(), want) {
		t.Fatalf("expected usage output to contain %q, got:\n%s", want, out.String())
	}
}
