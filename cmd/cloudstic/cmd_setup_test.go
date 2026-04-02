package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
)

func stubSetupWorkstationPlan(t *testing.T, plan *cloudstic.WorkstationSetupPlan, err error) {
	t.Helper()
	old := planWorkstationSetup
	planWorkstationSetup = func(context.Context, ...cloudstic.WorkstationSetupOption) (*cloudstic.WorkstationSetupPlan, error) {
		return plan, err
	}
	t.Cleanup(func() { planWorkstationSetup = old })
}

func TestRunSetupWorkstation_DryRun(t *testing.T) {
	t.Setenv("CLOUDSTIC_CONFIG_DIR", t.TempDir())
	stubSetupWorkstationPlan(t, &cloudstic.WorkstationSetupPlan{
		Hostname:    "testbox",
		StoreRef:    "primary",
		StoreAction: "use-existing",
		Profiles: []cloudstic.WorkstationProfileDraft{
			{Name: "documents", SourceURI: "local:/Users/test/Documents", StoreRef: "primary", Tags: []string{"workstation"}, Action: "create", Selected: true},
		},
		Coverage: cloudstic.WorkstationCoverageSummary{
			ProtectedNow:         []string{"Documents (/Users/test/Documents)"},
			SkippedIntentionally: []string{"Downloads (/Users/test/Downloads)"},
		},
	}, nil)

	osArgs := os.Args
	t.Cleanup(func() { os.Args = osArgs })
	os.Args = []string{"cloudstic", "setup", "workstation", "-dry-run"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, client: &stubClient{}}
	if code := r.runSetup(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Workstation setup plan (dry-run)") || !strings.Contains(got, "documents") {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestRunSetupWorkstation_JSON(t *testing.T) {
	t.Setenv("CLOUDSTIC_CONFIG_DIR", t.TempDir())
	stubSetupWorkstationPlan(t, &cloudstic.WorkstationSetupPlan{
		Hostname: "testbox",
	}, nil)

	osArgs := os.Args
	t.Cleanup(func() { os.Args = osArgs })
	os.Args = []string{"cloudstic", "setup", "workstation", "-dry-run", "-json"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, client: &stubClient{}}
	if code := r.runSetup(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "\"hostname\": \"testbox\"") {
		t.Fatalf("unexpected json output:\n%s", out.String())
	}
}

func TestRunSetupWorkstation_ApplyYes(t *testing.T) {
	stubSetupWorkstationPlan(t, &cloudstic.WorkstationSetupPlan{
		Hostname:    "testbox",
		StoreRef:    "primary",
		StoreAction: "use-existing",
		Profiles: []cloudstic.WorkstationProfileDraft{
			{Name: "documents", SourceURI: "local:/Users/test/Documents", StoreRef: "primary", Tags: []string{"workstation"}, Action: "create", Selected: true},
		},
	}, nil)
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores:  map[string]cloudstic.ProfileStore{"primary": {URI: "local:/repo"}},
	}); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	osArgs := os.Args
	t.Cleanup(func() { os.Args = osArgs })
	os.Args = []string{"cloudstic", "setup", "workstation", "-yes", "-profiles-file", profilesPath}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, client: &stubClient{}, noPrompt: true}
	if code := r.runSetup(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	cfg, err := cloudstic.LoadProfilesFile(profilesPath)
	if err != nil {
		t.Fatalf("LoadProfilesFile: %v", err)
	}
	if got := cfg.Profiles["documents"].Store; got != "primary" {
		t.Fatalf("documents store = %q, want primary", got)
	}
}

func TestRunSetupWorkstation_RequiresStoreResolutionWithoutPrompt(t *testing.T) {
	osArgs := os.Args
	t.Cleanup(func() { os.Args = osArgs })
	os.Args = []string{"cloudstic", "setup", "workstation", "-yes"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, client: &stubClient{}, noPrompt: true}
	if code := r.runSetup(context.Background()); code == 0 {
		t.Fatal("expected failure without store resolution")
	}
}

func TestReviewWorkstationPlan_CanSkipSources(t *testing.T) {
	cfg := &cloudstic.ProfilesConfig{}
	plan := &engine.WorkstationSetupPlan{
		Profiles: []engine.WorkstationProfileDraft{
			{Name: "documents", SourceURI: "local:/Users/test/Documents", Action: "create", DisplayLabel: "Documents (/Users/test/Documents)", Selected: true},
		},
		Coverage: engine.WorkstationCoverageSummary{
			ProtectedNow: []string{"Documents (/Users/test/Documents)"},
		},
	}
	err := reviewWorkstationPlan(context.Background(), cfg, plan, workstationReviewPrompts{
		confirm: func(context.Context, string, bool) (bool, error) { return false, nil },
		selectOne: func(context.Context, string, []string) (string, error) {
			t.Fatal("selectOne should not be called")
			return "", nil
		},
		input: func(context.Context, string, string, func(string) error) (string, error) {
			t.Fatal("input should not be called")
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("reviewWorkstationPlan: %v", err)
	}
	if plan.Profiles[0].Selected {
		t.Fatal("expected draft to be deselected")
	}
	if !strings.Contains(strings.Join(plan.Coverage.SkippedIntentionally, ","), "Documents (/Users/test/Documents)") {
		t.Fatalf("expected skipped coverage to include source: %#v", plan.Coverage)
	}
}

func TestReviewWorkstationPlan_RenameUpdate(t *testing.T) {
	cfg := &cloudstic.ProfilesConfig{
		Profiles: map[string]cloudstic.BackupProfile{
			"documents": {Source: "local:/Users/test/Documents"},
		},
	}
	plan := &engine.WorkstationSetupPlan{
		Profiles: []engine.WorkstationProfileDraft{
			{Name: "documents", SourceURI: "local:/Users/test/Documents", Action: "update", DisplayLabel: "Documents (/Users/test/Documents)", Selected: true},
		},
		Coverage: engine.WorkstationCoverageSummary{
			ProtectedNow: []string{"Documents (/Users/test/Documents)"},
		},
	}
	var asked bool
	err := reviewWorkstationPlan(context.Background(), cfg, plan, workstationReviewPrompts{
		confirm: func(context.Context, string, bool) (bool, error) { return true, nil },
		selectOne: func(context.Context, string, []string) (string, error) {
			return "Create renamed profile", nil
		},
		input: func(_ context.Context, label, defaultValue string, validate func(string) error) (string, error) {
			asked = true
			if err := validate("documents-2"); err != nil {
				t.Fatalf("validate: %v", err)
			}
			return "documents-2", nil
		},
	})
	if err != nil {
		t.Fatalf("reviewWorkstationPlan: %v", err)
	}
	if !asked || plan.Profiles[0].Name != "documents-2" || plan.Profiles[0].Action != "rename" {
		t.Fatalf("unexpected draft after rename: %#v", plan.Profiles[0])
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
