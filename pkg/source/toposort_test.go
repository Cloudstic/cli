package source

import (
	"slices"
	"testing"
)

// upserts builds a change batch from (id, parent) pairs. An empty parent means
// the entry has no parent in this source.
func upserts(t *testing.T, pairs ...[2]string) []FileChange {
	t.Helper()
	changes := make([]FileChange, 0, len(pairs))
	for _, p := range pairs {
		fc := FileChange{Type: ChangeUpsert}
		fc.Meta.FileID = p[0]
		if p[1] != "" {
			fc.Meta.Parents = []string{p[1]}
		}
		changes = append(changes, fc)
	}
	return changes
}

func ids(changes []FileChange) []string {
	out := make([]string, len(changes))
	for i, fc := range changes {
		out[i] = fc.Meta.FileID
	}
	return out
}

// assertParentsFirst checks the invariant the backup engine depends on: for
// every entry whose parent is also in the batch, the parent appears earlier.
func assertParentsFirst(t *testing.T, changes []FileChange) {
	t.Helper()
	seen := map[string]bool{}
	present := map[string]bool{}
	for _, fc := range changes {
		present[fc.Meta.FileID] = true
	}
	for _, fc := range changes {
		for _, p := range fc.Meta.Parents {
			if present[p] && !seen[p] {
				t.Errorf("%q emitted before its parent %q (order: %v)",
					fc.Meta.FileID, p, ids(changes))
			}
		}
		seen[fc.Meta.FileID] = true
	}
}

func TestTopoSortFolderChanges_ParentsBeforeChildren(t *testing.T) {
	// Deliberately reversed: children listed before their parents, which is
	// what a change feed ordered by modification time looks like.
	in := upserts(t,
		[2]string{"grandchild", "child"},
		[2]string{"child", "root"},
		[2]string{"root", ""},
	)

	got := TopoSortFolderChanges(in)

	if len(got) != len(in) {
		t.Fatalf("got %d changes, want %d (%v)", len(got), len(in), ids(got))
	}
	assertParentsFirst(t, got)
	if want := []string{"root", "child", "grandchild"}; !slices.Equal(ids(got), want) {
		t.Errorf("order = %v, want %v", ids(got), want)
	}
}

func TestTopoSortFolderChanges_ParentNotInBatch(t *testing.T) {
	// A parent outside the batch cannot be ordered against, and must not cause
	// the entry to be dropped — this is the common incremental case, where only
	// a leaf changed and its parents are untouched.
	in := upserts(t,
		[2]string{"b", "absent-parent"},
		[2]string{"a", ""},
	)

	got := TopoSortFolderChanges(in)

	if len(got) != 2 {
		t.Fatalf("got %d changes, want 2 (%v)", len(got), ids(got))
	}
	for _, want := range []string{"a", "b"} {
		if !slices.Contains(ids(got), want) {
			t.Errorf("%q was dropped; order = %v", want, ids(got))
		}
	}
}

func TestTopoSortFolderChanges_CycleTerminates(t *testing.T) {
	// A cycle must not hang or panic. Sources should never emit one, but a
	// buggy or hostile change feed must not wedge a backup. The visited set
	// is what breaks the recursion; if that regresses this test times out.
	in := upserts(t,
		[2]string{"x", "y"},
		[2]string{"y", "x"},
	)

	got := TopoSortFolderChanges(in)

	if len(got) != 2 {
		t.Errorf("got %d changes, want both entries preserved (%v)", len(got), ids(got))
	}
}

func TestTopoSortFolderChanges_Empty(t *testing.T) {
	if got := TopoSortFolderChanges(nil); len(got) != 0 {
		t.Errorf("TopoSortFolderChanges(nil) = %v, want empty", ids(got))
	}
}
