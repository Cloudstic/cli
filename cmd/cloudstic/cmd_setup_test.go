package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestRunSetupWorkstation_DryRun(t *testing.T) {
	client := &stubClient{
		setupPlan: &cloudstic.WorkstationSetupPlan{
			Hostname:    "testbox",
			StoreRef:    "primary",
			StoreAction: "use-existing",
			Profiles: []cloudstic.WorkstationProfileDraft{
				{Name: "documents", SourceURI: "local:/Users/test/Documents", StoreRef: "primary", Tags: []string{"workstation"}, Action: "create"},
			},
			Coverage: cloudstic.WorkstationCoverageSummary{
				ProtectedNow:         []string{"Documents (/Users/test/Documents)"},
				SkippedIntentionally: []string{"Downloads (/Users/test/Downloads)"},
			},
		},
	}

	osArgs := os.Args
	t.Cleanup(func() { os.Args = osArgs })
	os.Args = []string{"cloudstic", "setup", "workstation", "-dry-run"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, client: client}
	if code := r.runSetup(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Workstation setup plan (dry-run)") || !strings.Contains(got, "documents") {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestRunSetupWorkstation_JSON(t *testing.T) {
	client := &stubClient{
		setupPlan: &cloudstic.WorkstationSetupPlan{
			Hostname: "testbox",
		},
	}

	osArgs := os.Args
	t.Cleanup(func() { os.Args = osArgs })
	os.Args = []string{"cloudstic", "setup", "workstation", "-dry-run", "-json"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, client: client}
	if code := r.runSetup(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "\"hostname\": \"testbox\"") {
		t.Fatalf("unexpected json output:\n%s", out.String())
	}
}

func TestRunSetupWorkstation_RequiresDryRun(t *testing.T) {
	osArgs := os.Args
	t.Cleanup(func() { os.Args = osArgs })
	os.Args = []string{"cloudstic", "setup", "workstation"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, client: &stubClient{}}
	if code := r.runSetup(context.Background()); code == 0 {
		t.Fatal("expected failure without -dry-run")
	}
}

func TestDefaultProfilesPathNoCreate(t *testing.T) {
	configRoot := filepath.Join(t.TempDir(), "config")
	t.Setenv("CLOUDSTIC_CONFIG_DIR", configRoot)
	t.Setenv("CLOUDSTIC_PROFILES_FILE", "")

	got := defaultProfilesPathNoCreate()
	want := filepath.Join(configRoot, defaultProfilesFilename)
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if _, err := os.Stat(configRoot); !os.IsNotExist(err) {
		t.Fatalf("config dir should not be created, err=%v", err)
	}
}
