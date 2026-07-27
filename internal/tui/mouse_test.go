package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func mouseDashboard() Dashboard {
	ready := deriveProfileActions(ProfileStatusReady, StoreHealthReady)
	return Dashboard{
		ProfileCount: 3, StoreCount: 1, SelectedProfile: "alpha",
		Profiles: []ProfileCard{
			{Name: "alpha", Source: "local:/a", StoreRef: "s1", Enabled: true, Status: ProfileStatusReady,
				StoreHealth: StoreHealthReady, Reachability: StoreReachabilityReachable, Repository: RepositoryStateInitialized, Actions: ready},
			{Name: "beta", Source: "local:/b", StoreRef: "s1", Enabled: true, Status: ProfileStatusReady,
				StoreHealth: StoreHealthReady, Reachability: StoreReachabilityReachable, Repository: RepositoryStateInitialized, Actions: ready},
			{Name: "gamma", Source: "local:/c", StoreRef: "s1", Enabled: true, Status: ProfileStatusReady,
				StoreHealth: StoreHealthReady, Reachability: StoreReachabilityReachable, Repository: RepositoryStateInitialized, Actions: ready},
		},
	}
}

func leftClick(m Model, x, y int) Model {
	next, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	return next.(Model)
}

// firstRow returns an absolute Y whose recorded profile index matches idx.
func profileRowY(m Model, idx int) (int, bool) {
	for y, i := range m.regions.profileRows {
		if i == idx {
			return y, true
		}
	}
	return 0, false
}

func actionRowY(m Model, key string) (int, bool) {
	for y, k := range m.regions.actionRows {
		if k == key {
			return y, true
		}
	}
	return 0, false
}

func TestMouse_ClickProfileSelects(t *testing.T) {
	m := NewModel(mouseDashboard())
	m.width = 92
	_ = m.View() // populate regions
	y, ok := profileRowY(m, 2)
	if !ok {
		t.Fatalf("no recorded row for profile 2\nregions=%v", m.regions.profileRows)
	}
	m = leftClick(m, 3, y)
	if m.selected != 2 {
		t.Fatalf("selected=%d want 2 after clicking gamma's row", m.selected)
	}
}

func TestMouse_ClickOutsidePanelIgnored(t *testing.T) {
	m := NewModel(mouseDashboard())
	m.width = 92
	_ = m.View()
	y, _ := profileRowY(m, 2)
	// Same row, but an x far to the right of the profiles panel.
	m = leftClick(m, m.regions.profilesX1+5, y)
	if m.selected != 0 {
		t.Fatalf("selected=%d want 0 (click outside the profiles panel should be ignored)", m.selected)
	}
}

func TestMouse_ClickActionButtonStartsAction(t *testing.T) {
	runner := &stubRunner{updates: []ActionUpdate{
		{Panel: ActivityPanel{Status: ActivityStatusRunning}},
		{Panel: ActivityPanel{Status: ActivityStatusSuccess}, Done: true},
	}}
	m := NewModel(mouseDashboard()).WithRunner(runner)
	m.width = 92
	_ = m.View()
	y, ok := actionRowY(m, "b")
	if !ok {
		t.Fatalf("no recorded action row for b\nregions=%v", m.regions.actionRows)
	}
	x := m.regions.selectionX0 + 3
	next, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m = next.(Model)
	if !m.running {
		t.Fatalf("clicking the backup button should start an action")
	}
	if runner.started != ActionKindBackup {
		t.Fatalf("started=%q want backup", runner.started)
	}
	if cmd == nil {
		t.Fatalf("action click should return a listen command")
	}
}

func TestMouse_ClickEditButtonOpensForm(t *testing.T) {
	m := NewModel(mouseDashboard()).WithForms(&stubFormsBackend{stores: []string{"s1"}})
	m.width = 92
	_ = m.View()
	y, ok := actionRowY(m, "e")
	if !ok {
		t.Fatalf("no recorded action row for e")
	}
	m = leftClick(m, m.regions.selectionX0+3, y)
	if m.activeForm() == nil {
		t.Fatalf("clicking edit should open the profile form")
	}
}

func TestMouse_WheelScrollsSelection(t *testing.T) {
	m := NewModel(mouseDashboard())
	m.width = 92
	next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m = next.(Model)
	if m.selected != 1 {
		t.Fatalf("wheel down selected=%d want 1", m.selected)
	}
	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = next.(Model)
	if m.selected != 0 {
		t.Fatalf("wheel up selected=%d want 0", m.selected)
	}
}

func TestMouse_IgnoredWhileFormOpen(t *testing.T) {
	m := NewModel(mouseDashboard()).WithForms(&stubFormsBackend{stores: []string{"s1"}})
	m.width = 92
	_ = m.View()
	y, _ := profileRowY(m, 2)
	// Open a form, then a click must not change the underlying selection.
	m, _ = m.openCreateProfile()
	_ = m.View() // clears regions
	m = leftClick(m, 3, y)
	if m.selected != 0 {
		t.Fatalf("clicks must be ignored while a form is open; selected=%d", m.selected)
	}
}
