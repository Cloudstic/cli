package main

import (
	"context"
	"os"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestRunPrune_Normal(t *testing.T) {
	os.Args = []string{"cloudstic", "prune"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		pruneResult: &cloudstic.PruneResult{
			ObjectsScanned: 100,
			ObjectsDeleted: 10,
			BytesReclaimed: 2048,
			DryRun:         false,
		},
	}}

	r.runPrune(context.Background())

	got := out.String()
	if !strings.Contains(got, "Prune complete.") {
		t.Errorf("expected 'Prune complete.', got:\n%s", got)
	}
	if !strings.Contains(got, "100") {
		t.Errorf("expected ObjectsScanned=100 in output, got:\n%s", got)
	}
	if !strings.Contains(got, "10") {
		t.Errorf("expected ObjectsDeleted=10 in output, got:\n%s", got)
	}
	if !strings.Contains(got, "2.0 KiB") {
		t.Errorf("expected reclaimed space in output, got:\n%s", got)
	}
}

func TestRunPrune_DryRun(t *testing.T) {
	os.Args = []string{"cloudstic", "prune", "--dry-run"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		pruneResult: &cloudstic.PruneResult{
			ObjectsScanned: 50,
			ObjectsDeleted: 5,
			DryRun:         true,
		},
	}}

	r.runPrune(context.Background())

	got := out.String()
	if !strings.Contains(got, "Prune dry run complete.") {
		t.Errorf("expected dry run message, got:\n%s", got)
	}
	if !strings.Contains(got, "would delete") {
		t.Errorf("expected 'would delete' in output, got:\n%s", got)
	}
}

func TestPrintPruneStats_Normal(t *testing.T) {
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}
	r.printPruneStats(&cloudstic.PruneResult{
		ObjectsScanned: 200,
		ObjectsDeleted: 15,
		BytesReclaimed: 1024 * 1024,
		DryRun:         false,
	})

	got := out.String()
	for _, want := range []string{"Prune complete.", "200", "15", "1.0 MiB"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output, got:\n%s", want, got)
		}
	}
}

func TestPrintPruneStats_DryRun(t *testing.T) {
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}
	r.printPruneStats(&cloudstic.PruneResult{
		ObjectsScanned: 30,
		ObjectsDeleted: 3,
		DryRun:         true,
	})

	got := out.String()
	if !strings.Contains(got, "Prune dry run complete.") {
		t.Errorf("expected dry run header, got:\n%s", got)
	}
	if strings.Contains(got, "Space reclaimed") {
		t.Errorf("dry run should not show 'Space reclaimed', got:\n%s", got)
	}
}
