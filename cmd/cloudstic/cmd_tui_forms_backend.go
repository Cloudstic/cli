package main

import (
	"fmt"
	"github.com/cloudstic/cli/pkg/config"

	"github.com/cloudstic/cli/pkg/profile"

	"github.com/cloudstic/cli/internal/tui"
)

// tuiFormsBackend adapts the cmd-side profile/source helpers to the Bubble
// Tea model's forms boundary (tui.FormsBackend). It caches the profiles config
// and refreshes it on Reload so option lists and existence checks stay current
// across form opens.
type tuiFormsBackend struct {
	r            *runner
	profilesFile string
	cfg          *profile.Config
}

func newTUIFormsBackend(r *runner, profilesFile string, cfg *profile.Config) *tuiFormsBackend {
	return &tuiFormsBackend{r: r, profilesFile: profilesFile, cfg: cfg}
}

var _ tui.FormsBackend = (*tuiFormsBackend)(nil)

func (b *tuiFormsBackend) StoreOptions() []string {
	if b.cfg == nil {
		return nil
	}
	return sortedKeys(b.cfg.Stores)
}

func (b *tuiFormsBackend) ProviderForSourceType(sourceType string) string {
	return tuiProfileSource{Type: sourceType}.Provider()
}

func (b *tuiFormsBackend) AuthOptions(provider string) []string {
	if b.cfg == nil {
		return nil
	}
	return profileAuthOptions(b.cfg, provider)
}

func (b *tuiFormsBackend) SourceParts(sourceURI string) (string, string) {
	source := newTUIProfileSource(sourceURI)
	return source.Type, source.Value
}

func (b *tuiFormsBackend) SourceDetailLabel(sourceType string) string {
	return tuiProfileSource{Type: sourceType}.DetailLabel()
}

func (b *tuiFormsBackend) SourceDetailRequired(sourceType string) bool {
	return tuiProfileSource{Type: sourceType}.DetailRequired()
}

func (b *tuiFormsBackend) SourceExample(sourceType string) string {
	return tuiProfileSource{Type: sourceType}.ExampleText()
}

func (b *tuiFormsBackend) ComposeSource(sourceType, value string) (string, error) {
	source := tuiProfileSource{Type: sourceType, Value: value}.Compose()
	if source == "" {
		return "", nil
	}
	if _, err := config.ParseSourceURI(source); err != nil {
		return "", fmt.Errorf("invalid source: %v", err)
	}
	return source, nil
}

func (b *tuiFormsBackend) ValidateNewName(name string) error {
	return validateRefName("profile", name)
}

func (b *tuiFormsBackend) ProfileExists(name string) bool {
	if b.cfg == nil {
		return false
	}
	_, ok := b.cfg.Profiles[name]
	return ok
}

func (b *tuiFormsBackend) SaveProfile(name, sourceURI, storeRef, authRef string, editing bool) error {
	profile := profile.Profile{}
	if editing && b.cfg != nil {
		if existing, ok := b.cfg.Profiles[name]; ok {
			profile = existing
		}
	}
	profile.Source = sourceURI
	profile.Store = storeRef
	profile.AuthRef = authRef
	return tuiServiceFactory(nil, b.profilesFile).SaveProfile(b.profilesFile, name, profile)
}

func (b *tuiFormsBackend) DeleteProfile(name string) error {
	return tuiServiceFactory(nil, b.profilesFile).DeleteProfile(b.profilesFile, name)
}

func (b *tuiFormsBackend) Reload() (*profile.Config, error) {
	cfg, err := tuiLoadConfig(b.profilesFile)
	if err != nil {
		return nil, err
	}
	b.cfg = cfg
	return cfg, nil
}
