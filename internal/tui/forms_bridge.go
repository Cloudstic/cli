package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/tui/forms"
)

// FormsBackend is the domain boundary the model uses for profile management.
// It extends the profile form's own backend with the delete and config-reload
// operations the model orchestrates around a form. The cmd package implements
// it by reusing the existing source/options/validation/save helpers.
type FormsBackend interface {
	forms.ProfileBackend
	// DeleteProfile removes a profile from the profiles file.
	DeleteProfile(name string) error
	// Reload re-reads the profiles config after a mutation.
	Reload() (*engine.ProfilesConfig, error)
}

// WithForms enables the profile create/edit/delete flows (issue #340). Without
// it the model stays read-only plus actions.
func (m Model) WithForms(backend FormsBackend) Model {
	m.forms = backend
	return m
}

// confirmState is a minimal yes/no confirmation overlay (used for delete).
type confirmState struct {
	title   string
	message string
	target  string
}

// configReloadedMsg carries a reloaded profiles config back into the model
// after a form save or delete, with the profile name to reselect.
type configReloadedMsg struct {
	cfg        *engine.ProfilesConfig
	err        error
	selectName string
}

func (m Model) reloadConfigCmd(selectName string) tea.Cmd {
	backend := m.forms
	if backend == nil {
		return nil
	}
	return func() tea.Msg {
		cfg, err := backend.Reload()
		return configReloadedMsg{cfg: cfg, err: err, selectName: selectName}
	}
}

func (m Model) handleConfigReloaded(msg configReloadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.loadErr = msg.err.Error()
		return m, nil
	}
	m.cfg = msg.cfg
	// Keep existing probe results so unchanged stores don't flicker back to
	// "checking…"; probeAllCmd refreshes them (and any new store) below.
	m.dashboard = BuildDashboard(m.cfg, m.probes)
	if msg.selectName != "" {
		m.dashboard.SelectedProfile = msg.selectName
	} else {
		m.dashboard.SelectedProfile = m.selectedName()
	}
	m.selected = indexOfSelected(m.dashboard)
	return m, m.probeAllCmd()
}

// openCreateProfile opens the create-profile form.
func (m Model) openCreateProfile() (Model, tea.Cmd) {
	if m.forms == nil {
		return m, nil
	}
	m.form = forms.NewProfileForm(m.forms, "", "", "", "", false)
	return m, m.form.Init()
}

// openEditProfile opens the edit form for the selected profile.
func (m Model) openEditProfile() (Model, tea.Cmd) {
	if m.forms == nil {
		return m, nil
	}
	profile, ok := m.currentProfile()
	if !ok {
		return m, nil
	}
	m.form = forms.NewProfileForm(m.forms, profile.Name, profile.Source, profile.StoreRef, profile.AuthRef, true)
	return m, m.form.Init()
}

// openDeleteProfile opens a delete confirmation for the selected profile.
func (m Model) openDeleteProfile() (Model, tea.Cmd) {
	if m.forms == nil {
		return m, nil
	}
	profile, ok := m.currentProfile()
	if !ok {
		return m, nil
	}
	m.confirm = &confirmState{
		title:   "Delete Profile",
		message: "Delete profile \"" + profile.Name + "\"? This edits profiles.yaml only.",
		target:  profile.Name,
	}
	return m, nil
}

func (m Model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	form, cmd := m.form.Update(msg)
	m.form = form
	if !form.Done() {
		return m, cmd
	}

	canceled := form.Canceled()
	result := form.Result()
	m.form = nil
	if canceled {
		m.activity = managementActivity(ActivityStatusIdle, "Profile form", "canceled")
		return m, nil
	}
	m.activity = managementActivity(ActivityStatusSuccess, "Save profile", "saved \""+result+"\"")
	return m, m.reloadConfigCmd(result)
}

func (m Model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "n", "q":
		m.confirm = nil
		m.activity = managementActivity(ActivityStatusIdle, "Delete profile", "canceled")
		return m, nil
	case "enter", "y":
		target := m.confirm.target
		m.confirm = nil
		if err := m.forms.DeleteProfile(target); err != nil {
			m.loadErr = err.Error()
			return m, nil
		}
		m.activity = managementActivity(ActivityStatusSuccess, "Delete profile", "deleted \""+target+"\"")
		return m, m.reloadConfigCmd("")
	}
	return m, nil
}

func managementActivity(status ActivityStatus, action, summary string) ActivityPanel {
	return ActivityPanel{Status: status, Action: action, Summary: summary}
}
