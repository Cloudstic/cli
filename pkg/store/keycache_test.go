package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

type countingStore struct {
	mu     sync.RWMutex
	data   map[string][]byte
	puts   atomic.Int64
	exists atomic.Int64
}

func newCountingStore() *countingStore {
	return &countingStore{data: make(map[string][]byte)}
}

func (s *countingStore) Put(_ context.Context, key string, data []byte) error {
	s.puts.Add(1)
	s.mu.Lock()
	s.data[key] = data
	s.mu.Unlock()
	return nil
}

func (s *countingStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	d, ok := s.data[key]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	return d, nil
}

func (s *countingStore) Exists(_ context.Context, key string) (bool, error) {
	s.exists.Add(1)
	s.mu.RLock()
	_, ok := s.data[key]
	s.mu.RUnlock()
	return ok, nil
}

func (s *countingStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.data, key)
	s.mu.Unlock()
	return nil
}

func (s *countingStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var keys []string
	for k := range s.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (s *countingStore) Size(_ context.Context, key string) (int64, error) {
	s.mu.RLock()
	d, ok := s.data[key]
	s.mu.RUnlock()
	if !ok {
		return 0, ErrNotFound
	}
	return int64(len(d)), nil
}

func (s *countingStore) TotalSize(_ context.Context) (int64, error) { return 0, nil }

func (s *countingStore) Flush(_ context.Context) error { return nil }

var ErrNotFound = &notFoundError{}

type notFoundError struct{}

func (e *notFoundError) Error() string { return "not found" }

func TestKeyCacheStore_ExistsAfterPreload(t *testing.T) {
	ctx := context.Background()
	inner := newCountingStore()
	_ = inner.Put(ctx, "chunk/aaa", []byte("data"))
	_ = inner.Put(ctx, "content/bbb", []byte("data"))
	inner.exists.Store(0)

	kc := NewKeyCacheStore(inner)
	if err := kc.PreloadKeys(ctx, "chunk/", "content/"); err != nil {
		t.Fatal(err)
	}

	// Known key → true, no backend call
	ok, _ := kc.Exists(ctx, "chunk/aaa")
	if !ok {
		t.Error("chunk/aaa should exist")
	}
	if inner.exists.Load() != 0 {
		t.Error("Exists should not have called backend for known key")
	}

	// Unknown key under listed prefix → false, no backend call
	ok, _ = kc.Exists(ctx, "chunk/missing")
	if ok {
		t.Error("chunk/missing should not exist")
	}
	if inner.exists.Load() != 0 {
		t.Error("Exists should not have called backend for missing key under listed prefix")
	}

	// Unknown key under unlisted prefix → falls through to backend
	ok, _ = kc.Exists(ctx, "index/latest")
	if ok {
		t.Error("index/latest should not exist")
	}
	if inner.exists.Load() != 1 {
		t.Errorf("Exists should have called backend for unlisted prefix, got %d calls", inner.exists.Load())
	}
}

func TestKeyCacheStore_PutDeduplicates(t *testing.T) {
	ctx := context.Background()
	inner := newCountingStore()
	kc := NewKeyCacheStore(inner)
	_ = kc.PreloadKeys(ctx, "chunk/")

	// First put goes through
	_ = kc.Put(ctx, "chunk/abc", []byte("data"))
	if inner.puts.Load() != 1 {
		t.Fatalf("expected 1 put, got %d", inner.puts.Load())
	}

	// Second put for same key is skipped (already in knownKeys)
	_ = kc.Put(ctx, "chunk/abc", []byte("data"))
	if inner.puts.Load() != 1 {
		t.Errorf("expected 1 put (deduped), got %d", inner.puts.Load())
	}
}

func TestKeyCacheStore_PutConcurrentDedup(t *testing.T) {
	ctx := context.Background()
	inner := newCountingStore()
	kc := NewKeyCacheStore(inner)
	_ = kc.PreloadKeys(ctx, "chunk/")

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = kc.Put(ctx, "chunk/same", []byte("data"))
		}()
	}
	wg.Wait()

	if n := inner.puts.Load(); n != 1 {
		t.Errorf("expected exactly 1 backend put for concurrent puts, got %d", n)
	}
}

func TestKeyCacheStore_MutableKeyAlwaysWritten(t *testing.T) {
	ctx := context.Background()
	inner := newCountingStore()
	kc := NewKeyCacheStore(inner)
	_ = kc.PreloadKeys(ctx, "chunk/", "content/")

	// Mutable keys (not under listed prefix) should always write through
	_ = kc.Put(ctx, "index/latest", []byte("v1"))
	_ = kc.Put(ctx, "index/latest", []byte("v2"))
	if inner.puts.Load() != 2 {
		t.Errorf("expected 2 puts for mutable key, got %d", inner.puts.Load())
	}

	got, _ := inner.Get(ctx, "index/latest")
	if string(got) != "v2" {
		t.Errorf("expected v2, got %s", got)
	}
}

func TestKeyCacheStore_PutMakesExistsTrue(t *testing.T) {
	ctx := context.Background()
	inner := newCountingStore()
	kc := NewKeyCacheStore(inner)
	_ = kc.PreloadKeys(ctx, "chunk/")

	ok, _ := kc.Exists(ctx, "chunk/new")
	if ok {
		t.Error("should not exist before put")
	}

	_ = kc.Put(ctx, "chunk/new", []byte("data"))

	inner.exists.Store(0)
	ok, _ = kc.Exists(ctx, "chunk/new")
	if !ok {
		t.Error("should exist after put")
	}
	if inner.exists.Load() != 0 {
		t.Error("should resolve from cache after put")
	}
}

func TestKeyCacheStore_DeleteInvalidatesCache(t *testing.T) {
	ctx := context.Background()
	inner := newCountingStore()
	_ = inner.Put(ctx, "chunk/x", []byte("data"))

	kc := NewKeyCacheStore(inner)
	_ = kc.PreloadKeys(ctx, "chunk/")

	ok, _ := kc.Exists(ctx, "chunk/x")
	if !ok {
		t.Fatal("should exist")
	}

	_ = kc.Delete(ctx, "chunk/x")

	// After delete, key is removed from knownKeys; since prefix is listed
	// and key is absent from knownKeys, Exists returns false without backend call
	inner.exists.Store(0)
	ok, _ = kc.Exists(ctx, "chunk/x")
	if ok {
		t.Error("should not exist after delete")
	}
	if inner.exists.Load() != 0 {
		t.Error("should resolve from cache (listed prefix, absent key)")
	}
}
