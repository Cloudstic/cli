package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/tui/forms"
)

// FormsBackend is the domain boundary the model uses for profile and store
// management. It combines the profile and store form backends with the delete,
// config-reload, and edit-seed operations the model orchestrates around a form.
// The cmd package implements it by reusing the existing source/store/options/
// validation/save helpers.
type FormsBackend interface {
	forms.ProfileBackend
	forms.StoreBackend
	forms.SecretBackend
	// InitialStoreValues seeds an edit-store form with an existing store's
	// field values.
	InitialStoreValues(name string) map[string]string
	// DeleteProfile removes a profile from the profiles file.
	DeleteProfile(name string) error
	// Reload re-reads the profiles config after a mutation.
	Reload() (*engine.ProfilesConfig, error)
}

// formFrame is one level of the (possibly nested) form overlay. intent records
// why the frame was pushed, so its result can be applied to the parent frame.
type formFrame struct {
	form   *forms.Form
	intent forms.Intent
}

func (m Model) activeForm() *forms.Form {
	if len(m.formStack) == 0 {
		return nil
	}
	return m.formStack[len(m.formStack)-1].form
}

func (m Model) pushForm(form *forms.Form, intent forms.Intent) Model {
	m.formStack = append(m.formStack, formFrame{form: form, intent: intent})
	return m
}

func (m Model) popForm() Model {
	if len(m.formStack) == 0 {
		return m
	}
	m.formStack = m.formStack[:len(m.formStack)-1]
	return m
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
	form := forms.NewProfileForm(m.forms, "", "", "", "", false)
	m = m.pushForm(form, forms.Intent{})
	return m, form.Init()
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
	form := forms.NewProfileForm(m.forms, profile.Name, profile.Source, profile.StoreRef, profile.AuthRef, true)
	m = m.pushForm(form, forms.Intent{})
	return m, form.Init()
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

func (m Model) updateForms(msg tea.Msg) (tea.Model, tea.Cmd) {
	top := len(m.formStack) - 1
	form, cmd := m.formStack[top].form.Update(msg)
	m.formStack[top].form = form

	// A navigation intent opens a nested form instead of completing.
	if intent, ok := form.TakeIntent(); ok {
		return m.openNestedForm(intent)
	}
	if !form.Done() {
		return m, cmd
	}
	return m.completeTopForm()
}

// openNestedForm pushes the form requested by a parent form's intent.
func (m Model) openNestedForm(intent forms.Intent) (tea.Model, tea.Cmd) {
	switch intent.Name {
	case forms.IntentCreateStore:
		form := forms.NewStoreForm(m.forms, "", nil, false)
		m = m.pushForm(form, intent)
		return m, form.Init()
	case forms.IntentEditStore:
		form := forms.NewStoreForm(m.forms, intent.Value, m.forms.InitialStoreValues(intent.Value), true)
		m = m.pushForm(form, intent)
		return m, form.Init()
	case forms.IntentEditSecret:
		return m.openSecretForm(intent)
	}
	return m, nil
}

// openSecretForm pushes the guided secret-ref form for a store secret field.
func (m Model) openSecretForm(intent forms.Intent) (tea.Model, tea.Cmd) {
	parent := m.activeForm() // the store form
	if parent == nil {
		return m, nil
	}
	spec, ok := forms.StoreSecretFieldSpec(intent.Value)
	if !ok {
		return m, nil
	}
	storeName := parent.TrimmedValue(forms.FieldStoreName)
	existingRef := parent.TrimmedValue(intent.Value)
	form := forms.NewSecretForm(m.forms, spec, storeName, existingRef)
	m = m.pushForm(form, intent)
	return m, form.Init()
}

// completeTopForm pops the finished top form and either applies its result to
// the parent frame (nested) or commits the change (root).
func (m Model) completeTopForm() (tea.Model, tea.Cmd) {
	top := m.formStack[len(m.formStack)-1]
	canceled := top.form.Canceled()
	result := top.form.Result()
	m = m.popForm()

	// Nested form: hand its result back to the parent form.
	if len(m.formStack) > 0 {
		if !canceled && result != "" {
			m = m.applyNestedResult(top.intent, result)
		}
		return m, nil
	}

	// Root form (profile create/edit): commit.
	if canceled {
		m.activity = managementActivity(ActivityStatusIdle, "Profile form", "canceled")
		return m, nil
	}
	m.activity = managementActivity(ActivityStatusSuccess, "Save profile", "saved \""+result+"\"")
	return m, m.reloadConfigCmd(result)
}

// applyNestedResult injects a completed nested form's result into the parent
// form. A saved store refreshes the profile form's store selector and selects
// the new/edited store.
func (m Model) applyNestedResult(intent forms.Intent, result string) Model {
	parent := m.activeForm()
	if parent == nil {
		return m
	}
	switch intent.Name {
	case forms.IntentCreateStore, forms.IntentEditStore:
		parent.SetOptions("store", storeSelectorOptions(m.forms.StoreOptions()))
		parent.SetValue("store", result)
	case forms.IntentEditSecret:
		// intent.Value is the store form's secret field key.
		parent.SetValue(intent.Value, result)
	}
	return m
}

// storeSelectorOptions mirrors the profile form's store options (existing
// stores plus the create sentinel).
func storeSelectorOptions(stores []string) []string {
	return append(append([]string{}, stores...), forms.CreateStoreOption)
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
