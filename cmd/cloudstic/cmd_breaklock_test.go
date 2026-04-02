package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestRunBreakLock_NoLock(t *testing.T) {
	os.Args = []string{"cloudstic", "break-lock"}
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut, client: &stubClient{breakLockResult: nil}}

	r.runBreakLock(context.Background())

	if !strings.Contains(errOut.String(), "not locked") {
		t.Errorf("expected 'not locked' message, got:\n%s", errOut.String())
	}
}

func TestRunBreakLock_JSON(t *testing.T) {
	os.Args = []string{"cloudstic", "break-lock", "-json"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		breakLockResult: []*cloudstic.RepoLock{
			{Operation: "backup", Holder: "worker-1"},
		},
	}}

	if exit := r.runBreakLock(context.Background()); exit != 0 {
		t.Fatalf("runBreakLock() exit = %d, want 0", exit)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("json unmarshal: %v\noutput:\n%s", err, out.String())
	}
	if _, ok := got["locks"]; !ok {
		t.Fatalf("expected locks key in JSON output, got: %v", got)
	}
}

func TestRunBreakLock_LocksRemoved(t *testing.T) {
	os.Args = []string{"cloudstic", "break-lock"}
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut, client: &stubClient{
		breakLockResult: []*cloudstic.RepoLock{
			{
				Operation:  "backup",
				Holder:     "worker-1",
				AcquiredAt: "2024-01-01T10:00:00Z",
				ExpiresAt:  "2024-01-01T11:00:00Z",
				IsShared:   false,
			},
		},
	}}

	r.runBreakLock(context.Background())

	got := errOut.String()
	if !strings.Contains(got, "Locks removed") {
		t.Errorf("expected 'Locks removed', got:\n%s", got)
	}
	if !strings.Contains(got, "backup") {
		t.Errorf("expected operation 'backup' in output, got:\n%s", got)
	}
	if !strings.Contains(got, "worker-1") {
		t.Errorf("expected holder 'worker-1' in output, got:\n%s", got)
	}
}

func TestPrintBreakLockResult_NoLock(t *testing.T) {
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut}
	r.printBreakLockResult(nil)

	if !strings.Contains(errOut.String(), "not locked") {
		t.Errorf("expected 'not locked', got:\n%s", errOut.String())
	}
}

func TestPrintBreakLockResult_MultipleLocks(t *testing.T) {
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut}
	r.printBreakLockResult([]*cloudstic.RepoLock{
		{Operation: "prune", Holder: "host-a"},
		{Operation: "backup", Holder: "host-b"},
	})

	got := errOut.String()
	if !strings.Contains(got, "prune") {
		t.Errorf("expected 'prune' in output, got:\n%s", got)
	}
	if !strings.Contains(got, "host-b") {
		t.Errorf("expected 'host-b' in output, got:\n%s", got)
	}
}
