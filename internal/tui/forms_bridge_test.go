package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudstic/cli/internal/engine"
)

// stubFormsBackend implements FormsBackend for model-level tests.
type stubFormsBackend struct {
	stores     []string
	cfg        *engine.ProfilesConfig
	saved      bool
	deleted    string
	reloadHits int
}

func (b *stubFormsBackend) StoreOptions() []string                 { return b.stores }
func (b *stubFormsBackend) ProviderForSourceType(string) string    { return "" }
func (b *stubFormsBackend) AuthOptions(string) []string            { return nil }
func (b *stubFormsBackend) SourceParts(uri string) (string, string) {
	for i := 0; i < len(uri); i++ {
		if uri[i] == ':' {
			return uri[:i], uri[i+1:]
		}
	}
	return uri, ""
}
func (b *stubFormsBackend) SourceDetailLabel(string) string   { return "Path" }
func (b *stubFormsBackend) SourceDetailRequired(string) bool  { return true }
func (b *stubFormsBackend) SourceExample(string) string       { return "" }
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
func (b *stubFormsBackend) Reload() (*engine.ProfilesConfig, error) {
	b.reloadHits++
	return b.cfg, nil
}

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
	backend := &stubFormsBackend{stores: []string{"s1"}, cfg: &engine.ProfilesConfig{}}
	m := NewModel(formsTestDashboard()).WithForms(backend)

	next, _ := m.Update(keyPress("n"))
	m = next.(Model)
	if m.form == nil {
		t.Fatalf("pressing n should open the create form")
	}
	if !strings.Contains(m.View(), "Create Profile") {
		t.Fatalf("view should show the create form\n%s", m.View())
	}
}

func TestModel_CreateProfileSavesAndReloads(t *testing.T) {
	backend := &stubFormsBackend{stores: []string{"s1"}, cfg: &engine.ProfilesConfig{}}
	m := NewModel(formsTestDashboard()).WithForms(backend).WithConfig(&engine.ProfilesConfig{}, nil)

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
	if m.form != nil {
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
	backend := &stubFormsBackend{stores: []string{"s1"}, cfg: &engine.ProfilesConfig{}}
	m := NewModel(formsTestDashboard()).WithForms(backend).WithConfig(&engine.ProfilesConfig{}, nil)

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
	backend := &stubFormsBackend{stores: []string{"s1"}, cfg: &engine.ProfilesConfig{}}
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

func TestModel_FormKeysNoOpWithoutBackend(t *testing.T) {
	m := NewModel(formsTestDashboard())
	next, cmd := m.Update(keyPress("n"))
	m = next.(Model)
	if m.form != nil || cmd != nil {
		t.Fatalf("form keys must be a no-op without a forms backend")
	}
}

