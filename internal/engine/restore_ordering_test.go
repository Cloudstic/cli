package engine

import (
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

// indexOf reports where id lands in the ordering, or -1.
func indexOf(order []string, id string) int {
	for i, got := range order {
		if got == id {
			return i
		}
	}
	return -1
}

// topoSort's contract is parent-before-child. It returns FileIDs, so the caller
// keeps one copy of each entry rather than two.
func TestTopoSort_OrdersParentsBeforeChildren(t *testing.T) {
	byID := map[string]core.FileMeta{
		"root":  {FileID: "root", Type: core.FileTypeFolder},
		"a":     {FileID: "a", Type: core.FileTypeFolder, Parents: []string{"root"}},
		"b":     {FileID: "b", Type: core.FileTypeFolder, Parents: []string{"a"}},
		"leaf":  {FileID: "leaf", Parents: []string{"b"}},
		"other": {FileID: "other", Parents: []string{"root"}},
	}

	order := topoSort(byID)
	if len(order) != len(byID) {
		t.Fatalf("ordering has %d entries, want %d", len(order), len(byID))
	}

	seen := make(map[string]bool, len(order))
	for _, id := range order {
		if seen[id] {
			t.Fatalf("%s appears twice in the ordering", id)
		}
		seen[id] = true
	}

	for _, child := range []struct{ child, parent string }{
		{"a", "root"}, {"b", "a"}, {"leaf", "b"}, {"other", "root"},
	} {
		if indexOf(order, child.parent) > indexOf(order, child.child) {
			t.Errorf("%s (parent) is ordered after %s (child)", child.parent, child.child)
		}
	}
}

// A parent cycle is not reachable from a repository this code wrote, but it is
// reachable from a corrupt or hostile one. Marking an entry visited before
// recursing makes the walk terminate and emit each entry once; marking after
// recursing, as it did before, recurses until the stack is exhausted.
func TestTopoSort_TerminatesOnAParentCycle(t *testing.T) {
	byID := map[string]core.FileMeta{
		"x": {FileID: "x", Type: core.FileTypeFolder, Parents: []string{"y"}},
		"y": {FileID: "y", Type: core.FileTypeFolder, Parents: []string{"x"}},
		"z": {FileID: "z", Parents: []string{"x"}},
	}

	done := make(chan []string, 1)
	go func() { done <- topoSort(byID) }()

	order := <-done
	if len(order) != len(byID) {
		t.Fatalf("ordering has %d entries, want %d", len(order), len(byID))
	}
	seen := make(map[string]bool, len(order))
	for _, id := range order {
		if seen[id] {
			t.Fatalf("%s appears twice in the ordering", id)
		}
		seen[id] = true
	}
	for id := range byID {
		if !seen[id] {
			t.Errorf("%s is missing from the ordering", id)
		}
	}
}

// An entry naming a parent that is not in the snapshot must still be ordered,
// not dropped. Restore relies on that: a filtered or partial tree has entries
// whose parents were never loaded.
func TestTopoSort_KeepsEntriesWithUnknownParents(t *testing.T) {
	byID := map[string]core.FileMeta{
		"orphan": {FileID: "orphan", Parents: []string{"missing"}},
	}

	order := topoSort(byID)
	if len(order) != 1 || order[0] != "orphan" {
		t.Fatalf("order = %v, want [orphan]", order)
	}
}
