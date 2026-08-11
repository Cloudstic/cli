package storelayer

import (
	"context"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/cloudstic/cli/internal/objkey"
	"github.com/cloudstic/cli/pkg/store"
)

// KeyCacheStore wraps an store.ObjectStore and caches key existence from List calls,
// so that Exists returns immediately for known keys. Thread-safe.
//
// Preloaded keys are almost always content-addressed: "<prefix><64-hex-char
// sha256>". Those are decoded once and kept as a raw 32-byte digest in a map
// scoped to their prefix, so the set carries no pointers and the garbage
// collector does not have to scan it — a [32]byte map has no interior
// pointers to trace, unlike a map keyed by the hex string. A key under a
// listed prefix whose suffix does not decode to a digest (unexpected, but not
// impossible) falls back to knownKeys, keyed by the full string, so it is
// tracked correctly rather than silently dropped.
type KeyCacheStore struct {
	inner          store.ObjectStore
	knownDigests   map[string]map[[32]byte]struct{} // listed prefix -> digests known under it
	knownKeys      map[string]struct{}              // fallback for keys that aren't a bare digest
	listedPrefixes map[string]struct{}
	mu             sync.RWMutex
	putFlight      singleflight.Group
}

func (s *KeyCacheStore) Unwrap() store.ObjectStore { return s.inner }

func NewKeyCacheStore(inner store.ObjectStore) *KeyCacheStore {
	return &KeyCacheStore{
		inner:          inner,
		knownDigests:   make(map[string]map[[32]byte]struct{}),
		knownKeys:      make(map[string]struct{}),
		listedPrefixes: make(map[string]struct{}),
	}
}

func (s *KeyCacheStore) PreloadKeys(ctx context.Context, prefixes ...string) error {
	var g errgroup.Group
	for _, p := range prefixes {
		prefix := p
		g.Go(func() error {
			keys, err := s.inner.List(ctx, prefix)
			if err != nil {
				return err
			}
			s.mu.Lock()
			for _, key := range keys {
				s.learnLocked(prefix, key)
			}
			s.listedPrefixes[prefix] = struct{}{}
			s.mu.Unlock()
			return nil
		})
	}
	return g.Wait()
}

func (s *KeyCacheStore) Exists(ctx context.Context, key string) (bool, error) {
	prefix, matched := s.contentAddressedPrefix(key)
	if !matched {
		return s.inner.Exists(ctx, key)
	}
	return s.knows(prefix, key), nil
}

func (s *KeyCacheStore) Put(ctx context.Context, key string, data []byte) error {
	prefix, contentAddressed := s.contentAddressedPrefix(key)
	if !contentAddressed {
		return s.inner.Put(ctx, key, data)
	}

	if s.knows(prefix, key) {
		return nil
	}

	_, err, _ := s.putFlight.Do(key, func() (any, error) {
		if s.knows(prefix, key) {
			return nil, nil
		}

		if err := s.inner.Put(ctx, key, data); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.learnLocked(prefix, key)
		s.mu.Unlock()
		return nil, nil
	})
	return err
}

// contentAddressedPrefix returns the listed prefix key falls under, which
// means key is immutable (same key = same data) and eligible for existence
// caching and write elision. Mutable keys like index/* were never listed and
// must always be written through.
//
// listedPrefixes is a map, so iteration order is undefined; today's callers
// only ever list "chunk/", "content/" and "node/", none a prefix of another,
// so this can't actually pick two different answers for one key. It resolves
// the longest match anyway rather than leaving that an unstated assumption:
// a key matching two listed prefixes must consistently resolve to the same
// one across Exists, Put and Delete, or a write can be elided for a key the
// backend was never told about.
func (s *KeyCacheStore) contentAddressedPrefix(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var longest string
	var matched bool
	for prefix := range s.listedPrefixes {
		if strings.HasPrefix(key, prefix) && (!matched || len(prefix) > len(longest)) {
			longest, matched = prefix, true
		}
	}
	return longest, matched
}

// knows reports whether key, known to fall under prefix, has been recorded
// as existing.
func (s *KeyCacheStore) knows(prefix, key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if digest, ok := decodeDigest(prefix, key); ok {
		_, found := s.knownDigests[prefix][digest]
		return found
	}
	_, found := s.knownKeys[key]
	return found
}

// learnLocked records key, known to fall under prefix, as existing. Callers
// must hold s.mu for writing.
func (s *KeyCacheStore) learnLocked(prefix, key string) {
	digest, ok := decodeDigest(prefix, key)
	if !ok {
		s.knownKeys[key] = struct{}{}
		return
	}
	set := s.knownDigests[prefix]
	if set == nil {
		set = make(map[[32]byte]struct{})
		s.knownDigests[prefix] = set
	}
	set[digest] = struct{}{}
}

// forgetLocked removes key, known to fall under prefix, from the cache.
// Callers must hold s.mu for writing.
func (s *KeyCacheStore) forgetLocked(prefix, key string) {
	if digest, ok := decodeDigest(prefix, key); ok {
		delete(s.knownDigests[prefix], digest)
		return
	}
	delete(s.knownKeys, key)
}

// decodeDigest decodes the suffix of key after prefix as a raw sha256 digest.
// It returns ok=false for any key that isn't exactly "<prefix><64 lowercase
// hex chars>", so callers fall back to string-keyed storage instead of
// mis-filing a key that doesn't have that shape.
//
// Only the canonical lowercase spelling decodes, which objkey.DecodeDigest
// documents at length. The consequence here is specific: a decoder that also
// accepted uppercase would fold two byte-distinct object keys ("aa..." and
// "AA...") into one digest and let a write for one be elided because the other
// was seen — the exact "permissive direction" failure the prefix matching above
// is written to avoid.
func decodeDigest(prefix, key string) ([32]byte, bool) {
	return objkey.DecodeDigest(key[len(prefix):])
}

func (s *KeyCacheStore) Get(ctx context.Context, key string) ([]byte, error) {
	return s.inner.Get(ctx, key)
}

func (s *KeyCacheStore) Delete(ctx context.Context, key string) error {
	if prefix, ok := s.contentAddressedPrefix(key); ok {
		s.mu.Lock()
		s.forgetLocked(prefix, key)
		s.mu.Unlock()
	}
	return s.inner.Delete(ctx, key)
}

func (s *KeyCacheStore) List(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.List(ctx, prefix)
}

func (s *KeyCacheStore) Size(ctx context.Context, key string) (int64, error) {
	return s.inner.Size(ctx, key)
}

func (s *KeyCacheStore) TotalSize(ctx context.Context) (int64, error) {
	return s.inner.TotalSize(ctx)
}

func (s *KeyCacheStore) Flush(ctx context.Context) error {
	if f, ok := s.inner.(interface{ Flush(context.Context) error }); ok {
		return f.Flush(ctx)
	}
	return nil
}
