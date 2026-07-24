package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Model is the root Bubble Tea model for the interactive dashboard. This is
// the read-only foundation from issue #338: it renders the existing
// derived Dashboard view-model and supports navigation, but performs no
// actions or mutations. Async actions and forms land in later Phase 2
// stories (#339, #340).
type Model struct {
	dashboard Dashboard
	selected  int
	view      ProfileView
	width     int
	height    int
	theme     theme
	loadErr   string

	// reload, when non-nil, is run when the user presses "r". It returns a
	// DashboardLoadedMsg or DashboardLoadError. The read-only launcher can
	// leave it nil for a static snapshot.
	reload tea.Cmd
}

// NewModel builds a root model seeded with an already-derived dashboard.
func NewModel(d Dashboard) Model {
	m := Model{
		dashboard: d,
		view:      ProfileViewSummary,
		theme:     newTheme(),
	}
	m.selected = indexOfSelected(d)
	return m
}

// WithReload attaches a command used to refresh the dashboard on "r".
func (m Model) WithReload(cmd tea.Cmd) Model {
	m.reload = cmd
	return m
}

func indexOfSelected(d Dashboard) int {
	for i, p := range d.Profiles {
		if p.Name == d.SelectedProfile {
			return i
		}
	}
	return 0
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case DashboardLoadedMsg:
		m.dashboard = msg.Dashboard
		m.loadErr = ""
		if m.selected >= len(m.dashboard.Profiles) {
			m.selected = clampSelection(len(m.dashboard.Profiles))
		}
		return m, nil
	case DashboardLoadError:
		if msg.Err != nil {
			m.loadErr = msg.Err.Error()
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func clampSelection(n int) int {
	if n <= 0 {
		return 0
	}
	return n - 1
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
		return m, nil
	case "down", "j":
		if m.selected < len(m.dashboard.Profiles)-1 {
			m.selected++
		}
		return m, nil
	case "s":
		m.view = ProfileViewSummary
		return m, nil
	case "h":
		m.view = ProfileViewHistory
		return m, nil
	case "tab":
		if m.view == ProfileViewSummary {
			m.view = ProfileViewHistory
		} else {
			m.view = ProfileViewSummary
		}
		return m, nil
	case "r":
		return m, m.reload
	}
	return m, nil
}

func (m Model) View() string {
	header := m.renderHeader()
	body := lipgloss.JoinHorizontal(lipgloss.Top, m.renderProfileList(), m.renderDetail())
	footer := m.renderFooter()
	return strings.Join([]string{header, body, footer}, "\n")
}

func (m Model) renderHeader() string {
	title := m.theme.title.Render("cloudstic")
	counts := m.theme.subtle.Render(fmt.Sprintf(
		"%d profiles · %d stores · %d auth",
		m.dashboard.ProfileCount, m.dashboard.StoreCount, m.dashboard.AuthCount,
	))
	return title + "  " + counts
}

func (m Model) renderProfileList() string {
	if len(m.dashboard.Profiles) == 0 {
		return m.theme.box.Render(m.theme.subtle.Render("No profiles configured"))
	}
	lines := make([]string, 0, len(m.dashboard.Profiles))
	for i, profile := range m.dashboard.Profiles {
		marker := "  "
		name := profile.Name
		if i == m.selected {
			marker = m.theme.selected.Render("> ")
			name = m.theme.selected.Render(name)
		}
		state := m.theme.stateStyle(profile.Status).Render(plainProfileStateLabel(profile))
		lines = append(lines, fmt.Sprintf("%s%s [%s]", marker, name, state))
	}
	return m.theme.box.Render(strings.Join(lines, "\n"))
}

func (m Model) renderDetail() string {
	profile, ok := m.currentProfile()
	if !ok {
		return m.theme.box.Render(m.theme.subtle.Render("Select a profile"))
	}
	tabs := m.renderTabs()
	var body []string
	switch m.view {
	case ProfileViewHistory:
		body = m.renderHistory(profile)
	default:
		body = m.renderSummary(profile)
	}
	content := append([]string{tabs, ""}, body...)
	return m.theme.box.Render(strings.Join(content, "\n"))
}

func (m Model) renderTabs() string {
	summary := "[s] Summary"
	history := "[h] History"
	switch m.view {
	case ProfileViewHistory:
		history = m.theme.tabActive.Render(history)
		summary = m.theme.tabInactive.Render(summary)
	default:
		summary = m.theme.tabActive.Render(summary)
		history = m.theme.tabInactive.Render(history)
	}
	return summary + "  " + history
}

func (m Model) renderSummary(profile ProfileCard) []string {
	rows := [][2]string{
		{"Source", profile.Source},
		{"Store", profile.StoreRef},
	}
	if profile.AuthRef != "" {
		rows = append(rows, [2]string{"Auth", profile.AuthRef})
	}
	rows = append(rows, [2]string{"Health", m.styledHealth(profile)})
	if last := lastBackupLabel(profile); last != "" {
		rows = append(rows, [2]string{"Last backup", last})
	}
	if profile.LastRef != "" {
		rows = append(rows, [2]string{"Last ref", trimSnapshotRef(profile.LastRef)})
	}
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, m.theme.label.Render(fmt.Sprintf("%-12s", row[0]))+" "+row[1])
	}
	return lines
}

func (m Model) styledHealth(profile ProfileCard) string {
	summary := profileHealthSummary(profile)
	return m.theme.stateStyle(profile.Status).Render(summary)
}

func lastBackupLabel(profile ProfileCard) string {
	if profile.BackupState == BackupFreshnessNever || profile.LastBackup == "" {
		return "never"
	}
	if fresh := backupFreshnessLabel(profile.BackupState); fresh != "" {
		return fmt.Sprintf("%s (%s)", profile.LastBackup, fresh)
	}
	return profile.LastBackup
}

func (m Model) renderHistory(profile ProfileCard) []string {
	if len(profile.History) == 0 {
		return []string{m.theme.subtle.Render("No snapshots for this profile")}
	}
	lines := make([]string, 0, len(profile.History))
	for _, snap := range profile.History {
		lines = append(lines, fmt.Sprintf("%s  %s", snap.Created, trimSnapshotRef(snap.Ref)))
	}
	return lines
}

func (m Model) renderFooter() string {
	hints := "↑/↓ select · s/h view · r refresh · q quit"
	if m.loadErr != "" {
		return m.theme.stateError.Render("error: "+m.loadErr) + "\n" + m.theme.footer.Render(hints)
	}
	return m.theme.footer.Render(hints)
}

func (m Model) currentProfile() (ProfileCard, bool) {
	if m.selected < 0 || m.selected >= len(m.dashboard.Profiles) {
		return ProfileCard{}, false
	}
	return m.dashboard.Profiles[m.selected], true
}
