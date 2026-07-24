package tui

import tea "github.com/charmbracelet/bubbletea"

// clickRegions records the clickable rows of the last render so mouse clicks
// map back to profiles and action buttons. Coordinates are absolute terminal
// cells (0-indexed). It is populated during View and read during Update, which
// run sequentially on the Bubble Tea event loop.
type clickRegions struct {
	profileRows map[int]int    // absolute Y -> profile index
	actionRows  map[int]string // absolute Y -> action key
	// Column ranges [x0, x1) the profile / selection panels occupy, used to
	// disambiguate the two side-by-side panels that share Y coordinates.
	profilesX0, profilesX1   int
	selectionX0, selectionX1 int
}

func newClickRegions() *clickRegions {
	return &clickRegions{
		profileRows: map[int]int{},
		actionRows:  map[int]string{},
	}
}

func (r *clickRegions) reset() {
	if r == nil {
		return
	}
	clear(r.profileRows)
	clear(r.actionRows)
	r.profilesX0, r.profilesX1 = 0, 0
	r.selectionX0, r.selectionX1 = 0, 0
}

func (r *clickRegions) profileAt(x, y int) (int, bool) {
	if r == nil {
		return 0, false
	}
	idx, ok := r.profileRows[y]
	if !ok || x < r.profilesX0 || x >= r.profilesX1 {
		return 0, false
	}
	return idx, true
}

func (r *clickRegions) actionAt(x, y int) (string, bool) {
	if r == nil {
		return "", false
	}
	key, ok := r.actionRows[y]
	if !ok || x < r.selectionX0 || x >= r.selectionX1 {
		return "", false
	}
	return key, true
}

// handleMouse routes a mouse event: wheel scrolls the profile selection, and a
// left click selects a profile or triggers an action button.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Overlays own the screen; the dashboard is not clickable underneath.
	if m.activeForm() != nil || m.confirm != nil {
		return m, nil
	}
	switch {
	case msg.Button == tea.MouseButtonWheelUp:
		if m.selected > 0 {
			m.selected--
		}
		return m, nil
	case msg.Button == tea.MouseButtonWheelDown:
		if m.selected < len(m.dashboard.Profiles)-1 {
			m.selected++
		}
		return m, nil
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		return m.handleClick(msg.X, msg.Y)
	}
	return m, nil
}

func (m Model) handleClick(x, y int) (tea.Model, tea.Cmd) {
	if idx, ok := m.regions.profileAt(x, y); ok {
		if idx >= 0 && idx < len(m.dashboard.Profiles) {
			m.selected = idx
		}
		return m, nil
	}
	if key, ok := m.regions.actionAt(x, y); ok {
		switch key {
		case "b", "c":
			return m.startAction(key)
		case "e":
			return m.openEditProfile()
		case "d":
			return m.openDeleteProfile()
		}
	}
	return m, nil
}
