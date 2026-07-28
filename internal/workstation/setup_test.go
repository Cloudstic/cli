package workstation

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/cloudstic/cli/pkg/profile"
)

func TestPlan_BuildsPreview(t *testing.T) {
	reset := stubWorkstationSetupEnv(t)
	defer reset()

	hostnameFunc = func() (string, error) { return "MacBook-Pro", nil }
	workstationUserHomeDirFunc = func() (string, error) { return "/Users/test", nil }
	workstationGOOS = "darwin"
	workstationPathExistsFunc = func(path string) bool {
		switch path {
		case "/Users/test/Documents", "/Users/test/Desktop", "/Users/test/Pictures", "/Users/test/Downloads", "/Users/test/Projects":
			return true
		default:
			return false
		}
	}
	workstationDiscoverSourcesFunc = func(context.Context) ([]DiscoveredSource, error) {
		return []DiscoveredSource{
			{DisplayName: "System", SourceURI: "local:/", MountPoint: "/", Portable: false},
			{DisplayName: "Archive", DriveName: "Archive", SourceURI: "local:/Volumes/Archive", MountPoint: "/Volumes/Archive", Portable: true},
		}, nil
	}

	cfg := &profile.Config{
		Stores: map[string]profile.Store{
			"primary": {URI: "s3:bucket"},
		},
		Profiles: map[string]profile.Profile{
			"documents": {Source: "local:/Users/test/Documents"},
			"archive":   {Source: "local:/old-archive"},
		},
	}

	plan, err := Plan(context.Background(), WithProfiles(cfg))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if plan.StoreRef != "primary" || plan.StoreAction != "use-existing" {
		t.Fatalf("unexpected store resolution: %#v", plan)
	}
	if len(plan.PortableSources) != 1 || plan.PortableSources[0].DisplayName != "Archive" {
		t.Fatalf("unexpected portable sources: %#v", plan.PortableSources)
	}

	gotProfiles := map[string]ProfileDraft{}
	for _, profile := range plan.Profiles {
		gotProfiles[profile.Name] = profile
	}
	if gotProfiles["documents"].Action != "update" {
		t.Fatalf("documents action = %q, want update", gotProfiles["documents"].Action)
	}
	if gotProfiles["desktop"].Action != "create" {
		t.Fatalf("desktop action = %q, want create", gotProfiles["desktop"].Action)
	}
	if gotProfiles["macbook-pro-archive"].Action != "rename" {
		t.Fatalf("archive action = %q, want rename", gotProfiles["macbook-pro-archive"].Action)
	}
	if !reflect.DeepEqual(plan.Coverage.SkippedIntentionally, []string{"Downloads (/Users/test/Downloads)"}) {
		t.Fatalf("unexpected skipped coverage: %#v", plan.Coverage.SkippedIntentionally)
	}
}

func TestPlan_StoreWarnings(t *testing.T) {
	reset := stubWorkstationSetupEnv(t)
	defer reset()

	hostnameFunc = func() (string, error) { return "host", nil }
	workstationUserHomeDirFunc = func() (string, error) { return "/home/test", nil }
	workstationPathExistsFunc = func(path string) bool { return path == "/home/test/Documents" }
	workstationDiscoverSourcesFunc = func(context.Context) ([]DiscoveredSource, error) { return nil, nil }

	cfg := &profile.Config{
		Stores: map[string]profile.Store{
			"a": {URI: "local:/a"},
			"b": {URI: "local:/b"},
		},
	}
	plan, err := Plan(context.Background(), WithProfiles(cfg))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.StoreRef != "" || plan.StoreAction != "choose-existing" {
		t.Fatalf("unexpected store selection: %#v", plan)
	}
	if len(plan.Coverage.Warnings) == 0 {
		t.Fatal("expected warning for multiple stores")
	}
}

func TestPlan_ErrorPaths(t *testing.T) {
	reset := stubWorkstationSetupEnv(t)
	defer reset()

	hostnameFunc = func() (string, error) { return "", errors.New("boom") }
	if _, err := Plan(context.Background()); err == nil {
		t.Fatal("expected hostname error")
	}

	hostnameFunc = func() (string, error) { return "host", nil }
	workstationUserHomeDirFunc = func() (string, error) { return "", errors.New("no home") }
	if _, err := Plan(context.Background()); err == nil {
		t.Fatal("expected home dir error")
	}

	workstationUserHomeDirFunc = func() (string, error) { return "/home/test", nil }
	workstationPathExistsFunc = func(string) bool { return false }
	workstationDiscoverSourcesFunc = func(context.Context) ([]DiscoveredSource, error) {
		return nil, errors.New("discover failed")
	}
	if _, err := Plan(context.Background()); err == nil {
		t.Fatal("expected discover error")
	}
}

func TestApply(t *testing.T) {
	cfg := &profile.Config{
		Profiles: map[string]profile.Profile{
			"documents": {Source: "local:/old"},
		},
	}
	result, err := Apply(cfg, &SetupPlan{
		Profiles: []ProfileDraft{
			{Name: "documents", SourceURI: "local:/Users/test/Documents", StoreRef: "primary", Tags: []string{"workstation"}, Selected: true},
			{Name: "archive", SourceURI: "local:/Volumes/Archive", StoreRef: "primary", Tags: []string{"portable", "workstation"}, Selected: true},
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.ProfilesCreated != 1 || result.ProfilesUpdated != 1 {
		t.Fatalf("unexpected counts: %#v", result)
	}
	if got := cfg.Profiles["archive"].Store; got != "primary" {
		t.Fatalf("archive store = %q, want primary", got)
	}
	if got := cfg.Profiles["documents"].Source; got != "local:/Users/test/Documents" {
		t.Fatalf("documents source = %q", got)
	}
}

func TestApply_SkipsDeselectedDrafts(t *testing.T) {
	cfg := &profile.Config{}
	result, err := Apply(cfg, &SetupPlan{
		Profiles: []ProfileDraft{
			{Name: "documents", SourceURI: "local:/Users/test/Documents", Selected: true},
			{Name: "desktop", SourceURI: "local:/Users/test/Desktop", Selected: false},
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.ProfilesCreated != 1 {
		t.Fatalf("ProfilesCreated = %d, want 1", result.ProfilesCreated)
	}
	if _, ok := cfg.Profiles["desktop"]; ok {
		t.Fatal("desktop profile should not be created")
	}
}

func TestApply_Errors(t *testing.T) {
	if _, err := Apply(nil, nil); err == nil {
		t.Fatal("expected nil plan error")
	}
	if _, err := Apply(nil, &SetupPlan{
		Profiles: []ProfileDraft{{Name: "", SourceURI: "local:/tmp", Selected: true}},
	}); err == nil {
		t.Fatal("expected missing name error")
	}
	if _, err := Apply(nil, &SetupPlan{
		Profiles: []ProfileDraft{{Name: "docs", SourceURI: "", Selected: true}},
	}); err == nil {
		t.Fatal("expected missing source error")
	}
}

func TestSetup_DryRun(t *testing.T) {
	reset := stubWorkstationSetupEnv(t)
	defer reset()

	hostnameFunc = func() (string, error) { return "host", nil }
	workstationUserHomeDirFunc = func() (string, error) { return "/home/test", nil }
	workstationPathExistsFunc = func(path string) bool { return path == "/home/test/Documents" }
	workstationDiscoverSourcesFunc = func(context.Context) ([]DiscoveredSource, error) { return nil, nil }

	cfg := &profile.Config{}
	result, err := Setup(context.Background(), WithProfiles(cfg), WithDryRun())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result.Plan == nil || result.Applied != nil {
		t.Fatalf("unexpected setup result: %#v", result)
	}
	if len(cfg.Profiles) != 0 {
		t.Fatalf("dry-run should not mutate profiles: %#v", cfg.Profiles)
	}
}

func TestSetup_Apply(t *testing.T) {
	reset := stubWorkstationSetupEnv(t)
	defer reset()

	hostnameFunc = func() (string, error) { return "host", nil }
	workstationUserHomeDirFunc = func() (string, error) { return "/home/test", nil }
	workstationPathExistsFunc = func(path string) bool { return path == "/home/test/Documents" }
	workstationDiscoverSourcesFunc = func(context.Context) ([]DiscoveredSource, error) { return nil, nil }

	cfg := &profile.Config{
		Stores: map[string]profile.Store{
			"primary": {URI: "local:/repo"},
		},
	}
	result, err := Setup(context.Background(), WithProfiles(cfg), WithStoreRef("primary"))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result.Applied == nil || result.Applied.ProfilesCreated != 1 {
		t.Fatalf("unexpected apply result: %#v", result)
	}
	if got := cfg.Profiles["documents"].Store; got != "primary" {
		t.Fatalf("documents store = %q, want primary", got)
	}
}

func stubWorkstationSetupEnv(t *testing.T) func() {
	t.Helper()
	oldDiscover := workstationDiscoverSourcesFunc
	oldHome := workstationUserHomeDirFunc
	oldHost := hostnameFunc
	oldExists := workstationPathExistsFunc
	oldGOOS := workstationGOOS
	return func() {
		workstationDiscoverSourcesFunc = oldDiscover
		workstationUserHomeDirFunc = oldHome
		hostnameFunc = oldHost
		workstationPathExistsFunc = oldExists
		workstationGOOS = oldGOOS
	}
}
