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

// assertComplete checks that every entry appears exactly once.
func assertComplete(t *testing.T, order []string, byID map[string]core.FileMeta) {
	t.Helper()

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

// The ordering constraint restore actually depends on: an entry's parents are
// written before it is.
func TestRestoreOrder_OrdersParentsBeforeChildren(t *testing.T) {
	byID := map[string]core.FileMeta{
		"root":  {FileID: "root", Type: core.FileTypeFolder},
		"a":     {FileID: "a", Type: core.FileTypeFolder, Parents: []string{"root"}},
		"b":     {FileID: "b", Type: core.FileTypeFolder, Parents: []string{"a"}},
		"leaf":  {FileID: "leaf", Parents: []string{"b"}},
		"other": {FileID: "other", Parents: []string{"root"}},
	}
	walk := []string{"root", "a", "b", "leaf", "other"}

	order := restoreOrder(byID, walk)
	assertComplete(t, order, byID)

	for _, rel := range []struct{ child, parent string }{
		{"a", "root"}, {"b", "a"}, {"leaf", "b"}, {"other", "root"},
	} {
		if indexOf(order, rel.parent) > indexOf(order, rel.child) {
			t.Errorf("%s (parent) is ordered after %s (child)", rel.parent, rel.child)
		}
	}
}

// The point of the change: entries that are nobody's parent keep walk order,
// which is the order they sit in packfiles. A topological order over the whole
// tree groups them by directory instead, and scatters pack reads.
func TestRestoreOrder_LeavesKeepWalkOrder(t *testing.T) {
	byID := map[string]core.FileMeta{
		"dirA": {FileID: "dirA", Type: core.FileTypeFolder},
		"dirB": {FileID: "dirB", Type: core.FileTypeFolder},
		// Interleaved across two directories, as a pack laid them out.
		"a1": {FileID: "a1", Parents: []string{"dirA"}},
		"b1": {FileID: "b1", Parents: []string{"dirB"}},
		"a2": {FileID: "a2", Parents: []string{"dirA"}},
		"b2": {FileID: "b2", Parents: []string{"dirB"}},
	}
	walk := []string{"dirA", "a1", "dirB", "b1", "a2", "b2"}

	order := restoreOrder(byID, walk)
	assertComplete(t, order, byID)

	var leaves []string
	for _, id := range order {
		switch id {
		case "a1", "a2", "b1", "b2":
			leaves = append(leaves, id)
		}
	}
	want := []string{"a1", "b1", "a2", "b2"}
	for i := range want {
		if leaves[i] != want[i] {
			t.Fatalf("leaf order = %v, want %v (walk order)", leaves, want)
		}
	}

	for _, rel := range []struct{ child, parent string }{
		{"a1", "dirA"}, {"a2", "dirA"}, {"b1", "dirB"}, {"b2", "dirB"},
	} {
		if indexOf(order, rel.parent) > indexOf(order, rel.child) {
			t.Errorf("%s is ordered after %s", rel.parent, rel.child)
		}
	}
}

// Interior membership comes from the data, not from Type. A snapshot is read off
// a store, so an entry naming a plain file as its parent must still be ordered
// after it rather than trusted not to exist.
func TestRestoreOrder_TreatsAnyNamedParentAsInterior(t *testing.T) {
	byID := map[string]core.FileMeta{
		"file":  {FileID: "file", Type: core.FileTypeFile},
		"child": {FileID: "child", Type: core.FileTypeFile, Parents: []string{"file"}},
	}

	order := restoreOrder(byID, []string{"file", "child"})
	assertComplete(t, order, byID)
	if indexOf(order, "file") > indexOf(order, "child") {
		t.Errorf("a file named as a parent was ordered after its child: %v", order)
	}
}

// A parent cycle is not reachable from a repository this code wrote, but it is
// from a corrupt or hostile one. It must terminate and emit each entry once.
func TestRestoreOrder_TerminatesOnAParentCycle(t *testing.T) {
	byID := map[string]core.FileMeta{
		"x": {FileID: "x", Type: core.FileTypeFolder, Parents: []string{"y"}},
		"y": {FileID: "y", Type: core.FileTypeFolder, Parents: []string{"x"}},
		"z": {FileID: "z", Parents: []string{"x"}},
	}

	done := make(chan []string, 1)
	go func() { done <- restoreOrder(byID, []string{"x", "y", "z"}) }()
	assertComplete(t, <-done, byID)
}

// An entry naming a parent that is not in the snapshot must still be ordered,
// not dropped: a filtered or partial tree has entries whose parents were never
// loaded.
func TestRestoreOrder_KeepsEntriesWithUnknownParents(t *testing.T) {
	byID := map[string]core.FileMeta{
		"orphan": {FileID: "orphan", Parents: []string{"missing"}},
	}

	order := restoreOrder(byID, []string{"orphan"})
	if len(order) != 1 || order[0] != "orphan" {
		t.Fatalf("order = %v, want [orphan]", order)
	}
}

// A walk order that does not mention every entry must still restore all of them.
// Nothing produces that today; dropping a file silently is bad enough to guard.
func TestRestoreOrder_EmitsEntriesMissingFromWalkOrder(t *testing.T) {
	byID := map[string]core.FileMeta{
		"dir":     {FileID: "dir", Type: core.FileTypeFolder},
		"listed":  {FileID: "listed", Parents: []string{"dir"}},
		"missing": {FileID: "missing", Parents: []string{"dir"}},
	}

	order := restoreOrder(byID, []string{"dir", "listed"})
	assertComplete(t, order, byID)
	if indexOf(order, "dir") > indexOf(order, "missing") {
		t.Errorf("dir is ordered after missing: %v", order)
	}
}

// An empty snapshot is not an error.
func TestRestoreOrder_Empty(t *testing.T) {
	if order := restoreOrder(map[string]core.FileMeta{}, nil); len(order) != 0 {
		t.Fatalf("order = %v, want empty", order)
	}
}
