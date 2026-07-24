package tui

import "github.com/charmbracelet/lipgloss"

// theme holds the lipgloss styles for the Bubble Tea renderer: titled panels,
// aligned columns, and semantic status colors with the correct hierarchy
// (unlike the legacy renderer, where warning showed cyan and error was
// uncolored — see issue #341). Colors are adaptive for light/dark terminals.
type theme struct {
	title      lipgloss.Style
	subtitle   lipgloss.Style
	subtle     lipgloss.Style
	label      lipgloss.Style
	value      lipgloss.Style
	accent     lipgloss.Style
	selected   lipgloss.Style
	marker     lipgloss.Style
	key        lipgloss.Style
	panel      lipgloss.Style
	panelTitle lipgloss.Style
	tabActive  lipgloss.Style
	tabIdle    lipgloss.Style
	footer     lipgloss.Style
	footerKey  lipgloss.Style

	stateReady    lipgloss.Style
	stateWarning  lipgloss.Style
	stateError    lipgloss.Style
	stateDisabled lipgloss.Style
}

func newTheme() theme {
	subtle := lipgloss.AdaptiveColor{Light: "246", Dark: "244"}
	faint := lipgloss.AdaptiveColor{Light: "250", Dark: "240"}
	accent := lipgloss.AdaptiveColor{Light: "31", Dark: "45"}   // teal/cyan
	ready := lipgloss.AdaptiveColor{Light: "28", Dark: "42"}    // green
	warn := lipgloss.AdaptiveColor{Light: "130", Dark: "214"}   // amber
	danger := lipgloss.AdaptiveColor{Light: "160", Dark: "203"} // red

	return theme{
		title:      lipgloss.NewStyle().Bold(true).Foreground(accent),
		subtitle:   lipgloss.NewStyle().Foreground(subtle),
		subtle:     lipgloss.NewStyle().Foreground(subtle),
		label:      lipgloss.NewStyle().Foreground(subtle),
		value:      lipgloss.NewStyle(),
		accent:     lipgloss.NewStyle().Foreground(accent),
		selected:   lipgloss.NewStyle().Bold(true).Foreground(accent),
		marker:     lipgloss.NewStyle().Bold(true).Foreground(accent),
		key:        lipgloss.NewStyle().Bold(true).Foreground(accent),
		panel:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(faint).Padding(0, 1),
		panelTitle: lipgloss.NewStyle().Bold(true),
		tabActive:  lipgloss.NewStyle().Bold(true).Foreground(accent).Underline(true),
		tabIdle:    lipgloss.NewStyle().Foreground(subtle),
		footer:     lipgloss.NewStyle().Foreground(subtle),
		footerKey:  lipgloss.NewStyle().Bold(true).Foreground(accent),

		stateReady:    lipgloss.NewStyle().Foreground(ready),
		stateWarning:  lipgloss.NewStyle().Foreground(warn),
		stateError:    lipgloss.NewStyle().Bold(true).Foreground(danger),
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
