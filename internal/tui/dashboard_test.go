package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
)

func TestBuildDashboard_SortsProfilesAndCountsSections(t *testing.T) {
	enabled := true
	disabled := false
	cfg := &engine.ProfilesConfig{
		Stores: map[string]engine.ProfileStore{
			"remote": {URI: "s3:bucket/prod"},
		},
		Auth: map[string]engine.ProfileAuth{
			"google-work": {Provider: "google"},
		},
		Profiles: map[string]engine.BackupProfile{
			"zeta": {
				Source:  "local:/tmp/zeta",
				Store:   "remote",
				Enabled: &disabled,
			},
			"alpha": {
				Source:  "local:/tmp/alpha",
				Store:   "remote",
				AuthRef: "google-work",
				Enabled: &enabled,
			},
		},
	}

	got := BuildDashboard(cfg, map[string]StoreProbe{
		"remote": {
			Status: "ok",
			Snapshots: []engine.SnapshotEntry{
				{
					Ref:     "snapshot/abc",
					Created: mustTime(t, "2026-04-03T10:30:00Z"),
					Snap: core.Snapshot{
						Source: &core.SourceInfo{Type: "local", Path: "/tmp/alpha"},
					},
				},
			},
		},
	})
	if got.ProfileCount != 2 || got.StoreCount != 1 || got.AuthCount != 1 {
		t.Fatalf("unexpected counts: %+v", got)
	}
	if len(got.Profiles) != 2 {
		t.Fatalf("profiles=%d want 2", len(got.Profiles))
	}
	if got.Profiles[0].Name != "alpha" || got.Profiles[1].Name != "zeta" {
		t.Fatalf("profiles not sorted: %+v", got.Profiles)
	}
	if !got.Profiles[0].Enabled {
		t.Fatalf("alpha should be enabled")
	}
	if got.Profiles[1].Enabled {
		t.Fatalf("zeta should be disabled")
	}
	if got.Profiles[0].LastRef != "snapshot/abc" {
		t.Fatalf("last ref = %q want snapshot/abc", got.Profiles[0].LastRef)
	}
	if got.Profiles[0].Status != "ready" {
		t.Fatalf("status = %q want ready", got.Profiles[0].Status)
	}
	if got.Profiles[1].Status != "disabled" {
		t.Fatalf("status = %q want disabled", got.Profiles[1].Status)
	}
}

func TestBuildDashboard_NormalizesStoreProbeErrors(t *testing.T) {
	cfg := &engine.ProfilesConfig{
		Stores: map[string]engine.ProfileStore{
			"1": {URI: "local:/tmp/store"},
		},
		Profiles: map[string]engine.BackupProfile{
			"desktop": {
				Source: "local:/tmp/Desktop",
				Store:  "1",
			},
		},
	}

	got := BuildDashboard(cfg, map[string]StoreProbe{
		"1": {
			Status: "error",
			Error:  "1: repository not initialized -- run 'cloudstic init' first",
		},
	})
	if len(got.Profiles) != 1 {
		t.Fatalf("profiles=%d want 1", len(got.Profiles))
	}
	if got.Profiles[0].Status != "warning" {
		t.Fatalf("status=%q want warning", got.Profiles[0].Status)
	}
	if got.Profiles[0].StatusNote != "repository not initialized" {
		t.Fatalf("status note=%q want repository not initialized", got.Profiles[0].StatusNote)
	}
}

func TestBuildDashboardFromConfig_LoadsStoreSnapshots(t *testing.T) {
	cfg := &engine.ProfilesConfig{
		Stores: map[string]engine.ProfileStore{
			"remote": {URI: "s3:bucket/prod"},
		},
		Profiles: map[string]engine.BackupProfile{
			"docs": {Source: "local:/docs", Store: "remote"},
		},
	}

	got := BuildDashboardFromConfig(context.Background(), cfg, func(_ context.Context, name string, _ engine.ProfileStore) ([]engine.SnapshotEntry, error) {
		if name != "remote" {
			t.Fatalf("unexpected store %q", name)
		}
		return []engine.SnapshotEntry{{
			Ref:     "snapshot/1",
			Created: mustTime(t, "2026-04-03T10:00:00Z"),
			Snap: core.Snapshot{
				Source: &core.SourceInfo{Type: "local", Path: "/docs"},
			},
		}}, nil
	})
	if len(got.Profiles) != 1 || got.Profiles[0].LastRef != "snapshot/1" {
		t.Fatalf("unexpected dashboard: %+v", got)
	}
}

func TestBuildDashboardFromConfig_StoreErrorBecomesWarning(t *testing.T) {
	cfg := &engine.ProfilesConfig{
		Stores: map[string]engine.ProfileStore{
			"remote": {URI: "s3:bucket/prod"},
		},
		Profiles: map[string]engine.BackupProfile{
			"docs": {Source: "local:/docs", Store: "remote"},
		},
	}

	got := BuildDashboardFromConfig(context.Background(), cfg, func(context.Context, string, engine.ProfileStore) ([]engine.SnapshotEntry, error) {
		return nil, errors.New("unlock failed")
	})
	if got.Profiles[0].Status != "warning" || got.Profiles[0].StatusNote != "unlock failed" {
		t.Fatalf("unexpected profile status: %+v", got.Profiles[0])
	}
}

func mustTime(t *testing.T, raw string) time.Time {
	t.Helper()
	got, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("time.Parse: %v", err)
	}
	return got
}
