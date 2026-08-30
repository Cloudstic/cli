package engine

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudstic/cli/internal/blob"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"
)

// blobBudget is how much body plaintext one blob accumulates before it is
// sealed and written.
//
// Chosen from the bandwidth-delay product rather than by sweeping: a blob
// should be about time-to-first-byte times bandwidth, since below that a
// second request costs more than fetching and discarding the gap between two
// members. That is 4.5-9 MB on a fast link and around 1 MB on a domestic
// uplink (RFC 0026). 8 MB sits at the top of that band, where a restore
// reading a whole blob is cheapest, and the cost of overshooting is only that
// a partially-live blob holds a little more garbage before consolidation.
//
// Measured in plaintext rather than stored bytes so that blob size does not
// vary with how compressible a directory happens to be.
const blobBudget = 8 << 20

// blobWriter packs file bodies into blob/ objects.
//
// Bodies arrive from the upload workers in whatever order they finish, but a
// blob's membership is decided here, on one goroutine, under a mutex: members
// go in in arrival order and a blob seals when it is full. Packing in arrival
// order is what puts bodies read together into one blob, since the upload
// queue is walk-ordered.
//
// A body is not placed until its blob is sealed — the offset depends on every
// member before it — so callers get a promise they resolve after the flush.
type blobWriter struct {
	store  store.ObjectStore
	sealer *crypto.MemberSealer

	mu      sync.Mutex
	pending *blob.Writer
	// promises are the entries waiting on the blob currently being packed,
	// in the order their bodies were added.
	promises []*bodyPromise
}

// bodyPromise is one entry's claim on a body that has been handed to a blob
// but not yet placed in one. Resolved when that blob is sealed.
type bodyPromise struct {
	contentHash string
	ref         *hamt.BodyRef
}

func newBlobWriter(s store.ObjectStore, sealer *crypto.MemberSealer) *blobWriter {
	return &blobWriter{store: s, sealer: sealer}
}

// Add hands one body to the writer and returns the promise that will name
// where it landed. It writes a blob whenever one fills, so a caller's memory is
// bounded by blobBudget rather than by the run.
func (w *blobWriter) Add(ctx context.Context, contentHash string, body []byte) (*bodyPromise, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.pending == nil {
		w.pending = blob.NewWriter(w.sealer)
	}
	if err := w.pending.Add(contentHash, body); err != nil {
		return nil, err
	}
	p := &bodyPromise{contentHash: contentHash}
	w.promises = append(w.promises, p)

	if w.pending.PlaintextBytes() >= blobBudget {
		if err := w.sealLocked(ctx); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// Flush seals and writes whatever is still pending. It must be called before
// the tree is committed: an entry whose promise is unresolved has no body
// reference to encode.
func (w *blobWriter) Flush(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sealLocked(ctx)
}

func (w *blobWriter) sealLocked(ctx context.Context) error {
	if w.pending == nil || w.pending.Len() == 0 {
		w.pending, w.promises = nil, nil
		return nil
	}
	ref, data, members, err := w.pending.Seal()
	if err != nil {
		return fmt.Errorf("seal blob: %w", err)
	}

	// Written before any promise is resolved. An entry naming a blob that was
	// never stored is a dangling reference, and a snapshot carrying one is
	// worse than a failed backup.
	if err := w.store.Put(ctx, ref, data); err != nil {
		return fmt.Errorf("write %s: %w", ref, err)
	}

	placed := make(map[string]blob.Placement, len(members))
	for _, m := range members {
		placed[m.ContentHash] = m
	}
	total := int64(len(data))
	for _, p := range w.promises {
		m, ok := placed[p.contentHash]
		if !ok {
			// Unreachable: every promise came from an Add that the writer
			// accepted. Checked because the alternative is a nil BodyRef
			// reaching the encoder as "this entry has no body".
			return fmt.Errorf("blob %s does not contain %s, which was added to it", ref, p.contentHash)
		}
		p.ref = &hamt.BodyRef{Blob: ref, Offset: m.Offset, Length: m.Length, Total: total}
	}

	w.pending, w.promises = nil, nil
	return nil
}
