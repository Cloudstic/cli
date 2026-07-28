package forms

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type stubProfileBackend struct {
	stores    []string
	auth      map[string][]string
	provider  map[string]string
	existing  map[string]bool
	saved     *savedProfile
	saveErr   error
	nameError error
}

type savedProfile struct {
	name, source, store, auth string
	editing                   bool
}

func newStubBackend() *stubProfileBackend {
	return &stubProfileBackend{
		stores:   []string{"local-store", "remote"},
		auth:     map[string][]string{"google": {"gauth"}},
		provider: map[string]string{"gdrive": "google", "gdrive-changes": "google"},
		existing: map[string]bool{},
	}
}

func (b *stubProfileBackend) StoreOptions() []string { return b.stores }
func (b *stubProfileBackend) ProviderForSourceType(t string) string {
	return b.provider[t]
}
func (b *stubProfileBackend) AuthOptions(provider string) []string { return b.auth[provider] }
func (b *stubProfileBackend) SourceParts(uri string) (string, string) {
	// Minimal splitter for tests: "type:value".
	for i := 0; i < len(uri); i++ {
		if uri[i] == ':' {
			return uri[:i], uri[i+1:]
		}
	}
	return uri, ""
}
func (b *stubProfileBackend) SourceDetailLabel(t string) string {
	if t == "sftp" {
		return "Target"
	}
	return "Path"
}
func (b *stubProfileBackend) SourceDetailRequired(t string) bool {
	return t != "gdrive"
}
func (b *stubProfileBackend) SourceExample(string) string { return "example" }
func (b *stubProfileBackend) ComposeSource(t, v string) (string, error) {
	if t == "gdrive" && v == "" {
		return "gdrive", nil
	}
	if v == "" {
		return "", nil
	}
	return t + ":" + v, nil
}
func (b *stubProfileBackend) ValidateNewName(string) error { return b.nameError }
func (b *stubProfileBackend) ProfileExists(name string) bool {
	return b.existing[name]
}
func (b *stubProfileBackend) SaveProfile(name, source, store, auth string, editing bool) error {
	if b.saveErr != nil {
		return b.saveErr
	}
	b.saved = &savedProfile{name: name, source: source, store: store, auth: auth, editing: editing}
	return nil
}

func TestProfileForm_CreateSaves(t *testing.T) {
	b := newStubBackend()
	f := NewProfileForm(b, "", "", "", "", false)
	f = typeInto(f, "alpha")                      // name
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyTab}) // -> source_type
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyTab}) // -> source_value
	f = typeInto(f, "/data")

	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !f.Done() || f.Canceled() {
		t.Fatalf("expected save; done=%v canceled=%v", f.Done(), f.Canceled())
	}
	if b.saved == nil {
		t.Fatalf("SaveProfile not called")
	}
	if b.saved.name != "alpha" || b.saved.source != "local:/data" || b.saved.store != "local-store" {
		t.Fatalf("saved=%+v", *b.saved)
	}
	if b.saved.auth != "" {
		t.Fatalf("local source should have no auth, got %q", b.saved.auth)
	}
}

func TestProfileForm_DuplicateNameRejected(t *testing.T) {
	b := newStubBackend()
	b.existing["dupe"] = true
	f := NewProfileForm(b, "", "", "", "", false)
	f = typeInto(f, "dupe")
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyTab})
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyTab})
	f = typeInto(f, "/data")
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if f.Done() {
		t.Fatalf("duplicate name should block submit")
	}
	if b.saved != nil {
		t.Fatalf("SaveProfile must not be called for a duplicate")
	}
	if f.FocusedKey() != "name" {
		t.Fatalf("focus=%q want name", f.FocusedKey())
	}
}

func TestProfileForm_EditLocksNameAndPrefills(t *testing.T) {
	b := newStubBackend()
	f := NewProfileForm(b, "beta", "sftp:user@host/data", "remote", "", true)
	// Name field is disabled; first focus should be source_type.
	if f.FocusedKey() == "name" {
		t.Fatalf("edit form must not focus the locked name field")
	}
	if got := f.Value("source_type"); got != "sftp" {
		t.Fatalf("source_type=%q want sftp", got)
	}
	if got := f.Value("store"); got != "remote" {
		t.Fatalf("store=%q want remote", got)
	}
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !f.Done() {
		t.Fatalf("edit submit should succeed")
	}
	if b.saved == nil || !b.saved.editing || b.saved.name != "beta" {
		t.Fatalf("saved=%+v want editing beta", b.saved)
	}
}

func TestProfileForm_GdriveRequiresAuth(t *testing.T) {
	b := newStubBackend()
	f := NewProfileForm(b, "", "", "", "", false)
	f = typeInto(f, "g")
	// Move to source_type and switch to gdrive.
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyTab})
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRight}) // local -> sftp
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRight}) // sftp -> gdrive
	if got := f.Value("source_type"); got != "gdrive" {
		t.Fatalf("source_type=%q want gdrive", got)
	}
	// gdrive resolves auth to the single available option.
	if got := f.Value("auth"); got != "gauth" {
		t.Fatalf("auth=%q want gauth auto-selected", got)
	}
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !f.Done() || b.saved == nil {
		t.Fatalf("gdrive with auth should save; done=%v saved=%v", f.Done(), b.saved)
	}
	if b.saved.auth != "gauth" {
		t.Fatalf("saved auth=%q want gauth", b.saved.auth)
	}
}

func TestProfileForm_StoreFieldOffersCreateOption(t *testing.T) {
	b := newStubBackend()
	f := NewProfileForm(b, "", "", "", "", false)
	f = focusKey(f, "store")
	// The create sentinel is the last option; a single left-cycle wraps to it.
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if f.Value("store") != CreateStoreOption {
		t.Fatalf("store selector should reach %q, got %q", CreateStoreOption, f.Value("store"))
	}
	// Enter on the create sentinel yields a create-store intent, not a submit.
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	intent, ok := f.TakeIntent()
	if !ok || intent.Name != IntentCreateStore {
		t.Fatalf("expected create_store intent, got %+v ok=%v", intent, ok)
	}
	if f.Done() {
		t.Fatalf("form must not complete when yielding a create intent")
	}
}

func TestProfileForm_EditStoreIntent(t *testing.T) {
	b := newStubBackend()
	f := NewProfileForm(b, "", "", "local-store", "", false)
	f = focusKey(f, "store")
	if f.Value("store") != "local-store" {
		t.Fatalf("store=%q want local-store", f.Value("store"))
	}
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	intent, ok := f.TakeIntent()
	if !ok || intent.Name != IntentEditStore || intent.Value != "local-store" {
		t.Fatalf("expected edit_store intent for local-store, got %+v ok=%v", intent, ok)
	}
}

func TestProfileForm_SaveErrorSurfaces(t *testing.T) {
	b := newStubBackend()
	b.saveErr = errors.New("disk full")
	f := NewProfileForm(b, "", "", "", "", false)
	f = typeInto(f, "alpha")
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyTab})
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyTab})
	f = typeInto(f, "/data")
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if f.Done() {
		t.Fatalf("submit should not complete when SaveProfile errors")
	}
}
