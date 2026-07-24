package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudstic/cli/internal/engine"
)

type stubProber struct {
	results map[string]StoreProbe
}

func (p stubProber) Probe(_ context.Context, name string, _ engine.ProfileStore) StoreProbe {
	if probe, ok := p.results[name]; ok {
		return probe
	}
	return StoreProbe{Status: "error", Error: "no result"}
}

func probeConfig() *engine.ProfilesConfig {
	enabled := true
	return &engine.ProfilesConfig{
		Stores: map[string]engine.ProfileStore{
			"remote": {URI: "s3:bucket/prod"},
		},
		Profiles: map[string]engine.BackupProfile{
			"alpha": {Source: "local:/tmp/alpha", Store: "remote", Enabled: &enabled},
		},
	}
}

func TestModel_InitProbesConfiguredStores(t *testing.T) {
	cfg := probeConfig()
	prober := stubProber{results: map[string]StoreProbe{"remote": {Status: "ok"}}}
	skeleton := BuildDashboard(cfg, nil)
	m := NewModel(skeleton).WithConfig(cfg, prober)

	// First frame: store health is pending → "checking store".
	if !strings.Contains(m.View(), "checking store") {
		t.Fatalf("first frame missing pending badge\n%s", m.View())
	}

	cmd := m.Init()
	if cmd == nil {
		t.Fatalf("Init returned no probe command")
	}
	msg := cmd()
	next, _ := m.Update(msg)
	m = next.(Model)

	if got := m.dashboard.Profiles[0].StoreHealth; got != StoreHealthReady {
		t.Fatalf("store health=%q want ready after probe", got)
	}
}

func TestModel_InitWithoutConfigIsNoOp(t *testing.T) {
	m := NewModel(testDashboard())
	if cmd := m.Init(); cmd != nil {
		t.Fatalf("Init without a prober should return nil")
	}
}

func TestModel_ProbeResultPreservesSelection(t *testing.T) {
	cfg := probeConfig()
	prober := stubProber{results: map[string]StoreProbe{"remote": {Status: "ok"}}}
	m := NewModel(BuildDashboard(cfg, nil)).WithConfig(cfg, prober)
	m.dashboard.SelectedProfile = "alpha"
	m.selected = 0

	next, _ := m.Update(probeResultMsg{name: "remote", probe: StoreProbe{Status: "error", Error: "unreachable"}})
	m = next.(Model)

	if name := m.selectedName(); name != "alpha" {
		t.Fatalf("selection=%q want alpha preserved across probe", name)
	}
	if got := m.dashboard.Profiles[0].Status; got != ProfileStatusWarning {
		t.Fatalf("status=%q want warning after failed probe", got)
	}
}

func TestProbeStoreCmd_UsesTimeout(t *testing.T) {
	var gotDeadline bool
	prober := deadlineProber{seen: &gotDeadline}
	cmd := probeStoreCmd(prober, "remote", engine.ProfileStore{})
	msg := cmd()
	if _, ok := msg.(probeResultMsg); !ok {
		t.Fatalf("probeStoreCmd returned %T want probeResultMsg", msg)
	}
	if !gotDeadline {
		t.Fatalf("probe ctx had no deadline; per-store timeout not applied")
	}
}

type deadlineProber struct {
	seen *bool
}

func (p deadlineProber) Probe(ctx context.Context, _ string, _ engine.ProfileStore) StoreProbe {
	if _, ok := ctx.Deadline(); ok {
		*p.seen = true
	}
	return StoreProbe{Status: "ok"}
}

var _ tea.Msg = probeResultMsg{}
