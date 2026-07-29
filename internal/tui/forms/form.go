// Package forms provides Bubble Tea form components for the interactive TUI.
//
// It replaces the hand-rolled modal state machines and the ASCII-only byte
// reader in package main (RFC 0012 Phase 2, issue #340). Text fields are
// backed by bubbles/textinput, so Unicode input, cursor movement, and paste
// work by construction rather than being re-implemented byte by byte.
package forms

import (
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// FieldKind distinguishes a free-text field from a fixed-option selector.
type FieldKind int

const (
	// FieldText is a free-text field backed by bubbles/textinput.
	FieldText FieldKind = iota
	// FieldSelect cycles through a fixed set of options.
	FieldSelect
)

// Field is one editable row in a form.
type Field struct {
	Key      string
	Label    string
	Kind     FieldKind
	Options  []string
	Required bool
	Disabled bool
	// Help is an optional dimmed hint rendered under the field when focused.
	Help string

	input  textinput.Model
	selIdx int
}

func newTextField(key, label, value string, required bool) Field {
	ti := textinput.New()
	ti.SetValue(value)
	ti.Prompt = ""
	return Field{Key: key, Label: label, Kind: FieldText, Required: required, input: ti}
}

func newSelectField(key, label string, options []string, value string, required bool) Field {
	f := Field{Key: key, Label: label, Kind: FieldSelect, Options: options, Required: required}
	f.selIdx = indexOf(options, value)
	return f
}

// Value returns the field's current value.
func (f Field) Value() string {
	if f.Kind == FieldSelect {
		if f.selIdx < 0 || f.selIdx >= len(f.Options) {
			return ""
		}
		return f.Options[f.selIdx]
	}
	return f.input.Value()
}

func (f *Field) setValue(v string) {
	if f.Kind == FieldSelect {
		f.selIdx = indexOf(f.Options, v)
		return
	}
	f.input.SetValue(v)
}

func (f *Field) cycle(delta int) {
	if f.Kind != FieldSelect || len(f.Options) == 0 {
		return
	}
	f.selIdx = wrap(f.selIdx+delta, len(f.Options))
}

func indexOf(options []string, value string) int {
	for i, o := range options {
		if o == value {
			return i
		}
	}
	return 0
}

func wrap(i, n int) int {
	if n == 0 {
		return 0
	}
	i %= n
	if i < 0 {
		i += n
	}
	return i
}

// FieldError attaches an error message to a specific field so the form can
// highlight it. Submit hooks return it to report per-field validation errors.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Message }

// NewFieldError builds a FieldError for the given field key.
func NewFieldError(field, message string) error {
	return &FieldError{Field: field, Message: message}
}

// Form is a vertical, keyboard-driven form. The owning model delegates input
// to Update while a form is open and reads Done/Canceled/Result afterward.
type Form struct {
	Title    string
	Subtitle string

	fields []Field
	focus  int

	errField string
	errMsg   string

	done     bool
	canceled bool
	result   string

	// OnChange is called after any field value changes, so the form can
	// recompute derived fields (labels, options, enabled/disabled). It may be
	// nil for static forms.
	OnChange func(f *Form, changedKey string)
	// Submit validates and persists the form. It returns the result value on
	// success, or a *FieldError / plain error on failure. It is required.
	Submit func(f *Form) (string, error)
	// Interrupt, if set, is consulted for every key before normal handling. It
	// may return a navigation Intent to yield to the host (e.g. open a nested
	// form) without editing or completing the form.
	Interrupt func(f *Form, key tea.KeyMsg) (Intent, bool)

	pendingIntent Intent

	theme formTheme
}

// New builds a form with the given fields and focuses the first editable one.
func New(title, subtitle string, fields []Field) *Form {
	f := &Form{Title: title, Subtitle: subtitle, fields: fields, theme: newFormTheme()}
	f.focusFirst()
	return f
}

// Init returns the command that starts the focused text field's cursor blink.
func (f *Form) Init() tea.Cmd { return textinput.Blink }

// Fields exposes the form's fields for derivation hooks.
func (f *Form) Fields() []Field { return f.fields }

// Value returns the current value of the named field.
func (f *Form) Value(key string) string {
	if field := f.field(key); field != nil {
		return field.Value()
	}
	return ""
}

// TrimmedValue returns the named field's value with surrounding space removed.
func (f *Form) TrimmedValue(key string) string {
	return strings.TrimSpace(f.Value(key))
}

// SetValue sets the named field's value.
func (f *Form) SetValue(key, value string) {
	if field := f.field(key); field != nil {
		field.setValue(value)
	}
}

// SetOptions replaces a select field's options, keeping the current value when
// still present.
func (f *Form) SetOptions(key string, options []string) {
	field := f.field(key)
	if field == nil || field.Kind != FieldSelect {
		return
	}
	current := field.Value()
	field.Options = options
	field.selIdx = indexOf(options, current)
}

// SetLabel updates a field's label (used by derived source/store labels).
func (f *Form) SetLabel(key, label string) {
	if field := f.field(key); field != nil {
		field.Label = label
	}
}

// SetRequired toggles a field's required flag.
func (f *Form) SetRequired(key string, required bool) {
	if field := f.field(key); field != nil {
		field.Required = required
	}
}

// SetDisabled toggles a field's disabled flag; a disabled field is skipped by
// focus movement and rendered dimmed.
func (f *Form) SetDisabled(key string, disabled bool) {
	if field := f.field(key); field != nil {
		field.Disabled = disabled
	}
}

// SetHelp sets the dimmed hint shown under the field when focused.
func (f *Form) SetHelp(key, help string) {
	if field := f.field(key); field != nil {
		field.Help = help
	}
}

// FocusedKey returns the key of the currently focused field.
func (f *Form) FocusedKey() string {
	if f.focus < 0 || f.focus >= len(f.fields) {
		return ""
	}
	return f.fields[f.focus].Key
}

// Done reports whether the form was submitted successfully.
func (f *Form) Done() bool { return f.done }

// Canceled reports whether the form was canceled (Esc).
func (f *Form) Canceled() bool { return f.canceled }

// Result returns the value produced by a successful Submit.
func (f *Form) Result() string { return f.result }

func (f *Form) field(key string) *Field {
	for i := range f.fields {
		if f.fields[i].Key == key {
			return &f.fields[i]
		}
	}
	return nil
}

func (f *Form) focusFirst() {
	for i := range f.fields {
		if !f.fields[i].Disabled {
			f.setFocus(i)
			return
		}
	}
}

func (f *Form) setFocus(idx int) {
	for i := range f.fields {
		if f.fields[i].Kind == FieldText {
			f.fields[i].input.Blur()
		}
	}
	f.focus = idx
	if idx >= 0 && idx < len(f.fields) && f.fields[idx].Kind == FieldText {
		f.fields[idx].input.Focus()
	}
}

func (f *Form) moveFocus(delta int) {
	if len(f.fields) == 0 {
		return
	}
	idx := f.focus
	for range f.fields {
		idx = wrap(idx+delta, len(f.fields))
		if !f.fields[idx].Disabled {
			f.setFocus(idx)
			return
		}
	}
}

func (f *Form) clearError() {
	f.errField = ""
	f.errMsg = ""
}

// Intent is a navigation request a form yields to its host instead of
// completing — for example, opening a nested form. Name identifies the action;
// Value carries a parameter (e.g. the store to edit, or the secret field key).
type Intent struct {
	Name  string
	Value string
}

// TakeIntent returns and clears any pending intent set by the Interrupt hook.
// The host calls it after Update to decide whether to open a nested form.
func (f *Form) TakeIntent() (Intent, bool) {
	if f.pendingIntent.Name == "" {
		return Intent{}, false
	}
	intent := f.pendingIntent
	f.pendingIntent = Intent{}
	return intent, true
}

// Update processes one message while the form is open.
func (f *Form) Update(msg tea.Msg) (*Form, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return f, f.updateFocusedInput(msg)
	}

	// The Interrupt hook may intercept a key to yield a navigation intent
	// (e.g. "open the store form") without completing or editing the form.
	if f.Interrupt != nil {
		if intent, ok := f.Interrupt(f, key); ok {
			f.pendingIntent = intent
			return f, nil
		}
	}

	switch key.Type {
	case tea.KeyEsc:
		f.canceled = true
		f.done = true
		return f, nil
	case tea.KeyEnter:
		return f, f.submit()
	case tea.KeyTab, tea.KeyDown:
		f.moveFocus(1)
		return f, nil
	case tea.KeyShiftTab, tea.KeyUp:
		f.moveFocus(-1)
		return f, nil
	case tea.KeyLeft:
		if f.focusedIsSelect() {
			f.cycleFocused(-1)
			return f, nil
		}
	case tea.KeyRight:
		if f.focusedIsSelect() {
			f.cycleFocused(1)
			return f, nil
		}
	}
	// Text editing (including Unicode runes and paste) goes to textinput.
	return f, f.updateFocusedInput(msg)
}

func (f *Form) submit() tea.Cmd {
	if f.Submit == nil {
		return nil
	}
	result, err := f.Submit(f)
	if err != nil {
		var fieldErr *FieldError
		if errors.As(err, &fieldErr) {
			f.errField = fieldErr.Field
			f.errMsg = fieldErr.Message
			if field := f.field(fieldErr.Field); field != nil {
				f.setFocusToKey(fieldErr.Field)
			}
		} else {
			f.errField = ""
			f.errMsg = err.Error()
		}
		return nil
	}
	f.done = true
	f.result = result
	return nil
}

func (f *Form) setFocusToKey(key string) {
	for i := range f.fields {
		if f.fields[i].Key == key && !f.fields[i].Disabled {
			f.setFocus(i)
			return
		}
	}
}

func (f *Form) focusedIsSelect() bool {
	return f.focus >= 0 && f.focus < len(f.fields) && f.fields[f.focus].Kind == FieldSelect
}

func (f *Form) cycleFocused(delta int) {
	field := &f.fields[f.focus]
	field.cycle(delta)
	f.clearError()
	if f.OnChange != nil {
		f.OnChange(f, field.Key)
	}
}

func (f *Form) updateFocusedInput(msg tea.Msg) tea.Cmd {
	if f.focus < 0 || f.focus >= len(f.fields) {
		return nil
	}
	field := &f.fields[f.focus]
	if field.Kind != FieldText || field.Disabled {
		return nil
	}
	before := field.input.Value()
	var cmd tea.Cmd
	field.input, cmd = field.input.Update(msg)
	if field.input.Value() != before {
		f.clearError()
		if f.OnChange != nil {
			f.OnChange(f, field.Key)
		}
	}
	return cmd
}

// View renders the form as a bordered box.
func (f *Form) View() string {
	lines := []string{f.theme.title.Render(f.Title)}
	if f.Subtitle != "" {
		lines = append(lines, f.theme.subtle.Render(f.Subtitle), "")
	}
	for i := range f.fields {
		field := f.fields[i]
		if field.Disabled && !field.Required {
			continue
		}
		lines = append(lines, f.renderField(i))
		if i == f.focus && field.Help != "" {
			lines = append(lines, f.theme.subtle.Render("  "+field.Help))
		}
		if i == f.focus && f.errField == field.Key && f.errMsg != "" {
			lines = append(lines, f.theme.errStyle.Render("  "+f.errMsg))
		}
	}
	if f.errMsg != "" && f.errField == "" {
		lines = append(lines, "", f.theme.errStyle.Render(f.errMsg))
	}
	lines = append(lines, "", f.theme.subtle.Render("Tab/↑↓ move · ←/→ change · Enter save · Esc cancel"))
	return f.theme.box.Render(strings.Join(lines, "\n"))
}

func (f *Form) renderField(i int) string {
	field := f.fields[i]
	focused := i == f.focus
	label := f.theme.label.Render(padLabel(field.Label))
	var value string
	switch field.Kind {
	case FieldSelect:
		value = renderSelect(field, focused, f.theme)
	default:
		if focused {
			value = field.input.View()
		} else if field.input.Value() == "" {
			value = f.theme.subtle.Render("—")
		} else {
			value = field.input.Value()
		}
	}
	marker := "  "
	if focused {
		marker = f.theme.marker.Render("> ")
	}
	return marker + label + " " + value
}

func renderSelect(field Field, focused bool, t formTheme) string {
	value := field.Value()
	if value == "" {
		value = "—"
	}
	if !focused {
		return value
	}
	return t.selectStyle.Render("‹ " + value + " ›")
}

func padLabel(label string) string {
	const width = 16
	if lipgloss.Width(label) >= width {
		return label
	}
	return label + strings.Repeat(" ", width-lipgloss.Width(label))
}
