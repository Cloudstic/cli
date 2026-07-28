package forms

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type stubStoreBackend struct {
	saved      map[string]string
	savedName  string
	savedEdit  bool
	exists     map[string]bool
	saveErr    error
	secretErr  error
	composeErr error
}

func newStubStoreBackend() *stubStoreBackend {
	return &stubStoreBackend{exists: map[string]bool{}}
}

func (b *stubStoreBackend) StoreDetailLabel(t string) string {
	if t == "local" {
		return "Path"
	}
	return "Bucket/Prefix"
}
func (b *stubStoreBackend) StoreExample(string) string { return "example" }
func (b *stubStoreBackend) ComposeStore(t, v string) (string, error) {
	if b.composeErr != nil {
		return "", b.composeErr
	}
	if v == "" {
		return "", nil
	}
	return t + ":" + v, nil
}
func (b *stubStoreBackend) ValidateStoreName(string) error { return nil }
func (b *stubStoreBackend) StoreExists(name string) bool   { return b.exists[name] }
func (b *stubStoreBackend) ValidateSecretRef(string) error { return b.secretErr }
func (b *stubStoreBackend) SaveStore(name string, values map[string]string, editing bool) error {
	if b.saveErr != nil {
		return b.saveErr
	}
	b.saved = values
	b.savedName = name
	b.savedEdit = editing
	return nil
}

func focusKey(f *Form, key string) *Form {
	for range f.fields {
		if f.FocusedKey() == key {
			return f
		}
		f, _ = f.Update(tea.KeyMsg{Type: tea.KeyTab})
	}
	return f
}

func TestStoreForm_CreateLocalSaves(t *testing.T) {
	b := newStubStoreBackend()
	f := NewStoreForm(b, "", nil, false)
	f = typeInto(f, "mystore") // name
	f = focusKey(f, FieldStoreValue)
	f = typeInto(f, "/backups")
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if !f.Done() || f.Canceled() {
		t.Fatalf("expected save; done=%v canceled=%v", f.Done(), f.Canceled())
	}
	if b.savedName != "mystore" {
		t.Fatalf("savedName=%q want mystore", b.savedName)
	}
	if b.saved[FieldStoreURI] != "local:/backups" {
		t.Fatalf("uri=%q want local:/backups", b.saved[FieldStoreURI])
	}
}

func TestStoreForm_S3FieldsHiddenForLocal(t *testing.T) {
	b := newStubStoreBackend()
	f := NewStoreForm(b, "", nil, false)
	// Local store type: S3 region field must be disabled (hidden).
	if !f.field(FieldS3Region).Disabled {
		t.Fatalf("S3 region should be hidden for a local store")
	}
	// Switch to s3: move to store_type and cycle right.
	f = focusKey(f, FieldStoreType)
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRight}) // local -> s3
	if f.Value(FieldStoreType) != "s3" {
		t.Fatalf("store_type=%q want s3", f.Value(FieldStoreType))
	}
	if f.field(FieldS3Region).Disabled {
		t.Fatalf("S3 region should be visible for an s3 store")
	}
}

func TestStoreForm_PasswordModeRequiresSecret(t *testing.T) {
	b := newStubStoreBackend()
	f := NewStoreForm(b, "", nil, false)
	f = typeInto(f, "s")
	f = focusKey(f, FieldStoreValue)
	f = typeInto(f, "/data")
	// Set encryption mode to password.
	f = focusKey(f, FieldEncryptionMode)
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRight}) // none -> password
	if f.Value(FieldEncryptionMode) != EncPassword {
		t.Fatalf("mode=%q want password", f.Value(FieldEncryptionMode))
	}
	// Submit without a password ref must fail on that field.
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if f.Done() {
		t.Fatalf("submit should fail when password ref missing")
	}
	if f.FocusedKey() != FieldPasswordSecret {
		t.Fatalf("focus=%q want password_secret", f.FocusedKey())
	}
	// Provide a ref and submit.
	f = typeInto(f, "env://CLOUDSTIC_PASSWORD")
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !f.Done() || b.saved == nil {
		t.Fatalf("submit should succeed with a password ref; done=%v", f.Done())
	}
	if b.saved[FieldPasswordSecret] != "env://CLOUDSTIC_PASSWORD" {
		t.Fatalf("password_secret=%q", b.saved[FieldPasswordSecret])
	}
}

func TestStoreForm_InvalidSecretRefRejected(t *testing.T) {
	b := newStubStoreBackend()
	b.secretErr = errors.New("invalid secret reference")
	f := NewStoreForm(b, "", nil, false)
	f = typeInto(f, "s")
	f = focusKey(f, FieldStoreType)
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRight}) // s3
	f = focusKey(f, FieldStoreValue)
	f = typeInto(f, "bucket")
	f = focusKey(f, FieldS3AccessKey)
	f = typeInto(f, "not-a-ref")
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if f.Done() {
		t.Fatalf("submit should fail on invalid secret ref")
	}
	if f.FocusedKey() != FieldS3AccessKey {
		t.Fatalf("focus=%q want s3 access key", f.FocusedKey())
	}
}

func TestStoreForm_EditLocksName(t *testing.T) {
	b := newStubStoreBackend()
	initial := map[string]string{FieldStoreType: "local", FieldStoreValue: "/old"}
	f := NewStoreForm(b, "existing", initial, true)
	if f.FocusedKey() == FieldStoreName {
		t.Fatalf("edit form should not focus the locked name field")
	}
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !f.Done() || !b.savedEdit || b.savedName != "existing" {
		t.Fatalf("edit save: done=%v edit=%v name=%q", f.Done(), b.savedEdit, b.savedName)
	}
}
