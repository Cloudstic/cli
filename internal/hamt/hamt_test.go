package hamt

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

// inMemoryStore implements store.ObjectStore for testing.
type inMemoryStore struct {
	data map[string][]byte
}

func newInMemoryStore() *inMemoryStore {
	return &inMemoryStore{data: make(map[string][]byte)}
}

func (s *inMemoryStore) Put(_ context.Context, key string, data []byte) error {
	s.data[key] = data
	return nil
}
func (s *inMemoryStore) Get(_ context.Context, key string) ([]byte, error) {
	d, ok := s.data[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	return d, nil
}
func (s *inMemoryStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := s.data[key]
	return ok, nil
}
func (s *inMemoryStore) Delete(_ context.Context, key string) error {
	delete(s.data, key)
	return nil
}
func (s *inMemoryStore) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range s.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
func (s *inMemoryStore) Size(_ context.Context, key string) (int64, error) {
	d, ok := s.data[key]
	if !ok {
		return 0, fmt.Errorf("key not found: %s", key)
	}
	return int64(len(d)), nil
}

func (s *inMemoryStore) TotalSize(_ context.Context) (int64, error) {
	var total int64
	for _, d := range s.data {
		total += int64(len(d))
	}
	return total, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestInsertAndLookup(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root, err := tree.Insert("", "file1", "ref1")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	val, err := tree.Lookup(root, "file1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if val != "ref1" {
		t.Fatalf("got %q, want %q", val, "ref1")
	}
}

func TestMultipleInserts(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root := ""
	var err error
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("file-%d", i)
		value := fmt.Sprintf("ref-%d", i)
		root, err = tree.Insert(root, key, value)
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("file-%d", i)
		expected := fmt.Sprintf("ref-%d", i)
		val, err := tree.Lookup(root, key)
		if err != nil {
			t.Fatalf("Lookup %d: %v", i, err)
		}
		if val != expected {
			t.Fatalf("Lookup %d: got %q, want %q", i, val, expected)
		}
	}
}

func TestUpdate(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root, _ := tree.Insert("", "file1", "ref-old")
	root, _ = tree.Insert(root, "file1", "ref-new")

	val, _ := tree.Lookup(root, "file1")
	if val != "ref-new" {
		t.Fatalf("got %q, want %q", val, "ref-new")
	}
}

func TestLookupMiss(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root, _ := tree.Insert("", "file1", "ref1")
	val, err := tree.Lookup(root, "nonexistent")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if val != "" {
		t.Fatalf("expected empty, got %q", val)
	}
}

func TestWalk(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root := ""
	var err error
	for i := 0; i < 50; i++ {
		root, err = tree.Insert(root, fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	seen := map[string]string{}
	err = tree.Walk(root, func(key, value string) error {
		seen[key] = value
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(seen) != 50 {
		t.Fatalf("Walk returned %d entries, want 50", len(seen))
	}
}

func TestDelete(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root := ""
	var err error
	root, _ = tree.Insert(root, "a", "va")
	root, _ = tree.Insert(root, "b", "vb")
	root, _ = tree.Insert(root, "c", "vc")

	root, err = tree.Delete(root, "b")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	val, _ := tree.Lookup(root, "b")
	if val != "" {
		t.Fatalf("expected empty after delete, got %q", val)
	}

	val, _ = tree.Lookup(root, "a")
	if val != "va" {
		t.Fatalf("expected va, got %q", val)
	}
	val, _ = tree.Lookup(root, "c")
	if val != "vc" {
		t.Fatalf("expected vc, got %q", val)
	}
}

func TestDeleteNonexistent(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root, _ := tree.Insert("", "a", "va")
	root2, err := tree.Delete(root, "nonexistent")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if root2 != root {
		t.Fatalf("expected unchanged root")
	}
}

func TestDeleteAll(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root, _ := tree.Insert("", "a", "va")
	root, err := tree.Delete(root, "a")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if root != "" {
		t.Fatalf("expected empty root, got %q", root)
	}
}

func TestDiffBothEmpty(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	var called bool
	err := tree.Diff("", "", func(DiffEntry) error { called = true; return nil })
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if called {
		t.Fatal("should not call fn for equal empty roots")
	}
}

func TestDiffSameRoot(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root, _ := tree.Insert("", "a", "va")
	var called bool
	err := tree.Diff(root, root, func(DiffEntry) error { called = true; return nil })
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if called {
		t.Fatal("should not call fn when roots are equal")
	}
}

func TestDiffAddsAndRemoves(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root1, _ := tree.Insert("", "a", "va")
	root1, _ = tree.Insert(root1, "b", "vb")

	root2, _ := tree.Insert("", "b", "vb")
	root2, _ = tree.Insert(root2, "c", "vc")

	var diffs []DiffEntry
	err := tree.Diff(root1, root2, func(d DiffEntry) error {
		diffs = append(diffs, d)
		return nil
	})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	added := false
	removed := false
	for _, d := range diffs {
		if d.Key == "c" && d.OldValue == "" && d.NewValue == "vc" {
			added = true
		}
		if d.Key == "a" && d.OldValue == "va" && d.NewValue == "" {
			removed = true
		}
	}
	if !added {
		t.Fatal("expected addition of c")
	}
	if !removed {
		t.Fatal("expected removal of a")
	}
}

func TestLargeTree(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root := ""
	var err error
	count := 500
	for i := 0; i < count; i++ {
		root, err = tree.Insert(root, fmt.Sprintf("key-%04d", i), fmt.Sprintf("val-%04d", i))
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	for i := 0; i < count; i++ {
		val, err := tree.Lookup(root, fmt.Sprintf("key-%04d", i))
		if err != nil {
			t.Fatalf("Lookup %d: %v", i, err)
		}
		if val != fmt.Sprintf("val-%04d", i) {
			t.Fatalf("Lookup %d: got %q, want %q", i, val, fmt.Sprintf("val-%04d", i))
		}
	}

	walked := 0
	err = tree.Walk(root, func(key, value string) error {
		walked++
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if walked != count {
		t.Fatalf("Walk: got %d, want %d", walked, count)
	}
}

func TestNodeRefs(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root := ""
	var err error
	for i := 0; i < 100; i++ {
		root, err = tree.Insert(root, fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	var refs []string
	err = tree.NodeRefs(root, func(ref string) error {
		refs = append(refs, ref)
		return nil
	})
	if err != nil {
		t.Fatalf("NodeRefs: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("expected at least one ref")
	}
	if refs[0] != root {
		t.Fatalf("first ref should be root: got %q", refs[0])
	}

	// All refs should exist in the store.
	for _, ref := range refs {
		if _, ok := store.data[ref]; !ok {
			t.Fatalf("ref %s not found in store", ref)
		}
	}
}

func TestTransactionalStore(t *testing.T) {
	persistent := newInMemoryStore()
	ts := NewTransactionalStore(persistent)

	tree := NewTree(ts)
	root, err := tree.Insert("", "a", "va")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Before flush, persistent should be empty (or at least not contain the node).
	if len(persistent.data) != 0 {
		t.Fatalf("expected empty persistent store before flush, got %d entries", len(persistent.data))
	}

	if err := ts.Flush(root); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// After flush, persistent should have at least the root node.
	if len(persistent.data) == 0 {
		t.Fatal("expected non-empty persistent store after flush")
	}
}

func TestDeleteFromLargeTree(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root := ""
	var err error
	count := 200
	for i := 0; i < count; i++ {
		root, err = tree.Insert(root, fmt.Sprintf("key-%04d", i), fmt.Sprintf("val-%04d", i))
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	for i := 0; i < count; i += 2 {
		root, err = tree.Delete(root, fmt.Sprintf("key-%04d", i))
		if err != nil {
			t.Fatalf("Delete %d: %v", i, err)
		}
	}

	for i := 0; i < count; i++ {
		val, err := tree.Lookup(root, fmt.Sprintf("key-%04d", i))
		if err != nil {
			t.Fatalf("Lookup %d: %v", i, err)
		}
		if i%2 == 0 {
			if val != "" {
				t.Fatalf("key-%04d: should be deleted, got %q", i, val)
			}
		} else {
			expected := fmt.Sprintf("val-%04d", i)
			if val != expected {
				t.Fatalf("key-%04d: got %q, want %q", i, val, expected)
			}
		}
	}
}

func TestInternalNodeType(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root := ""
	var err error
	for i := 0; i < maxLeafSize+10; i++ {
		root, err = tree.Insert(root, fmt.Sprintf("key-%04d", i), fmt.Sprintf("val-%04d", i))
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	node, err := tree.loadNode(root)
	if err != nil {
		t.Fatalf("loadNode: %v", err)
	}
	if node.Type != core.ObjectTypeInternal {
		t.Fatalf("expected internal node, got %s", node.Type)
	}
}
