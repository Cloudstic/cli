package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudstic/cli/internal/engine"
)

func testDashboard() Dashboard {
	enabled := true
	disabled := false
	cfg := &engine.ProfilesConfig{
		Stores: map[string]engine.ProfileStore{
			"remote": {URI: "s3:bucket/prod"},
		},
		Profiles: map[string]engine.BackupProfile{
			"alpha": {Source: "local:/tmp/alpha", Store: "remote", Enabled: &enabled},
			"zeta":  {Source: "local:/tmp/zeta", Store: "remote", Enabled: &disabled},
		},
	}
	d := BuildDashboard(cfg, map[string]StoreProbe{"remote": {Status: "ok"}})
	d.SelectedProfile = "alpha"
	return d
}

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func update(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func TestNewModel_SelectsDashboardProfile(t *testing.T) {
	m := NewModel(testDashboard())
	if m.selected != 0 {
		t.Fatalf("selected=%d want 0 (alpha)", m.selected)
	}
	if got, _ := m.currentProfile(); got.Name != "alpha" {
		t.Fatalf("currentProfile=%q want alpha", got.Name)
	}
}

func TestModel_View_RendersProfilesAndCounts(t *testing.T) {
	m := NewModel(testDashboard())
	view := m.View()
	for _, want := range []string{"alpha", "zeta", "Profiles 2", "Stores 1", "local:/tmp/alpha"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q\n%s", want, view)
		}
	}
}

func TestModel_Navigation_MovesSelection(t *testing.T) {
	m := NewModel(testDashboard())
	m = update(m, keyRunes("j"))
	if m.selected != 1 {
		t.Fatalf("after down selected=%d want 1", m.selected)
	}
	// Clamp at the bottom.
	m = update(m, keyRunes("j"))
	if m.selected != 1 {
		t.Fatalf("down past end selected=%d want 1", m.selected)
	}
	m = update(m, keyRunes("k"))
	if m.selected != 0 {
		t.Fatalf("after up selected=%d want 0", m.selected)
	}
	// Clamp at the top.
	m = update(m, keyRunes("k"))
	if m.selected != 0 {
		t.Fatalf("up past start selected=%d want 0", m.selected)
	}
}

func TestModel_TabSwitchesView(t *testing.T) {
	m := NewModel(testDashboard())
	if m.view != ProfileViewSummary {
		t.Fatalf("initial view=%q want summary", m.view)
	}
	m = update(m, keyRunes("h"))
	if m.view != ProfileViewHistory {
		t.Fatalf("after h view=%q want history", m.view)
	}
	if !strings.Contains(m.View(), "No snapshots for this profile") {
		t.Fatalf("history view missing empty-state\n%s", m.View())
	}
	m = update(m, keyRunes("s"))
	if m.view != ProfileViewSummary {
		t.Fatalf("after s view=%q want summary", m.view)
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.view != ProfileViewHistory {
		t.Fatalf("after tab view=%q want history", m.view)
	}
}

func TestModel_QuitKeys(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		keyRunes("q"),
		{Type: tea.KeyCtrlC},
		{Type: tea.KeyEsc},
	} {
		_, cmd := NewModel(testDashboard()).Update(key)
		if cmd == nil {
			t.Fatalf("key %v produced no command; want tea.Quit", key)
		}
		if msg := cmd(); msg != tea.Quit() {
			t.Fatalf("key %v did not map to tea.Quit", key)
		}
	}
}

func TestModel_DashboardLoadedMsg_ReplacesState(t *testing.T) {
	m := NewModel(Dashboard{})
	m = update(m, DashboardLoadedMsg{Dashboard: testDashboard()})
	if m.dashboard.ProfileCount != 2 {
		t.Fatalf("ProfileCount=%d want 2 after reload", m.dashboard.ProfileCount)
	}
	if !strings.Contains(m.View(), "alpha") {
		t.Fatalf("view missing reloaded profile\n%s", m.View())
	}
}

func TestModel_DashboardLoadError_ShownInFooter(t *testing.T) {
	m := NewModel(testDashboard())
	m = update(m, DashboardLoadError{Err: errString("probe boom")})
	if !strings.Contains(m.View(), "probe boom") {
		t.Fatalf("view missing load error\n%s", m.View())
	}
}

func TestModel_EmptyDashboard(t *testing.T) {
	m := NewModel(Dashboard{})
	view := m.View()
	if !strings.Contains(view, "No profiles configured") {
		t.Fatalf("empty view missing placeholder\n%s", view)
	}
	// Navigation on an empty dashboard must not panic or move out of range.
	m = update(m, keyRunes("j"))
	if m.selected != 0 {
		t.Fatalf("empty selected=%d want 0", m.selected)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
