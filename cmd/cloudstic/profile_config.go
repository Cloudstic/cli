package main

import (
	"fmt"
	"regexp"
	"sort"
)

// The naming and ordering rules shared by everything that reads or writes the
// profiles file — `store new`, `auth new`, `profile new`, `setup`, the TUI
// forms, and the completion candidates.

// validRefName is the shape of a store, auth or profile reference name. The
// names become YAML keys and appear in `-profile`/`-store-ref` arguments, so
// they are kept to what is unambiguous in both.
var validRefName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func validateRefName(kind, name string) error {
	if !validRefName.MatchString(name) {
		return fmt.Errorf("invalid %s name %q: must start with a letter or digit and contain only letters, digits, dots, hyphens, or underscores", kind, name)
	}
	return nil
}

// sortedKeys returns a map's keys in lexical order.
//
// Every caller is iterating one of the profiles file's maps for output — a
// table, a completion candidate list — where Go's randomized map order would
// otherwise make the same file render differently on each run.
func sortedKeys[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
