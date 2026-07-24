package forms

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// CreateStoreOption is the sentinel store-field option that opens the nested
// store-create form (#232).
const CreateStoreOption = "+ Create store"

// Intent names yielded by the profile form's store field.
const (
	IntentCreateStore = "create_store"
	IntentEditStore   = "edit_store"
)

// ProfileBackend is the domain boundary for the profile form. The cmd package
// implements it by reusing the existing source-URI, options, validation, and
// save helpers, so this package stays free of URI-parsing and persistence
// concerns.
type ProfileBackend interface {
	// StoreOptions returns the selectable store names.
	StoreOptions() []string
	// ProviderForSourceType returns the auth provider a source type requires,
	// or "" when the source needs no auth.
	ProviderForSourceType(sourceType string) string
	// AuthOptions returns the auth refs available for a provider.
	AuthOptions(provider string) []string
	// SourceParts splits an existing source URI into its (type, value) pair.
	SourceParts(sourceURI string) (sourceType, value string)
	// SourceDetailLabel returns the label for the source detail field.
	SourceDetailLabel(sourceType string) string
	// SourceDetailRequired reports whether the source detail is mandatory.
	SourceDetailRequired(sourceType string) bool
	// SourceExample returns an example hint for the source detail field.
	SourceExample(sourceType string) string
	// ComposeSource builds and validates a source URI from type and value.
	ComposeSource(sourceType, value string) (string, error)
	// ValidateNewName validates a candidate new profile name.
	ValidateNewName(name string) error
	// ProfileExists reports whether a profile name is already taken.
	ProfileExists(name string) bool
	// SaveProfile persists the profile.
	SaveProfile(name, sourceURI, storeRef, authRef string, editing bool) error
}

// SourceTypes are the source schemes offered by the profile form, in order.
var SourceTypes = []string{
	"local",
	"sftp",
	"gdrive",
	"gdrive-changes",
	"onedrive",
	"onedrive-changes",
}

// NewProfileForm builds the create/edit profile form. For editing, name and
// the current source/store/auth are pre-filled and the name field is locked.
func NewProfileForm(backend ProfileBackend, existingName, existingSource, existingStore, existingAuth string, editing bool) *Form {
	sourceType, sourceValue := "local", ""
	if existingSource != "" {
		sourceType, sourceValue = backend.SourceParts(existingSource)
	}
	stores := storeFieldOptions(backend.StoreOptions())

	fields := []Field{
		newTextField("name", "Name", existingName, true),
		newSelectField("source_type", "Source Type", append([]string{}, SourceTypes...), sourceType, true),
		newTextField("source_value", backend.SourceDetailLabel(sourceType), sourceValue, backend.SourceDetailRequired(sourceType)),
		newSelectField("store", "Store", stores, firstNonEmpty(existingStore, firstOr(stores, "")), true),
		newSelectField("auth", "Auth", nil, existingAuth, false),
	}
	if editing {
		fields[0].Disabled = true
	}

	title := "Create Profile"
	if editing {
		title = "Edit Profile"
	}

	f := New(title, "", fields)
	pf := &profileFormState{backend: backend, editing: editing, originalName: existingName}
	f.OnChange = pf.onChange
	f.Submit = pf.submit
	f.Interrupt = pf.interrupt
	pf.refreshDerived(f)
	f.focusFirst()
	return f
}

// storeFieldOptions appends the create-store sentinel to the available stores.
func storeFieldOptions(stores []string) []string {
	return append(append([]string{}, stores...), CreateStoreOption)
}

// interrupt lets the store field open the nested store form: Enter on the
// "+ Create store" option, or "e" on a real store to edit it (#232).
func (p *profileFormState) interrupt(f *Form, key tea.KeyMsg) (Intent, bool) {
	if f.FocusedKey() != "store" {
		return Intent{}, false
	}
	value := f.Value("store")
	switch {
	case key.Type == tea.KeyEnter && value == CreateStoreOption:
		return Intent{Name: IntentCreateStore}, true
	case key.Type == tea.KeyRunes && strings.EqualFold(string(key.Runes), "e") &&
		value != "" && value != CreateStoreOption:
		return Intent{Name: IntentEditStore, Value: value}, true
	}
	return Intent{}, false
}

type profileFormState struct {
	backend      ProfileBackend
	editing      bool
	originalName string
}

func (p *profileFormState) onChange(f *Form, changedKey string) {
	switch changedKey {
	case "source_type", "source_value":
		p.refreshDerived(f)
	case "store":
		f.SetHelp("store", storeFieldHelp(f.Value("store")))
	}
}

// storeFieldHelp returns the contextual hint for the store selector.
func storeFieldHelp(value string) string {
	switch {
	case value == CreateStoreOption:
		return "Press Enter to create a store."
	case value != "":
		return "Press e to edit the selected store."
	default:
		return ""
	}
}

func (p *profileFormState) refreshDerived(f *Form) {
	sourceType := f.Value("source_type")
	f.SetLabel("source_value", p.backend.SourceDetailLabel(sourceType))
	f.SetRequired("source_value", p.backend.SourceDetailRequired(sourceType))
	if f.FocusedKey() == "source_value" {
		f.SetHelp("source_value", p.backend.SourceExample(sourceType))
	} else {
		f.SetHelp("source_value", "")
	}

	f.SetHelp("store", storeFieldHelp(f.Value("store")))

	provider := p.backend.ProviderForSourceType(sourceType)
	if provider == "" {
		f.SetDisabled("auth", true)
		f.SetRequired("auth", false)
		f.SetLabel("auth", "Auth")
		f.SetOptions("auth", nil)
		f.SetValue("auth", "")
		return
	}
	options := p.backend.AuthOptions(provider)
	f.SetDisabled("auth", false)
	f.SetRequired("auth", true)
	f.SetLabel("auth", fmt.Sprintf("Auth (%s)", provider))
	f.SetOptions("auth", options)
	if len(options) > 0 && indexOf(options, f.Value("auth")) == 0 && f.Value("auth") != options[0] {
		f.SetValue("auth", options[0])
	}
}

func (p *profileFormState) submit(f *Form) (string, error) {
	name := f.TrimmedValue("name")
	if p.editing {
		name = p.originalName
	} else {
		if name == "" {
			return "", NewFieldError("name", "profile name is required")
		}
		if err := p.backend.ValidateNewName(name); err != nil {
			return "", NewFieldError("name", err.Error())
		}
		if p.backend.ProfileExists(name) {
			return "", NewFieldError("name", fmt.Sprintf("profile %q already exists", name))
		}
	}

	sourceType := f.Value("source_type")
	source, err := p.backend.ComposeSource(sourceType, f.TrimmedValue("source_value"))
	if err != nil {
		return "", NewFieldError("source_value", err.Error())
	}
	if source == "" {
		return "", NewFieldError("source_value", "source details are required")
	}

	storeRef := f.TrimmedValue("store")
	if storeRef == "" {
		return "", NewFieldError("store", "store reference is required")
	}
	if storeRef == CreateStoreOption {
		return "", NewFieldError("store", "create a store before saving the profile")
	}

	authRef := f.TrimmedValue("auth")
	provider := p.backend.ProviderForSourceType(sourceType)
	if provider != "" {
		if authRef == "" {
			return "", NewFieldError("auth", fmt.Sprintf("auth reference is required for %s sources", provider))
		}
	} else {
		authRef = ""
	}

	if err := p.backend.SaveProfile(name, source, storeRef, authRef, p.editing); err != nil {
		return "", err
	}
	return name, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstOr(options []string, fallback string) string {
	if len(options) == 0 {
		return fallback
	}
	return options[0]
}
