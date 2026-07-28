package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/profile"

	"github.com/cloudstic/cli/internal/workstation"
)

func stubSetupWorkstationPlan(t *testing.T, plan *workstation.SetupPlan, err error) {
	t.Helper()
	old := planWorkstationSetup
	planWorkstationSetup = func(context.Context, ...workstation.SetupOption) (*workstation.SetupPlan, error) {
		return plan, err
	}
	t.Cleanup(func() { planWorkstationSetup = old })
}

func TestRunSetupWorkstation_DryRun(t *testing.T) {
	t.Setenv("CLOUDSTIC_CONFIG_DIR", t.TempDir())
	stubSetupWorkstationPlan(t, &workstation.SetupPlan{
		Hostname:    "testbox",
		StoreRef:    "primary",
		StoreAction: "use-existing",
		Profiles: []workstation.ProfileDraft{
			{Name: "documents", SourceURI: "local:/Users/test/Documents", StoreRef: "primary", Tags: []string{"workstation"}, Action: "create", Selected: true},
		},
		Coverage: workstation.CoverageSummary{
			ProtectedNow:         []string{"Documents (/Users/test/Documents)"},
			SkippedIntentionally: []string{"Downloads (/Users/test/Downloads)"},
		},
	}, nil)
	args := []string{"workstation", "-dry-run"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, client: &stubClient{}}
	if code := setupCommand().execute(r.withArgs(args), context.Background(), "setup"); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Workstation setup plan (dry-run)") || !strings.Contains(got, "documents") {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestRunSetupWorkstation_JSON(t *testing.T) {
	t.Setenv("CLOUDSTIC_CONFIG_DIR", t.TempDir())
	stubSetupWorkstationPlan(t, &workstation.SetupPlan{
		Hostname: "testbox",
	}, nil)
	args := []string{"workstation", "-dry-run", "-json"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, client: &stubClient{}}
	if code := setupCommand().execute(r.withArgs(args), context.Background(), "setup"); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "\"hostname\": \"testbox\"") {
		t.Fatalf("unexpected json output:\n%s", out.String())
	}
}

func TestRunSetupWorkstation_ApplyYes(t *testing.T) {
	stubSetupWorkstationPlan(t, &workstation.SetupPlan{
		Hostname:    "testbox",
		StoreRef:    "primary",
		StoreAction: "use-existing",
		Profiles: []workstation.ProfileDraft{
			{Name: "documents", SourceURI: "local:/Users/test/Documents", StoreRef: "primary", Tags: []string{"workstation"}, Action: "create", Selected: true},
		},
	}, nil)
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := profile.Save(profilesPath, &profile.Config{
		Version: 1,
		Stores:  map[string]profile.Store{"primary": {URI: "local:/repo"}},
	}); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}
	args := []string{"workstation", "-yes", "-profiles-file", profilesPath}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, client: &stubClient{}, noPrompt: true}
	if code := setupCommand().execute(r.withArgs(args), context.Background(), "setup"); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	cfg, err := profile.Load(profilesPath)
	if err != nil {
		t.Fatalf("LoadProfilesFile: %v", err)
	}
	if got := cfg.Profiles["documents"].Store; got != "primary" {
		t.Fatalf("documents store = %q, want primary", got)
	}
}

func TestRunSetupWorkstation_RequiresStoreResolutionWithoutPrompt(t *testing.T) {
	args := []string{"workstation", "-yes"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, client: &stubClient{}, noPrompt: true}
	if code := setupCommand().execute(r.withArgs(args), context.Background(), "setup"); code == 0 {
		t.Fatal("expected failure without store resolution")
	}
}

func TestReviewWorkstationPlan_CanSkipSources(t *testing.T) {
	cfg := &profile.Config{}
	plan := &workstation.SetupPlan{
		Profiles: []workstation.ProfileDraft{
			{Name: "documents", SourceURI: "local:/Users/test/Documents", Action: "create", DisplayLabel: "Documents (/Users/test/Documents)", Selected: true},
		},
		Coverage: workstation.CoverageSummary{
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
	cfg := &profile.Config{
		Profiles: map[string]profile.Profile{
			"documents": {Source: "local:/Users/test/Documents"},
		},
	}
	plan := &workstation.SetupPlan{
		Profiles: []workstation.ProfileDraft{
			{Name: "documents", SourceURI: "local:/Users/test/Documents", Action: "update", DisplayLabel: "Documents (/Users/test/Documents)", Selected: true},
		},
		Coverage: workstation.CoverageSummary{
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

func TestDefaultProfilesPath(t *testing.T) {
	configRoot := filepath.Join(t.TempDir(), "config")
	t.Setenv("CLOUDSTIC_CONFIG_DIR", configRoot)
	t.Setenv("CLOUDSTIC_PROFILES_FILE", "")

	got, err := defaultProfilesPath("")
	if err != nil {
		t.Fatalf("defaultProfilesPath: %v", err)
	}
	want := filepath.Join(configRoot, defaultProfilesFilename)
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if _, err := os.Stat(configRoot); !os.IsNotExist(err) {
		t.Fatalf("config dir should not be created, err=%v", err)
	}
}

func TestDefaultProfilesPath_ConfigDirOverridesEnvironment(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CLOUDSTIC_CONFIG_DIR", filepath.Join(tmp, "from-env"))
	t.Setenv("CLOUDSTIC_PROFILES_FILE", "")

	fromFlag := filepath.Join(tmp, "from-flag")
	got, err := defaultProfilesPath(fromFlag)
	if err != nil {
		t.Fatalf("defaultProfilesPath: %v", err)
	}
	if want := filepath.Join(fromFlag, defaultProfilesFilename); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}
