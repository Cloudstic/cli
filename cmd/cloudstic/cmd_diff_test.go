package main

import (
	"context"
	"os"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
)

func TestRunDiff_Success(t *testing.T) {
	os.Args = []string{"cloudstic", "diff", "aaa", "bbb"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		diffResult: &cloudstic.DiffResult{
			Ref1: "snapshot/aaa",
			Ref2: "snapshot/bbb",
			Changes: []engine.FileChange{
				{Type: "added", Path: "docs/readme.md"},
				{Type: "modified", Path: "src/main.go"},
				{Type: "removed", Path: "old/file.txt"},
			},
		},
	}}

	r.runDiff(context.Background())

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

func TestRunDiff_NoChanges(t *testing.T) {
	os.Args = []string{"cloudstic", "diff", "aaa", "bbb"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		diffResult: &cloudstic.DiffResult{
			Ref1:    "snapshot/aaa",
			Ref2:    "snapshot/bbb",
			Changes: nil,
		},
	}}

	r.runDiff(context.Background())

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 line for empty diff, got %d lines:\n%s", len(lines), out.String())
	}
}
