package storelayer

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
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
		return nil, store.ErrNotFound
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
		return 0, store.ErrNotFound
	}
	return int64(len(d)), nil
}

func (s *countingStore) TotalSize(_ context.Context) (int64, error) { return 0, nil }

func (s *countingStore) Flush(_ context.Context) error { return nil }

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

func TestKeyCacheStore_DigestKeyExistsAfterPreload(t *testing.T) {
	ctx := context.Background()
	inner := newCountingStore()
	digestKey := "chunk/" + strings.Repeat("ab", 32)
	_ = inner.Put(ctx, digestKey, []byte("data"))
	inner.exists.Store(0)

	kc := NewKeyCacheStore(inner)
	if err := kc.PreloadKeys(ctx, "chunk/"); err != nil {
		t.Fatal(err)
	}

	ok, _ := kc.Exists(ctx, digestKey)
	if !ok {
		t.Error("digest key should exist after preload")
	}
	if inner.exists.Load() != 0 {
		t.Error("Exists should not have called backend for known digest key")
	}

	// A different digest under the same prefix must not collide.
	otherKey := "chunk/" + strings.Repeat("cd", 32)
	ok, _ = kc.Exists(ctx, otherKey)
	if ok {
		t.Error("distinct digest key should not exist")
	}
}

// TestKeyCacheStore_CaseVariantDigestKeyDoesNotAlias guards against decoding
// both hex cases into one digest: that would let a Put for one spelling be
// elided because the other, byte-distinct, key was already known.
func TestKeyCacheStore_CaseVariantDigestKeyDoesNotAlias(t *testing.T) {
	ctx := context.Background()
	inner := newCountingStore()
	lower := "chunk/" + strings.Repeat("ab", 32)
	upper := "chunk/" + strings.Repeat("AB", 32)
	_ = inner.Put(ctx, lower, []byte("data"))
	inner.exists.Store(0)

	kc := NewKeyCacheStore(inner)
	if err := kc.PreloadKeys(ctx, "chunk/"); err != nil {
		t.Fatal(err)
	}

	// The uppercase spelling is a different object key and must not read as
	// known just because the lowercase one was preloaded.
	ok, _ := kc.Exists(ctx, upper)
	if ok {
		t.Error("uppercase-hex key must not alias the preloaded lowercase key")
	}

	// Put for the uppercase key must go through, not be elided.
	inner.puts.Store(0)
	if err := kc.Put(ctx, upper, []byte("other data")); err != nil {
		t.Fatal(err)
	}
	if inner.puts.Load() != 1 {
		t.Errorf("Put for distinct uppercase-hex key should not be elided, got %d puts", inner.puts.Load())
	}

	// Both keys are now independently known via the fallback (non-canonical)
	// and digest paths respectively.
	inner.exists.Store(0)
	if ok, _ := kc.Exists(ctx, lower); !ok {
		t.Error("lowercase key should still exist")
	}
	if ok, _ := kc.Exists(ctx, upper); !ok {
		t.Error("uppercase key should exist after being put")
	}
	if inner.exists.Load() != 0 {
		t.Error("both keys should resolve from cache")
	}
}

// TestKeyCacheStore_OverlappingPrefixesResolveLongestMatch guards
// contentAddressedPrefix's tie-break: a key matching two listed prefixes must
// consistently resolve to the longer, more specific one everywhere, not
// whichever the map happens to iterate first.
func TestKeyCacheStore_OverlappingPrefixesResolveLongestMatch(t *testing.T) {
	ctx := context.Background()
	inner := newCountingStore()
	kc := NewKeyCacheStore(inner)
	if err := kc.PreloadKeys(ctx, "chunk/", "chunk/a"); err != nil {
		t.Fatal(err)
	}

	key := "chunk/a" + strings.Repeat("b", digestHexLen)
	for range 20 {
		prefix, ok := kc.contentAddressedPrefix(key)
		if !ok || prefix != "chunk/a" {
			t.Fatalf("contentAddressedPrefix(%q) = (%q, %v), want (\"chunk/a\", true)", key, prefix, ok)
		}
	}
}

func TestKeyCacheStore_NonHexKeyUnderListedPrefixFallsBack(t *testing.T) {
	// A key under a listed prefix whose suffix does not decode to a digest
	// must not be mis-filed into the digest map for a different key. Cover
	// both ways decodeDigest can say no: wrong length, and right length with
	// a character that isn't a hex nibble.
	cases := map[string]string{
		"wrong-length": "chunk/not-a-valid-digest",
		"bad-nibble":   "chunk/" + strings.Repeat("a", digestHexLen-1) + "g",
	}
	for name, nonHexKey := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			inner := newCountingStore()
			_ = inner.Put(ctx, nonHexKey, []byte("data"))
			inner.exists.Store(0)

			kc := NewKeyCacheStore(inner)
			if err := kc.PreloadKeys(ctx, "chunk/"); err != nil {
				t.Fatal(err)
			}

			ok, _ := kc.Exists(ctx, nonHexKey)
			if !ok {
				t.Error("non-hex key should exist after preload via fallback")
			}
			if inner.exists.Load() != 0 {
				t.Error("Exists should not have called backend for known non-hex key")
			}

			// Put-elision and Delete must also work through the fallback path.
			inner.puts.Store(0)
			if err := kc.Put(ctx, nonHexKey, []byte("data")); err != nil {
				t.Fatal(err)
			}
			if inner.puts.Load() != 0 {
				t.Error("Put should have been elided for already-known non-hex key")
			}

			if err := kc.Delete(ctx, nonHexKey); err != nil {
				t.Fatal(err)
			}
			inner.exists.Store(0)
			ok, _ = kc.Exists(ctx, nonHexKey)
			if ok {
				t.Error("non-hex key should not exist after delete")
			}
			if inner.exists.Load() != 0 {
				t.Error("should resolve from cache (listed prefix, absent key)")
			}
		})
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
