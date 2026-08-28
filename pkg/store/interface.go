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

// BatchDeleter is an optional interface for backends that can delete many keys
// in fewer requests than one per key.
//
// Delete is the one direction object stores do batch: S3's DeleteObjects takes
// up to 1,000 keys per request (MinIO and B2's S3-compatible endpoint
// included), Azure Blob Batch takes 256, GCS batches at 100, while none of them
// offers a multi-object GET or PUT — which is why aggregation for reads had to
// move into the data layout instead (RFC 0026).
//
// Implementations report per-key failures rather than collapsing them: return
// DeleteErrors naming every key that could not be confirmed deleted, and a
// bare error only when the backend genuinely says nothing per key. A caller
// must never be able to read a partial failure as "all deleted" — prune is a
// garbage collector, and docs/compatibility.md forbids it proceeding on data it
// could not fully act on. FailedDeletes tells the two cases apart.
//
// Deleting a key that is not there is not a failure, matching what
// DeleteObjects reports for a missing key; DeleteEach supplies that behaviour
// for backends whose single-key Delete errors instead.
//
// Backends that cannot batch simply do not implement it, and callers go through
// the DeleteAll helper, which loops. A custom backend therefore keeps working
// unchanged.
type BatchDeleter interface {
	DeleteAll(ctx context.Context, keys []string) error
}

// ReadPlan is a store's answer to a caller that is about to read a set of keys.
//
// It carries what the store knows and the caller does not: where the objects
// physically live, and how much of that layout the store can hold at once.
type ReadPlan struct {
	// Groups partitions the requested keys so that consuming one group at a
	// time keeps each underlying bundle live for a contiguous span. Keys the
	// store knows nothing about form singleton groups and keep their relative
	// order.
	//
	// It is advice, not a contract: the keys are the same either way, so a
	// caller whose order is already good may keep it (see PlanReads).
	Groups [][]string

	// Concurrency is how many groups may be read at once without defeating the
	// grouping. A worker reading a group holds that group's bundle for as long
	// as it takes, so this is a statement about the store's buffer capacity,
	// not about requests in flight — and it is generally much smaller than
	// ConcurrencyHint, which answers the latter.
	//
	// **Always at least 1**, guaranteed by PlanReads regardless of what a store
	// or a ConcurrencyHinter returned. Callers feed this straight to things like
	// errgroup.SetLimit, where zero does not mean "unlimited" or "default" — it
	// means no goroutine may ever run, so the first submission blocks forever.
	// A backend returning a zero hint is a plausible mistake for an interface
	// this package exports to other modules, and it should not be able to hang
	// a restore.
	Concurrency int
}

// ReadPlanner is an optional interface for stores that can use advance
// knowledge of which keys a caller is about to read.
//
// Declaring is the purpose; the returned plan is the advice that comes with it.
// A store that bundles many objects per transfer has to decide, on first
// contact with a bundle, whether to transfer the whole thing or read a piece of
// it — a decision it can only guess at while it sees one key at a time. A
// caller holding its whole read set already knows the answer.
//
// Declaring is a statement about a caller's own intent, not a lock or a
// reservation. A declared key need not be read, a key not declared may be, and
// reads from other callers proceed unaffected.
type ReadPlanner interface {
	PlanReads(ctx context.Context, keys []string) ReadPlan
}

// PlanReads tells the store which keys the caller is about to read and returns
// its advice on how to read them.
//
// **Declaring is the point; taking the advice is optional.** A caller whose
// order is already good may use the plan for its demand alone and keep its own
// sequence — restore's write phase does exactly that, because it writes leaves
// in walk order, which is the order backup laid them into bundles. Reordering
// them by locality was measured and is a regression. Ignoring Groups is
// therefore a supported use, not a misuse.
//
// The walk matters: PackStore is the store that knows about locality, and it
// sits beneath CompressedStore, EncryptedStore and MeteredStore, so a caller
// holding the outermost wrapper cannot see it directly.
//
// The context is taken because an implementation may have to read to answer —
// PackStore needs its catalog. That is not speculative I/O: a caller declares
// because it is about to read these keys, and the same load would happen on the
// first of them. Planning never fails; a store that cannot answer returns
// singleton groups, which claim no locality and serialise nobody.
func PlanReads(ctx context.Context, s ObjectStore, keys []string) ReadPlan {
	outer := s
	for s != nil {
		if p, ok := s.(ReadPlanner); ok {
			return withUsableConcurrency(p.PlanReads(ctx, keys))
		}
		if u, ok := s.(Unwrapper); ok {
			s = u.Unwrap()
		} else {
			break
		}
	}
	groups := make([][]string, len(keys))
	for i, k := range keys {
		groups[i] = []string{k}
	}
	// Singleton groups hold nothing between reads, so the limit is the ordinary
	// requests-in-flight question this store already answers.
	return withUsableConcurrency(ReadPlan{Groups: groups, Concurrency: GetConcurrencyHint(outer, 10)})
}

// withUsableConcurrency enforces ReadPlan.Concurrency >= 1.
//
// It is applied to every plan this function returns, including one a store
// produced itself, because the guarantee is only worth anything if callers can
// rely on it without knowing which store answered. Both sources can produce a
// zero: a ReadPlanner in another module, and ConcurrencyHint, whose value
// GetConcurrencyHint returns verbatim.
func withUsableConcurrency(p ReadPlan) ReadPlan {
	if p.Concurrency < 1 {
		p.Concurrency = 1
	}
	return p
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
