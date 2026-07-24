package forms

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func typeInto(f *Form, s string) *Form {
	for _, r := range s {
		f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return f
}

func textForm(t *testing.T) *Form {
	t.Helper()
	fields := []Field{
		newTextField("name", "Name", "", true),
		newSelectField("kind", "Kind", []string{"a", "b", "c"}, "a", true),
	}
	f := New("Test", "", fields)
	return f
}

func TestForm_UnicodeTextInput(t *testing.T) {
	f := textForm(t)
	// Accented and non-Latin runes must be accepted, unlike the old
	// ASCII-only byte reader.
	f = typeInto(f, "café-Ω-日本")
	if got := f.Value("name"); got != "café-Ω-日本" {
		t.Fatalf("name=%q want café-Ω-日本", got)
	}
}

func TestForm_Backspace(t *testing.T) {
	f := textForm(t)
	f = typeInto(f, "abé")
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := f.Value("name"); got != "ab" {
		t.Fatalf("name=%q want ab", got)
	}
}

func TestForm_SelectCyclesWithArrows(t *testing.T) {
	f := textForm(t)
	// Move focus to the select field.
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyTab})
	if f.FocusedKey() != "kind" {
		t.Fatalf("focus=%q want kind", f.FocusedKey())
	}
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyRight})
	if got := f.Value("kind"); got != "b" {
		t.Fatalf("kind=%q want b after right", got)
	}
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyLeft})
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if got := f.Value("kind"); got != "c" {
		t.Fatalf("kind=%q want c after wrap-around left", got)
	}
}

func TestForm_LeftRightEditsTextCursorNotSelect(t *testing.T) {
	f := textForm(t)
	f = typeInto(f, "abc")
	// Left arrow on a text field moves the cursor; it must not be swallowed as
	// a select change. Typing then inserts before the last char.
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyLeft})
	f = typeInto(f, "X")
	if got := f.Value("name"); got != "abXc" {
		t.Fatalf("name=%q want abXc", got)
	}
}

func TestForm_TabSkipsDisabledFields(t *testing.T) {
	fields := []Field{
		newTextField("a", "A", "", false),
		newTextField("b", "B", "", false),
		newTextField("c", "C", "", false),
	}
	f := New("t", "", fields)
	f.SetDisabled("b", true)
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyTab})
	if f.FocusedKey() != "c" {
		t.Fatalf("focus=%q want c (b disabled, skipped)", f.FocusedKey())
	}
}

func TestForm_SubmitSuccess(t *testing.T) {
	f := textForm(t)
	f.Submit = func(f *Form) (string, error) {
		return f.Value("name"), nil
	}
	f = typeInto(f, "hello")
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !f.Done() || f.Canceled() {
		t.Fatalf("expected done+not-canceled, got done=%v canceled=%v", f.Done(), f.Canceled())
	}
	if f.Result() != "hello" {
		t.Fatalf("result=%q want hello", f.Result())
	}
}

func TestForm_SubmitFieldErrorFocusesField(t *testing.T) {
	f := textForm(t)
	f.Submit = func(f *Form) (string, error) {
		return "", NewFieldError("kind", "kind is wrong")
	}
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if f.Done() {
		t.Fatalf("form should not be done after a field error")
	}
	if f.FocusedKey() != "kind" {
		t.Fatalf("focus=%q want kind (error field)", f.FocusedKey())
	}
	if !strings.Contains(f.View(), "kind is wrong") {
		t.Fatalf("view missing error message\n%s", f.View())
	}
}

func TestForm_Cancel(t *testing.T) {
	f := textForm(t)
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !f.Done() || !f.Canceled() {
		t.Fatalf("esc should cancel: done=%v canceled=%v", f.Done(), f.Canceled())
	}
}

func TestForm_OnChangeFires(t *testing.T) {
	f := textForm(t)
	var changed []string
	f.OnChange = func(_ *Form, key string) { changed = append(changed, key) }
	f = typeInto(f, "x")
	f, _ = f.Update(tea.KeyMsg{Type: tea.KeyTab})
	if _, cmd := f.Update(tea.KeyMsg{Type: tea.KeyRight}); cmd != nil {
		t.Fatalf("select change should not emit a command")
	}
	if len(changed) < 2 || changed[0] != "name" {
		t.Fatalf("OnChange keys=%v want name then kind", changed)
	}
}
