package engine

import (
	"context"
	"sync"

	"github.com/cloudstic/cli/internal/hamt"
)

// Whole-file deduplication in format v3 (issue #514).
//
// The packfile format deduplicates a whole file by probing content/<ref>
// before reading it: a file whose bytes the repository already holds costs no
// read and no write. Format v3 has no content object to probe — a small file's
// body is a member of a blob/ object, addressed by where it sits rather than
// by what it is — so every file change detection called new or changed was
// packed again regardless of what was already stored. Touching 300 files whose
// bytes did not change grew a repository by 3,304 KB against 216 KB for the
// packfile format, and the same mechanism is 83% of what renaming a large
// directory costs (issue #543 §4).
//
// What is reused here is a *placement*, not an object. The new entry points at
// the blob member that already holds exactly those bytes, so nothing is
// stored, no object is added, and a restore issues no request it would not
// have issued anyway — the blob is one the previous snapshot already names,
// and in the rename case it holds precisely the sibling bodies the entry moved
// with. That is what separates this from the per-file chunk promotion RFC 0026
// measured and rejected, which added an object per duplicated file and a
// restore request per file referencing it.
//
// # Why this is not the index RFC 0026 exists to remove
//
// The forbidden structure is a repository-wide content-hash index: an object
// in the store, written by every backup and read by every backup, whose size
// is the repository's file count. This is none of those things. It is written
// nowhere, read nowhere, holds only what one backup's own change-detection
// sweep walked past — a sweep that already reads every leaf and already
// decodes every previous filemeta — and is released when the upload phase
// ends. A miss costs today's behaviour exactly; there is no correctness that
// depends on the index being complete, which is what lets it be capped.
//
// # What is reused, and when it is safe
//
// A placement is only ever taken from one of two places:
//
//   - The previous snapshot's tree, read during change detection. Its blobs
//     exist, because a retained snapshot references them; the new snapshot
//     references them too the moment the reusing entry is inserted, so prune
//     marks them (prune deliberately never deduplicates on the entry's value
//     for exactly this reason — see markSnapshot).
//   - This run's own blob writer. Those blobs are written before the tree is
//     committed, and the entry waits on the same promise the first writer of
//     those bytes waited on.
//
// The member's seal is keyed by (repository secret, content hash, containing
// blob's ref), all three of which the reusing entry carries: the hash in its
// metadata, the blob and extent in its BodyRef. A reused member therefore
// opens exactly as the original does.
//
// Consolidation must not go through this index. It moves a body *out* of a
// blob it is retiring, and the index would hand it back the placement it is
// trying to retire; it calls blobWriter.Add directly, and the index is
// released before it runs.

// bodyIndexBytes bounds what the index holds.
//
// Same shape and same reason as consolidateTrackBytes: a v3 backup may hold a
// working set, never something proportional to the repository. Reaching the
// cap costs deduplication and nothing else — the index stops recording, serves
// what it already has, and a body with no placement to reuse is packed exactly
// as it is today. It never degrades to an error, and it never evicts: an
// entry already handed out may be the one an in-flight promise resolves
// through.
//
// 32 MB is roughly 130,000 placements at the sizes approxSize counts, which
// covers a source tree of that many distinct file bodies. Past it the coverage
// is whichever entries the scan reached first, which is walk order and so
// contiguous rather than arbitrary.
const bodyIndexBytes = 32 << 20

// envBodyIndex overrides bodyIndexBytes, for tests only. It joins the
// CLOUDSTIC_TEST_* family described in AGENTS.md.
const envBodyIndex = "CLOUDSTIC_TEST_BODY_INDEX_BYTES"

func bodyIndexLimit() int64 { return envInt64(envBodyIndex, bodyIndexBytes) }

// bodyIndex maps a content hash to a body placement this backup can point an
// entry at instead of packing those bytes again.
//
// It is consulted from the upload workers, which run concurrently, and
// populated from the scan, which does not — hence one mutex covering both.
// The lock is held across blobWriter.Add so that two workers offering the same
// body cannot both pack it; the writer takes its own mutex there anyway, so
// this adds no serialization that was not already present.
type bodyIndex struct {
	mu     sync.Mutex
	placed map[string]placement
	held   int64
	limit  int64

	hits     int64
	hitBytes int64
}

// placement is one reusable home for a body: where it is (or will be), and how
// many plaintext bytes live there.
//
// The size is carried because a reader needs it — blob.ReadMember is given the
// entry's Size as the member's plaintext length — and because it is the only
// thing a caller reusing a placement *before* reading the file has no way to
// establish for itself. Requiring it to match is what keeps that fast path
// from writing an entry whose recorded size disagrees with the bytes it points
// at, which would restore wrongly and be re-detected as changed on every run.
type placement struct {
	promise *bodyPromise
	size    int64
}

func newBodyIndex() *bodyIndex {
	return &bodyIndex{placed: make(map[string]placement), limit: bodyIndexLimit()}
}

// approxPlacementSize is what one entry costs the index, for the byte cap: the
// hash and the blob ref it retains, plus a fixed allowance for the map slot,
// the promise and the BodyRef. Like the other budgets here it is a target
// rather than a contract.
func approxPlacementSize(contentHash, blobRef string) int64 {
	return int64(len(contentHash)+len(blobRef)) + 128
}

// inherit records a placement a previous backup made, as read out of the
// previous snapshot's tree during change detection. size is the entry's
// content size, which for an entry with a body is that body's plaintext
// length.
//
// body is the tree's own *hamt.BodyRef rather than a copy, the way
// blobEntry.body is: a BodyRef is separately allocated and immutable once
// attached, so holding it retains nothing but itself.
//
// A reference this backup could not reason about is not recorded. Offset,
// Length and Total come off a store, and the rest of the engine — span
// coalescing, consolidation's denominator — assumes they are usable; an entry
// carrying one is left exactly where it is rather than propagated to a second
// entry.
func (x *bodyIndex) inherit(contentHash string, body *hamt.BodyRef, size int64) {
	if x == nil || contentHash == "" || body == nil || size <= 0 {
		return
	}
	if body.Blob == "" || body.Total <= 0 || body.Length <= 0 || body.Offset < 0 {
		return
	}

	x.mu.Lock()
	defer x.mu.Unlock()
	if _, ok := x.placed[contentHash]; ok {
		return
	}
	held := approxPlacementSize(contentHash, body.Blob)
	if x.held+held > x.limit {
		return
	}
	p := &bodyPromise{contentHash: contentHash, inherited: true}
	p.ref.Store(body)
	x.placed[contentHash] = placement{promise: p, size: size}
	x.held += held
}

// lookup returns a placement holding exactly size bytes hashing to
// contentHash, or nil.
//
// It is the half of the fast path that needs no body: a source that reports a
// content hash of its own (Google Drive, OneDrive) lets a duplicate be settled
// without opening the file at all, which is what the packfile format's
// content/ probe does. The size has to agree because on that path it is the
// source's claim rather than something this run measured, and it is what a
// reader will size the member by.
func (x *bodyIndex) lookup(contentHash string, size int64) *bodyPromise {
	if x == nil || contentHash == "" {
		return nil
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	p, ok := x.placed[contentHash]
	if !ok || p.size != size {
		return nil
	}
	x.hits++
	x.hitBytes += size
	return p.promise
}

// place gives one body a home: an existing placement when the repository
// already holds those bytes, and otherwise the blob writer, whose promise is
// recorded so that a later duplicate in the same run reuses it too.
//
// The writer deduplicates within the blob it is currently packing; this is
// what makes the same body free across blobs, across the run's insert
// batches, and across backups.
func (x *bodyIndex) place(ctx context.Context, w *blobWriter, contentHash string, body []byte) (*bodyPromise, error) {
	if x == nil {
		return w.Add(ctx, contentHash, body)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	existing, known := x.placed[contentHash]
	if known && existing.size == int64(len(body)) {
		x.hits++
		x.hitBytes += int64(len(body))
		return existing.promise, nil
	}
	p, err := w.Add(ctx, contentHash, body)
	if err != nil {
		return nil, err
	}
	// Recorded only while there is room, and never over a hash already
	// recorded — the promise is handed back either way, so a full index costs
	// the next duplicate and not this entry, and the byte accounting stays
	// one entry per key.
	held := approxPlacementSize(contentHash, "")
	if !known && x.held+held <= x.limit {
		x.placed[contentHash] = placement{promise: p, size: int64(len(body))}
		x.held += held
	}
	return p, nil
}

// stats reports what the index saved, for the debug log.
func (x *bodyIndex) stats() (hits, bytes, entries int64) {
	if x == nil {
		return 0, 0, 0
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.hits, x.hitBytes, int64(len(x.placed))
}
