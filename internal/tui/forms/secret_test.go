package forms

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type stubSecretBackend struct {
	stored   map[string]string
	storeErr error
}

func newStubSecretBackend() *stubSecretBackend {
	return &stubSecretBackend{stored: map[string]string{}}
}

func (b *stubSecretBackend) StorageOptions() []string { return []string{StorageEnv, "keychain"} }
func (b *stubSecretBackend) DefaultRef(scheme, storeName, account string) string {
	return scheme + "://" + storeName + "/" + account
}
func (b *stubSecretBackend) ParseRef(ref string) (string, string, bool) {
	scheme, rest, ok := strings.Cut(ref, "://")
	if !ok {
		return "", "", false
	}
	if scheme == StorageEnv {
		return StorageEnv, rest, true
	}
	return scheme, "", true
}
func (b *stubSecretBackend) ValidateRef(string) error { return nil }
func (b *stubSecretBackend) StoreSecret(ref, value string) error {
	if b.storeErr != nil {
		return b.storeErr
	}
	b.stored[ref] = value
	return nil
}

func secretSpec() SecretFieldInfo {
	return SecretFieldInfo{Label: "repository password", DefaultEnvName: "CLOUDSTIC_PASSWORD", DefaultAccount: "password"}
}

func TestSecretForm_EnvReturnsReferenceWithoutStoring(t *testing.T) {
	b := newStubSecretBackend()
	f := NewSecretForm(b, secretSpec(), "mystore", "")
	// Defaults to env storage with the suggested var name.
	if f.Value(FieldSecretStorage) != StorageEnv {
		t.Fatalf("storage=%q want env", f.Value(FieldSecretStorage))
	}
	if f.Value(FieldSecretRef) != "CLOUDSTIC_PASSWORD" {
		t.Fatalf("ref=%q want CLOUDSTIC_PASSWORD", f.Value(FieldSecretRef))
	}
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !f.Done() {
		t.Fatalf("env submit should succeed")
	}
	if f.Result() != "env://CLOUDSTIC_PASSWORD" {
		t.Fatalf("result=%q want env://CLOUDSTIC_PASSWORD", f.Result())
	}
	if len(b.stored) != 0 {
		t.Fatalf("env storage must not store a value, stored=%v", b.stored)
	}
}

func TestSecretForm_BackendStoresValue(t *testing.T) {
	b := newStubSecretBackend()
	f := NewSecretForm(b, secretSpec(), "mystore", "")
	// Switch storage to keychain.
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRight})
	if f.Value(FieldSecretStorage) != "keychain" {
		t.Fatalf("storage=%q want keychain", f.Value(FieldSecretStorage))
	}
	// The ref defaults to the backend's suggestion; the value field is now
	// enabled and required.
	if f.field(FieldSecretValue).Disabled {
		t.Fatalf("value field should be enabled for a writable backend")
	}
	// Submit with no value must fail.
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if f.Done() {
		t.Fatalf("submit should fail without a secret value")
	}
	// Enter a value and submit.
	f = focusKey(f, FieldSecretValue)
	f = typeInto(f, "s3cr3t")
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !f.Done() {
		t.Fatalf("submit should succeed with a value")
	}
	ref := "keychain://mystore/password"
	if b.stored[ref] != "s3cr3t" {
		t.Fatalf("stored[%q]=%q want s3cr3t (stored=%v)", ref, b.stored[ref], b.stored)
	}
	if f.Result() != ref {
		t.Fatalf("result=%q want %q", f.Result(), ref)
	}
}

func TestSecretForm_ExistingBackendRefUnchanged(t *testing.T) {
	b := newStubSecretBackend()
	// Editing an existing keychain reference: submitting with no new value
	// keeps the reference and does not re-store.
	f := NewSecretForm(b, secretSpec(), "mystore", "keychain://mystore/password")
	if f.Value(FieldSecretStorage) != "keychain" {
		t.Fatalf("storage=%q want keychain from existing ref", f.Value(FieldSecretStorage))
	}
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !f.Done() {
		t.Fatalf("submit with unchanged existing ref should succeed")
	}
	if f.Result() != "keychain://mystore/password" {
		t.Fatalf("result=%q want existing ref", f.Result())
	}
	if len(b.stored) != 0 {
		t.Fatalf("unchanged ref must not re-store")
	}
}
