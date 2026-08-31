package hamt

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

// ctx is the context every tree call in these tests runs under. The hamt
// package does no cancellation-sensitive work of its own; it only forwards ctx
// to the object store.
var ctx = context.Background()

// routingKey mirrors engine.AffinityKey, the routing policy the backup engine
// feeds this package. The tree itself is policy-free — it only needs a hex
// routing key — so its tests derive one the same way a caller does.
func routingKey(parentID, fileID string) string {
	return core.ComputeHash([]byte(parentID))[:4] + core.ComputeHash([]byte(fileID))[4:]
}

// insertCommit and deleteCommit apply a single mutation and commit it, which is
// what the tree used to do implicitly on every step. Most tests here are about
// tree semantics rather than transaction batching, so they drive the Txn through
// these two helpers; the tests that care about batching (see
// TestCommitWritesOnlyTheFinalTree) use Txn directly.
func insertCommit(tree *Tree, root, routingKey, key, value string) (string, error) {
	tx := tree.Edit(root)
	if err := tx.Insert(ctx, routingKey, key, value); err != nil {
		return "", err
	}
	return tx.Commit(ctx)
}

func insertPayloadCommit(tree *Tree, root, routingKey, key, value string, p *Payload) (string, error) {
	tx := tree.Edit(root)
	if err := tx.InsertWithPayload(ctx, routingKey, key, value, p); err != nil {
		return "", err
	}
	return tx.Commit(ctx)
}

func deleteCommit(tree *Tree, root, routingKey, key string) (string, error) {
	tx := tree.Edit(root)
	if err := tx.Delete(ctx, routingKey, key); err != nil {
		return "", err
	}
	return tx.Commit(ctx)
}

// inMemoryStore implements store.ObjectStore for testing. Commit fans its node
// writes out across goroutines, so this must be safe for concurrent use like
// every real backend.
type inMemoryStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newInMemoryStore() *inMemoryStore {
	return &inMemoryStore{data: make(map[string][]byte)}
}

func (s *inMemoryStore) Put(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = data
	return nil
}
func (s *inMemoryStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	return d, nil
}
func (s *inMemoryStore) Exists(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[key]
	return ok, nil
}
func (s *inMemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}
func (s *inMemoryStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
func (s *inMemoryStore) Size(_ context.Context, key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[key]
	if !ok {
		return 0, fmt.Errorf("key not found: %s", key)
	}
	return int64(len(d)), nil
}

func (s *inMemoryStore) TotalSize(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, d := range s.data {
		total += int64(len(d))
	}
	return total, nil
}

// len reports the number of stored objects.
func (s *inMemoryStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data)
}

func (s *inMemoryStore) Flush(_ context.Context) error {
	return nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestInsertAndLookup(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root, err := insertCommit(tree, "", routingKey("", "file1"), "file1", "ref1")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	val, err := tree.Lookup(ctx, root, routingKey("", "file1"), "file1")
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
		root, err = insertCommit(tree, root, routingKey("", key), key, value)
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("file-%d", i)
		expected := fmt.Sprintf("ref-%d", i)
		val, err := tree.Lookup(ctx, root, routingKey("", key), key)
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

	root, _ := insertCommit(tree, "", routingKey("", "file1"), "file1", "ref-old")
	root, _ = insertCommit(tree, root, routingKey("", "file1"), "file1", "ref-new")

	val, _ := tree.Lookup(ctx, root, routingKey("", "file1"), "file1")
	if val != "ref-new" {
		t.Fatalf("got %q, want %q", val, "ref-new")
	}
}

func TestLookupMiss(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root, _ := insertCommit(tree, "", routingKey("", "file1"), "file1", "ref1")
	val, err := tree.Lookup(ctx, root, routingKey("", "nonexistent"), "nonexistent")
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
		root, err = insertCommit(tree, root, routingKey("", fmt.Sprintf("k%d", i)), fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	seen := map[string]string{}
	err = tree.Walk(ctx, root, func(key, value string) error {
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
	root, _ = insertCommit(tree, root, routingKey("", "a"), "a", "va")
	root, _ = insertCommit(tree, root, routingKey("", "b"), "b", "vb")
	root, _ = insertCommit(tree, root, routingKey("", "c"), "c", "vc")

	root, err = deleteCommit(tree, root, routingKey("", "b"), "b")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	val, _ := tree.Lookup(ctx, root, routingKey("", "b"), "b")
	if val != "" {
		t.Fatalf("expected empty after delete, got %q", val)
	}

	val, _ = tree.Lookup(ctx, root, routingKey("", "a"), "a")
	if val != "va" {
		t.Fatalf("expected va, got %q", val)
	}
	val, _ = tree.Lookup(ctx, root, routingKey("", "c"), "c")
	if val != "vc" {
		t.Fatalf("expected vc, got %q", val)
	}
}

func TestDeleteNonexistent(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root, _ := insertCommit(tree, "", routingKey("", "a"), "a", "va")
	root2, err := deleteCommit(tree, root, routingKey("", "nonexistent"), "nonexistent")
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

	root, _ := insertCommit(tree, "", routingKey("", "a"), "a", "va")
	root, err := deleteCommit(tree, root, routingKey("", "a"), "a")
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
	err := tree.Diff(ctx, "", "", func(DiffEntry) error { called = true; return nil })
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

	root, _ := insertCommit(tree, "", routingKey("", "a"), "a", "va")
	var called bool
	err := tree.Diff(ctx, root, root, func(DiffEntry) error { called = true; return nil })
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

	root1, _ := insertCommit(tree, "", routingKey("", "a"), "a", "va")
	root1, _ = insertCommit(tree, root1, routingKey("", "b"), "b", "vb")

	root2, _ := insertCommit(tree, "", routingKey("", "b"), "b", "vb")
	root2, _ = insertCommit(tree, root2, routingKey("", "c"), "c", "vc")

	var diffs []DiffEntry
	err := tree.Diff(ctx, root1, root2, func(d DiffEntry) error {
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
		root, err = insertCommit(tree, root, routingKey("", fmt.Sprintf("key-%04d", i)), fmt.Sprintf("key-%04d", i), fmt.Sprintf("val-%04d", i))
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	for i := 0; i < count; i++ {
		val, err := tree.Lookup(ctx, root, routingKey("", fmt.Sprintf("key-%04d", i)), fmt.Sprintf("key-%04d", i))
		if err != nil {
			t.Fatalf("Lookup %d: %v", i, err)
		}
		if val != fmt.Sprintf("val-%04d", i) {
			t.Fatalf("Lookup %d: got %q, want %q", i, val, fmt.Sprintf("val-%04d", i))
		}
	}

	walked := 0
	err = tree.Walk(ctx, root, func(key, value string) error {
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
		root, err = insertCommit(tree, root, routingKey("", fmt.Sprintf("k%d", i)), fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	var refs []string
	err = tree.NodeRefs(ctx, root, func(ref string) error {
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

func TestInsertWritesNothingUntilCommit(t *testing.T) {
	persistent := newInMemoryStore()
	tree := NewTree(persistent)

	tx := tree.Edit("")
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("k%d", i)
		if err := tx.Insert(ctx, routingKey("", key), key, "v"+key); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	if persistent.len() != 0 {
		t.Fatalf("expected an empty store before Commit, got %d objects", persistent.len())
	}

	// Root reports the ref the tree would have without committing to it, so a
	// dry run stays genuinely read-only.
	previewed, err := tx.Root(ctx)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if persistent.len() != 0 {
		t.Fatalf("Root wrote %d objects; it must write nothing", persistent.len())
	}

	root, err := tx.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if root != previewed {
		t.Fatalf("Commit returned %s but Root previewed %s", root, previewed)
	}
	if persistent.len() == 0 {
		t.Fatal("expected nodes in the store after Commit")
	}

	// Everything is reachable from the committed root.
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("k%d", i)
		got, err := tree.Lookup(ctx, root, routingKey("", key), key)
		if err != nil {
			t.Fatalf("Lookup %s: %v", key, err)
		}
		if got != "v"+key {
			t.Fatalf("Lookup %s = %q, want %q", key, got, "v"+key)
		}
	}
}

// A second Commit of an unchanged transaction must be free: everything is
// already clean, so there is nothing left to serialize or write.
func TestCommitIsIdempotent(t *testing.T) {
	persistent := newCountingStore()
	tree := NewTree(persistent)

	tx := tree.Edit("")
	if err := tx.Insert(ctx, routingKey("", "a"), "a", "va"); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	first, err := tx.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	persistent.reset()
	second, err := tx.Commit(ctx)
	if err != nil {
		t.Fatalf("second Commit: %v", err)
	}
	if second != first {
		t.Fatalf("second Commit returned %s, want %s", second, first)
	}
	if persistent.puts != 0 {
		t.Fatalf("second Commit wrote %d objects; expected none", persistent.puts)
	}
}

func TestDeleteFromLargeTree(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root := ""
	var err error
	count := 200
	for i := 0; i < count; i++ {
		root, err = insertCommit(tree, root, routingKey("", fmt.Sprintf("key-%04d", i)), fmt.Sprintf("key-%04d", i), fmt.Sprintf("val-%04d", i))
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	for i := 0; i < count; i += 2 {
		root, err = deleteCommit(tree, root, routingKey("", fmt.Sprintf("key-%04d", i)), fmt.Sprintf("key-%04d", i))
		if err != nil {
			t.Fatalf("Delete %d: %v", i, err)
		}
	}

	for i := 0; i < count; i++ {
		val, err := tree.Lookup(ctx, root, routingKey("", fmt.Sprintf("key-%04d", i)), fmt.Sprintf("key-%04d", i))
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

// countingStore wraps inMemoryStore and counts every operation, simulating
// what DebugStore does in production but with programmatic assertions.
// Thread-safe: writeParallel calls Put from multiple goroutines.
type countingStore struct {
	mu         sync.Mutex
	inner      *inMemoryStore
	puts       int
	gets       int
	exists     int
	putKeys    []string
	getKeys    []string
	existsKeys []string
}

func newCountingStore() *countingStore {
	return &countingStore{inner: newInMemoryStore()}
}

func (s *countingStore) Put(ctx context.Context, key string, data []byte) error {
	s.mu.Lock()
	s.puts++
	s.putKeys = append(s.putKeys, key)
	s.inner.data[key] = data
	s.mu.Unlock()
	return nil
}
func (s *countingStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	s.gets++
	s.getKeys = append(s.getKeys, key)
	d, ok := s.inner.data[key]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	return d, nil
}
func (s *countingStore) Exists(ctx context.Context, key string) (bool, error) {
	s.mu.Lock()
	s.exists++
	s.existsKeys = append(s.existsKeys, key)
	_, ok := s.inner.data[key]
	s.mu.Unlock()
	return ok, nil
}
func (s *countingStore) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, key)
}
func (s *countingStore) List(ctx context.Context, p string) ([]string, error) {
	return s.inner.List(ctx, p)
}
func (s *countingStore) Size(ctx context.Context, key string) (int64, error) {
	return s.inner.Size(ctx, key)
}
func (s *countingStore) TotalSize(ctx context.Context) (int64, error) { return s.inner.TotalSize(ctx) }
func (s *countingStore) Flush(_ context.Context) error                { return nil }

func (s *countingStore) reset() {
	s.mu.Lock()
	s.puts = 0
	s.gets = 0
	s.exists = 0
	s.putKeys = nil
	s.getKeys = nil
	s.existsKeys = nil
	s.mu.Unlock()
}

// Commit issues only Puts — no Exists probing. Dedup is the KeyCacheStore
// layer's job, not the tree's.
func TestCommitIssuesOnlyPuts(t *testing.T) {
	persistent := newCountingStore()
	tree := NewTree(persistent)

	tx := tree.Edit("")
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("k%d", i)
		if err := tx.Insert(ctx, routingKey("", key), key, "v"+key); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	if persistent.puts != 0 {
		t.Fatalf("expected 0 Puts before Commit, got %d", persistent.puts)
	}

	if _, err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if persistent.exists != 0 {
		t.Fatalf("expected 0 Exists calls during Commit, got %d", persistent.exists)
	}
	if persistent.puts == 0 {
		t.Fatal("expected Commit to write nodes")
	}
}

// TestCommitWritesOnlyTheFinalTree is the point of the whole design: a node
// superseded by a later insert in the same transaction is never serialized, so
// Commit writes exactly the nodes the final tree is made of — no more.
//
// Under the previous design each of these 100 inserts persisted a fresh spine
// and a reachability pass discarded the garbage afterwards. Here there is no
// garbage to discard, which the assertion states directly: the number of Puts
// equals the number of nodes reachable from the root.
func TestCommitWritesOnlyTheFinalTree(t *testing.T) {
	persistent := newCountingStore()
	tree := NewTree(persistent)

	tx := tree.Edit("")
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("k%d", i)
		if err := tx.Insert(ctx, routingKey("", key), key, "v"+key); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	root, err := tx.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	reachable := 0
	if err := tree.NodeRefs(ctx, root, func(string) error { reachable++; return nil }); err != nil {
		t.Fatalf("NodeRefs: %v", err)
	}

	if persistent.puts != reachable {
		t.Fatalf("Commit wrote %d nodes but only %d are reachable from the root",
			persistent.puts, reachable)
	}
	t.Logf("100 inserts wrote %d nodes, all of them reachable", persistent.puts)

	// Distinct keys must all have survived the batching.
	seen := 0
	if err := tree.Walk(ctx, root, func(string, string) error { seen++; return nil }); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if seen != 100 {
		t.Fatalf("Walk saw %d entries, want 100", seen)
	}
}

// TestNodeCacheLRUEviction verifies the bounded read cache in NodeStore: once
// it is full, evicted nodes require a fresh Get from the underlying store.
func TestNodeCacheLRUEviction(t *testing.T) {
	persistent := newCountingStore()

	n := nodeCacheSize + 100
	refs := make([]string, n)
	for i := 0; i < n; i++ {
		// Refs are content addresses, so they have to be computed, not made up:
		// NodeStore verifies every node it loads against its own ref.
		data := []byte(fmt.Sprintf(`{"type":"leaf","entries":[{"key":"k%d","filemeta":"v%d"}]}`, i, i))
		ref := nodePrefix + core.ComputeHash(data)
		refs[i] = ref
		_ = persistent.inner.Put(ctx, ref, data)
	}
	persistent.reset()

	nodes := NewNodeStore(persistent)

	for _, ref := range refs {
		if _, err := nodes.load(ctx, ref); err != nil {
			t.Fatalf("load %s: %v", ref, err)
		}
	}
	if persistent.gets != n {
		t.Fatalf("expected %d Gets on first pass, got %d", n, persistent.gets)
	}

	persistent.reset()

	// The most recent nodeCacheSize refs are still cached.
	for _, ref := range refs[n-nodeCacheSize:] {
		if _, err := nodes.load(ctx, ref); err != nil {
			t.Fatalf("load %s: %v", ref, err)
		}
	}
	if persistent.gets != 0 {
		t.Fatalf("expected 0 Gets for cached nodes, got %d", persistent.gets)
	}

	// The first 100 were evicted and must be re-fetched.
	for _, ref := range refs[:100] {
		if _, err := nodes.load(ctx, ref); err != nil {
			t.Fatalf("load %s: %v", ref, err)
		}
	}
	if persistent.gets != 100 {
		t.Fatalf("expected 100 Gets for evicted nodes, got %d", persistent.gets)
	}
}

func TestInternalNodeType(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root := ""
	var err error
	for i := 0; i < maxLeafSize+10; i++ {
		root, err = insertCommit(tree, root, routingKey("", fmt.Sprintf("key-%04d", i)), fmt.Sprintf("key-%04d", i), fmt.Sprintf("val-%04d", i))
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	n, err := tree.nodes.load(ctx, root)
	if err != nil {
		t.Fatalf("load node: %v", err)
	}
	if n.leaf {
		t.Fatal("expected an internal node at the root once the leaf has split")
	}
}

// ---------------------------------------------------------------------------
// Integrity
// ---------------------------------------------------------------------------

// A node ref is a content address, so a node that does not hash to the ref it
// was fetched under is corrupt or substituted. Every reader must refuse it
// rather than serve whatever it decoded.
func TestLoadRejectsTamperedNode(t *testing.T) {
	persistent := newInMemoryStore()
	tree := NewTree(persistent)

	root := ""
	var err error
	for i := 0; i < 60; i++ {
		key := fmt.Sprintf("k%02d", i)
		root, err = insertCommit(tree, root, routingKey("", key), key, "filemeta/v"+key)
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// Pick a node that is actually reachable from the final root — the store
	// also holds superseded nodes from the intermediate commits.
	var victim string
	if err := tree.NodeRefs(ctx, root, func(ref string) error {
		if ref != root && victim == "" {
			victim = ref
		}
		return nil
	}); err != nil {
		t.Fatalf("NodeRefs: %v", err)
	}
	if victim == "" {
		t.Fatal("no non-root node to tamper with")
	}

	// Rewrite its contents in place, leaving the ref untouched — exactly what a
	// compromised or bit-rotted backend would produce.
	persistent.data[victim] = []byte(`{"type":"leaf","entries":[{"key":"evil","filemeta":"filemeta/attacker"}]}`)

	// A fresh Tree, so the tampered node is re-read rather than served from the
	// cache populated while building.
	reader := NewTree(persistent)
	err = reader.Walk(ctx, root, func(string, string) error { return nil })
	if err == nil {
		t.Fatal("Walk accepted a tampered node")
	}
	if !strings.Contains(err.Error(), "integrity check") {
		t.Fatalf("expected an integrity error, got: %v", err)
	}
}

// buildOverDeepChain writes a chain of internal nodes ending in a leaf, nested
// deeper than routing can address. Every node is written under its true content
// address, so this passes the integrity check and isolates the depth guard: it
// is what a valid-looking but malformed tree looks like.
func buildOverDeepChain(t *testing.T, s *inMemoryStore, depth int) string {
	t.Helper()

	put := func(hn *storedNode) string {
		hash, data, err := core.ComputeJSONHash(hn)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		ref := nodePrefix + hash
		if err := s.Put(ctx, ref, data); err != nil {
			t.Fatalf("put: %v", err)
		}
		return ref
	}

	ref := put(&storedNode{
		Type:    nodeTypeLeaf,
		Entries: []leafEntry{{Key: "deep", PathKey: routingKey("", "deep"), Value: "filemeta/deep"}},
	})
	for i := 0; i < depth; i++ {
		ref = put(&storedNode{Type: nodeTypeInternal, Bitmap: 1, Children: []string{ref}})
	}
	return ref
}

func TestTraversalRefusesUnboundedNesting(t *testing.T) {
	persistent := newInMemoryStore()
	root := buildOverDeepChain(t, persistent, maxTreeDepth+3)
	nodes := NewNodeStore(persistent)

	t.Run("walk", func(t *testing.T) {
		assertTooDeep(t, walk(ctx, nodes, child{ref: root}, 0, func(leafEntry) error { return nil }))
	})
	t.Run("nodeRefs", func(t *testing.T) {
		assertTooDeep(t, nodeRefs(ctx, nodes, root, 0, func(string) error { return nil }))
	})
	t.Run("collectAll", func(t *testing.T) {
		n, err := nodes.load(ctx, root)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		assertTooDeep(t, collectAll(ctx, nodes, n, 0, func(leafEntry) error { return nil }))
	})
	t.Run("diffNodes", func(t *testing.T) {
		n, err := nodes.load(ctx, root)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		assertTooDeep(t, diffNodes(ctx, nodes, n, n, 0, func(DiffEntry) error { return nil }))
	})

	// Lookup is bounded independently, and more tightly: it consumes 5 routing
	// bits per level, so a 32-bit prefix runs out at level 6 — before the depth
	// guard is ever reached. "00000000" routes to bucket 0 at every level,
	// following this chain as far as it can go.
	t.Run("lookup", func(t *testing.T) {
		_, _, err := lookupEntry(ctx, nodes, child{ref: root}, "00000000", "deep")
		if err == nil {
			t.Fatal("expected Lookup to refuse an over-deep tree")
		}
		if !strings.Contains(err.Error(), "too deep") {
			t.Fatalf("expected a depth error, got: %v", err)
		}
	})
}

func assertTooDeep(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a depth-limit error, got nil")
	}
	if !strings.Contains(err.Error(), "cyclic or corrupt") {
		t.Fatalf("expected a depth-limit error, got: %v", err)
	}
}
