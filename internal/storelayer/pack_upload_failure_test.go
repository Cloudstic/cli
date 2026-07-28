package storelayer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
)

// packRejectingStore fails writes of packfiles while letting everything else
// through, standing in for a backend that rejects the one upload that matters:
// a bucket that has hit its quota, a connection that drops on the large object,
// a credential that expired mid-run.
type packRejectingStore struct {
	store.ObjectStore
	reject   bool
	rejected int
}

var errPackUploadRejected = errors.New("AccessDenied: upload rejected")

func (p *packRejectingStore) Put(ctx context.Context, key string, data []byte) error {
	if p.reject && strings.HasPrefix(key, packPrefix) {
		p.rejected++
		return errPackUploadRejected
	}
	return p.ObjectStore.Put(ctx, key, data)
}

// A pack's entries enter the catalog before the pack is uploaded — that is what
// lets the upload happen off the lock. When the upload then fails, the entries
// must not survive: the catalog would assert the objects are stored, Exists
// would agree, the next backup would deduplicate against them instead of
// uploading, and the snapshot it wrote would name objects nothing can fetch.
//
// The test asserts the property that matters to a user, not the mechanism: after
// a failed flush, the store must not claim to hold an object it does not hold.
func TestPackStore_FailedPackUploadLeavesNoPhantomEntries(t *testing.T) {
	ctx := context.Background()
	_, inner := newCatalogTestStore(t)
	rejecting := &packRejectingStore{ObjectStore: inner, reject: true}
	ps := newPackStoreT(t, rejecting)

	const key = "chunk/aaaa"
	if err := ps.Put(ctx, key, []byte("data that never reaches the backend")); err != nil {
		t.Fatalf("put: %v", err)
	}

	if err := ps.Flush(ctx); err == nil {
		t.Fatal("flush reported success although the pack upload was rejected")
	}
	if rejecting.rejected == 0 {
		t.Fatal("no pack upload was attempted; the test proves nothing")
	}

	// The claim that must not survive: this object is stored.
	exists, err := ps.Exists(ctx, key)
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if exists {
		t.Error("store reports holding an object whose pack upload failed; " +
			"a backup would deduplicate against it and write an unrestorable snapshot")
	}

	// And it must not survive into the durable index either. A fresh store over
	// the same backend is what the next process sees.
	fresh := newPackStoreT(t, inner)
	if exists, err := fresh.Exists(ctx, key); err != nil {
		t.Fatalf("fresh exists: %v", err)
	} else if exists {
		t.Error("a new process reads the failed object as present; the phantom entry was made durable")
	}
}

// Retrying after the backend recovers must actually store the data, rather than
// short-circuiting on a catalog entry left behind by the failed attempt.
func TestPackStore_RetryAfterFailedPackUploadStoresData(t *testing.T) {
	ctx := context.Background()
	_, inner := newCatalogTestStore(t)
	rejecting := &packRejectingStore{ObjectStore: inner, reject: true}
	ps := newPackStoreT(t, rejecting)

	const key = "filemeta/bbbb"
	payload := []byte("the bytes the user expects to get back")

	if err := ps.Put(ctx, key, payload); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := ps.Flush(ctx); err == nil {
		t.Fatal("flush reported success although the pack upload was rejected")
	}

	// Backend recovers; the caller retries the write it knows failed.
	rejecting.reject = false
	if err := ps.Put(ctx, key, payload); err != nil {
		t.Fatalf("retry put: %v", err)
	}
	if err := ps.Flush(ctx); err != nil {
		t.Fatalf("retry flush: %v", err)
	}

	fresh := newPackStoreT(t, inner)
	got, err := fresh.Get(ctx, key)
	if err != nil {
		t.Fatalf("read back after retry: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("read back %q, want %q", got, payload)
	}
}
