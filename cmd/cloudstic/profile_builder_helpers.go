package main

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/cloudstic/cli/pkg/profile"
)

var validRefName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func validateRefName(kind, name string) error {
	if !validRefName.MatchString(name) {
		return fmt.Errorf("invalid %s name %q: must start with a letter or digit and contain only letters, digits, dots, hyphens, or underscores", kind, name)
	}
	return nil
}

// profilesFileFlag declares -profiles-file, wherever it appears: as a global
// flag on repository commands, and as its own flag on the commands that manage
// the profiles file without opening a repository.
//
// The default is a path inside the config directory, so it can only be
// computed once -config-dir has taken its final value — hence withLateDefault
// rather than a default passed in here. That is also why the declaration takes
// g: the two flags are resolved together, and this is the one place that
// relationship is written down.
func profilesFileFlag(target *string, g *globalFlags) flagSpec {
	return stringFlag(target, "profiles-file", "", "Path to profiles YAML file",
		withEnv("CLOUDSTIC_PROFILES_FILE"), withPlaceholder("<path>"), withCompleter("_files"),
		withLateDefault(func() (string, error) { return defaultProfilesPath(g.configDir) }))
}

func loadProfilesOrInit(path string) (*profile.Config, error) {
	return profile.LoadOrEmpty(path)
}

func ensureProfilesMaps(cfg *profile.Config) {
	profile.EnsureMaps(cfg)
}

func sortedKeys[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
