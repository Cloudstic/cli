package forms

import "github.com/charmbracelet/lipgloss"

// formTheme holds the lipgloss styles for form rendering. It mirrors the small
// theme used by the dashboard model; the shared adaptive/NO_COLOR theme lands
// with the legacy-engine deletion (issue #341).
type formTheme struct {
	title       lipgloss.Style
	subtle      lipgloss.Style
	label       lipgloss.Style
	marker      lipgloss.Style
	selectStyle lipgloss.Style
	errStyle    lipgloss.Style
	box         lipgloss.Style
}

func newFormTheme() formTheme {
	subtle := lipgloss.AdaptiveColor{Light: "245", Dark: "241"}
	return formTheme{
		title:       lipgloss.NewStyle().Bold(true),
		subtle:      lipgloss.NewStyle().Foreground(subtle),
		label:       lipgloss.NewStyle().Foreground(subtle),
		marker:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		selectStyle: lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		errStyle:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1")),
		box:         lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(subtle).Padding(0, 1),
	}
}
