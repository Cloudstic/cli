package engine

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestPlanWorkstationSetup_BuildsPreview(t *testing.T) {
	reset := stubWorkstationSetupEnv(t)
	defer reset()

	workstationHostnameFunc = func() (string, error) { return "MacBook-Pro", nil }
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

	cfg := &ProfilesConfig{
		Stores: map[string]ProfileStore{
			"primary": {URI: "s3:bucket"},
		},
		Profiles: map[string]BackupProfile{
			"documents": {Source: "local:/Users/test/Documents"},
			"archive":   {Source: "local:/old-archive"},
		},
	}

	plan, err := PlanWorkstationSetup(context.Background(), WithWorkstationProfiles(cfg))
	if err != nil {
		t.Fatalf("PlanWorkstationSetup: %v", err)
	}

	if plan.StoreRef != "primary" || plan.StoreAction != "use-existing" {
		t.Fatalf("unexpected store resolution: %#v", plan)
	}
	if len(plan.PortableSources) != 1 || plan.PortableSources[0].DisplayName != "Archive" {
		t.Fatalf("unexpected portable sources: %#v", plan.PortableSources)
	}

	gotProfiles := map[string]WorkstationProfileDraft{}
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

func TestPlanWorkstationSetup_StoreWarnings(t *testing.T) {
	reset := stubWorkstationSetupEnv(t)
	defer reset()

	workstationHostnameFunc = func() (string, error) { return "host", nil }
	workstationUserHomeDirFunc = func() (string, error) { return "/home/test", nil }
	workstationPathExistsFunc = func(path string) bool { return path == "/home/test/Documents" }
	workstationDiscoverSourcesFunc = func(context.Context) ([]DiscoveredSource, error) { return nil, nil }

	cfg := &ProfilesConfig{
		Stores: map[string]ProfileStore{
			"a": {URI: "local:/a"},
			"b": {URI: "local:/b"},
		},
	}
	plan, err := PlanWorkstationSetup(context.Background(), WithWorkstationProfiles(cfg))
	if err != nil {
		t.Fatalf("PlanWorkstationSetup: %v", err)
	}
	if plan.StoreRef != "" || plan.StoreAction != "choose-existing" {
		t.Fatalf("unexpected store selection: %#v", plan)
	}
	if len(plan.Coverage.Warnings) == 0 {
		t.Fatal("expected warning for multiple stores")
	}
}

func TestPlanWorkstationSetup_ErrorPaths(t *testing.T) {
	reset := stubWorkstationSetupEnv(t)
	defer reset()

	workstationHostnameFunc = func() (string, error) { return "", errors.New("boom") }
	if _, err := PlanWorkstationSetup(context.Background()); err == nil {
		t.Fatal("expected hostname error")
	}

	workstationHostnameFunc = func() (string, error) { return "host", nil }
	workstationUserHomeDirFunc = func() (string, error) { return "", errors.New("no home") }
	if _, err := PlanWorkstationSetup(context.Background()); err == nil {
		t.Fatal("expected home dir error")
	}

	workstationUserHomeDirFunc = func() (string, error) { return "/home/test", nil }
	workstationPathExistsFunc = func(string) bool { return false }
	workstationDiscoverSourcesFunc = func(context.Context) ([]DiscoveredSource, error) {
		return nil, errors.New("discover failed")
	}
	if _, err := PlanWorkstationSetup(context.Background()); err == nil {
		t.Fatal("expected discover error")
	}
}

func TestApplyWorkstationSetupPlan(t *testing.T) {
	cfg := &ProfilesConfig{
		Profiles: map[string]BackupProfile{
			"documents": {Source: "local:/old"},
		},
	}
	result, err := ApplyWorkstationSetupPlan(cfg, &WorkstationSetupPlan{
		Profiles: []WorkstationProfileDraft{
			{Name: "documents", SourceURI: "local:/Users/test/Documents", StoreRef: "primary", Tags: []string{"workstation"}},
			{Name: "archive", SourceURI: "local:/Volumes/Archive", StoreRef: "primary", Tags: []string{"portable", "workstation"}},
		},
	})
	if err != nil {
		t.Fatalf("ApplyWorkstationSetupPlan: %v", err)
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

func TestApplyWorkstationSetupPlan_Errors(t *testing.T) {
	if _, err := ApplyWorkstationSetupPlan(nil, nil); err == nil {
		t.Fatal("expected nil plan error")
	}
	if _, err := ApplyWorkstationSetupPlan(nil, &WorkstationSetupPlan{
		Profiles: []WorkstationProfileDraft{{Name: "", SourceURI: "local:/tmp"}},
	}); err == nil {
		t.Fatal("expected missing name error")
	}
	if _, err := ApplyWorkstationSetupPlan(nil, &WorkstationSetupPlan{
		Profiles: []WorkstationProfileDraft{{Name: "docs", SourceURI: ""}},
	}); err == nil {
		t.Fatal("expected missing source error")
	}
}

func stubWorkstationSetupEnv(t *testing.T) func() {
	t.Helper()
	oldDiscover := workstationDiscoverSourcesFunc
	oldHome := workstationUserHomeDirFunc
	oldHost := workstationHostnameFunc
	oldExists := workstationPathExistsFunc
	oldGOOS := workstationGOOS
	return func() {
		workstationDiscoverSourcesFunc = oldDiscover
		workstationUserHomeDirFunc = oldHome
		workstationHostnameFunc = oldHost
		workstationPathExistsFunc = oldExists
		workstationGOOS = oldGOOS
	}
}
