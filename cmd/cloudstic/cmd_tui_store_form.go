package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/secretref"
	"github.com/cloudstic/cli/internal/tui"
	"github.com/cloudstic/cli/internal/ui"
)

type tuiStoreConfig struct {
	Type  string
	Value string
}

type tuiStoreEncryptionMode string

const (
	tuiStoreEncryptionNone     tuiStoreEncryptionMode = "none"
	tuiStoreEncryptionPassword tuiStoreEncryptionMode = "password"
	tuiStoreEncryptionPlatform tuiStoreEncryptionMode = "platform"
	tuiStoreEncryptionKMS      tuiStoreEncryptionMode = "kms"
)

type tuiSecretFieldSpec struct {
	FieldKey       string
	SecretLabel    string
	DefaultEnvName string
	DefaultAccount string
}

var tuiSecretResolver = profileSecretResolver

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

func newTUIStoreEncryptionMode(existing cloudstic.ProfileStore) tuiStoreEncryptionMode {
	switch {
	case existing.KMSKeyARN != "":
		return tuiStoreEncryptionKMS
	case existing.EncryptionKeySecret != "":
		return tuiStoreEncryptionPlatform
	case existing.PasswordSecret != "":
		return tuiStoreEncryptionPassword
	default:
		return tuiStoreEncryptionNone
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

var tuiStoreEncryptionOptions = []string{
	string(tuiStoreEncryptionNone),
	string(tuiStoreEncryptionPassword),
	string(tuiStoreEncryptionPlatform),
	string(tuiStoreEncryptionKMS),
}

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
	encryptionMode := newTUIStoreEncryptionMode(existing)
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
				{Key: "s3_region", Label: "S3 Region", Kind: tui.ModalFieldText, Value: existing.S3Region},
				{Key: "s3_profile", Label: "S3 Profile", Kind: tui.ModalFieldText, Value: existing.S3Profile},
				{Key: "s3_endpoint", Label: "S3 Endpoint", Kind: tui.ModalFieldText, Value: existing.S3Endpoint},
				{Key: "s3_access_key_secret", Label: "Access Key Ref", Kind: tui.ModalFieldText, Value: existing.S3AccessKeySecret},
				{Key: "s3_secret_key_secret", Label: "Secret Key Ref", Kind: tui.ModalFieldText, Value: existing.S3SecretKeySecret},
				{Key: "sftp_password_secret", Label: "Password Ref", Kind: tui.ModalFieldText, Value: existing.StoreSFTPPasswordSecret},
				{Key: "sftp_key_secret", Label: "Key Ref", Kind: tui.ModalFieldText, Value: existing.StoreSFTPKeySecret},
				{Key: "encryption_mode", Label: "Encryption", Kind: tui.ModalFieldSelect, Value: string(encryptionMode), Options: append([]string{}, tuiStoreEncryptionOptions...), Required: true},
				{Key: "password_secret", Label: "Password Ref", Kind: tui.ModalFieldText, Value: existing.PasswordSecret},
				{Key: "encryption_key_secret", Label: "Platform Key Ref", Kind: tui.ModalFieldText, Value: existing.EncryptionKeySecret},
				{Key: "kms_key_arn", Label: "KMS Key ARN", Kind: tui.ModalFieldText, Value: existing.KMSKeyARN},
				{Key: "kms_region", Label: "KMS Region", Kind: tui.ModalFieldText, Value: firstNonEmpty(existing.KMSRegion, "us-east-1")},
				{Key: "kms_endpoint", Label: "KMS Endpoint", Kind: tui.ModalFieldText, Value: existing.KMSEndpoint},
			},
		},
	}
	m.rebuildDerivedFields()
	m.selectFirstEditableField()
	return m, nil
}

func (m *tuiStoreModal) View() tui.Modal {
	view := m.modal
	store := m.currentStore()
	view.Subtitle = store.Description(m.editing, storeUsageCount(m.cfg, m.originalName))
	view.Message = storeFieldHelp(m.selectedFieldKey(), store, m.currentEncryptionMode())
	view.Fields, view.Selected = visibleStoreModalFields(m.modal.Fields, m.modal.Selected)
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

func (m *tuiStoreModal) wantsEditSecret(input tuiModalInput) (tuiSecretFieldSpec, bool) {
	if input.Kind != tuiModalInputText || !strings.EqualFold(input.Text, "e") {
		return tuiSecretFieldSpec{}, false
	}
	spec, ok := tuiSecretFieldSpecForKey(m.selectedFieldKey())
	return spec, ok
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
	switch field.Key {
	case "store_type", "encryption_mode":
		m.rebuildDerivedFields()
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

	store := cloudstic.ProfileStore{}
	if m.editing {
		store = m.cfg.Stores[m.originalName]
	}
	store.URI = uri
	m.applyConnectionFields(&store)
	if err := m.applyEncryptionFields(&store); err != nil {
		return "", err
	}

	if err := tuiServiceFactory(nil, m.profilesFile, nil).SaveStore(m.profilesFile, name, store); err != nil {
		return "", err
	}
	return name, nil
}

func (m *tuiStoreModal) applyConnectionFields(store *cloudstic.ProfileStore) {
	storeType := m.currentStore().Type
	switch storeType {
	case "s3", "b2":
		store.S3Region = m.textFieldValue("s3_region")
		store.S3Profile = m.textFieldValue("s3_profile")
		store.S3Endpoint = m.textFieldValue("s3_endpoint")
		store.S3AccessKeySecret = m.textFieldValue("s3_access_key_secret")
		store.S3SecretKeySecret = m.textFieldValue("s3_secret_key_secret")
	case "local":
		store.S3Region = ""
		store.S3Profile = ""
		store.S3Endpoint = ""
		store.S3AccessKeySecret = ""
		store.S3SecretKeySecret = ""
	}
	if storeType != "s3" && storeType != "b2" {
		store.S3Region = ""
		store.S3Profile = ""
		store.S3Endpoint = ""
		store.S3AccessKeySecret = ""
		store.S3SecretKeySecret = ""
	}
	if storeType == "sftp" {
		store.StoreSFTPPasswordSecret = m.textFieldValue("sftp_password_secret")
		store.StoreSFTPKeySecret = m.textFieldValue("sftp_key_secret")
	} else {
		store.StoreSFTPPasswordSecret = ""
		store.StoreSFTPKeySecret = ""
	}
}

func (m *tuiStoreModal) applyEncryptionFields(store *cloudstic.ProfileStore) error {
	mode := m.currentEncryptionMode()
	passwordRef := m.textFieldValue("password_secret")
	platformRef := m.textFieldValue("encryption_key_secret")
	kmsKeyARN := m.textFieldValue("kms_key_arn")
	kmsRegion := m.textFieldValue("kms_region")
	kmsEndpoint := m.textFieldValue("kms_endpoint")

	for _, refField := range []struct {
		key   string
		value string
	}{
		{key: "s3_access_key_secret", value: m.textFieldValue("s3_access_key_secret")},
		{key: "s3_secret_key_secret", value: m.textFieldValue("s3_secret_key_secret")},
		{key: "sftp_password_secret", value: m.textFieldValue("sftp_password_secret")},
		{key: "sftp_key_secret", value: m.textFieldValue("sftp_key_secret")},
		{key: "password_secret", value: passwordRef},
		{key: "encryption_key_secret", value: platformRef},
	} {
		if err := validateSecretRefField(refField.key, refField.value); err != nil {
			return err
		}
	}

	store.PasswordSecret = ""
	store.EncryptionKeySecret = ""
	store.RecoveryKeySecret = ""
	store.KMSKeyARN = ""
	store.KMSRegion = ""
	store.KMSEndpoint = ""

	switch mode {
	case tuiStoreEncryptionNone:
		return nil
	case tuiStoreEncryptionPassword:
		if passwordRef == "" {
			return fieldError("password_secret", "password secret reference is required")
		}
		store.PasswordSecret = passwordRef
	case tuiStoreEncryptionPlatform:
		if platformRef == "" {
			return fieldError("encryption_key_secret", "platform key secret reference is required")
		}
		store.EncryptionKeySecret = platformRef
	case tuiStoreEncryptionKMS:
		if kmsKeyARN == "" {
			return fieldError("kms_key_arn", "KMS key ARN is required")
		}
		store.KMSKeyARN = kmsKeyARN
		store.KMSRegion = firstNonEmpty(kmsRegion, "us-east-1")
		store.KMSEndpoint = kmsEndpoint
	}
	return nil
}

func validateSecretRefField(key, value string) error {
	if value == "" {
		return nil
	}
	if _, err := secretref.Parse(value); err != nil {
		return fieldError(key, fmt.Sprintf("invalid secret reference: %v", err))
	}
	return nil
}

func (m *tuiStoreModal) currentStore() tuiStoreConfig {
	return tuiStoreConfig{
		Type:  firstNonEmpty(m.fieldValue("store_type"), "local"),
		Value: m.fieldValue("store_value"),
	}
}

func (m *tuiStoreModal) currentEncryptionMode() tuiStoreEncryptionMode {
	return tuiStoreEncryptionMode(firstNonEmpty(m.fieldValue("encryption_mode"), string(tuiStoreEncryptionNone)))
}

func (m *tuiStoreModal) rebuildDerivedFields() {
	m.updateStoreFieldMetadata()
	m.updateConnectionFields()
	m.updateEncryptionFields()
}

func (m *tuiStoreModal) updateStoreFieldMetadata() {
	field := m.fieldByKey("store_value")
	if field == nil {
		return
	}
	field.Label = m.currentStore().DetailLabel()
	field.Required = true
}

func (m *tuiStoreModal) updateConnectionFields() {
	storeType := m.currentStore().Type
	s3Fields := []string{"s3_region", "s3_profile", "s3_endpoint", "s3_access_key_secret", "s3_secret_key_secret"}
	sftpFields := []string{"sftp_password_secret", "sftp_key_secret"}
	enableS3 := storeType == "s3" || storeType == "b2"
	for _, key := range s3Fields {
		if field := m.fieldByKey(key); field != nil {
			field.Disabled = !enableS3
			field.Required = false
		}
	}
	enableSFTP := storeType == "sftp"
	for _, key := range sftpFields {
		if field := m.fieldByKey(key); field != nil {
			field.Disabled = !enableSFTP
			field.Required = false
		}
	}
}

func (m *tuiStoreModal) updateEncryptionFields() {
	mode := m.currentEncryptionMode()
	for _, key := range []string{"password_secret", "encryption_key_secret", "kms_key_arn", "kms_region", "kms_endpoint"} {
		if field := m.fieldByKey(key); field != nil {
			field.Disabled = true
			field.Required = false
		}
	}
	switch mode {
	case tuiStoreEncryptionPassword:
		if field := m.fieldByKey("password_secret"); field != nil {
			field.Disabled = false
			field.Required = true
		}
	case tuiStoreEncryptionPlatform:
		if field := m.fieldByKey("encryption_key_secret"); field != nil {
			field.Disabled = false
			field.Required = true
		}
	case tuiStoreEncryptionKMS:
		if field := m.fieldByKey("kms_key_arn"); field != nil {
			field.Disabled = false
			field.Required = true
		}
		if field := m.fieldByKey("kms_region"); field != nil {
			field.Disabled = false
		}
		if field := m.fieldByKey("kms_endpoint"); field != nil {
			field.Disabled = false
		}
	}
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

func (m *tuiStoreModal) setFieldValue(key, value string) {
	field := m.fieldByKey(key)
	if field == nil {
		return
	}
	field.Value = value
}

func (m *tuiStoreModal) textFieldValue(key string) string {
	return strings.TrimSpace(m.fieldValue(key))
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

func storeFieldHelp(selectedField string, store tuiStoreConfig, mode tuiStoreEncryptionMode) []string {
	switch selectedField {
	case "store_value":
		example := store.ExampleText()
		if example == "" {
			return nil
		}
		return []string{fmt.Sprintf("%s%s%s", ui.Dim, example, ui.Reset)}
	case "s3_access_key_secret", "s3_secret_key_secret", "sftp_password_secret", "sftp_key_secret", "password_secret", "encryption_key_secret":
		return []string{fmt.Sprintf("%sType e to configure secret storage.%s", ui.Dim, ui.Reset)}
	case "kms_key_arn":
		return []string{fmt.Sprintf("%sExample: arn:aws:kms:us-east-1:123456789012:key/abcd...%s", ui.Dim, ui.Reset)}
	case "kms_region":
		if mode != tuiStoreEncryptionKMS {
			return nil
		}
		return []string{fmt.Sprintf("%sExample: us-east-1%s", ui.Dim, ui.Reset)}
	case "kms_endpoint":
		if mode != tuiStoreEncryptionKMS {
			return nil
		}
		return []string{fmt.Sprintf("%sExample: https://kms.example.com%s", ui.Dim, ui.Reset)}
	}
	return nil
}

func visibleStoreModalFields(fields []tui.ModalField, selected int) ([]tui.ModalField, int) {
	visible := make([]tui.ModalField, 0, len(fields))
	selectedVisible := 0
	for i, field := range fields {
		if field.Disabled && !field.Required {
			continue
		}
		if i == selected {
			selectedVisible = len(visible)
		}
		visible = append(visible, field)
	}
	if len(visible) == 0 {
		return nil, 0
	}
	if selectedVisible >= len(visible) {
		selectedVisible = len(visible) - 1
	}
	return visible, selectedVisible
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

func (m *tuiStoreModal) storeName() string {
	name := strings.TrimSpace(m.fieldValue("name"))
	if name != "" {
		return name
	}
	if m.originalName != "" {
		return m.originalName
	}
	return "store"
}

func tuiSecretFieldSpecForKey(key string) (tuiSecretFieldSpec, bool) {
	specs := map[string]tuiSecretFieldSpec{
		"s3_access_key_secret": {
			FieldKey:       "s3_access_key_secret",
			SecretLabel:    "S3 access key",
			DefaultEnvName: "AWS_ACCESS_KEY_ID",
			DefaultAccount: "s3-access-key",
		},
		"s3_secret_key_secret": {
			FieldKey:       "s3_secret_key_secret",
			SecretLabel:    "S3 secret key",
			DefaultEnvName: "AWS_SECRET_ACCESS_KEY",
			DefaultAccount: "s3-secret-key",
		},
		"sftp_password_secret": {
			FieldKey:       "sftp_password_secret",
			SecretLabel:    "SFTP password",
			DefaultEnvName: "CLOUDSTIC_STORE_SFTP_PASSWORD",
			DefaultAccount: "store-sftp-password",
		},
		"sftp_key_secret": {
			FieldKey:       "sftp_key_secret",
			SecretLabel:    "SFTP key path",
			DefaultEnvName: "CLOUDSTIC_STORE_SFTP_KEY",
			DefaultAccount: "store-sftp-key",
		},
		"password_secret": {
			FieldKey:       "password_secret",
			SecretLabel:    "repository password",
			DefaultEnvName: "CLOUDSTIC_PASSWORD",
			DefaultAccount: "password",
		},
		"encryption_key_secret": {
			FieldKey:       "encryption_key_secret",
			SecretLabel:    "platform key",
			DefaultEnvName: "CLOUDSTIC_ENCRYPTION_KEY",
			DefaultAccount: "encryption-key",
		},
	}
	spec, ok := specs[key]
	return spec, ok
}

type tuiSecretRefModal struct {
	storeName    string
	spec         tuiSecretFieldSpec
	existingRef  string
	resolver     *secretref.Resolver
	backendByRef map[string]secretref.WritableBackend
	modal        tui.Modal
}

func newTUISecretRefModal(storeName string, spec tuiSecretFieldSpec, existingRef string) *tuiSecretRefModal {
	if storeName == "" {
		storeName = "store"
	}
	resolver := tuiSecretResolver
	if resolver == nil {
		resolver = secretref.NewDefaultResolver()
	}
	backends := resolver.WritableBackends()
	options := []string{"env"}
	backendByRef := map[string]secretref.WritableBackend{}
	for _, backend := range backends {
		options = append(options, backend.Scheme())
		backendByRef[backend.Scheme()] = backend
	}
	storage, refValue := initialSecretRefSelection(spec, existingRef)
	m := &tuiSecretRefModal{
		storeName:    storeName,
		spec:         spec,
		existingRef:  existingRef,
		resolver:     resolver,
		backendByRef: backendByRef,
		modal: tui.Modal{
			Kind:        tui.ModalKindProfileForm,
			Title:       "Configure Secret",
			Subtitle:    fmt.Sprintf("Choose where %s should be stored.", spec.SecretLabel),
			Hint:        "↑/↓ or Tab to move, ←/→ to change selections, Enter to save, Esc to cancel.",
			SubmitLabel: "Save",
			CancelLabel: "Cancel",
			Fields: []tui.ModalField{
				{Key: "storage", Label: "Storage", Kind: tui.ModalFieldSelect, Value: storage, Options: options, Required: true},
				{Key: "ref", Label: "Env Var", Kind: tui.ModalFieldText, Value: refValue, Required: true},
				{Key: "value", Label: "Secret Value", Kind: tui.ModalFieldText, Value: ""},
			},
		},
	}
	m.updateFields()
	return m
}

func initialSecretRefSelection(spec tuiSecretFieldSpec, existingRef string) (string, string) {
	if existingRef != "" {
		if ref, err := secretref.Parse(existingRef); err == nil {
			if ref.Scheme == "env" {
				return "env", strings.TrimLeft(ref.Path, "/")
			}
			return ref.Scheme, existingRef
		}
	}
	return "env", spec.DefaultEnvName
}

func (m *tuiSecretRefModal) View() tui.Modal {
	view := m.modal
	view.Fields, view.Selected = visibleStoreModalFields(m.modal.Fields, m.modal.Selected)
	view.Message = secretRefHelp(m.currentStorage(), m.spec)
	return view
}

func (m *tuiSecretRefModal) Handle(input tuiModalInput) (bool, string, error) {
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
		ref, err := m.submit(context.Background())
		if err != nil {
			if fieldErr, ok := err.(*tuiFieldError); ok {
				m.modal.ErrorField = fieldErr.Field
				m.modal.Error = fieldErr.Message
			} else {
				m.modal.Error = err.Error()
				m.modal.ErrorField = ""
			}
			return false, "", nil
		}
		return true, ref, nil
	}
	return false, "", nil
}

func (m *tuiSecretRefModal) moveField(delta int) {
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

func (m *tuiSecretRefModal) cycleField(delta int) {
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
	m.updateFields()
}

func (m *tuiSecretRefModal) appendField(text string) {
	field := &m.modal.Fields[m.modal.Selected]
	if field.Disabled || field.Kind != tui.ModalFieldText {
		return
	}
	field.Value += text
	m.clearError()
}

func (m *tuiSecretRefModal) backspaceField() {
	field := &m.modal.Fields[m.modal.Selected]
	if field.Disabled || field.Kind != tui.ModalFieldText || field.Value == "" {
		return
	}
	runes := []rune(field.Value)
	field.Value = string(runes[:len(runes)-1])
	m.clearError()
}

func (m *tuiSecretRefModal) updateFields() {
	refField := m.fieldByKey("ref")
	valueField := m.fieldByKey("value")
	if refField == nil || valueField == nil {
		return
	}
	if m.currentStorage() == "env" {
		refField.Label = "Env Var"
		refField.Required = true
		if strings.Contains(refField.Value, "://") || strings.TrimSpace(refField.Value) == "" {
			refField.Value = m.spec.DefaultEnvName
		}
		valueField.Disabled = true
		valueField.Required = false
		valueField.Value = ""
		return
	}
	refField.Label = "Reference"
	refField.Required = true
	if parsed, err := secretref.Parse(refField.Value); err != nil || parsed.Scheme != m.currentStorage() {
		if backend := m.backendByRef[m.currentStorage()]; backend != nil {
			refField.Value = backend.DefaultRef(m.storeName, m.spec.DefaultAccount)
		}
	}
	valueField.Disabled = false
	valueField.Required = true
}

func (m *tuiSecretRefModal) submit(ctx context.Context) (string, error) {
	rawRef := strings.TrimSpace(m.fieldValue("ref"))
	if m.currentStorage() == "env" {
		if rawRef == "" {
			return "", fieldError("ref", "environment variable name is required")
		}
		ref := envRef(rawRef)
		if _, err := secretref.Parse(ref); err != nil {
			return "", fieldError("ref", err.Error())
		}
		return ref, nil
	}
	if rawRef == "" {
		return "", fieldError("ref", "reference is required")
	}
	parsed, err := secretref.Parse(rawRef)
	if err != nil {
		return "", fieldError("ref", err.Error())
	}
	if parsed.Scheme != m.currentStorage() {
		return "", fieldError("ref", fmt.Sprintf("reference must use %s://", m.currentStorage()))
	}
	secretValue := m.fieldValue("value")
	if secretValue == "" {
		if rawRef == m.existingRef {
			return rawRef, nil
		}
		return "", fieldError("value", "secret value is required")
	}
	if err := m.resolver.Store(ctx, rawRef, secretValue); err != nil {
		return "", err
	}
	return rawRef, nil
}

func (m *tuiSecretRefModal) currentStorage() string {
	return firstNonEmpty(m.fieldValue("storage"), "env")
}

func (m *tuiSecretRefModal) fieldByKey(key string) *tui.ModalField {
	for i := range m.modal.Fields {
		if m.modal.Fields[i].Key == key {
			return &m.modal.Fields[i]
		}
	}
	return nil
}

func (m *tuiSecretRefModal) fieldValue(key string) string {
	field := m.fieldByKey(key)
	if field == nil {
		return ""
	}
	return field.Value
}

func (m *tuiSecretRefModal) clearError() {
	m.modal.Error = ""
	m.modal.ErrorField = ""
}

func secretRefHelp(storage string, spec tuiSecretFieldSpec) []string {
	if storage == "env" {
		return []string{fmt.Sprintf("%sSave only a reference like env://%s. The secret value stays outside profiles.yaml.%s", ui.Dim, spec.DefaultEnvName, ui.Reset)}
	}
	return []string{fmt.Sprintf("%sThe secret will be stored now and the resulting %s:// reference will be saved in profiles.yaml.%s", ui.Dim, storage, ui.Reset)}
}
