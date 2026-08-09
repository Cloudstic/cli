package store

import (
	"context"
	"fmt"
)

// ObjectStore is the interface for content-addressable object storage.
// Keys are slash-separated paths like "chunk/<hash>" or "snapshot/<hash>".
type ObjectStore interface {
	// Put must not retain data beyond the call: read or copy it synchronously
	// before returning. Callers (chunking in particular) pool and reuse their
	// write buffers the instant Put returns, so an implementation that keeps
	// the slice — instead of the bytes — would see it mutated out from under
	// a previously "stored" value.
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Exists(ctx context.Context, key string) (bool, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
	Size(ctx context.Context, key string) (int64, error)
	TotalSize(ctx context.Context) (int64, error)
	Flush(ctx context.Context) error
}

// ConcurrencyHinter is an optional interface that ObjectStore implementations
// can implement to indicate the optimal number of concurrent operations.
// Remote stores (S3) benefit from high concurrency; local stores do not.
type ConcurrencyHinter interface {
	ConcurrencyHint() int
}

// Unwrapper is an optional interface for wrapper stores (CompressedStore,
// EncryptedStore, etc.) to expose their inner store for introspection.
type Unwrapper interface {
	Unwrap() ObjectStore
}

// RangeGetter is an optional interface for backends that can read a byte range
// without transferring the whole object. It lets PackStore read a packfile's
// trailing footer without downloading the entire 8 MB pack.
//
// Implementations return exactly length bytes starting at offset, or an error.
// Backends that cannot do this simply do not implement it; callers fall back to
// a full Get, which is correct everywhere and merely slower.
type RangeGetter interface {
	GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error)
}

// LocalityGrouper is an optional interface for stores that know something about
// where objects physically live, and can say which order is cheapest to read a
// set of them in.
//
// A caller that knows every key it is about to read — a restore has its whole
// plan before it fetches anything — can hand them over and get back a cheaper
// ordering. The store that benefits is one bundling many objects per transfer:
// reading a bundle's keys consecutively transfers it once, where reading them
// scattered among other bundles' keys can transfer it repeatedly.
//
// Implementations return a permutation: the same keys, once each, no additions
// and no drops. Keys the store knows nothing about keep their relative order,
// so a partially-known set is still safe to hand over wholesale.
//
// Backends with no such structure simply do not implement it, and callers get
// their own order back — correct everywhere, merely no faster.
type LocalityGrouper interface {
	GroupByLocality(keys []string) []string
}

// DemandDeclarer is an optional interface for stores that can use advance
// knowledge of which keys a caller is about to read.
//
// LocalityGrouper answers "in what order", this answers "how many, and from
// where". A store bundling many objects per transfer has to decide, on first
// contact with a bundle, whether to transfer the whole thing or read a piece of
// it — a decision it can only make by guessing, since it sees one key at a time.
// A caller holding its whole read set already knows the answer.
//
// Declaring is a hint about a caller's own intent, not a lock or a reservation.
// A declared key need not be read, a key not declared may be, and reads from
// other callers proceed unaffected. A store may ignore the declaration entirely,
// which is what a store with nothing to gain from it does by not implementing
// this at all.
//
// The returned function ends the declaration and must be called — deferring it
// is the intended use. Ending it early costs efficiency, never correctness.
type DemandDeclarer interface {
	DeclareDemand(keys []string, scope DemandScope) (release func())
}

// DemandScope says whether a caller may still declare more keys from the same
// bundles, which decides whether running out of declared demand means the
// bundle is finished or merely quiet.
//
// The distinction is not bookkeeping. A reader whose later keys are fields of
// its earlier ones cannot name them up front — a restore learns a file's
// content object only by reading that file's metadata — so its first pass is
// necessarily partial, and a store that treated exhaustion as completion would
// discard bundles the second pass immediately asks for again.
type DemandScope int

const (
	// DemandPartial promises nothing beyond the keys named. More keys from the
	// same bundles may follow, so exhausting this declaration means only that
	// this pass is done with them.
	DemandPartial DemandScope = iota

	// DemandFinal additionally promises that no further keys from these bundles
	// will be declared by this caller. Exhausting it therefore means the bundle
	// is finished and its resources can be released rather than waiting to be
	// evicted.
	//
	// Wrong only costs performance: a bundle released early is fetched again.
	DemandFinal
)

// DeclareDemand walks the store wrapper chain and declares to the first
// DemandDeclarer it finds, returning a no-op release if none exists.
//
// The walk matters for the same reason GroupByLocality's does: PackStore is the
// store that bundles, and it sits beneath CompressedStore, EncryptedStore and
// MeteredStore.
func DeclareDemand(s ObjectStore, keys []string, scope DemandScope) (release func()) {
	for s != nil {
		if d, ok := s.(DemandDeclarer); ok {
			return d.DeclareDemand(keys, scope)
		}
		if u, ok := s.(Unwrapper); ok {
			s = u.Unwrap()
		} else {
			break
		}
	}
	return func() {}
}

// GroupByLocality walks the store wrapper chain and applies the first
// LocalityGrouper it finds, returning keys unchanged if none exists.
//
// The walk matters: PackStore is the store that knows about locality, and it
// sits beneath CompressedStore, EncryptedStore and MeteredStore, so a caller
// holding the outermost wrapper cannot see it directly.
func GroupByLocality(s ObjectStore, keys []string) []string {
	for s != nil {
		if g, ok := s.(LocalityGrouper); ok {
			return g.GroupByLocality(keys)
		}
		if u, ok := s.(Unwrapper); ok {
			s = u.Unwrap()
		} else {
			break
		}
	}
	return keys
}

// GetConcurrencyHint walks the store wrapper chain and returns the first
// ConcurrencyHint it finds, defaulting to defaultConcurrency if none exists.
func GetConcurrencyHint(s ObjectStore, defaultConcurrency int) int {
	for s != nil {
		if h, ok := s.(ConcurrencyHinter); ok {
			return h.ConcurrencyHint()
		}
		if u, ok := s.(Unwrapper); ok {
			s = u.Unwrap()
		} else {
			break
		}
	}
	return defaultConcurrency
}

// HTTPRangeHeader renders an inclusive byte range for backends that speak HTTP
// range requests, for use when implementing RangeGetter. The end offset is
// inclusive, which is the classic off-by-one in this header.
func HTTPRangeHeader(offset, length int64) string {
	return fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
}

// KeySlotPrefix is the object key prefix for encryption key slot objects.
// These objects are stored unencrypted (they contain already-wrapped keys)
// so they can be read without the encryption key — avoiding a chicken-and-egg
// problem during key loading.
//
// It lives in the contract package rather than with the encryption layer
// because it is a repository key-namespace fact that pkg/keychain also needs,
// alongside the chunk/, snapshot/ and index/ conventions.
const KeySlotPrefix = "keys/"
