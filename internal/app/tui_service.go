package app

import (
	"context"
	"fmt"

	"github.com/cloudstic/cli/pkg/profile"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/tui"
)

type TUIBackend interface {
	LoadStoreSnapshots(context.Context, string, profile.Store) ([]engine.SnapshotEntry, error)
	// Each takes the profile's name and the whole config rather than the
	// resolved profile.Profile as well: every implementation needs the store
	// the profile points at, and profile.Config.StoreFor is the one lookup
	// that resolves it. Passing both invited a caller to supply a profile that
	// is not the one the name selects.
	InitProfile(context.Context, string, string, *profile.Config) error
	BackupProfile(context.Context, string, string, *profile.Config, cloudstic.Reporter) error
	CheckProfile(context.Context, string, string, *profile.Config, cloudstic.Reporter) error
}

type TUIService struct {
	loadProfiles func(string) (*profile.Config, error)
	saveProfiles func(string, *profile.Config) error
	backend      TUIBackend
}

func NewTUIService(backend TUIBackend) *TUIService {
	return &TUIService{
		loadProfiles: loadProfilesConfig,
		saveProfiles: profile.Save,
		backend:      backend,
	}
}

func (s *TUIService) RunProfileAction(ctx context.Context, profilesFile string, profile tui.ProfileCard, reporter cloudstic.Reporter) error {
	cfg, err := s.loadConfig(profilesFile)
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}

	if _, ok := cfg.Profiles[profile.Name]; !ok {
		return fmt.Errorf("unknown profile %q", profile.Name)
	}

	if profileNeedsInit(profile) {
		if s.backend == nil {
			return fmt.Errorf("init action is not configured")
		}
		return s.backend.InitProfile(ctx, profilesFile, profile.Name, cfg)
	}

	if s.backend == nil {
		return fmt.Errorf("backup action is not configured")
	}
	return s.backend.BackupProfile(ctx, profilesFile, profile.Name, cfg, reporter)
}

func (s *TUIService) RunProfileCheck(ctx context.Context, profilesFile string, profile tui.ProfileCard, reporter cloudstic.Reporter) error {
	cfg, err := s.loadConfig(profilesFile)
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}

	if _, ok := cfg.Profiles[profile.Name]; !ok {
		return fmt.Errorf("unknown profile %q", profile.Name)
	}
	if profileNeedsInit(profile) {
		return fmt.Errorf("repository is not initialized")
	}
	if s.backend == nil {
		return fmt.Errorf("check action is not configured")
	}
	return s.backend.CheckProfile(ctx, profilesFile, profile.Name, cfg, reporter)
}

func (s *TUIService) SaveProfile(profilesFile, name string, p profile.Profile) error {
	cfg, err := s.loadConfig(profilesFile)
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}
	cfg.Profiles[name] = p
	save := s.saveProfiles
	if save == nil {
		save = profile.Save
	}
	if err := save(profilesFile, cfg); err != nil {
		return fmt.Errorf("save profiles: %w", err)
	}
	return nil
}

func (s *TUIService) DeleteProfile(profilesFile, name string) error {
	cfg, err := s.loadConfig(profilesFile)
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}
	if _, ok := cfg.Profiles[name]; !ok {
		return fmt.Errorf("unknown profile %q", name)
	}
	delete(cfg.Profiles, name)
	save := s.saveProfiles
	if save == nil {
		save = profile.Save
	}
	if err := save(profilesFile, cfg); err != nil {
		return fmt.Errorf("save profiles: %w", err)
	}
	return nil
}

func (s *TUIService) SaveStore(profilesFile, name string, store profile.Store) error {
	cfg, err := s.loadConfig(profilesFile)
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}
	cfg.Stores[name] = store
	save := s.saveProfiles
	if save == nil {
		save = profile.Save
	}
	if err := save(profilesFile, cfg); err != nil {
		return fmt.Errorf("save profiles: %w", err)
	}
	return nil
}

// LoadDashboardConfig loads and normalizes the profiles config for the TUI.
// The Bubble Tea renderer builds a probe-less dashboard skeleton from it and
// then probes stores concurrently, rather than probing serially up front.
func (s *TUIService) LoadDashboardConfig(profilesFile string) (*profile.Config, error) {
	return s.loadConfig(profilesFile)
}

func (s *TUIService) loadConfig(profilesFile string) (*profile.Config, error) {
	load := s.loadProfiles
	if load == nil {
		load = loadProfilesConfig
	}
	cfg, err := load(profilesFile)
	if err != nil {
		return nil, err
	}
	ensureProfilesMaps(cfg)
	return cfg, nil
}

func loadProfilesConfig(path string) (*profile.Config, error) {
	return profile.LoadOrEmpty(path)
}

func ensureProfilesMaps(cfg *profile.Config) {
	profile.EnsureMaps(cfg)
}

func profileNeedsInit(profile tui.ProfileCard) bool {
	return profile.StoreHealth == tui.StoreHealthNotInitialized
}
