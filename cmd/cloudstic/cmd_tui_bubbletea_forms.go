package main

import (
	"fmt"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/tui"
)

// bubbleFormsBackend adapts the cmd-side profile/source helpers to the Bubble
// Tea model's forms boundary (tui.FormsBackend). It caches the profiles config
// and refreshes it on Reload so option lists and existence checks stay current
// across form opens.
type bubbleFormsBackend struct {
	r            *runner
	profilesFile string
	cfg          *cloudstic.ProfilesConfig
}

func newBubbleFormsBackend(r *runner, profilesFile string, cfg *cloudstic.ProfilesConfig) *bubbleFormsBackend {
	return &bubbleFormsBackend{r: r, profilesFile: profilesFile, cfg: cfg}
}

var _ tui.FormsBackend = (*bubbleFormsBackend)(nil)

func (b *bubbleFormsBackend) StoreOptions() []string {
	if b.cfg == nil {
		return nil
	}
	return sortedKeys(b.cfg.Stores)
}

func (b *bubbleFormsBackend) ProviderForSourceType(sourceType string) string {
	return tuiProfileSource{Type: sourceType}.Provider()
}

func (b *bubbleFormsBackend) AuthOptions(provider string) []string {
	if b.cfg == nil {
		return nil
	}
	return profileAuthOptions(b.cfg, provider)
}

func (b *bubbleFormsBackend) SourceParts(sourceURI string) (string, string) {
	source := newTUIProfileSource(sourceURI)
	return source.Type, source.Value
}

func (b *bubbleFormsBackend) SourceDetailLabel(sourceType string) string {
	return tuiProfileSource{Type: sourceType}.DetailLabel()
}

func (b *bubbleFormsBackend) SourceDetailRequired(sourceType string) bool {
	return tuiProfileSource{Type: sourceType}.DetailRequired()
}

func (b *bubbleFormsBackend) SourceExample(sourceType string) string {
	return tuiProfileSource{Type: sourceType}.ExampleText()
}

func (b *bubbleFormsBackend) ComposeSource(sourceType, value string) (string, error) {
	source := tuiProfileSource{Type: sourceType, Value: value}.Compose()
	if source == "" {
		return "", nil
	}
	if _, err := parseSourceURI(source); err != nil {
		return "", fmt.Errorf("invalid source: %v", err)
	}
	return source, nil
}

func (b *bubbleFormsBackend) ValidateNewName(name string) error {
	return validateRefName("profile", name)
}

func (b *bubbleFormsBackend) ProfileExists(name string) bool {
	if b.cfg == nil {
		return false
	}
	_, ok := b.cfg.Profiles[name]
	return ok
}

func (b *bubbleFormsBackend) SaveProfile(name, sourceURI, storeRef, authRef string, editing bool) error {
	profile := cloudstic.BackupProfile{}
	if editing && b.cfg != nil {
		if existing, ok := b.cfg.Profiles[name]; ok {
			profile = existing
		}
	}
	profile.Source = sourceURI
	profile.Store = storeRef
	profile.AuthRef = authRef
	return tuiServiceFactory(nil, b.profilesFile, nil).SaveProfile(b.profilesFile, name, profile)
}

func (b *bubbleFormsBackend) DeleteProfile(name string) error {
	return tuiServiceFactory(nil, b.profilesFile, nil).DeleteProfile(b.profilesFile, name)
}

func (b *bubbleFormsBackend) Reload() (*cloudstic.ProfilesConfig, error) {
	cfg, err := tuiLoadConfig(b.profilesFile)
	if err != nil {
		return nil, err
	}
	b.cfg = cfg
	return cfg, nil
}
