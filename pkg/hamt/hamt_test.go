package hamt

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/core"
)

// inMemoryStore implements store.ObjectStore for testing.
type inMemoryStore struct {
	data map[string][]byte
}

func newInMemoryStore() *inMemoryStore {
	return &inMemoryStore{data: make(map[string][]byte)}
}

func (s *inMemoryStore) Put(key string, data []byte) error   { s.data[key] = data; return nil }
func (s *inMemoryStore) Get(key string) ([]byte, error) {
	d, ok := s.data[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	return d, nil
}
func (s *inMemoryStore) Exists(key string) (bool, error) { _, ok := s.data[key]; return ok, nil }
func (s *inMemoryStore) Delete(key string) error         { delete(s.data, key); return nil }
func (s *inMemoryStore) List(prefix string) ([]string, error) {
	var keys []string
	for k := range s.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
func (s *inMemoryStore) Size(key string) (int64, error) {
	d, ok := s.data[key]
	if !ok {
		return 0, fmt.Errorf("key not found: %s", key)
	}
	return int64(len(d)), nil
}

func (s *inMemoryStore) TotalSize() (int64, error) {
	var total int64
	for _, d := range s.data {
		total += int64(len(d))
	}
	return total, nil
}

func putMeta(t *testing.T, s *inMemoryStore, m *core.FileMeta) string {
	t.Helper()
	h, d, err := core.ComputeJSONHash(m)
	if err != nil {
		t.Fatal(err)
	}
	ref := "filemeta/" + h
	_ = s.Put(ref, d)
	return ref
}

func TestTree_InsertAndLookup(t *testing.T) {
	s := newInMemoryStore()
	tree := NewTree(s)

	ref1 := putMeta(t, s, &core.FileMeta{FileID: "file1", Name: "file1.txt", Size: 100})
	ref2 := putMeta(t, s, &core.FileMeta{FileID: "file2", Name: "file2.txt", Size: 200})

	root, err := tree.Insert("", "file1", ref1)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := tree.Lookup(root, "file1")
	if err != nil || got != ref1 {
		t.Fatalf("Lookup file1: got %q, err %v", got, err)
	}

	root2, err := tree.Insert(root, "file2", ref2)
	if err != nil {
		t.Fatalf("Insert file2: %v", err)
	}
	if root2 == root {
		t.Error("expected new root after second insert")
	}

	got, _ = tree.Lookup(root2, "file1")
	if got != ref1 {
		t.Error("file1 missing from root2")
	}
	got, _ = tree.Lookup(root2, "file2")
	if got != ref2 {
		t.Error("file2 missing from root2")
	}
}

func TestTree_Update(t *testing.T) {
	s := newInMemoryStore()
	tree := NewTree(s)

	ref1 := putMeta(t, s, &core.FileMeta{FileID: "file1", Name: "v1.txt", Size: 100})
	ref2 := putMeta(t, s, &core.FileMeta{FileID: "file1", Name: "v2.txt", Size: 101})

	root, _ := tree.Insert("", "file1", ref1)
	root2, err := tree.Insert(root, "file1", ref2)
	if err != nil {
		t.Fatal(err)
	}

	got, _ := tree.Lookup(root2, "file1")
	if got != ref2 {
		t.Errorf("expected v2 ref, got %s", got)
	}

	// Old root still has v1.
	got, _ = tree.Lookup(root, "file1")
	if got != ref1 {
		t.Errorf("expected v1 ref in old root, got %s", got)
	}
}

func TestTree_Splitting(t *testing.T) {
	s := newInMemoryStore()
	tree := NewTree(s)

	root := ""
	refs := make(map[string]string)
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("file-%d", i)
		ref := putMeta(t, s, &core.FileMeta{FileID: id, Name: id})
		refs[id] = ref

		var err error
		root, err = tree.Insert(root, id, ref)
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	for id, ref := range refs {
		got, err := tree.Lookup(root, id)
		if err != nil || got != ref {
			t.Fatalf("Lookup %s: got %q, err %v", id, got, err)
		}
	}
}

func TestTree_Walk(t *testing.T) {
	s := newInMemoryStore()
	tree := NewTree(s)

	root := ""
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("f%d", i)
		var err error
		root, err = tree.Insert(root, id, "val-"+id)
		if err != nil {
			t.Fatal(err)
		}
	}

	collected := make(map[string]string)
	err := tree.Walk(root, func(k, v string) error {
		collected[k] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(collected) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(collected))
	}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("f%d", i)
		if collected[id] != "val-"+id {
			t.Errorf("Walk: wrong value for %s", id)
		}
	}
}

func TestTree_Diff(t *testing.T) {
	s := newInMemoryStore()
	tree := NewTree(s)

	// Tree 1: file1, file2
	root1, _ := tree.Insert("", "file1", "meta1")
	root1, _ = tree.Insert(root1, "file2", "meta2")

	// Tree 2: file1 (same), file2 (modified), file3 (added)
	root2, _ := tree.Insert("", "file1", "meta1")
	root2, _ = tree.Insert(root2, "file2", "meta2-v2")
	root2, _ = tree.Insert(root2, "file3", "meta3")

	changes := make(map[string]DiffEntry)
	err := tree.Diff(root1, root2, func(d DiffEntry) error {
		changes[d.Key] = d
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 changes, got %d: %+v", len(changes), changes)
	}

	// file2 modified
	d := changes["file2"]
	if d.OldValue != "meta2" || d.NewValue != "meta2-v2" {
		t.Errorf("file2: %+v", d)
	}

	// file3 added
	d = changes["file3"]
	if d.OldValue != "" || d.NewValue != "meta3" {
		t.Errorf("file3: %+v", d)
	}

	// Reverse: root2 vs root1
	revChanges := make(map[string]DiffEntry)
	_ = tree.Diff(root2, root1, func(d DiffEntry) error {
		revChanges[d.Key] = d
		return nil
	})

	d = revChanges["file3"]
	if d.OldValue != "meta3" || d.NewValue != "" {
		t.Errorf("reverse file3: %+v", d)
	}
}

func TestTree_NodeRefs(t *testing.T) {
	s := newInMemoryStore()
	tree := NewTree(s)

	root := ""
	for i := 0; i < 5; i++ {
		root, _ = tree.Insert(root, fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
	}

	var refs []string
	err := tree.NodeRefs(root, func(ref string) error {
		refs = append(refs, ref)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) == 0 {
		t.Error("expected at least one node ref")
	}
	if refs[0] != root {
		t.Errorf("first ref should be root, got %s", refs[0])
	}
}

func TestTree_DeleteSingle(t *testing.T) {
	s := newInMemoryStore()
	tree := NewTree(s)

	root, _ := tree.Insert("", "file1", "meta1")
	root, _ = tree.Insert(root, "file2", "meta2")
	root, _ = tree.Insert(root, "file3", "meta3")

	newRoot, err := tree.Delete(root, "file2")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if newRoot == root {
		t.Error("expected new root after delete")
	}

	got, _ := tree.Lookup(newRoot, "file1")
	if got != "meta1" {
		t.Errorf("file1: got %q, want meta1", got)
	}
	got, _ = tree.Lookup(newRoot, "file2")
	if got != "" {
		t.Errorf("file2 should be gone, got %q", got)
	}
	got, _ = tree.Lookup(newRoot, "file3")
	if got != "meta3" {
		t.Errorf("file3: got %q, want meta3", got)
	}

	// Old root is unchanged (persistence).
	got, _ = tree.Lookup(root, "file2")
	if got != "meta2" {
		t.Errorf("file2 should still exist in old root, got %q", got)
	}
}

func TestTree_DeleteNonExistent(t *testing.T) {
	s := newInMemoryStore()
	tree := NewTree(s)

	root, _ := tree.Insert("", "file1", "meta1")

	newRoot, err := tree.Delete(root, "no-such-key")
	if err != nil {
		t.Fatalf("Delete non-existent: %v", err)
	}
	if newRoot != root {
		t.Error("root should be unchanged when deleting a non-existent key")
	}
}

func TestTree_DeleteEmpty(t *testing.T) {
	tree := NewTree(newInMemoryStore())

	newRoot, err := tree.Delete("", "anything")
	if err != nil {
		t.Fatalf("Delete from empty: %v", err)
	}
	if newRoot != "" {
		t.Errorf("expected empty root, got %q", newRoot)
	}
}

func TestTree_DeleteAll(t *testing.T) {
	s := newInMemoryStore()
	tree := NewTree(s)

	root, _ := tree.Insert("", "a", "va")
	root, _ = tree.Insert(root, "b", "vb")

	root, err := tree.Delete(root, "a")
	if err != nil {
		t.Fatal(err)
	}
	root, err = tree.Delete(root, "b")
	if err != nil {
		t.Fatal(err)
	}

	collected := 0
	_ = tree.Walk(root, func(k, v string) error {
		collected++
		return nil
	})
	if collected != 0 {
		t.Errorf("expected empty tree, got %d entries", collected)
	}
}

func TestTree_DeleteFromLargeTree(t *testing.T) {
	s := newInMemoryStore()
	tree := NewTree(s)

	root := ""
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("file-%03d", i)
		root, _ = tree.Insert(root, id, "val-"+id)
	}

	// Delete every other entry.
	for i := 0; i < 50; i += 2 {
		id := fmt.Sprintf("file-%03d", i)
		var err error
		root, err = tree.Delete(root, id)
		if err != nil {
			t.Fatalf("Delete %s: %v", id, err)
		}
	}

	// Verify remaining entries.
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("file-%03d", i)
		got, err := tree.Lookup(root, id)
		if err != nil {
			t.Fatalf("Lookup %s: %v", id, err)
		}
		if i%2 == 0 && got != "" {
			t.Errorf("%s should be deleted, got %q", id, got)
		}
		if i%2 != 0 && got != "val-"+id {
			t.Errorf("%s: got %q, want %q", id, got, "val-"+id)
		}
	}
}

func TestTree_Persistence(t *testing.T) {
	s := newInMemoryStore()
	tree := NewTree(s)

	root1, _ := tree.Insert("", "A", "metaA")
	root2, _ := tree.Insert(root1, "B", "metaB")
	root3, _ := tree.Insert(root2, "C", "metaC")

	// All three accessible from root3.
	for _, k := range []string{"A", "B", "C"} {
		v, err := tree.Lookup(root3, k)
		if err != nil || v == "" {
			t.Fatalf("%s not found in root3", k)
		}
	}

	// A accessible from root1, B is not.
	v, _ := tree.Lookup(root1, "A")
	if v == "" {
		t.Fatal("A not found in root1")
	}
	v, _ = tree.Lookup(root1, "B")
	if v != "" {
		t.Fatal("B should not be in root1")
	}
}
