package hamt

import (
	"fmt"
	"slices"
	"testing"
)

// A leaf's entries are ordered by key, and every write path used to arrive
// there by appending and sorting the whole leaf. Placing the entry directly
// (node.placeFor) has to reach the same result, because the order is the
// encoded order and so part of every root hash — TestRootHashGolden pins the
// bytes, and these pin the mechanism that produces them.

func TestPlaceForOrdersANewEntry(t *testing.T) {
	n := &node{leaf: true, entries: []leafEntry{{Key: "b"}, {Key: "d"}, {Key: "f"}}}

	for _, tc := range []struct {
		key string
		pos int
	}{
		{"a", 0}, // before every entry
		{"c", 1},
		{"e", 2},
		{"g", 3}, // after every entry
	} {
		pos, replaced := n.placeFor(tc.key)
		if replaced {
			t.Errorf("placeFor(%q) reported a replacement in a leaf that does not hold it", tc.key)
		}
		if pos != tc.pos {
			t.Errorf("placeFor(%q) = %d, want %d", tc.key, pos, tc.pos)
		}
	}
}

func TestPlaceForFindsAReplacement(t *testing.T) {
	n := &node{leaf: true, entries: []leafEntry{{Key: "b"}, {Key: "d"}, {Key: "f"}}}

	for want, key := range []string{"b", "d", "f"} {
		pos, replaced := n.placeFor(key)
		if !replaced || pos != want {
			t.Errorf("placeFor(%q) = (%d, %v), want (%d, true)", key, pos, replaced, want)
		}
	}
}

// The replacement search is exhaustive rather than a binary search, so that
// whether an entry is replaced never depends on the leaf it is handed being
// ordered. Every writer in this package orders its leaves, but a stored node
// is bytes from a repository and "replaced" is what keeps a tree's shape a
// function of its contents: answering it wrong would leave two entries under
// one key.
func TestPlaceForFindsAReplacementInAnUnorderedLeaf(t *testing.T) {
	n := &node{leaf: true, entries: []leafEntry{{Key: "f"}, {Key: "b"}, {Key: "d"}}}

	for want, key := range []string{"f", "b", "d"} {
		pos, replaced := n.placeFor(key)
		if !replaced || pos != want {
			t.Errorf("placeFor(%q) = (%d, %v), want (%d, true)", key, pos, replaced, want)
		}
	}
}

// The property the whole change rests on: however entries arrive, the leaf
// they land in is ordered by key.
func TestLeafInsertionLeavesEntriesOrdered(t *testing.T) {
	tree, _ := v3TestTree()
	tx := tree.Edit("")

	// One routing key for every entry, so they all share a single leaf and
	// the ordering under test is the in-leaf one rather than the routing.
	const shared = "shared"
	const n = 64
	// A stride coprime with n visits every entry exactly once in an order
	// unrelated to the key ordering, which is the interesting case: appending
	// in key order would keep a leaf sorted by accident.
	for i := range n {
		key := fmt.Sprintf("file-%02d", (i*29)%n)
		if err := tx.Insert(ctx, routingKey(shared, shared), key, "filemeta/"+key); err != nil {
			t.Fatalf("insert %s: %v", key, err)
		}
	}

	root, err := tx.Commit(ctx)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	var keys []string
	if err := tree.WalkEntries(ctx, root, func(key, _ string, _ *Payload) error {
		keys = append(keys, key)
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if !slices.IsSorted(keys) {
		t.Errorf("leaf entries are not ordered by key: %v", keys)
	}
	if len(keys) != n {
		t.Errorf("walked %d entries, want %d", len(keys), n)
	}
}
