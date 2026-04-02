package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
)

func TestRunCheck_Healthy(t *testing.T) {
	os.Args = []string{"cloudstic", "check"}
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut, client: &stubClient{
		checkResult: &cloudstic.CheckResult{
			SnapshotsChecked: 5,
			ObjectsVerified:  120,
			Errors:           nil,
		},
	}}

	r.runCheck(context.Background())

	got := errOut.String()
	if !strings.Contains(got, "No errors found") {
		t.Errorf("expected 'No errors found', got:\n%s", got)
	}
	if !strings.Contains(got, "5") {
		t.Errorf("expected SnapshotsChecked=5 in output, got:\n%s", got)
	}
}

func TestRunCheck_JSONWithErrorsReturnsExitOne(t *testing.T) {
	os.Args = []string{"cloudstic", "check", "-json"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		checkResult: &cloudstic.CheckResult{
			SnapshotsChecked: 1,
			ObjectsVerified:  2,
			Errors: []engine.CheckError{
				{Type: "corrupt", Key: "content/xyz", Message: "checksum mismatch"},
			},
		},
	}}

	if exit := r.runCheck(context.Background()); exit != 1 {
		t.Fatalf("runCheck() exit = %d, want 1", exit)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("json unmarshal: %v\noutput:\n%s", err, out.String())
	}
	if _, ok := got["Errors"]; !ok {
		t.Fatalf("expected Errors key in JSON output, got: %v", got)
	}
}

func TestPrintCheckResult_Healthy(t *testing.T) {
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut}

	hasErrors := r.printCheckResult(&cloudstic.CheckResult{
		SnapshotsChecked: 10,
		ObjectsVerified:  300,
	})

	if hasErrors {
		t.Error("expected false for healthy result")
	}
	got := errOut.String()
	if !strings.Contains(got, "repository is healthy") {
		t.Errorf("expected healthy message, got:\n%s", got)
	}
}

func TestPrintCheckResult_WithErrors(t *testing.T) {
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut}

	hasErrors := r.printCheckResult(&cloudstic.CheckResult{
		SnapshotsChecked: 2,
		ObjectsVerified:  40,
		Errors: []engine.CheckError{
			{Type: "corrupt", Key: "content/xyz", Message: "checksum mismatch"},
		},
	})

	if !hasErrors {
		t.Error("expected true when errors present")
	}
	got := errOut.String()
	if !strings.Contains(got, "corrupt") {
		t.Errorf("expected error type in output, got:\n%s", got)
	}
}
