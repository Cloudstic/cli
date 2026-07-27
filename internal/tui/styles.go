package tui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// theme holds the lipgloss styles for the dashboard: titled panels, aligned
// columns, and semantic status colors with the correct hierarchy — the legacy
// renderer it replaced showed warning in cyan and left error uncolored, which
// inverted the visual hierarchy exactly where it mattered most (issue #341).
// Colors are adaptive for light/dark terminals.
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

// newTheme builds the dashboard theme for the current environment.
func newTheme() theme {
	return newThemeWithColor(colorEnabled())
}

// colorEnabled reports whether the dashboard should emit color. It honours the
// NO_COLOR convention (https://no-color.org) explicitly, on top of lipgloss's
// own terminal-capability detection, so the plain-text theme is reachable and
// testable without a terminal.
func colorEnabled() bool {
	return os.Getenv("NO_COLOR") == ""
}

// newThemeWithColor builds the theme with or without color. Without it, only
// the foreground colors drop: bold, underline, and the panel borders are not
// color and stay, so the layout and emphasis survive under NO_COLOR.
func newThemeWithColor(color bool) theme {
	subtle := lipgloss.AdaptiveColor{Light: "246", Dark: "244"}
	faint := lipgloss.AdaptiveColor{Light: "250", Dark: "240"}
	accent := lipgloss.AdaptiveColor{Light: "31", Dark: "45"}   // teal/cyan
	ready := lipgloss.AdaptiveColor{Light: "28", Dark: "42"}    // green
	warn := lipgloss.AdaptiveColor{Light: "130", Dark: "214"}   // amber
	danger := lipgloss.AdaptiveColor{Light: "160", Dark: "203"} // red

	// fg applies a foreground color, or nothing when color is disabled.
	fg := func(c lipgloss.TerminalColor) lipgloss.Style {
		if !color {
			return lipgloss.NewStyle()
		}
		return lipgloss.NewStyle().Foreground(c)
	}
	border := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if color {
		border = border.BorderForeground(faint)
	}

	return theme{
		title:      fg(accent).Bold(true),
		subtitle:   fg(subtle),
		subtle:     fg(subtle),
		label:      fg(subtle),
		value:      lipgloss.NewStyle(),
		accent:     fg(accent),
		selected:   fg(accent).Bold(true),
		marker:     fg(accent).Bold(true),
		key:        fg(accent).Bold(true),
		panel:      border,
		panelTitle: lipgloss.NewStyle().Bold(true),
		tabActive:  fg(accent).Bold(true).Underline(true),
		tabIdle:    fg(subtle),
		footer:     fg(subtle),
		footerKey:  fg(accent).Bold(true),

		stateReady:    fg(ready),
		stateWarning:  fg(warn),
		stateError:    fg(danger).Bold(true),
		stateDisabled: fg(subtle),
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
