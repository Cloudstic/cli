package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestRunDiff_Success(t *testing.T) {
	args := []string{"aaa", "bbb"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		diffResult: &cloudstic.DiffResult{
			Ref1: "snapshot/aaa",
			Ref2: "snapshot/bbb",
			Changes: []cloudstic.FileChange{
				{Type: "added", Path: "docs/readme.md"},
				{Type: "modified", Path: "src/main.go"},
				{Type: "removed", Path: "old/file.txt"},
			},
		},
	}}

	diffCommand().execute(r.withArgs(args), context.Background(), "diff")

	got := out.String()
	if !strings.Contains(got, "snapshot/aaa") {
		t.Errorf("expected ref1 in output, got:\n%s", got)
	}
	if !strings.Contains(got, "snapshot/bbb") {
		t.Errorf("expected ref2 in output, got:\n%s", got)
	}
	if !strings.Contains(got, "added docs/readme.md") {
		t.Errorf("expected added file in output, got:\n%s", got)
	}
	if !strings.Contains(got, "removed old/file.txt") {
		t.Errorf("expected removed file in output, got:\n%s", got)
	}
}

func TestRunDiff_JSON(t *testing.T) {
	args := []string{"-json", "aaa", "bbb"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		diffResult: &cloudstic.DiffResult{
			Ref1: "snapshot/aaa",
			Ref2: "snapshot/bbb",
			Changes: []cloudstic.FileChange{
				{Type: "A", Path: "docs/readme.md"},
			},
		},
	}}

	if exit := diffCommand().execute(r.withArgs(args), context.Background(), "diff"); exit != 0 {
		t.Fatalf("runDiff() exit = %d, want 0", exit)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("json unmarshal: %v\noutput:\n%s", err, out.String())
	}
	if got["Ref1"] != "snapshot/aaa" {
		t.Fatalf("Ref1 = %v, want snapshot/aaa", got["Ref1"])
	}
}

func TestRunDiff_NoChanges(t *testing.T) {
	args := []string{"aaa", "bbb"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		diffResult: &cloudstic.DiffResult{
			Ref1:    "snapshot/aaa",
			Ref2:    "snapshot/bbb",
			Changes: nil,
		},
	}}

	diffCommand().execute(r.withArgs(args), context.Background(), "diff")

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 line for empty diff, got %d lines:\n%s", len(lines), out.String())
	}
}
