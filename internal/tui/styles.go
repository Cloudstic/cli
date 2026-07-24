package tui

import "github.com/charmbracelet/lipgloss"

// theme holds the lipgloss styles for the Bubble Tea renderer. It is
// intentionally small for the read-only foundation (issue #338); the full
// adaptive light/dark, NO_COLOR-aware theme lands with the legacy engine
// deletion (issue #341).
type theme struct {
	title       lipgloss.Style
	subtle      lipgloss.Style
	label       lipgloss.Style
	value       lipgloss.Style
	selected    lipgloss.Style
	box         lipgloss.Style
	tabActive   lipgloss.Style
	tabInactive lipgloss.Style
	footer      lipgloss.Style

	stateReady    lipgloss.Style
	stateWarning  lipgloss.Style
	stateError    lipgloss.Style
	stateDisabled lipgloss.Style
}

func newTheme() theme {
	subtle := lipgloss.AdaptiveColor{Light: "245", Dark: "241"}
	return theme{
		title:       lipgloss.NewStyle().Bold(true),
		subtle:      lipgloss.NewStyle().Foreground(subtle),
		label:       lipgloss.NewStyle().Foreground(subtle),
		value:       lipgloss.NewStyle(),
		selected:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		box:         lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(subtle).Padding(0, 1),
		tabActive:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		tabInactive: lipgloss.NewStyle().Foreground(subtle),
		footer:      lipgloss.NewStyle().Foreground(subtle),

		// Semantic status colors, with the correct hierarchy (unlike the
		// legacy renderer, where warning showed cyan and error was
		// uncolored — see issue #341).
		stateReady:    lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		stateWarning:  lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		stateError:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1")),
		stateDisabled: lipgloss.NewStyle().Foreground(subtle),
	}
}

// stateStyle maps a profile status to its semantic style.
func (t theme) stateStyle(status ProfileStatus) lipgloss.Style {
	switch status {
	case ProfileStatusError:
		return t.stateError
	case ProfileStatusWarning:
		return t.stateWarning
	case ProfileStatusDisabled:
		return t.stateDisabled
	default:
		return t.stateReady
	}
}
