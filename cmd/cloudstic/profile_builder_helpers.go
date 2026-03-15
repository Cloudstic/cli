package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"

	cloudstic "github.com/cloudstic/cli"
)

var validRefName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func validateRefName(kind, name string) error {
	if !validRefName.MatchString(name) {
		return fmt.Errorf("invalid %s name %q: must start with a letter or digit and contain only letters, digits, dots, hyphens, or underscores", kind, name)
	}
	return nil
}

func defaultProfilesPathFallback() string {
	defaultPath, err := defaultProfilesPath()
	if err != nil {
		return defaultProfilesFilename
	}
	return defaultPath
}

func loadProfilesOrInit(path string) (*cloudstic.ProfilesConfig, error) {
	cfg, err := cloudstic.LoadProfilesFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &cloudstic.ProfilesConfig{Version: 1}, nil
		}
		return nil, err
	}
	return cfg, nil
}

func ensureProfilesMaps(cfg *cloudstic.ProfilesConfig) {
	if cfg.Stores == nil {
		cfg.Stores = map[string]cloudstic.ProfileStore{}
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]cloudstic.BackupProfile{}
	}
	if cfg.Auth == nil {
		cfg.Auth = map[string]cloudstic.ProfileAuth{}
	}
}

func sortedKeys[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
