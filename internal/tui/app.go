package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/cloudstic/cli/pkg/profile"

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

	// Concurrent-probe state (issue #339). When cfg and prober are set, the
	// model probes every store concurrently on Init and rebuilds the
	// dashboard from probes as they arrive, so the first paint shows
	// "checking…" instead of blocking.
	cfg    *profile.Config
	prober StoreProber
	probes map[string]StoreProbe

	// Async action state (issue #339). runner executes profile actions off
	// the event loop; cancel stops the running one.
	runner   ActionRunner
	running  bool
	cancel   context.CancelFunc
	activity ActivityPanel

	// Profile/store management state (issues #340, #350). forms is the domain
	// backend; formStack is the (possibly nested) form overlay — a profile form
	// can push a store form (#232) — and confirm is the delete overlay.
	forms     FormsBackend
	formStack []formFrame
	confirm   *confirmState

	// regions holds the clickable rows recorded during the last render, so
	// mouse clicks map to profiles/actions without re-deriving the layout
	// (issue: legacy mouse support). It is a pointer so the value-receiver
	// View can populate it.
	regions *clickRegions

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
		regions:   newClickRegions(),
	}
	m.selected = indexOfSelected(d)
	return m
}

// WithConfig attaches the profiles config and a store prober, enabling
// concurrent probing on Init. The seeded dashboard should be a probe-less
// skeleton (BuildDashboard(cfg, nil)) so the first frame renders immediately.
func (m Model) WithConfig(cfg *profile.Config, prober StoreProber) Model {
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
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case tea.KeyMsg:
		if m.activeForm() != nil {
			return m.updateForms(msg)
		}
		if m.confirm != nil {
			return m.updateConfirm(msg)
		}
		return m.handleKey(msg)
	default:
		// Route other messages (e.g. textinput cursor blink) to an open form.
		if m.activeForm() != nil {
			return m.updateForms(msg)
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
	width := m.viewWidth()
	// Overlays cover the dashboard, so any recorded click targets are stale.
	m.regions.reset()
	if form := m.activeForm(); form != nil {
		return strings.Join([]string{m.renderTitle(), form.View(), m.renderFooter()}, "\n")
	}
	if m.confirm != nil {
		return strings.Join([]string{m.renderTitle(), m.renderConfirm(), m.renderFooter()}, "\n")
	}
	title := m.renderTitle()
	overview := m.renderOverview(width)
	yPanes := lipgloss.Height(title) + lipgloss.Height(overview)
	sections := []string{
		title,
		overview,
		m.renderPanes(width, yPanes),
		m.renderActivitySection(width),
		m.renderFooter(),
	}
	return strings.Join(sections, "\n")
}

func (m Model) viewWidth() int {
	if m.width > 0 {
		return m.width
	}
	return 92
}

// fullContentWidth is the panel content width for a full-width panel at the
// given terminal width (border + padding cost 4 columns).
func (m Model) fullContentWidth(width int) int {
	if w := width - 4; w >= 24 {
		return w
	}
	return 24
}

// panel renders a titled, bordered box at a fixed content width.
func (m Model) panel(title, body string, contentWidth int) string {
	style := m.theme.panel
	if contentWidth > 0 {
		style = style.Width(contentWidth)
	}
	return style.Render(m.theme.panelTitle.Render(title) + "\n\n" + body)
}

// panelH is panel with a fixed content height, used to equalize the two
// side-by-side panes so their borders align.
func (m Model) panelH(title, body string, contentWidth, contentHeight int) string {
	return m.theme.panel.Width(contentWidth).Height(contentHeight).
		Render(m.theme.panelTitle.Render(title) + "\n\n" + body)
}

func (m Model) renderTitle() string {
	return m.theme.title.Render("Cloudstic") + "  " +
		m.theme.subtitle.Render("Operator dashboard for profiles, stores, and auth.")
}

func (m Model) renderConfirm() string {
	c := m.confirm
	body := c.message + "\n\n" + m.theme.subtle.Render("Enter/y delete · Esc/n cancel")
	return m.panel(c.title, body, 0)
}

func (m Model) renderOverview(width int) string {
	stats := strings.Join([]string{
		m.theme.accent.Render("Profiles") + " " + strconv.Itoa(m.dashboard.ProfileCount),
		m.theme.accent.Render("Stores") + " " + strconv.Itoa(m.dashboard.StoreCount),
		m.theme.accent.Render("Auth") + " " + strconv.Itoa(m.dashboard.AuthCount),
	}, "    ")
	return m.panel("Overview", stats, m.fullContentWidth(width))
}

// panelBodyOffset is the number of rows between a panel's top edge and its
// first body line: top border + title + blank line.
const panelBodyOffset = 3

// renderPanes lays out the Profiles and Selection panels side by side, or
// stacked vertically on narrow terminals (responsive, per RFC 0012 Style). It
// records click regions relative to yTop so mouse clicks map to profile rows
// and action buttons.
func (m Model) renderPanes(width, yTop int) string {
	profiles := m.renderProfileListBody()
	selectionBody, actionRows := m.selectionLines()
	selection := strings.Join(selectionBody, "\n")

	if width < 80 {
		fw := m.fullContentWidth(width)
		left := m.panel("Profiles", profiles, fw)
		right := m.panel("Selection", selection, fw)
		total := fw + 4
		m.recordProfileRegions(yTop, 0, total)
		selectionTop := yTop + lipgloss.Height(left)
		m.recordActionRegions(actionRows, selectionTop, 0, total)
		return left + "\n" + right
	}

	leftW, rightW := m.paneWidths(width)
	h := max(
		lipgloss.Height(m.theme.panelTitle.Render("Profiles")+"\n\n"+profiles),
		lipgloss.Height(m.theme.panelTitle.Render("Selection")+"\n\n"+selection),
	)
	left := m.panelH("Profiles", profiles, leftW, h)
	right := m.panelH("Selection", selection, rightW, h)

	leftTotal := leftW + 4
	rightX := leftTotal + 2 // 2-col gap between panels
	m.recordProfileRegions(yTop, 0, leftTotal)
	m.recordActionRegions(actionRows, yTop, rightX, rightX+rightW+4)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
}

// recordProfileRegions maps each profile row's absolute Y to its index, for
// clicks landing within [x0, x1).
func (m Model) recordProfileRegions(yTop, x0, x1 int) {
	if len(m.dashboard.Profiles) == 0 {
		return
	}
	for i := range m.dashboard.Profiles {
		m.regions.profileRows[yTop+panelBodyOffset+i] = i
	}
	m.regions.profilesX0, m.regions.profilesX1 = x0, x1
}

// recordActionRegions maps each action-button row's absolute Y to its key, for
// clicks landing within [x0, x1).
func (m Model) recordActionRegions(actionRows map[int]string, yTop, x0, x1 int) {
	for bodyIdx, key := range actionRows {
		m.regions.actionRows[yTop+panelBodyOffset+bodyIdx] = key
	}
	m.regions.selectionX0, m.regions.selectionX1 = x0, x1
}

func (m Model) paneWidths(width int) (int, int) {
	inner := max(width-10, 40) // two panels (+4 each) plus a 2-col gap
	left := max(inner/3, 18)
	right := inner - left
	if right < 32 {
		right = 32
		left = max(inner-right, 16)
	}
	return left, right
}

func (m Model) renderProfileListBody() string {
	if len(m.dashboard.Profiles) == 0 {
		return m.theme.subtle.Render("No profiles configured.")
	}
	nameWidth, badgeWidth := profileListWidths(m.dashboard.Profiles)
	lines := make([]string, 0, len(m.dashboard.Profiles))
	for i, profile := range m.dashboard.Profiles {
		lines = append(lines, m.profileRow(profile, i == m.selected, nameWidth, badgeWidth))
	}
	return strings.Join(lines, "\n")
}

func (m Model) profileRow(profile ProfileCard, selected bool, nameWidth, badgeWidth int) string {
	marker := "  "
	name := padRight(profile.Name, nameWidth)
	if selected {
		marker = m.theme.marker.Render("› ")
		name = m.theme.selected.Render(name)
	}
	return marker + name + "  " + m.renderBadge(profile, badgeWidth)
}

func (m Model) renderBadge(profile ProfileCard, width int) string {
	inner := padRight(plainProfileStateLabel(profile), width-2)
	return "[" + m.theme.stateStyle(profile.Status).Render(inner) + "]"
}

// selectionLines builds the Selection panel body and, alongside it, a map from
// body line index to the action key on that line, so mouse hit-testing does not
// re-derive the layout.
func (m Model) selectionLines() ([]string, map[int]string) {
	profile, ok := m.currentProfile()
	if !ok {
		return []string{m.theme.subtle.Render("No profile selected.")}, nil
	}
	lines := []string{
		m.theme.selected.Render(profile.Name),
		m.renderTabs(),
		"",
	}
	if m.view == ProfileViewHistory {
		lines = append(lines, m.renderHistory(profile)...)
	} else {
		lines = append(lines, m.renderSummary(profile)...)
	}

	actionRows := map[int]string{}
	buttons := selectedProfileActionButtons(profile)
	if len(buttons) > 0 {
		lines = append(lines, "")
		for _, b := range buttons {
			label := b.Label
			if !b.Enabled {
				label = m.theme.subtle.Render(label)
			} else {
				actionRows[len(lines)] = b.Key
			}
			lines = append(lines, "  "+m.theme.key.Render("["+b.Key+"]")+" "+label)
			if !b.Enabled && b.Reason != "" {
				lines = append(lines, m.theme.subtle.Render("      "+b.Reason))
			}
		}
	}
	return lines, actionRows
}

func (m Model) renderTabs() string {
	summary, history := "Summary (s)", "History (h)"
	if m.view == ProfileViewHistory {
		return m.theme.tabIdle.Render(summary) + "   " + m.theme.tabActive.Render(history)
	}
	return m.theme.tabActive.Render(summary) + "   " + m.theme.tabIdle.Render(history)
}

func (m Model) renderSummary(profile ProfileCard) []string {
	rows := [][2]string{
		{"State", plainProfileStateLabel(profile)},
		{"Source", profile.Source},
		{"Store", dashIfEmpty(profile.StoreRef)},
	}
	if profile.AuthRef != "" {
		rows = append(rows, [2]string{"Auth", profile.AuthRef})
	}
	rows = append(rows, [2]string{"Health", m.styledHealth(profile)})
	if last := lastBackupLabel(profile); last != "" {
		rows = append(rows, [2]string{"Backup", last})
	}
	if profile.LastRef != "" {
		rows = append(rows, [2]string{"Ref", trimSnapshotRef(profile.LastRef)})
	}
	if check := profileCheckSummary(m.activity, profile); check != "" {
		rows = append(rows, [2]string{"Check", check})
	}
	if note := profileStatusSummary(profile); note != "" {
		rows = append(rows, [2]string{"Status", note})
	}
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, m.detailRow(row[0], row[1]))
	}
	return lines
}

func (m Model) detailRow(label, value string) string {
	return m.theme.label.Render(padRight(label, 8)) + "  " + value
}

func (m Model) renderActivitySection(width int) string {
	return m.panel("Activity", m.activityBody(), m.fullContentWidth(width))
}

func (m Model) activityBody() string {
	a := m.activity
	if a.Status == ActivityStatusIdle && a.Action == "" && len(a.Lines) == 0 {
		return m.theme.subtle.Render("No recent activity.")
	}
	var lines []string
	if status := m.activityStatusLabel(a.Status); status != "" {
		lines = append(lines, m.detailRow("Status", status))
	}
	if a.Action != "" {
		lines = append(lines, m.detailRow("Action", a.Action))
	}
	if a.Phase != "" {
		lines = append(lines, m.detailRow("Phase", a.Phase))
	}
	if bar := progressBarLine(a, 28); bar != "" {
		lines = append(lines, m.detailRow("Progress", bar))
	}
	if a.Summary != "" {
		lines = append(lines, m.detailRow("Result", a.Summary))
	}
	if a.UpdatedAt != "" {
		lines = append(lines, m.detailRow("Updated", a.UpdatedAt))
	}
	if len(a.Lines) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, a.Lines...)
	}
	return strings.Join(lines, "\n")
}

func (m Model) activityStatusLabel(status ActivityStatus) string {
	switch status {
	case ActivityStatusRunning:
		return m.theme.stateWarning.Render("running")
	case ActivityStatusSuccess:
		return m.theme.stateReady.Render("success")
	case ActivityStatusError:
		return m.theme.stateError.Render("error")
	default:
		return ""
	}
}

func padRight(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
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
	if m.activeForm() != nil || m.confirm != nil {
		// The overlay renders its own key hints.
		return ""
	}
	if m.running {
		return m.hint("x", "cancel") + m.theme.subtle.Render("   ·   ") + m.theme.stateWarning.Render("running…")
	}
	line := strings.Join(m.footerHints(), m.theme.subtle.Render("  ·  "))
	if m.loadErr != "" {
		return m.theme.stateError.Render("error: "+m.loadErr) + "\n" + line
	}
	return line
}

// footerHints builds the context-aware key-hint bar: only actions and
// management keys that will actually do something for the current selection.
func (m Model) footerHints() []string {
	var parts []string
	profile, hasProfile := m.currentProfile()
	if m.runner != nil && hasProfile {
		for _, action := range profile.Actions {
			if !action.Enabled {
				continue
			}
			switch action.Kind {
			case ActionKindInit:
				parts = append(parts, m.hint("b", "init"))
			case ActionKindBackup:
				parts = append(parts, m.hint("b", "backup"))
			case ActionKindCheck:
				parts = append(parts, m.hint("c", "check"))
			}
		}
	}
	if m.forms != nil {
		parts = append(parts, m.hint("n", "new"))
		if hasProfile {
			parts = append(parts, m.hint("e", "edit"), m.hint("d", "delete"))
		}
	}
	parts = append(parts,
		m.hint("↑↓", "select"),
		m.hint("s/h", "view"),
		m.hint("r", "refresh"),
		m.hint("q", "quit"),
	)
	return parts
}

func (m Model) hint(key, label string) string {
	return m.theme.footerKey.Render(key) + m.theme.footer.Render(" "+label)
}

func (m Model) currentProfile() (ProfileCard, bool) {
	if m.selected < 0 || m.selected >= len(m.dashboard.Profiles) {
		return ProfileCard{}, false
	}
	return m.dashboard.Profiles[m.selected], true
}
