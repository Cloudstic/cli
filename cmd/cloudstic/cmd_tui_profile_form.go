package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/tui"
	"github.com/cloudstic/cli/internal/ui"
)

type tuiModalInputKind int

const (
	tuiModalInputNone tuiModalInputKind = iota
	tuiModalInputText
	tuiModalInputBackspace
	tuiModalInputEnter
	tuiModalInputEscape
	tuiModalInputUp
	tuiModalInputDown
	tuiModalInputLeft
	tuiModalInputRight
	tuiModalInputTab
)

type tuiModalInput struct {
	Kind tuiModalInputKind
	Text string
}

type tuiProfileModal struct {
	profilesFile string
	cfg          *cloudstic.ProfilesConfig
	editing      bool
	originalName string
	modal        tui.Modal
}

const tuiCreateStoreOption = "+ Create store"

var tuiSourceTypes = []string{
	"local",
	"sftp",
	"gdrive",
	"gdrive-changes",
	"onedrive",
	"onedrive-changes",
}

func newTUIProfileModal(profilesFile, existingName string, editing bool) (*tuiProfileModal, error) {
	cfg, err := loadProfilesOrInit(profilesFile)
	if err != nil {
		return nil, fmt.Errorf("load profiles: %w", err)
	}
	ensureProfilesMaps(cfg)

	var existing cloudstic.BackupProfile
	if editing {
		var ok bool
		existing, ok = cfg.Profiles[existingName]
		if !ok {
			return nil, fmt.Errorf("unknown profile %q", existingName)
		}
	}

	storeOptions := profileStoreOptions(cfg, existing.Store)
	source := newTUIProfileSource(existing.Source)

	m := &tuiProfileModal{
		profilesFile: profilesFile,
		cfg:          cfg,
		editing:      editing,
		originalName: existingName,
		modal: tui.Modal{
			Kind:        tui.ModalKindProfileForm,
			Title:       profileModalTitle(editing),
			Subtitle:    "Edit the fields below and press Enter to save.",
			Hint:        "Type to edit, ↑/↓ or Tab to move, ←/→ to change selections, Enter to save, Esc to cancel.",
			SubmitLabel: "Save",
			CancelLabel: "Cancel",
			Fields: []tui.ModalField{
				{Key: "name", Label: "Name", Kind: tui.ModalFieldText, Value: existingName, Required: true, Disabled: editing},
				{Key: "source_type", Label: "Source Type", Kind: tui.ModalFieldSelect, Value: source.Type, Options: append([]string{}, tuiSourceTypes...), Required: true},
				{Key: "source_value", Label: source.DetailLabel(), Kind: tui.ModalFieldText, Value: source.Value, Required: source.DetailRequired()},
				{Key: "store", Label: "Store", Kind: tui.ModalFieldSelect, Value: firstNonEmpty(existing.Store, firstOption(storeOptions)), Options: storeOptions, Required: true},
				{Key: "auth", Label: "Auth", Kind: tui.ModalFieldSelect, Value: existing.AuthRef},
			},
		},
	}
	m.rebuildDerivedFields()
	m.selectFirstEditableField()
	return m, nil
}

func (m *tuiProfileModal) View() tui.Modal {
	source := m.currentSource()
	view := m.modal
	view.Subtitle = profileModalSubtitle(source, m.cfg)
	view.Message = sourceFieldExamples(m.selectedFieldKey(), source)
	if selected := m.selectedFieldKey(); selected == "store" {
		view.Message = append(view.Message, profileStoreFieldHelp(m.fieldValue("store"))...)
	}
	return view
}

func (m *tuiProfileModal) Handle(input tuiModalInput) (bool, string, error) {
	switch input.Kind {
	case tuiModalInputEscape:
		return true, "", nil
	case tuiModalInputUp:
		m.moveField(-1)
	case tuiModalInputDown, tuiModalInputTab:
		m.moveField(1)
	case tuiModalInputLeft:
		m.cycleField(-1)
	case tuiModalInputRight:
		m.cycleField(1)
	case tuiModalInputBackspace:
		m.backspaceField()
	case tuiModalInputText:
		m.appendField(input.Text)
	case tuiModalInputEnter:
		name, err := m.submit()
		if err != nil {
			if fieldErr, ok := err.(*tuiFieldError); ok {
				m.modal.ErrorField = fieldErr.Field
				m.modal.Error = fieldErr.Message
			} else {
				m.modal.ErrorField = ""
				m.modal.Error = err.Error()
			}
			return false, "", nil
		}
		return true, name, nil
	}
	return false, "", nil
}

func (m *tuiProfileModal) selectFirstEditableField() {
	for i, field := range m.modal.Fields {
		if !field.Disabled {
			m.modal.Selected = i
			return
		}
	}
	m.modal.Selected = 0
}

func (m *tuiProfileModal) moveField(delta int) {
	if len(m.modal.Fields) == 0 || delta == 0 {
		return
	}
	idx := m.modal.Selected
	for range m.modal.Fields {
		idx += delta
		if idx < 0 {
			idx = len(m.modal.Fields) - 1
		}
		if idx >= len(m.modal.Fields) {
			idx = 0
		}
		if !m.modal.Fields[idx].Disabled {
			m.modal.Selected = idx
			return
		}
	}
}

func (m *tuiProfileModal) cycleField(delta int) {
	field := &m.modal.Fields[m.modal.Selected]
	if field.Disabled || field.Kind != tui.ModalFieldSelect || len(field.Options) == 0 {
		return
	}
	idx := slices.Index(field.Options, field.Value)
	if idx < 0 {
		idx = 0
	}
	idx += delta
	if idx < 0 {
		idx = len(field.Options) - 1
	}
	if idx >= len(field.Options) {
		idx = 0
	}
	field.Value = field.Options[idx]
	m.clearError()
	if field.Key == "source_type" {
		m.rebuildDerivedFields()
	}
}

func (m *tuiProfileModal) appendField(text string) {
	field := &m.modal.Fields[m.modal.Selected]
	if field.Disabled || field.Kind != tui.ModalFieldText {
		return
	}
	field.Value += text
	m.clearError()
	if field.Key == "source_value" {
		m.rebuildDerivedFields()
	}
}

func (m *tuiProfileModal) backspaceField() {
	field := &m.modal.Fields[m.modal.Selected]
	if field.Disabled || field.Kind != tui.ModalFieldText || field.Value == "" {
		return
	}
	runes := []rune(field.Value)
	field.Value = string(runes[:len(runes)-1])
	m.clearError()
	if field.Key == "source_value" {
		m.rebuildDerivedFields()
	}
}

func (m *tuiProfileModal) rebuildDerivedFields() {
	m.updateSourceFieldMetadata()
	m.updateAuthField()
}

func (m *tuiProfileModal) updateAuthField() {
	field := m.fieldByKey("auth")
	if field == nil {
		return
	}
	provider := m.currentSource().Provider()
	if provider == "" {
		field.Disabled = true
		field.Options = nil
		field.Value = ""
		field.Required = false
		field.Label = "Auth"
		return
	}
	options := profileAuthOptions(m.cfg, provider)
	field.Disabled = false
	field.Required = true
	field.Options = options
	field.Label = fmt.Sprintf("Auth (%s)", provider)
	if len(options) == 0 {
		field.Value = ""
		return
	}
	if slices.Index(options, field.Value) < 0 {
		field.Value = options[0]
	}
}

func (m *tuiProfileModal) submit() (string, error) {
	name := m.textFieldValue("name")
	if !m.editing {
		if name == "" {
			return "", fieldError("name", "profile name is required")
		}
		if err := validateRefName("profile", name); err != nil {
			return "", fieldError("name", err.Error())
		}
		if _, exists := m.cfg.Profiles[name]; exists {
			return "", fieldError("name", fmt.Sprintf("profile %q already exists", name))
		}
	} else {
		name = m.originalName
	}

	source := m.currentSource().Compose()
	if source == "" {
		return "", fieldError("source_value", "source details are required")
	}
	if _, err := parseSourceURI(source); err != nil {
		return "", fieldError("source_value", fmt.Sprintf("invalid source: %v", err))
	}

	storeRef := m.textFieldValue("store")
	if storeRef == "" {
		return "", fieldError("store", "store reference is required")
	}
	if storeRef == tuiCreateStoreOption {
		return "", fieldError("store", "create a store before saving the profile")
	}
	if _, ok := m.cfg.Stores[storeRef]; !ok {
		return "", fieldError("store", fmt.Sprintf("unknown store %q", storeRef))
	}

	authRef := m.textFieldValue("auth")
	provider := m.currentSource().Provider()
	if provider != "" {
		if authRef == "" {
			return "", fieldError("auth", fmt.Sprintf("auth reference is required for %s sources", provider))
		}
		auth, ok := m.cfg.Auth[authRef]
		if !ok {
			return "", fieldError("auth", fmt.Sprintf("unknown auth %q", authRef))
		}
		if auth.Provider != provider {
			return "", fieldError("auth", fmt.Sprintf("auth %q is not a %s entry", authRef, provider))
		}
	} else {
		authRef = ""
	}

	profile := cloudstic.BackupProfile{
		Source:  source,
		Store:   storeRef,
		AuthRef: authRef,
	}
	if m.editing {
		profile = m.cfg.Profiles[m.originalName]
		profile.Source = source
		profile.Store = storeRef
		profile.AuthRef = authRef
	}
	if err := tuiServiceFactory(nil, m.profilesFile, nil).SaveProfile(m.profilesFile, name, profile); err != nil {
		return "", err
	}
	return name, nil
}

func (m *tuiProfileModal) wantsCreateStore(input tuiModalInput) bool {
	return m.selectedFieldKey() == "store" && input.Kind == tuiModalInputEnter && m.fieldValue("store") == tuiCreateStoreOption
}

func (m *tuiProfileModal) wantsEditStore(input tuiModalInput) (string, bool) {
	if m.selectedFieldKey() != "store" || input.Kind != tuiModalInputText || !strings.EqualFold(input.Text, "e") {
		return "", false
	}
	storeRef := m.fieldValue("store")
	if storeRef == "" || storeRef == tuiCreateStoreOption {
		return "", false
	}
	if _, ok := m.cfg.Stores[storeRef]; !ok {
		return "", false
	}
	return storeRef, true
}

func (m *tuiProfileModal) reloadStoreOptions(selected string) error {
	cfg, err := loadProfilesOrInit(m.profilesFile)
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}
	ensureProfilesMaps(cfg)
	m.cfg = cfg
	field := m.fieldByKey("store")
	if field == nil {
		return nil
	}
	field.Options = profileStoreOptions(cfg, selected)
	field.Value = firstNonEmpty(selected, firstOption(field.Options))
	m.clearError()
	return nil
}

func (m *tuiProfileModal) clearError() {
	m.modal.Error = ""
	m.modal.ErrorField = ""
}

func (m *tuiProfileModal) currentSource() tuiProfileSource {
	return tuiProfileSource{
		Type:  firstNonEmpty(m.fieldValue("source_type"), "local"),
		Value: m.fieldValue("source_value"),
	}
}

func (m *tuiProfileModal) updateSourceFieldMetadata() {
	field := m.fieldByKey("source_value")
	if field == nil {
		return
	}
	source := m.currentSource()
	field.Label = source.DetailLabel()
	field.Required = source.DetailRequired()
}

func (m *tuiProfileModal) fieldByKey(key string) *tui.ModalField {
	for i := range m.modal.Fields {
		if m.modal.Fields[i].Key == key {
			return &m.modal.Fields[i]
		}
	}
	return nil
}

func (m *tuiProfileModal) fieldValue(key string) string {
	field := m.fieldByKey(key)
	if field == nil {
		return ""
	}
	return field.Value
}

func (m *tuiProfileModal) textFieldValue(key string) string {
	return strings.TrimSpace(m.fieldValue(key))
}

func (m *tuiProfileModal) selectedFieldKey() string {
	if m.modal.Selected < 0 || m.modal.Selected >= len(m.modal.Fields) {
		return ""
	}
	return m.modal.Fields[m.modal.Selected].Key
}

func (s *tuiSession) runProfileModal(ctx context.Context, existingName string, editing bool) error {
	modal, err := newTUIProfileModal(s.profilesFile, existingName, editing)
	if err != nil {
		return err
	}
	action := "Create profile"
	if editing {
		action = "Edit profile"
	}
	for {
		view := modal.View()
		s.dashboard.Modal = &view
		if err := s.render(); err != nil {
			return err
		}
		input, err := readTUIModalInput(s.r.lineReader())
		if err != nil {
			return err
		}
		if modal.wantsCreateStore(input) {
			storeName, canceled, err := s.runStoreModal("", false)
			if err != nil {
				return err
			}
			if canceled {
				continue
			}
			if err := modal.reloadStoreOptions(storeName); err != nil {
				return err
			}
			continue
		}
		if storeRef, ok := modal.wantsEditStore(input); ok {
			storeName, canceled, err := s.runStoreModal(storeRef, true)
			if err != nil {
				return err
			}
			if canceled {
				continue
			}
			if err := modal.reloadStoreOptions(storeName); err != nil {
				return err
			}
			continue
		}
		done, name, err := modal.Handle(input)
		if err != nil {
			return err
		}
		if !done {
			continue
		}
		s.dashboard.Modal = nil
		if name == "" {
			s.dashboard.Activity = managementActivity(tui.ActivityStatusIdle, action, "canceled")
			return nil
		}
		if err := s.refresh(ctx); err != nil {
			return fmt.Errorf("failed to refresh TUI dashboard: %v", err)
		}
		s.dashboard.SelectedProfile = name
		s.dashboard.Activity = managementActivity(tui.ActivityStatusSuccess, action, fmt.Sprintf("saved %q", name))
		return nil
	}
}

func (s *tuiSession) runStoreModal(existingName string, editing bool) (string, bool, error) {
	modal, err := newTUIStoreModal(s.profilesFile, existingName, editing)
	if err != nil {
		return "", false, err
	}
	for {
		view := modal.View()
		s.dashboard.Modal = &view
		if err := s.render(); err != nil {
			return "", false, err
		}
		input, err := readTUIModalInput(s.r.lineReader())
		if err != nil {
			return "", false, err
		}
		done, name, err := modal.Handle(input)
		if err != nil {
			return "", false, err
		}
		if !done {
			continue
		}
		if name == "" {
			return "", true, nil
		}
		return name, false, nil
	}
}

func (s *tuiSession) runDeleteModal(ctx context.Context, profile tui.ProfileCard) error {
	modal := tui.Modal{
		Kind:        tui.ModalKindConfirm,
		Title:       "Delete Profile",
		Subtitle:    "Confirm profile deletion.",
		Message:     []string{fmt.Sprintf("Delete profile %q?", profile.Name), "", "Press Enter to delete or Esc to cancel."},
		Hint:        "This removes the profile from profiles.yaml only.",
		SubmitLabel: "Delete",
		CancelLabel: "Cancel",
	}
	for {
		s.dashboard.Modal = &modal
		if err := s.render(); err != nil {
			return err
		}
		input, err := readTUIModalInput(s.r.lineReader())
		if err != nil {
			return err
		}
		switch input.Kind {
		case tuiModalInputEscape:
			s.dashboard.Modal = nil
			s.dashboard.Activity = managementActivity(tui.ActivityStatusIdle, "Delete profile", "canceled")
			return nil
		case tuiModalInputEnter:
			s.dashboard.Modal = nil
			if err := tuiServiceFactory(nil, s.profilesFile, nil).DeleteProfile(s.profilesFile, profile.Name); err != nil {
				return err
			}
			if err := s.refresh(ctx); err != nil {
				return fmt.Errorf("failed to refresh TUI dashboard: %v", err)
			}
			s.dashboard.SelectedProfile = ""
			s.dashboard = ensureSelectedProfile(s.dashboard)
			s.dashboard.Activity = managementActivity(tui.ActivityStatusSuccess, "Delete profile", fmt.Sprintf("deleted %q", profile.Name))
			return nil
		}
	}
}

func readTUIModalInput(r io.ByteReader) (tuiModalInput, error) {
	b, err := r.ReadByte()
	if err != nil {
		return tuiModalInput{}, err
	}
	switch b {
	case '\r', '\n':
		return tuiModalInput{Kind: tuiModalInputEnter}, nil
	case 0x1b:
		if isStandaloneEscape(r) {
			return tuiModalInput{Kind: tuiModalInputEscape}, nil
		}
		next, err := r.ReadByte()
		if err != nil {
			return tuiModalInput{Kind: tuiModalInputEscape}, nil
		}
		if next == 'O' {
			dir, err := r.ReadByte()
			if err != nil {
				return tuiModalInput{Kind: tuiModalInputEscape}, nil
			}
			switch dir {
			case 'A':
				return tuiModalInput{Kind: tuiModalInputUp}, nil
			case 'B':
				return tuiModalInput{Kind: tuiModalInputDown}, nil
			case 'C':
				return tuiModalInput{Kind: tuiModalInputRight}, nil
			case 'D':
				return tuiModalInput{Kind: tuiModalInputLeft}, nil
			default:
				return tuiModalInput{Kind: tuiModalInputNone}, nil
			}
		}
		if next != '[' {
			return tuiModalInput{Kind: tuiModalInputEscape}, nil
		}
		csi, err := readTUICSISequence(r)
		if err != nil || len(csi) == 0 {
			return tuiModalInput{Kind: tuiModalInputNone}, nil
		}
		switch csi[len(csi)-1] {
		case 'A':
			return tuiModalInput{Kind: tuiModalInputUp}, nil
		case 'B':
			return tuiModalInput{Kind: tuiModalInputDown}, nil
		case 'C':
			return tuiModalInput{Kind: tuiModalInputRight}, nil
		case 'D':
			return tuiModalInput{Kind: tuiModalInputLeft}, nil
		case 'Z':
			return tuiModalInput{Kind: tuiModalInputUp}, nil
		default:
			return tuiModalInput{Kind: tuiModalInputNone}, nil
		}
	case '\t':
		return tuiModalInput{Kind: tuiModalInputTab}, nil
	case 0x7f, 0x08:
		return tuiModalInput{Kind: tuiModalInputBackspace}, nil
	default:
		if b >= 0x20 && b <= 0x7e {
			return tuiModalInput{Kind: tuiModalInputText, Text: string(rune(b))}, nil
		}
		return tuiModalInput{Kind: tuiModalInputNone}, nil
	}
}

func isStandaloneEscape(r io.ByteReader) bool {
	buffered, ok := r.(interface{ Buffered() int })
	if !ok {
		return false
	}
	return buffered.Buffered() == 0
}

func profileAuthOptions(cfg *cloudstic.ProfilesConfig, provider string) []string {
	options := []string{}
	for name, auth := range cfg.Auth {
		if auth.Provider == provider {
			options = append(options, name)
		}
	}
	slices.Sort(options)
	return options
}

func profileModalTitle(editing bool) string {
	if editing {
		return "Edit Profile"
	}
	return "Create Profile"
}

func profileModalSubtitle(source tuiProfileSource, cfg *cloudstic.ProfilesConfig) string {
	provider := source.Provider()
	switch {
	case provider == "":
		return source.Description()
	case len(profileAuthOptions(cfg, provider)) == 0:
		return fmt.Sprintf("No %s auth refs available yet.", provider)
	default:
		return fmt.Sprintf("Source requires a %s auth reference.", provider)
	}
}

func profileStoreOptions(cfg *cloudstic.ProfilesConfig, current string) []string {
	options := sortedKeys(cfg.Stores)
	moveDefaultToFront(options, current)
	options = append(options, tuiCreateStoreOption)
	return options
}

func profileStoreFieldHelp(storeRef string) []string {
	lines := []string{}
	if storeRef == tuiCreateStoreOption {
		lines = append(lines, fmt.Sprintf("%sPress Enter to create a store.%s", ui.Dim, ui.Reset))
		return lines
	}
	if storeRef != "" {
		lines = append(lines, fmt.Sprintf("%sType e to edit the selected store.%s", ui.Dim, ui.Reset))
	}
	return lines
}

func sourceFieldExamples(selectedField string, source tuiProfileSource) []string {
	if selectedField != "source_value" {
		return nil
	}
	example := source.ExampleText()
	if example == "" {
		return nil
	}
	return []string{fmt.Sprintf("%s%s%s", ui.Dim, example, ui.Reset)}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstOption(options []string) string {
	if len(options) == 0 {
		return ""
	}
	return options[0]
}

func sourceTypeFromSource(raw string) string {
	parts, err := parseSourceURI(raw)
	if err != nil {
		return ""
	}
	return parts.scheme
}

func sourceValueFromSource(raw string) string {
	parts, err := parseSourceURI(raw)
	if err != nil {
		return ""
	}
	switch parts.scheme {
	case "local":
		return parts.path
	case "sftp":
		target := ""
		if parts.user != "" {
			target += parts.user + "@"
		}
		target += parts.host
		if parts.port != "" {
			target += ":" + parts.port
		}
		target += parts.path
		return target
	case "gdrive", "gdrive-changes", "onedrive", "onedrive-changes":
		if parts.host != "" {
			if parts.path == "/" {
				return parts.host
			}
			return parts.host + parts.path
		}
		if parts.path == "/" {
			return "/"
		}
		return parts.path
	default:
		return raw
	}
}

func moveDefaultToFront(options []string, current string) {
	if current == "" {
		return
	}
	if idx := slices.Index(options, current); idx > 0 {
		options[0], options[idx] = options[idx], options[0]
	}
}

type tuiFieldError struct {
	Field   string
	Message string
}

func (e *tuiFieldError) Error() string {
	return e.Message
}

func fieldError(field, message string) error {
	return &tuiFieldError{Field: field, Message: message}
}

func managementActivity(status tui.ActivityStatus, action, summary string, lines ...string) tui.ActivityPanel {
	return tui.ActivityPanel{
		Status:  status,
		Action:  action,
		Summary: summary,
		Lines:   append([]string{}, lines...),
	}
}
