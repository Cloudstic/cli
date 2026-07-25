package main

import (
	"fmt"
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

func profilesFileFlag(target *string) flagSpec {
	return stringFlag(target, "profiles-file", defaultProfilesPathFallback(), "Path to profiles YAML file",
		withEnv("CLOUDSTIC_PROFILES_FILE"), withPlaceholder("<path>"), withCompleter("_files"))
}

func loadProfilesOrInit(path string) (*cloudstic.ProfilesConfig, error) {
	return cloudstic.LoadProfilesFileOrEmpty(path)
}

func ensureProfilesMaps(cfg *cloudstic.ProfilesConfig) {
	cloudstic.EnsureProfilesMaps(cfg)
}

func sortedKeys[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
