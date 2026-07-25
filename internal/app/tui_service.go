package app

import (
	"context"
	"fmt"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/tui"
)

type TUIBackend interface {
	LoadStoreSnapshots(context.Context, string, cloudstic.ProfileStore) ([]engine.SnapshotEntry, error)
	InitProfile(context.Context, string, string, cloudstic.BackupProfile, *cloudstic.ProfilesConfig) error
	BackupProfile(context.Context, string, string, cloudstic.BackupProfile, *cloudstic.ProfilesConfig, cloudstic.Reporter) error
	CheckProfile(context.Context, string, string, cloudstic.BackupProfile, *cloudstic.ProfilesConfig, cloudstic.Reporter) error
}

type TUIService struct {
	loadProfiles func(string) (*cloudstic.ProfilesConfig, error)
	saveProfiles func(string, *cloudstic.ProfilesConfig) error
	backend      TUIBackend
}

func NewTUIService(backend TUIBackend) *TUIService {
	return &TUIService{
		loadProfiles: loadProfilesConfig,
		saveProfiles: cloudstic.SaveProfilesFile,
		backend:      backend,
	}
}

func (s *TUIService) BuildDashboard(ctx context.Context, profilesFile string) (tui.Dashboard, error) {
	cfg, err := s.loadConfig(profilesFile)
	if err != nil {
		return tui.Dashboard{}, err
	}
	var load tui.SnapshotLoader
	if s.backend != nil {
		load = func(ctx context.Context, storeName string, storeCfg cloudstic.ProfileStore) ([]engine.SnapshotEntry, error) {
			return s.backend.LoadStoreSnapshots(ctx, storeName, storeCfg)
		}
	}
	return tui.BuildDashboardFromConfig(ctx, cfg, load), nil
}

func (s *TUIService) RunProfileAction(ctx context.Context, profilesFile string, profile tui.ProfileCard, reporter cloudstic.Reporter) error {
	cfg, err := s.loadConfig(profilesFile)
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}

	profileCfg, ok := cfg.Profiles[profile.Name]
	if !ok {
		return fmt.Errorf("unknown profile %q", profile.Name)
	}

	if profileNeedsInit(profile) {
		if s.backend == nil {
			return fmt.Errorf("init action is not configured")
		}
		return s.backend.InitProfile(ctx, profilesFile, profile.Name, profileCfg, cfg)
	}

	if s.backend == nil {
		return fmt.Errorf("backup action is not configured")
	}
	return s.backend.BackupProfile(ctx, profilesFile, profile.Name, profileCfg, cfg, reporter)
}

func (s *TUIService) RunProfileCheck(ctx context.Context, profilesFile string, profile tui.ProfileCard, reporter cloudstic.Reporter) error {
	cfg, err := s.loadConfig(profilesFile)
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}

	profileCfg, ok := cfg.Profiles[profile.Name]
	if !ok {
		return fmt.Errorf("unknown profile %q", profile.Name)
	}
	if profileNeedsInit(profile) {
		return fmt.Errorf("repository is not initialized")
	}
	if s.backend == nil {
		return fmt.Errorf("check action is not configured")
	}
	return s.backend.CheckProfile(ctx, profilesFile, profile.Name, profileCfg, cfg, reporter)
}

func (s *TUIService) SaveProfile(profilesFile, name string, profile cloudstic.BackupProfile) error {
	cfg, err := s.loadConfig(profilesFile)
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}
	cfg.Profiles[name] = profile
	save := s.saveProfiles
	if save == nil {
		save = cloudstic.SaveProfilesFile
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
		save = cloudstic.SaveProfilesFile
	}
	if err := save(profilesFile, cfg); err != nil {
		return fmt.Errorf("save profiles: %w", err)
	}
	return nil
}

func (s *TUIService) SaveStore(profilesFile, name string, store cloudstic.ProfileStore) error {
	cfg, err := s.loadConfig(profilesFile)
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}
	cfg.Stores[name] = store
	save := s.saveProfiles
	if save == nil {
		save = cloudstic.SaveProfilesFile
	}
	if err := save(profilesFile, cfg); err != nil {
		return fmt.Errorf("save profiles: %w", err)
	}
	return nil
}

// LoadDashboardConfig loads and normalizes the profiles config for the TUI.
// The Bubble Tea renderer builds a probe-less dashboard skeleton from it and
// then probes stores concurrently, rather than probing serially up front.
func (s *TUIService) LoadDashboardConfig(profilesFile string) (*cloudstic.ProfilesConfig, error) {
	return s.loadConfig(profilesFile)
}

func (s *TUIService) loadConfig(profilesFile string) (*cloudstic.ProfilesConfig, error) {
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

func loadProfilesConfig(path string) (*cloudstic.ProfilesConfig, error) {
	return cloudstic.LoadProfilesFileOrEmpty(path)
}

func ensureProfilesMaps(cfg *cloudstic.ProfilesConfig) {
	cloudstic.EnsureProfilesMaps(cfg)
}

func profileNeedsInit(profile tui.ProfileCard) bool {
	return profile.StoreHealth == tui.StoreHealthNotInitialized
}
