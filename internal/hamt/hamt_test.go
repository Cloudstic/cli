package hamt

import (
	"context"
	"fmt"
	"strings"
	"sync"
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

func (s *inMemoryStore) Flush(_ context.Context) error {
	return nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestInsertAndLookup(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root, err := tree.Insert("", "", "file1", "ref1")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	val, err := tree.Lookup(root, "", "file1")
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
		root, err = tree.Insert(root, "", key, value)
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("file-%d", i)
		expected := fmt.Sprintf("ref-%d", i)
		val, err := tree.Lookup(root, "", key)
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

	root, _ := tree.Insert("", "", "file1", "ref-old")
	root, _ = tree.Insert(root, "", "file1", "ref-new")

	val, _ := tree.Lookup(root, "", "file1")
	if val != "ref-new" {
		t.Fatalf("got %q, want %q", val, "ref-new")
	}
}

func TestLookupMiss(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root, _ := tree.Insert("", "", "file1", "ref1")
	val, err := tree.Lookup(root, "", "nonexistent")
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
		root, err = tree.Insert(root, "", fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
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
	root, _ = tree.Insert(root, "", "a", "va")
	root, _ = tree.Insert(root, "", "b", "vb")
	root, _ = tree.Insert(root, "", "c", "vc")

	root, err = tree.Delete(root, "", "b")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	val, _ := tree.Lookup(root, "", "b")
	if val != "" {
		t.Fatalf("expected empty after delete, got %q", val)
	}

	val, _ = tree.Lookup(root, "", "a")
	if val != "va" {
		t.Fatalf("expected va, got %q", val)
	}
	val, _ = tree.Lookup(root, "", "c")
	if val != "vc" {
		t.Fatalf("expected vc, got %q", val)
	}
}

func TestDeleteNonexistent(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root, _ := tree.Insert("", "", "a", "va")
	root2, err := tree.Delete(root, "", "nonexistent")
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

	root, _ := tree.Insert("", "", "a", "va")
	root, err := tree.Delete(root, "", "a")
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

	root, _ := tree.Insert("", "", "a", "va")
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

	root1, _ := tree.Insert("", "", "a", "va")
	root1, _ = tree.Insert(root1, "", "b", "vb")

	root2, _ := tree.Insert("", "", "b", "vb")
	root2, _ = tree.Insert(root2, "", "c", "vc")

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
		root, err = tree.Insert(root, "", fmt.Sprintf("key-%04d", i), fmt.Sprintf("val-%04d", i))
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	for i := 0; i < count; i++ {
		val, err := tree.Lookup(root, "", fmt.Sprintf("key-%04d", i))
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
		root, err = tree.Insert(root, "", fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
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
	root, err := tree.Insert("", "", "a", "va")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Before flush, persistent should be empty (or at least not contain the node).
	if len(persistent.data) != 0 {
		t.Fatalf("expected empty persistent store before flush, got %d entries", len(persistent.data))
	}

	if err := ts.FlushReachable(root); err != nil {
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
		root, err = tree.Insert(root, "", fmt.Sprintf("key-%04d", i), fmt.Sprintf("val-%04d", i))
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	for i := 0; i < count; i += 2 {
		root, err = tree.Delete(root, "", fmt.Sprintf("key-%04d", i))
		if err != nil {
			t.Fatalf("Delete %d: %v", i, err)
		}
	}

	for i := 0; i < count; i++ {
		val, err := tree.Lookup(root, "", fmt.Sprintf("key-%04d", i))
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

// TestFlushReachable_NoExistsCalls verifies that writeParallel issues only Put
// calls (no Exists) during FlushReachable. Before the refactor, every node was
// preceded by an Exists check; now the KeyCacheStore layer handles dedup.
func TestFlushReachable_NoExistsCalls(t *testing.T) {
	persistent := newCountingStore()
	ts := NewTransactionalStore(persistent)
	tree := NewTree(ts)

	root := ""
	var err error
	for i := 0; i < 100; i++ {
		root, err = tree.Insert(root, "", fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	if persistent.puts != 0 {
		t.Fatalf("expected 0 persistent Puts before flush, got %d", persistent.puts)
	}

	if err := ts.FlushReachable(root); err != nil {
		t.Fatalf("FlushReachable: %v", err)
	}

	if persistent.exists != 0 {
		t.Fatalf("expected 0 Exists calls during flush, got %d (keys: %v)", persistent.exists, persistent.existsKeys)
	}
	if persistent.puts == 0 {
		t.Fatal("expected at least one Put during flush")
	}

	// Verify every flushed node is actually reachable from root.
	reachable := map[string]bool{}
	err = tree.NodeRefs(root, func(ref string) error {
		reachable[ref] = true
		return nil
	})
	if err != nil {
		t.Fatalf("NodeRefs: %v", err)
	}
	for _, key := range persistent.putKeys {
		if !reachable[key] {
			t.Fatalf("flushed unreachable node: %s", key)
		}
	}
}

// TestFlushReachable_DiscardsIntermediateNodes verifies that FlushReachable
// only writes the final reachable set, not every intermediate node produced
// during 100 sequential inserts.
func TestFlushReachable_DiscardsIntermediateNodes(t *testing.T) {
	persistent := newCountingStore()
	ts := NewTransactionalStore(persistent)
	tree := NewTree(ts)

	root := ""
	var err error
	for i := 0; i < 100; i++ {
		root, err = tree.Insert(root, "", fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	stagedBefore := len(ts.staging)

	if err := ts.FlushReachable(root); err != nil {
		t.Fatalf("FlushReachable: %v", err)
	}

	// The final tree for 100 keys is much smaller than the sum of all
	// intermediate trees generated during sequential inserts.
	if persistent.puts >= stagedBefore {
		t.Fatalf("expected fewer Puts (%d) than staged nodes (%d) — intermediate nodes should be discarded",
			persistent.puts, stagedBefore)
	}
	t.Logf("staged %d intermediate nodes, flushed %d reachable", stagedBefore, persistent.puts)
}

// TestReadCache_LRUEviction verifies the bounded LRU read cache: once the
// cache is full, evicted entries require a fresh Get from the persistent store.
func TestReadCache_LRUEviction(t *testing.T) {
	persistent := newCountingStore()

	// Pre-populate the persistent store with many small node-like objects.
	ctx := context.Background()
	n := readCacheSize + 100
	keys := make([]string, n)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("node/test-%04d", i)
		keys[i] = key
		_ = persistent.inner.Put(ctx, key, []byte(fmt.Sprintf(`{"type":"leaf","entries":[{"key":"k%d","filemeta":"v%d"}]}`, i, i)))
	}
	persistent.reset()

	ts := NewTransactionalStore(persistent)

	// Read all keys — each triggers a persistent Get and fills the LRU.
	for _, key := range keys {
		if _, err := ts.Get(ctx, key); err != nil {
			t.Fatalf("Get %s: %v", key, err)
		}
	}

	firstPassGets := persistent.gets
	if firstPassGets != n {
		t.Fatalf("expected %d Gets on first pass, got %d", n, firstPassGets)
	}

	persistent.reset()

	// Re-read the last `readCacheSize` keys — these should be LRU-cached.
	for _, key := range keys[n-readCacheSize:] {
		if _, err := ts.Get(ctx, key); err != nil {
			t.Fatalf("Get %s: %v", key, err)
		}
	}
	if persistent.gets != 0 {
		t.Fatalf("expected 0 Gets for recently cached keys, got %d", persistent.gets)
	}

	// Re-read the first 100 keys — these were evicted and must be re-fetched.
	for _, key := range keys[:100] {
		if _, err := ts.Get(ctx, key); err != nil {
			t.Fatalf("Get %s: %v", key, err)
		}
	}
	if persistent.gets != 100 {
		t.Fatalf("expected 100 Gets for evicted keys, got %d", persistent.gets)
	}
}

// TestStagingTakesPrecedenceOverReadCache verifies that a key written to
// staging is always returned, even if the read cache holds stale data.
func TestStagingTakesPrecedenceOverReadCache(t *testing.T) {
	persistent := newCountingStore()
	ctx := context.Background()

	_ = persistent.inner.Put(ctx, "node/x", []byte(`{"type":"leaf","entries":[{"key":"old","filemeta":"old-ref"}]}`))
	persistent.reset()

	ts := NewTransactionalStore(persistent)

	// Populate the read cache.
	_, _ = ts.Get(ctx, "node/x")
	if persistent.gets != 1 {
		t.Fatalf("expected 1 Get to populate read cache, got %d", persistent.gets)
	}

	// Write a new version to staging.
	newData := []byte(`{"type":"leaf","entries":[{"key":"new","filemeta":"new-ref"}]}`)
	_ = ts.Put(ctx, "node/x", newData)

	persistent.reset()

	// Get should return staging data, not read cache, and no persistent call.
	got, err := ts.Get(ctx, "node/x")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(newData) {
		t.Fatalf("expected staging data, got %q", got)
	}
	if persistent.gets != 0 {
		t.Fatalf("expected 0 persistent Gets when staging has the key, got %d", persistent.gets)
	}
}

func TestInternalNodeType(t *testing.T) {
	store := newInMemoryStore()
	tree := NewTree(store)

	root := ""
	var err error
	for i := 0; i < maxLeafSize+10; i++ {
		root, err = tree.Insert(root, "", fmt.Sprintf("key-%04d", i), fmt.Sprintf("val-%04d", i))
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
