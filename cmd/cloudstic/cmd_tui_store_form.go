package main

import (
	"fmt"
	"slices"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/tui"
	"github.com/cloudstic/cli/internal/ui"
)

type tuiStoreConfig struct {
	Type  string
	Value string
}

func newTUIStoreConfig(raw string) tuiStoreConfig {
	parts, err := parseStoreURI(raw)
	if err != nil {
		return tuiStoreConfig{Type: "local"}
	}
	switch parts.scheme {
	case "local":
		return tuiStoreConfig{Type: "local", Value: parts.path}
	case "s3", "b2":
		value := parts.bucket
		if parts.prefix != "" {
			value += "/" + parts.prefix
		}
		return tuiStoreConfig{Type: parts.scheme, Value: value}
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
		return tuiStoreConfig{Type: "sftp", Value: target}
	default:
		return tuiStoreConfig{Type: "local"}
	}
}

func (s tuiStoreConfig) Compose() string {
	value := strings.TrimSpace(s.Value)
	switch s.Type {
	case "local":
		return "local:" + value
	case "s3", "b2":
		return s.Type + ":" + value
	case "sftp":
		if value == "" {
			return ""
		}
		return "sftp://" + value
	default:
		return value
	}
}

func (s tuiStoreConfig) DetailLabel() string {
	switch s.Type {
	case "local":
		return "Path"
	case "sftp":
		return "Target"
	default:
		return "Bucket/Prefix"
	}
}

func (s tuiStoreConfig) Description(editing bool, usedBy int) string {
	if editing {
		if usedBy > 1 {
			return fmt.Sprintf("This store is shared by %d profiles.", usedBy)
		}
		if usedBy == 1 {
			return "This store is currently referenced by 1 profile."
		}
		return "Edit the store settings below."
	}
	switch s.Type {
	case "local":
		return "Store backups in a local filesystem path."
	case "sftp":
		return "Store backups on a remote SFTP server."
	case "b2":
		return "Store backups in a Backblaze B2 bucket."
	default:
		return "Store backups in an S3-compatible bucket."
	}
}

func (s tuiStoreConfig) ExampleText() string {
	switch s.Type {
	case "local":
		return "Example: /Users/me/.cloudstic"
	case "sftp":
		return "Example: backup@host.example.com/backups"
	case "b2":
		return "Example: my-bucket/backups"
	default:
		return "Example: my-bucket/backups"
	}
}

type tuiStoreModal struct {
	profilesFile string
	cfg          *cloudstic.ProfilesConfig
	editing      bool
	originalName string
	modal        tui.Modal
}

var tuiStoreTypes = []string{"local", "s3", "b2", "sftp"}

func newTUIStoreModal(profilesFile, existingName string, editing bool) (*tuiStoreModal, error) {
	cfg, err := loadProfilesOrInit(profilesFile)
	if err != nil {
		return nil, fmt.Errorf("load profiles: %w", err)
	}
	ensureProfilesMaps(cfg)

	var existing cloudstic.ProfileStore
	if editing {
		var ok bool
		existing, ok = cfg.Stores[existingName]
		if !ok {
			return nil, fmt.Errorf("unknown store %q", existingName)
		}
	}
	storeCfg := newTUIStoreConfig(existing.URI)
	m := &tuiStoreModal{
		profilesFile: profilesFile,
		cfg:          cfg,
		editing:      editing,
		originalName: existingName,
		modal: tui.Modal{
			Kind:        tui.ModalKindProfileForm,
			Title:       storeModalTitle(editing),
			Subtitle:    storeCfg.Description(editing, storeUsageCount(cfg, existingName)),
			Hint:        "Type to edit, ↑/↓ or Tab to move, ←/→ to change selections, Enter to save, Esc to cancel.",
			SubmitLabel: "Save",
			CancelLabel: "Cancel",
			Fields: []tui.ModalField{
				{Key: "name", Label: "Name", Kind: tui.ModalFieldText, Value: existingName, Required: true, Disabled: editing},
				{Key: "store_type", Label: "Store Type", Kind: tui.ModalFieldSelect, Value: firstNonEmpty(storeCfg.Type, "local"), Options: append([]string{}, tuiStoreTypes...), Required: true},
				{Key: "store_value", Label: storeCfg.DetailLabel(), Kind: tui.ModalFieldText, Value: storeCfg.Value, Required: true},
			},
		},
	}
	m.updateStoreFieldMetadata()
	m.selectFirstEditableField()
	return m, nil
}

func (m *tuiStoreModal) View() tui.Modal {
	view := m.modal
	store := m.currentStore()
	view.Subtitle = store.Description(m.editing, storeUsageCount(m.cfg, m.originalName))
	view.Message = storeFieldExamples(m.selectedFieldKey(), store)
	return view
}

func (m *tuiStoreModal) Handle(input tuiModalInput) (bool, string, error) {
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

func (m *tuiStoreModal) selectFirstEditableField() {
	for i, field := range m.modal.Fields {
		if !field.Disabled {
			m.modal.Selected = i
			return
		}
	}
	m.modal.Selected = 0
}

func (m *tuiStoreModal) moveField(delta int) {
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

func (m *tuiStoreModal) cycleField(delta int) {
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
	if field.Key == "store_type" {
		m.updateStoreFieldMetadata()
	}
}

func (m *tuiStoreModal) appendField(text string) {
	field := &m.modal.Fields[m.modal.Selected]
	if field.Disabled || field.Kind != tui.ModalFieldText {
		return
	}
	field.Value += text
	m.clearError()
}

func (m *tuiStoreModal) backspaceField() {
	field := &m.modal.Fields[m.modal.Selected]
	if field.Disabled || field.Kind != tui.ModalFieldText || field.Value == "" {
		return
	}
	runes := []rune(field.Value)
	field.Value = string(runes[:len(runes)-1])
	m.clearError()
}

func (m *tuiStoreModal) submit() (string, error) {
	name := strings.TrimSpace(m.fieldValue("name"))
	if !m.editing {
		if name == "" {
			return "", fieldError("name", "store name is required")
		}
		if err := validateRefName("store", name); err != nil {
			return "", fieldError("name", err.Error())
		}
		if _, exists := m.cfg.Stores[name]; exists {
			return "", fieldError("name", fmt.Sprintf("store %q already exists", name))
		}
	} else {
		name = m.originalName
	}

	uri := m.currentStore().Compose()
	if uri == "" {
		return "", fieldError("store_value", "store details are required")
	}
	if _, err := parseStoreURI(uri); err != nil {
		return "", fieldError("store_value", fmt.Sprintf("invalid store: %v", err))
	}
	store := cloudstic.ProfileStore{URI: uri}
	if m.editing {
		store = m.cfg.Stores[m.originalName]
		store.URI = uri
	}
	if err := tuiServiceFactory(nil, m.profilesFile, nil).SaveStore(m.profilesFile, name, store); err != nil {
		return "", err
	}
	return name, nil
}

func (m *tuiStoreModal) currentStore() tuiStoreConfig {
	return tuiStoreConfig{
		Type:  firstNonEmpty(m.fieldValue("store_type"), "local"),
		Value: m.fieldValue("store_value"),
	}
}

func (m *tuiStoreModal) updateStoreFieldMetadata() {
	field := m.fieldByKey("store_value")
	if field == nil {
		return
	}
	field.Label = m.currentStore().DetailLabel()
	field.Required = true
}

func (m *tuiStoreModal) fieldByKey(key string) *tui.ModalField {
	for i := range m.modal.Fields {
		if m.modal.Fields[i].Key == key {
			return &m.modal.Fields[i]
		}
	}
	return nil
}

func (m *tuiStoreModal) fieldValue(key string) string {
	field := m.fieldByKey(key)
	if field == nil {
		return ""
	}
	return field.Value
}

func (m *tuiStoreModal) selectedFieldKey() string {
	if m.modal.Selected < 0 || m.modal.Selected >= len(m.modal.Fields) {
		return ""
	}
	return m.modal.Fields[m.modal.Selected].Key
}

func (m *tuiStoreModal) clearError() {
	m.modal.Error = ""
	m.modal.ErrorField = ""
}

func storeFieldExamples(selectedField string, store tuiStoreConfig) []string {
	if selectedField != "store_value" {
		return nil
	}
	example := store.ExampleText()
	if example == "" {
		return nil
	}
	return []string{fmt.Sprintf("%s%s%s", ui.Dim, example, ui.Reset)}
}

func storeModalTitle(editing bool) string {
	if editing {
		return "Edit Store"
	}
	return "Create Store"
}

func storeUsageCount(cfg *cloudstic.ProfilesConfig, storeName string) int {
	if cfg == nil || storeName == "" {
		return 0
	}
	count := 0
	for _, profile := range cfg.Profiles {
		if profile.Store == storeName {
			count++
		}
	}
	return count
}
