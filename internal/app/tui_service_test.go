package app

import (
	"context"
	"errors"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/tui"
)

type stubTUIBackend struct {
	loadStoreSnapshots func(context.Context, string, cloudstic.ProfileStore) ([]engine.SnapshotEntry, error)
	initProfile        func(context.Context, string, string, cloudstic.BackupProfile, *cloudstic.ProfilesConfig) error
	backupProfile      func(context.Context, string, string, cloudstic.BackupProfile, *cloudstic.ProfilesConfig, cloudstic.Reporter) error
	checkProfile       func(context.Context, string, string, cloudstic.BackupProfile, *cloudstic.ProfilesConfig, cloudstic.Reporter) error
}

func (b stubTUIBackend) LoadStoreSnapshots(ctx context.Context, storeName string, storeCfg cloudstic.ProfileStore) ([]engine.SnapshotEntry, error) {
	if b.loadStoreSnapshots == nil {
		return nil, nil
	}
	return b.loadStoreSnapshots(ctx, storeName, storeCfg)
}

func (b stubTUIBackend) InitProfile(ctx context.Context, profilesFile, profileName string, profileCfg cloudstic.BackupProfile, cfg *cloudstic.ProfilesConfig) error {
	if b.initProfile == nil {
		return nil
	}
	return b.initProfile(ctx, profilesFile, profileName, profileCfg, cfg)
}

func (b stubTUIBackend) BackupProfile(ctx context.Context, profilesFile, profileName string, profileCfg cloudstic.BackupProfile, cfg *cloudstic.ProfilesConfig, reporter cloudstic.Reporter) error {
	if b.backupProfile == nil {
		return nil
	}
	return b.backupProfile(ctx, profilesFile, profileName, profileCfg, cfg, reporter)
}

func (b stubTUIBackend) CheckProfile(ctx context.Context, profilesFile, profileName string, profileCfg cloudstic.BackupProfile, cfg *cloudstic.ProfilesConfig, reporter cloudstic.Reporter) error {
	if b.checkProfile == nil {
		return nil
	}
	return b.checkProfile(ctx, profilesFile, profileName, profileCfg, cfg, reporter)
}

func TestTUIServiceBuildDashboardInitializesMaps(t *testing.T) {
	svc := NewTUIService(nil)
	svc.loadProfiles = func(string) (*cloudstic.ProfilesConfig, error) {
		return &cloudstic.ProfilesConfig{Version: 1}, nil
	}

	got, err := svc.BuildDashboard(context.Background(), "profiles.yaml")
	if err != nil {
		t.Fatalf("BuildDashboard: %v", err)
	}
	if got.ProfileCount != 0 || got.StoreCount != 0 || got.AuthCount != 0 {
		t.Fatalf("unexpected dashboard: %+v", got)
	}
}

func TestTUIServiceRunProfileActionRunsInitWhenNeeded(t *testing.T) {
	called := ""
	svc := NewTUIService(stubTUIBackend{
		initProfile: func(context.Context, string, string, cloudstic.BackupProfile, *cloudstic.ProfilesConfig) error {
			called = "init"
			return nil
		},
		backupProfile: func(context.Context, string, string, cloudstic.BackupProfile, *cloudstic.ProfilesConfig, cloudstic.Reporter) error {
			called = "backup"
			return nil
		},
	})
	svc.loadProfiles = func(string) (*cloudstic.ProfilesConfig, error) {
		return &cloudstic.ProfilesConfig{
			Version: 1,
			Profiles: map[string]cloudstic.BackupProfile{
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
		backupProfile: func(context.Context, string, string, cloudstic.BackupProfile, *cloudstic.ProfilesConfig, cloudstic.Reporter) error {
			called = "backup"
			return nil
		},
	})
	svc.loadProfiles = func(string) (*cloudstic.ProfilesConfig, error) {
		return &cloudstic.ProfilesConfig{
			Version: 1,
			Profiles: map[string]cloudstic.BackupProfile{
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
	svc.loadProfiles = func(string) (*cloudstic.ProfilesConfig, error) {
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
		checkProfile: func(context.Context, string, string, cloudstic.BackupProfile, *cloudstic.ProfilesConfig, cloudstic.Reporter) error {
			called = "check"
			return nil
		},
	})
	svc.loadProfiles = func(string) (*cloudstic.ProfilesConfig, error) {
		return &cloudstic.ProfilesConfig{
			Version: 1,
			Profiles: map[string]cloudstic.BackupProfile{
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
	svc.loadProfiles = func(string) (*cloudstic.ProfilesConfig, error) {
		return &cloudstic.ProfilesConfig{
			Version: 1,
			Profiles: map[string]cloudstic.BackupProfile{
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
