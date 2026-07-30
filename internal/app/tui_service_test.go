package app

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudstic/cli/pkg/profile"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/tui"
)

type stubTUIBackend struct {
	loadStoreSnapshots func(context.Context, string, profile.Store) ([]engine.SnapshotEntry, error)
	initProfile        func(context.Context, string, string, *profile.Config) error
	backupProfile      func(context.Context, string, string, *profile.Config, cloudstic.Reporter) error
	checkProfile       func(context.Context, string, string, *profile.Config, cloudstic.Reporter) error
}

func (b stubTUIBackend) LoadStoreSnapshots(ctx context.Context, storeName string, storeCfg profile.Store) ([]engine.SnapshotEntry, error) {
	if b.loadStoreSnapshots == nil {
		return nil, nil
	}
	return b.loadStoreSnapshots(ctx, storeName, storeCfg)
}

func (b stubTUIBackend) InitProfile(ctx context.Context, profilesFile, profileName string, cfg *profile.Config) error {
	if b.initProfile == nil {
		return nil
	}
	return b.initProfile(ctx, profilesFile, profileName, cfg)
}

func (b stubTUIBackend) BackupProfile(ctx context.Context, profilesFile, profileName string, cfg *profile.Config, reporter cloudstic.Reporter) error {
	if b.backupProfile == nil {
		return nil
	}
	return b.backupProfile(ctx, profilesFile, profileName, cfg, reporter)
}

func (b stubTUIBackend) CheckProfile(ctx context.Context, profilesFile, profileName string, cfg *profile.Config, reporter cloudstic.Reporter) error {
	if b.checkProfile == nil {
		return nil
	}
	return b.checkProfile(ctx, profilesFile, profileName, cfg, reporter)
}

func TestTUIServiceLoadDashboardConfigInitializesMaps(t *testing.T) {
	svc := NewTUIService(nil)
	svc.loadProfiles = func(string) (*profile.Config, error) {
		return &profile.Config{Version: 1}, nil
	}

	cfg, err := svc.LoadDashboardConfig("profiles.yaml")
	if err != nil {
		t.Fatalf("LoadDashboardConfig: %v", err)
	}
	if cfg.Profiles == nil || cfg.Stores == nil || cfg.Auth == nil {
		t.Fatalf("maps not initialized: %+v", cfg)
	}
	if got := tui.BuildDashboard(cfg, nil); got.ProfileCount != 0 || got.StoreCount != 0 || got.AuthCount != 0 {
		t.Fatalf("unexpected dashboard: %+v", got)
	}
}

func TestTUIServiceRunProfileActionRunsInitWhenNeeded(t *testing.T) {
	called := ""
	svc := NewTUIService(stubTUIBackend{
		initProfile: func(context.Context, string, string, *profile.Config) error {
			called = "init"
			return nil
		},
		backupProfile: func(context.Context, string, string, *profile.Config, cloudstic.Reporter) error {
			called = "backup"
			return nil
		},
	})
	svc.loadProfiles = func(string) (*profile.Config, error) {
		return &profile.Config{
			Version: 1,
			Profiles: map[string]profile.Profile{
				"docs": {Source: "local:/docs", Store: "remote"},
			},
		}, nil
	}

	err := svc.RunProfileAction(context.Background(), "profiles.yaml", tui.ProfileCard{
		Name:        "docs",
		StoreHealth: tui.StoreHealthNotInitialized,
	}, nil)
	if err != nil {
		t.Fatalf("RunProfileAction: %v", err)
	}
	if called != "init" {
		t.Fatalf("called %q want init", called)
	}
}

func TestTUIServiceRunProfileActionRunsBackup(t *testing.T) {
	called := ""
	svc := NewTUIService(stubTUIBackend{
		backupProfile: func(context.Context, string, string, *profile.Config, cloudstic.Reporter) error {
			called = "backup"
			return nil
		},
	})
	svc.loadProfiles = func(string) (*profile.Config, error) {
		return &profile.Config{
			Version: 1,
			Profiles: map[string]profile.Profile{
				"docs": {Source: "local:/docs", Store: "remote"},
			},
		}, nil
	}

	err := svc.RunProfileAction(context.Background(), "profiles.yaml", tui.ProfileCard{
		Name:   "docs",
		Status: tui.ProfileStatusReady,
	}, nil)
	if err != nil {
		t.Fatalf("RunProfileAction: %v", err)
	}
	if called != "backup" {
		t.Fatalf("called %q want backup", called)
	}
}

func TestTUIServiceRunProfileActionPropagatesLoadError(t *testing.T) {
	svc := NewTUIService(nil)
	svc.loadProfiles = func(string) (*profile.Config, error) {
		return nil, errors.New("boom")
	}

	err := svc.RunProfileAction(context.Background(), "profiles.yaml", tui.ProfileCard{Name: "docs"}, nil)
	if err == nil || err.Error() != "load profiles: boom" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTUIServiceRunProfileCheckRunsBackend(t *testing.T) {
	called := ""
	svc := NewTUIService(stubTUIBackend{
		checkProfile: func(context.Context, string, string, *profile.Config, cloudstic.Reporter) error {
			called = "check"
			return nil
		},
	})
	svc.loadProfiles = func(string) (*profile.Config, error) {
		return &profile.Config{
			Version: 1,
			Profiles: map[string]profile.Profile{
				"docs": {Source: "local:/docs", Store: "remote"},
			},
		}, nil
	}

	err := svc.RunProfileCheck(context.Background(), "profiles.yaml", tui.ProfileCard{
		Name:        "docs",
		Status:      tui.ProfileStatusReady,
		StoreHealth: tui.StoreHealthReady,
	}, nil)
	if err != nil {
		t.Fatalf("RunProfileCheck: %v", err)
	}
	if called != "check" {
		t.Fatalf("called %q want check", called)
	}
}

func TestTUIServiceRunProfileCheckRejectsUninitializedRepo(t *testing.T) {
	svc := NewTUIService(stubTUIBackend{})
	svc.loadProfiles = func(string) (*profile.Config, error) {
		return &profile.Config{
			Version: 1,
			Profiles: map[string]profile.Profile{
				"docs": {Source: "local:/docs", Store: "remote"},
			},
		}, nil
	}

	err := svc.RunProfileCheck(context.Background(), "profiles.yaml", tui.ProfileCard{
		Name:        "docs",
		StoreHealth: tui.StoreHealthNotInitialized,
	}, nil)
	if err == nil || err.Error() != "repository is not initialized" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTUIServiceSaveProfilePersistsConfig(t *testing.T) {
	svc := NewTUIService(nil)
	svc.loadProfiles = func(string) (*profile.Config, error) {
		return &profile.Config{
			Version: 1,
			Stores: map[string]profile.Store{
				"remote": {URI: "s3:bucket"},
			},
			Profiles: map[string]profile.Profile{},
		}, nil
	}
	var saved *profile.Config
	svc.saveProfiles = func(_ string, cfg *profile.Config) error {
		saved = cfg
		return nil
	}

	err := svc.SaveProfile("profiles.yaml", "docs", profile.Profile{
		Source: "local:/docs",
		Store:  "remote",
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if saved == nil {
		t.Fatalf("saveProfiles was not called")
	}
	if got := saved.Profiles["docs"].Source; got != "local:/docs" {
		t.Fatalf("saved profile source=%q want local:/docs", got)
	}
}

func TestTUIServiceDeleteProfileRemovesProfile(t *testing.T) {
	svc := NewTUIService(nil)
	svc.loadProfiles = func(string) (*profile.Config, error) {
		return &profile.Config{
			Version: 1,
			Profiles: map[string]profile.Profile{
				"docs": {Source: "local:/docs", Store: "remote"},
			},
		}, nil
	}
	var saved *profile.Config
	svc.saveProfiles = func(_ string, cfg *profile.Config) error {
		saved = cfg
		return nil
	}

	err := svc.DeleteProfile("profiles.yaml", "docs")
	if err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	if saved == nil {
		t.Fatalf("saveProfiles was not called")
	}
	if _, ok := saved.Profiles["docs"]; ok {
		t.Fatalf("profile docs still present after delete")
	}
}

func TestTUIServiceSaveStorePersistsConfig(t *testing.T) {
	svc := NewTUIService(nil)
	svc.loadProfiles = func(string) (*profile.Config, error) {
		return &profile.Config{
			Version: 1,
			Stores:  map[string]profile.Store{},
		}, nil
	}
	var saved *profile.Config
	svc.saveProfiles = func(_ string, cfg *profile.Config) error {
		saved = cfg
		return nil
	}

	err := svc.SaveStore("profiles.yaml", "remote", profile.Store{URI: "local:/tmp/store"})
	if err != nil {
		t.Fatalf("SaveStore: %v", err)
	}
	if saved == nil {
		t.Fatalf("saveProfiles was not called")
	}
	if got := saved.Stores["remote"].URI; got != "local:/tmp/store" {
		t.Fatalf("saved store uri=%q want local:/tmp/store", got)
	}
}
