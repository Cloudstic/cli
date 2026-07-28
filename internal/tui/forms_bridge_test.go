package tui

import (
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/profile"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cloudstic/cli/internal/tui/forms"
)

// stubFormsBackend implements FormsBackend for model-level tests.
type stubFormsBackend struct {
	stores       []string
	cfg          *profile.Config
	saved        bool
	deleted      string
	reloadHits   int
	savedStore   string
	storeExists  map[string]bool
	storedSecret string
}

func (b *stubFormsBackend) StoreOptions() []string              { return b.stores }
func (b *stubFormsBackend) ProviderForSourceType(string) string { return "" }
func (b *stubFormsBackend) AuthOptions(string) []string         { return nil }
func (b *stubFormsBackend) SourceParts(uri string) (string, string) {
	for i := 0; i < len(uri); i++ {
		if uri[i] == ':' {
			return uri[:i], uri[i+1:]
		}
	}
	return uri, ""
}
func (b *stubFormsBackend) SourceDetailLabel(string) string  { return "Path" }
func (b *stubFormsBackend) SourceDetailRequired(string) bool { return true }
func (b *stubFormsBackend) SourceExample(string) string      { return "" }
func (b *stubFormsBackend) ComposeSource(t, v string) (string, error) {
	if v == "" {
		return "", nil
	}
	return t + ":" + v, nil
}
func (b *stubFormsBackend) ValidateNewName(string) error { return nil }
func (b *stubFormsBackend) ProfileExists(string) bool    { return false }
func (b *stubFormsBackend) SaveProfile(string, string, string, string, bool) error {
	b.saved = true
	return nil
}
func (b *stubFormsBackend) DeleteProfile(name string) error {
	b.deleted = name
	return nil
}
func (b *stubFormsBackend) Reload() (*profile.Config, error) {
	b.reloadHits++
	return b.cfg, nil
}

// StoreBackend methods.
func (b *stubFormsBackend) StoreDetailLabel(string) string { return "Path" }
func (b *stubFormsBackend) StoreExample(string) string     { return "" }
func (b *stubFormsBackend) ComposeStore(t, v string) (string, error) {
	if v == "" {
		return "", nil
	}
	return t + ":" + v, nil
}
func (b *stubFormsBackend) ValidateStoreName(string) error { return nil }
func (b *stubFormsBackend) StoreExists(name string) bool   { return b.storeExists[name] }
func (b *stubFormsBackend) ValidateSecretRef(string) error { return nil }
func (b *stubFormsBackend) SaveStore(name string, _ map[string]string, _ bool) error {
	b.savedStore = name
	b.stores = append(b.stores, name)
	return nil
}
func (b *stubFormsBackend) InitialStoreValues(string) map[string]string { return nil }

// SecretBackend methods.
func (b *stubFormsBackend) StorageOptions() []string { return []string{"env", "keychain"} }
func (b *stubFormsBackend) DefaultRef(scheme, storeName, account string) string {
	return scheme + "://" + storeName + "/" + account
}
func (b *stubFormsBackend) ParseRef(ref string) (string, string, bool) {
	scheme, rest, ok := strings.Cut(ref, "://")
	if !ok {
		return "", "", false
	}
	if scheme == "env" {
		return "env", rest, true
	}
	return scheme, "", true
}
func (b *stubFormsBackend) ValidateRef(string) error            { return nil }
func (b *stubFormsBackend) StoreSecret(ref, value string) error { b.storedSecret = ref; return nil }

func formsTestDashboard() Dashboard {
	return Dashboard{
		SelectedProfile: "alpha",
		Profiles: []ProfileCard{
			{Name: "alpha", Source: "local:/tmp/a", StoreRef: "s1"},
		},
	}
}

func keyPress(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestModel_OpenCreateProfileForm(t *testing.T) {
	backend := &stubFormsBackend{stores: []string{"s1"}, cfg: &profile.Config{}}
	m := NewModel(formsTestDashboard()).WithForms(backend)

	next, _ := m.Update(keyPress("n"))
	m = next.(Model)
	if m.activeForm() == nil {
		t.Fatalf("pressing n should open the create form")
	}
	if !strings.Contains(m.View(), "Create Profile") {
		t.Fatalf("view should show the create form\n%s", m.View())
	}
}

func TestModel_CreateProfileSavesAndReloads(t *testing.T) {
	backend := &stubFormsBackend{stores: []string{"s1"}, cfg: &profile.Config{}}
	m := NewModel(formsTestDashboard()).WithForms(backend).WithConfig(&profile.Config{}, nil)

	next, _ := m.Update(keyPress("n"))
	m = next.(Model)

	// name -> tab -> tab -> type path -> enter
	for _, r := range "beta" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	for _, r := range "/data" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)

	if !backend.saved {
		t.Fatalf("SaveProfile was not called")
	}
	if m.activeForm() != nil {
		t.Fatalf("form should close after a successful save")
	}
	if cmd == nil {
		t.Fatalf("save should trigger a config reload command")
	}
	// The reload command should hit the backend and produce a configReloadedMsg.
	msg := cmd()
	if _, ok := msg.(configReloadedMsg); !ok {
		t.Fatalf("reload cmd produced %T want configReloadedMsg", msg)
	}
	if backend.reloadHits != 1 {
		t.Fatalf("reload hits=%d want 1", backend.reloadHits)
	}
}

func TestModel_DeleteProfileConfirmFlow(t *testing.T) {
	backend := &stubFormsBackend{stores: []string{"s1"}, cfg: &profile.Config{}}
	m := NewModel(formsTestDashboard()).WithForms(backend).WithConfig(&profile.Config{}, nil)

	next, _ := m.Update(keyPress("d"))
	m = next.(Model)
	if m.confirm == nil {
		t.Fatalf("pressing d should open the delete confirm")
	}
	if !strings.Contains(m.View(), "Delete profile") {
		t.Fatalf("confirm view should mention deletion\n%s", m.View())
	}

	// Confirm with Enter.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if backend.deleted != "alpha" {
		t.Fatalf("deleted=%q want alpha", backend.deleted)
	}
	if m.confirm != nil {
		t.Fatalf("confirm should close after deletion")
	}
	if cmd == nil {
		t.Fatalf("deletion should trigger a config reload")
	}
}

func TestModel_DeleteProfileCancel(t *testing.T) {
	backend := &stubFormsBackend{stores: []string{"s1"}, cfg: &profile.Config{}}
	m := NewModel(formsTestDashboard()).WithForms(backend)

	next, _ := m.Update(keyPress("d"))
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.confirm != nil {
		t.Fatalf("esc should cancel the confirm")
	}
	if backend.deleted != "" {
		t.Fatalf("no deletion should occur on cancel, got %q", backend.deleted)
	}
}

func TestModel_NestedEditSecretFromStoreForm(t *testing.T) {
	backend := &stubFormsBackend{stores: []string{"s1"}, cfg: &profile.Config{}}
	m := NewModel(formsTestDashboard()).WithForms(backend)

	// Open profile form, jump to store field, open a nested create-store form.
	next, _ := m.Update(keyPress("n"))
	m = next.(Model)
	form := m.activeForm()
	for form.FocusedKey() != "store" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = next.(Model)
		form = m.activeForm()
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft}) // "+ Create store"
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if len(m.formStack) != 2 {
		t.Fatalf("store form not opened; depth=%d", len(m.formStack))
	}

	// In the store form, set encryption to password and focus the password ref.
	store := m.activeForm()
	for store.FocusedKey() != forms.FieldEncryptionMode {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = next.(Model)
		store = m.activeForm()
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight}) // none -> password
	m = next.(Model)
	store = m.activeForm()
	for store.FocusedKey() != forms.FieldPasswordSecret {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = next.(Model)
		store = m.activeForm()
	}

	// Ctrl+E opens the nested secret form.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = next.(Model)
	if len(m.formStack) != 3 {
		t.Fatalf("secret form not opened; depth=%d\n%s", len(m.formStack), m.View())
	}
	if !strings.Contains(m.View(), "Configure Secret") {
		t.Fatalf("view should show the secret form\n%s", m.View())
	}

	// Accept the default env reference (Enter).
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if len(m.formStack) != 2 {
		t.Fatalf("should return to the store form; depth=%d", len(m.formStack))
	}
	if got := m.activeForm().Value(forms.FieldPasswordSecret); got != "env://CLOUDSTIC_PASSWORD" {
		t.Fatalf("password secret field=%q want env://CLOUDSTIC_PASSWORD", got)
	}
}

func TestModel_FormKeysNoOpWithoutBackend(t *testing.T) {
	m := NewModel(formsTestDashboard())
	next, cmd := m.Update(keyPress("n"))
	m = next.(Model)
	if m.activeForm() != nil || cmd != nil {
		t.Fatalf("form keys must be a no-op without a forms backend")
	}
}

func TestModel_NestedCreateStoreFromProfileForm(t *testing.T) {
	backend := &stubFormsBackend{stores: []string{"s1"}, cfg: &profile.Config{}}
	m := NewModel(formsTestDashboard()).WithForms(backend)

	// Open the create-profile form.
	next, _ := m.Update(keyPress("n"))
	m = next.(Model)
	if len(m.formStack) != 1 {
		t.Fatalf("form stack depth=%d want 1", len(m.formStack))
	}

	// Navigate to the store field and select the create sentinel, then Enter.
	form := m.activeForm()
	for form.FocusedKey() != "store" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = next.(Model)
		form = m.activeForm()
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft}) // wrap to "+ Create store"
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)

	// A nested store form should now be on top of the stack.
	if len(m.formStack) != 2 {
		t.Fatalf("nested store form not pushed; depth=%d\n%s", len(m.formStack), m.View())
	}
	if !strings.Contains(m.View(), "Create Store") {
		t.Fatalf("view should show the nested store form\n%s", m.View())
	}

	// Fill the store form: name + path, then submit.
	for _, r := range "newstore" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	store := m.activeForm()
	for store.FocusedKey() != forms.FieldStoreValue {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = next.(Model)
		store = m.activeForm()
	}
	for _, r := range "/srv" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)

	if backend.savedStore != "newstore" {
		t.Fatalf("store not saved; savedStore=%q", backend.savedStore)
	}
	// Back to the profile form, with the new store selected.
	if len(m.formStack) != 1 {
		t.Fatalf("should return to the profile form; depth=%d", len(m.formStack))
	}
	if got := m.activeForm().Value("store"); got != "newstore" {
		t.Fatalf("profile store field=%q want newstore selected after nested create", got)
	}
}
