package forms

import (
	"fmt"
	"strings"
)

// Secret form field keys.
const (
	FieldSecretStorage = "storage"
	FieldSecretRef     = "ref"
	FieldSecretValue   = "value"
)

// StorageEnv is the built-in storage option that saves only an environment
// variable reference (the secret value stays outside profiles.yaml).
const StorageEnv = "env"

// SecretFieldInfo describes the secret a secret-ref form configures. It is
// carried from the store field being edited.
type SecretFieldInfo struct {
	// Label names the secret in prose, e.g. "S3 access key".
	Label string
	// DefaultEnvName is the suggested environment variable name.
	DefaultEnvName string
	// DefaultAccount is the account/key hint passed to writable backends.
	DefaultAccount string
}

// SecretBackend is the domain boundary for the secret-ref form. The cmd
// package implements it over the secretref resolver.
type SecretBackend interface {
	// StorageOptions returns the selectable storage schemes: "env" plus the
	// available writable backend schemes (keychain, secret-service, …).
	StorageOptions() []string
	// DefaultRef returns a suggested reference for a writable backend scheme.
	DefaultRef(scheme, storeName, account string) string
	// ParseRef reports a reference's scheme, and for env refs the variable
	// name; ok is false when the reference does not parse.
	ParseRef(ref string) (scheme, envName string, ok bool)
	// ValidateRef validates a reference's format.
	ValidateRef(ref string) error
	// StoreSecret stores value under the reference in its backend.
	StoreSecret(ref, value string) error
}

// NewSecretForm builds the guided secret-reference form. It lets the user keep
// the secret outside profiles.yaml as an env reference, or store the value in a
// platform backend and save the resulting reference.
func NewSecretForm(backend SecretBackend, spec SecretFieldInfo, storeName, existingRef string) *Form {
	if storeName == "" {
		storeName = "store"
	}
	storage, refValue := initialSecretSelection(backend, spec, existingRef)

	fields := []Field{
		newSelectField(FieldSecretStorage, "Storage", backend.StorageOptions(), storage, true),
		newTextField(FieldSecretRef, "Env Var", refValue, true),
		newTextField(FieldSecretValue, "Secret Value", "", false),
	}
	f := New("Configure Secret", fmt.Sprintf("Choose where %s should be stored.", spec.Label), fields)
	sf := &secretFormState{backend: backend, spec: spec, storeName: storeName, existingRef: existingRef}
	f.OnChange = sf.onChange
	f.Submit = sf.submit
	sf.refresh(f)
	f.focusFirst()
	return f
}

func initialSecretSelection(backend SecretBackend, spec SecretFieldInfo, existingRef string) (string, string) {
	if existingRef != "" {
		if scheme, envName, ok := backend.ParseRef(existingRef); ok {
			if scheme == StorageEnv {
				return StorageEnv, envName
			}
			return scheme, existingRef
		}
	}
	return StorageEnv, spec.DefaultEnvName
}

type secretFormState struct {
	backend     SecretBackend
	spec        SecretFieldInfo
	storeName   string
	existingRef string
}

func (s *secretFormState) onChange(f *Form, changedKey string) {
	if changedKey == FieldSecretStorage {
		s.refresh(f)
	}
}

func (s *secretFormState) refresh(f *Form) {
	storage := f.Value(FieldSecretStorage)
	if storage == StorageEnv {
		f.SetLabel(FieldSecretRef, "Env Var")
		f.SetRequired(FieldSecretRef, true)
		if ref := f.Value(FieldSecretRef); strings.Contains(ref, "://") || strings.TrimSpace(ref) == "" {
			f.SetValue(FieldSecretRef, s.spec.DefaultEnvName)
		}
		f.SetValue(FieldSecretValue, "")
		f.SetDisabled(FieldSecretValue, true)
		f.SetRequired(FieldSecretValue, false)
		f.SetHelp(FieldSecretRef, fmt.Sprintf("Saves only a reference like env://%s; the value stays outside profiles.yaml.", s.spec.DefaultEnvName))
		return
	}

	f.SetLabel(FieldSecretRef, "Reference")
	f.SetRequired(FieldSecretRef, true)
	if scheme, _, ok := s.backend.ParseRef(f.Value(FieldSecretRef)); !ok || scheme != storage {
		f.SetValue(FieldSecretRef, s.backend.DefaultRef(storage, s.storeName, s.spec.DefaultAccount))
	}
	f.SetDisabled(FieldSecretValue, false)
	f.SetRequired(FieldSecretValue, true)
	f.SetHelp(FieldSecretRef, fmt.Sprintf("The value is stored now and the %s:// reference is saved in profiles.yaml.", storage))
}

func (s *secretFormState) submit(f *Form) (string, error) {
	storage := f.Value(FieldSecretStorage)
	rawRef := strings.TrimSpace(f.Value(FieldSecretRef))

	if storage == StorageEnv {
		if rawRef == "" {
			return "", NewFieldError(FieldSecretRef, "environment variable name is required")
		}
		ref := "env://" + rawRef
		if err := s.backend.ValidateRef(ref); err != nil {
			return "", NewFieldError(FieldSecretRef, err.Error())
		}
		return ref, nil
	}

	if rawRef == "" {
		return "", NewFieldError(FieldSecretRef, "reference is required")
	}
	if err := s.backend.ValidateRef(rawRef); err != nil {
		return "", NewFieldError(FieldSecretRef, err.Error())
	}
	if scheme, _, _ := s.backend.ParseRef(rawRef); scheme != storage {
		return "", NewFieldError(FieldSecretRef, fmt.Sprintf("reference must use %s://", storage))
	}

	value := f.Value(FieldSecretValue)
	if value == "" {
		if rawRef == s.existingRef {
			return rawRef, nil // unchanged reference, value already stored
		}
		return "", NewFieldError(FieldSecretValue, "secret value is required")
	}
	if err := s.backend.StoreSecret(rawRef, value); err != nil {
		return "", err
	}
	return rawRef, nil
}
