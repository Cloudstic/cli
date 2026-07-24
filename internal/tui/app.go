package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/tui/forms"
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

	// Concurrent-probe state (issue #339). When cfg and prober are set, the
	// model probes every store concurrently on Init and rebuilds the
	// dashboard from probes as they arrive, so the first paint shows
	// "checking…" instead of blocking.
	cfg    *engine.ProfilesConfig
	prober StoreProber
	probes map[string]StoreProbe

	// Async action state (issue #339). runner executes profile actions off
	// the event loop; cancel stops the running one.
	runner   ActionRunner
	running  bool
	cancel   context.CancelFunc
	activity ActivityPanel

	// Profile management state (issue #340). forms is the domain backend;
	// form and confirm are the active overlay, at most one at a time.
	forms   FormsBackend
	form    *forms.Form
	confirm *confirmState

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
		probes:    map[string]StoreProbe{},
	}
	m.selected = indexOfSelected(d)
	return m
}

// WithConfig attaches the profiles config and a store prober, enabling
// concurrent probing on Init. The seeded dashboard should be a probe-less
// skeleton (BuildDashboard(cfg, nil)) so the first frame renders immediately.
func (m Model) WithConfig(cfg *engine.ProfilesConfig, prober StoreProber) Model {
	m.cfg = cfg
	m.prober = prober
	return m
}

// WithRunner attaches the async action runner used by the backup/check/init
// keys.
func (m Model) WithRunner(runner ActionRunner) Model {
	m.runner = runner
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

// Init kicks off concurrent store probing when the model is configured with a
// prober; otherwise it renders the seeded dashboard as-is.
func (m Model) Init() tea.Cmd {
	return m.probeAllCmd()
}

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
	case probeResultMsg:
		m.applyProbe(msg.name, msg.probe)
		return m, nil
	case actionUpdateMsg:
		return m.handleActionUpdate(msg)
	case configReloadedMsg:
		return m.handleConfigReloaded(msg)
	case tea.KeyMsg:
		if m.form != nil {
			return m.updateForm(msg)
		}
		if m.confirm != nil {
			return m.updateConfirm(msg)
		}
		return m.handleKey(msg)
	default:
		// Route other messages (e.g. textinput cursor blink) to an open form.
		if m.form != nil {
			return m.updateForm(msg)
		}
	}
	return m, nil
}

// applyProbe records a store probe result and rebuilds the derived dashboard
// from the accumulated probes, preserving the current selection.
func (m *Model) applyProbe(name string, probe StoreProbe) {
	if m.probes == nil {
		m.probes = map[string]StoreProbe{}
	}
	m.probes[name] = probe
	if m.cfg == nil {
		return
	}
	selected := m.selectedName()
	m.dashboard = BuildDashboard(m.cfg, m.probes)
	m.dashboard.SelectedProfile = selected
	m.selected = indexOfSelected(m.dashboard)
}

func (m Model) selectedName() string {
	if p, ok := m.currentProfile(); ok {
		return p.Name
	}
	return m.dashboard.SelectedProfile
}

func (m Model) handleActionUpdate(msg actionUpdateMsg) (tea.Model, tea.Cmd) {
	m.activity = msg.update.Panel
	if !msg.update.Done {
		return m, waitForAction(msg.ch)
	}
	m.running = false
	m.cancel = nil
	// Refresh the store backing the profile we just acted on so state
	// changes (e.g. a freshly initialized repository) show up.
	return m, m.probeSelectedStoreCmd()
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
		// Cancel a running action before leaving so we don't orphan it.
		m.cancelAction()
		return m, tea.Quit
	case "x":
		if m.running {
			m.cancelAction()
		}
		return m, nil
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
	case "b", "c":
		return m.startAction(msg.String())
	case "n":
		return m.openCreateProfile()
	case "e":
		return m.openEditProfile()
	case "d":
		return m.openDeleteProfile()
	case "r":
		return m, m.reload
	}
	return m, nil
}

func (m Model) View() string {
	if m.form != nil {
		return strings.Join([]string{m.renderHeader(), m.form.View(), m.renderFooter()}, "\n")
	}
	if m.confirm != nil {
		return strings.Join([]string{m.renderHeader(), m.renderConfirm(), m.renderFooter()}, "\n")
	}
	header := m.renderHeader()
	body := lipgloss.JoinHorizontal(lipgloss.Top, m.renderProfileList(), m.renderDetail())
	sections := []string{header, body}
	if activity := m.renderActivity(); activity != "" {
		sections = append(sections, activity)
	}
	sections = append(sections, m.renderFooter())
	return strings.Join(sections, "\n")
}

func (m Model) renderConfirm() string {
	c := m.confirm
	lines := []string{
		m.theme.title.Render(c.title),
		"",
		c.message,
		"",
		m.theme.subtle.Render("Enter/y delete · Esc/n cancel"),
	}
	return m.theme.box.Render(strings.Join(lines, "\n"))
}

func (m Model) renderActivity() string {
	a := m.activity
	if a.Status == ActivityStatusIdle && a.Action == "" {
		return ""
	}
	label := a.Action
	if label == "" {
		label = string(a.ActionKind)
	}
	var head string
	switch a.Status {
	case ActivityStatusRunning:
		head = m.theme.stateWarning.Render("● running") + "  " + label
	case ActivityStatusSuccess:
		head = m.theme.stateReady.Render("✓ done") + "  " + label
	case ActivityStatusError:
		head = m.theme.stateError.Render("✗ failed") + "  " + label
	default:
		head = label
	}
	lines := []string{head}
	if a.Phase != "" {
		lines = append(lines, m.theme.subtle.Render(a.Phase))
	}
	if bar := progressBarLine(a, 30); bar != "" {
		lines = append(lines, bar)
	}
	if a.Summary != "" {
		lines = append(lines, m.theme.subtle.Render(a.Summary))
	}
	return m.theme.box.Render(strings.Join(lines, "\n"))
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
	var hints string
	switch {
	case m.form != nil, m.confirm != nil:
		// The overlay renders its own key hints.
		return ""
	case m.running:
		hints = "x cancel · running…"
	default:
		hints = m.actionHints() + m.formHints() + "↑/↓ select · s/h view · r refresh · q quit"
	}
	if m.loadErr != "" {
		return m.theme.stateError.Render("error: "+m.loadErr) + "\n" + m.theme.footer.Render(hints)
	}
	return m.theme.footer.Render(hints)
}

// formHints advertises the profile-management keys when the forms backend is
// wired in.
func (m Model) formHints() string {
	if m.forms == nil {
		return ""
	}
	if _, ok := m.currentProfile(); ok {
		return "n new · e edit · d delete · "
	}
	return "n new · "
}

// actionHints lists the enabled action keys for the selected profile so the
// footer advertises only actions that will actually run.
func (m Model) actionHints() string {
	if m.runner == nil {
		return ""
	}
	profile, ok := m.currentProfile()
	if !ok {
		return ""
	}
	var parts []string
	for _, action := range profile.Actions {
		if !action.Enabled {
			continue
		}
		switch action.Kind {
		case ActionKindInit:
			parts = append(parts, "b init")
		case ActionKindBackup:
			parts = append(parts, "b backup")
		case ActionKindCheck:
			parts = append(parts, "c check")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ") + " · "
}

func (m Model) currentProfile() (ProfileCard, bool) {
	if m.selected < 0 || m.selected >= len(m.dashboard.Profiles) {
		return ProfileCard{}, false
	}
	return m.dashboard.Profiles[m.selected], true
}
