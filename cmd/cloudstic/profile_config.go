package main

import (
	"sort"
)

// The naming and ordering rules shared by everything that reads or writes the
// profiles file — `store new`, `auth new`, `profile new`, `setup`, the TUI
// forms, and the completion candidates.

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
