package forms

import (
	"fmt"
	"strings"
)

// Store form field keys. They are shared with the cmd-side StoreBackend adapter
// (which assembles a ProfileStore from the collected values), so both sides
// must agree on them.
const (
	FieldStoreName      = "name"
	FieldStoreType      = "store_type"
	FieldStoreValue     = "store_value"
	FieldS3Region       = "s3_region"
	FieldS3Profile      = "s3_profile"
	FieldS3Endpoint     = "s3_endpoint"
	FieldS3AccessKey    = "s3_access_key_secret"
	FieldS3SecretKey    = "s3_secret_key_secret"
	FieldSFTPPassword   = "sftp_password_secret"
	FieldSFTPKey        = "sftp_key_secret"
	FieldEncryptionMode = "encryption_mode"
	FieldPasswordSecret = "password_secret"
	FieldPlatformSecret = "encryption_key_secret"
	FieldKMSKeyARN      = "kms_key_arn"
	FieldKMSRegion      = "kms_region"
	FieldKMSEndpoint    = "kms_endpoint"
	// FieldStoreURI is not an input; the form writes the composed URI here for
	// the backend to consume.
	FieldStoreURI = "uri"
)

// Encryption modes offered by the store form.
const (
	EncNone     = "none"
	EncPassword = "password"
	EncPlatform = "platform"
	EncKMS      = "kms"
)

// StoreTypes lists the store schemes offered by the form, in order.
var StoreTypes = []string{"local", "s3", "b2", "sftp"}

// EncryptionModes lists the encryption modes offered by the form, in order.
var EncryptionModes = []string{EncNone, EncPassword, EncPlatform, EncKMS}

var (
	storeS3Fields   = []string{FieldS3Region, FieldS3Profile, FieldS3Endpoint, FieldS3AccessKey, FieldS3SecretKey}
	storeSFTPFields = []string{FieldSFTPPassword, FieldSFTPKey}
	storeEncFields  = []string{FieldPasswordSecret, FieldPlatformSecret, FieldKMSKeyARN, FieldKMSRegion, FieldKMSEndpoint}
	storeSecretRefFields = []string{
		FieldS3AccessKey, FieldS3SecretKey, FieldSFTPPassword, FieldSFTPKey,
		FieldPasswordSecret, FieldPlatformSecret,
	}
	// allStoreFieldKeys is the set of keys sent to SaveStore.
	allStoreFieldKeys = []string{
		FieldStoreType, FieldStoreValue,
		FieldS3Region, FieldS3Profile, FieldS3Endpoint, FieldS3AccessKey, FieldS3SecretKey,
		FieldSFTPPassword, FieldSFTPKey,
		FieldEncryptionMode, FieldPasswordSecret, FieldPlatformSecret,
		FieldKMSKeyARN, FieldKMSRegion, FieldKMSEndpoint,
	}
)

// StoreBackend is the domain boundary for the store form. The cmd package
// implements it by reusing the store-URI and secret-ref helpers, keeping this
// package free of parsing and persistence concerns.
type StoreBackend interface {
	// StoreDetailLabel returns the label for the primary value field.
	StoreDetailLabel(storeType string) string
	// StoreExample returns an example hint for the primary value field.
	StoreExample(storeType string) string
	// ComposeStore builds and validates a store URI from type and value.
	ComposeStore(storeType, value string) (string, error)
	// ValidateStoreName validates a candidate new store name.
	ValidateStoreName(name string) error
	// StoreExists reports whether a store name is already taken.
	StoreExists(name string) bool
	// ValidateSecretRef validates a non-empty secret reference's format.
	ValidateSecretRef(ref string) error
	// SaveStore persists the store from the collected field values.
	SaveStore(name string, values map[string]string, editing bool) error
}

// NewStoreForm builds the create/edit store form. initial seeds field values
// (built by the backend from an existing ProfileStore for edits); for creates
// it may be nil.
func NewStoreForm(backend StoreBackend, existingName string, initial map[string]string, editing bool) *Form {
	get := func(key, def string) string {
		if v := initial[key]; v != "" {
			return v
		}
		return def
	}
	storeType := get(FieldStoreType, "local")

	fields := []Field{
		newTextField(FieldStoreName, "Name", existingName, true),
		newSelectField(FieldStoreType, "Store Type", append([]string{}, StoreTypes...), storeType, true),
		newTextField(FieldStoreValue, backend.StoreDetailLabel(storeType), initial[FieldStoreValue], true),
		newTextField(FieldS3Region, "S3 Region", initial[FieldS3Region], false),
		newTextField(FieldS3Profile, "S3 Profile", initial[FieldS3Profile], false),
		newTextField(FieldS3Endpoint, "S3 Endpoint", initial[FieldS3Endpoint], false),
		newTextField(FieldS3AccessKey, "Access Key Ref", initial[FieldS3AccessKey], false),
		newTextField(FieldS3SecretKey, "Secret Key Ref", initial[FieldS3SecretKey], false),
		newTextField(FieldSFTPPassword, "Password Ref", initial[FieldSFTPPassword], false),
		newTextField(FieldSFTPKey, "Key Ref", initial[FieldSFTPKey], false),
		newSelectField(FieldEncryptionMode, "Encryption", append([]string{}, EncryptionModes...), get(FieldEncryptionMode, EncNone), true),
		newTextField(FieldPasswordSecret, "Password Ref", initial[FieldPasswordSecret], false),
		newTextField(FieldPlatformSecret, "Platform Key Ref", initial[FieldPlatformSecret], false),
		newTextField(FieldKMSKeyARN, "KMS Key ARN", initial[FieldKMSKeyARN], false),
		newTextField(FieldKMSRegion, "KMS Region", get(FieldKMSRegion, "us-east-1"), false),
		newTextField(FieldKMSEndpoint, "KMS Endpoint", initial[FieldKMSEndpoint], false),
	}
	if editing {
		fields[0].Disabled = true
	}

	title := "Create Store"
	if editing {
		title = "Edit Store"
	}
	f := New(title, "", fields)
	sf := &storeFormState{backend: backend, editing: editing, originalName: existingName}
	f.OnChange = sf.onChange
	f.Submit = sf.submit
	sf.refresh(f)
	f.focusFirst()
	return f
}

type storeFormState struct {
	backend      StoreBackend
	editing      bool
	originalName string
}

func (s *storeFormState) onChange(f *Form, changedKey string) {
	switch changedKey {
	case FieldStoreType, FieldEncryptionMode, FieldStoreValue:
		s.refresh(f)
	}
}

// refresh applies type- and mode-driven visibility: connection fields show only
// for their store type, and encryption sub-fields only for their mode. Hidden
// fields are Disabled and not Required, so the generic form skips them.
func (s *storeFormState) refresh(f *Form) {
	storeType := f.Value(FieldStoreType)
	f.SetLabel(FieldStoreValue, s.backend.StoreDetailLabel(storeType))
	f.SetHelp(FieldStoreValue, s.backend.StoreExample(storeType))

	enableS3 := storeType == "s3" || storeType == "b2"
	for _, key := range storeS3Fields {
		f.SetDisabled(key, !enableS3)
		f.SetRequired(key, false)
	}
	enableSFTP := storeType == "sftp"
	for _, key := range storeSFTPFields {
		f.SetDisabled(key, !enableSFTP)
		f.SetRequired(key, false)
	}

	for _, key := range storeEncFields {
		f.SetDisabled(key, true)
		f.SetRequired(key, false)
	}
	switch f.Value(FieldEncryptionMode) {
	case EncPassword:
		f.SetDisabled(FieldPasswordSecret, false)
		f.SetRequired(FieldPasswordSecret, true)
	case EncPlatform:
		f.SetDisabled(FieldPlatformSecret, false)
		f.SetRequired(FieldPlatformSecret, true)
	case EncKMS:
		f.SetDisabled(FieldKMSKeyARN, false)
		f.SetRequired(FieldKMSKeyARN, true)
		f.SetDisabled(FieldKMSRegion, false)
		f.SetDisabled(FieldKMSEndpoint, false)
	}
}

func (s *storeFormState) submit(f *Form) (string, error) {
	name := strings.TrimSpace(f.Value(FieldStoreName))
	if s.editing {
		name = s.originalName
	} else {
		if name == "" {
			return "", NewFieldError(FieldStoreName, "store name is required")
		}
		if err := s.backend.ValidateStoreName(name); err != nil {
			return "", NewFieldError(FieldStoreName, err.Error())
		}
		if s.backend.StoreExists(name) {
			return "", NewFieldError(FieldStoreName, fmt.Sprintf("store %q already exists", name))
		}
	}

	storeType := f.Value(FieldStoreType)
	uri, err := s.backend.ComposeStore(storeType, f.TrimmedValue(FieldStoreValue))
	if err != nil {
		return "", NewFieldError(FieldStoreValue, err.Error())
	}
	if uri == "" {
		return "", NewFieldError(FieldStoreValue, "store details are required")
	}

	// Validate the format of any secret references that are visible.
	for _, key := range storeSecretRefFields {
		if f.field(key).Disabled {
			continue
		}
		if v := f.TrimmedValue(key); v != "" {
			if err := s.backend.ValidateSecretRef(v); err != nil {
				return "", NewFieldError(key, err.Error())
			}
		}
	}

	switch f.Value(FieldEncryptionMode) {
	case EncPassword:
		if f.TrimmedValue(FieldPasswordSecret) == "" {
			return "", NewFieldError(FieldPasswordSecret, "password secret reference is required")
		}
	case EncPlatform:
		if f.TrimmedValue(FieldPlatformSecret) == "" {
			return "", NewFieldError(FieldPlatformSecret, "platform key secret reference is required")
		}
	case EncKMS:
		if f.TrimmedValue(FieldKMSKeyARN) == "" {
			return "", NewFieldError(FieldKMSKeyARN, "KMS key ARN is required")
		}
	}

	values := make(map[string]string, len(allStoreFieldKeys)+1)
	for _, key := range allStoreFieldKeys {
		values[key] = f.TrimmedValue(key)
	}
	values[FieldStoreURI] = uri
	if err := s.backend.SaveStore(name, values, s.editing); err != nil {
		return "", err
	}
	return name, nil
}
