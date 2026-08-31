package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/store"
)

// failingPutStore fails every Put, so a blob that cannot be stored can be
// distinguished from one that was.
type failingPutStore struct {
	store.ObjectStore
	err error
}

func (f *failingPutStore) Put(context.Context, string, []byte) error { return f.err }

// A body must never be reported as placed unless its blob reached the store.
// An entry naming a blob that was never written is a dangling reference, and a
// snapshot carrying one is worse than a failed backup.
func TestBlobWriterDoesNotPlaceABodyItCouldNotStore(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("store is down")
	w := newBlobWriter(&failingPutStore{ObjectStore: NewMockStore(), err: boom}, nil)

	body := []byte("a body whose blob will not be written")
	p, err := w.Add(ctx, core.ComputeHash(body), body)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := w.Flush(ctx); !errors.Is(err, boom) {
		t.Fatalf("Flush hid the store failure: %v", err)
	}
	if placed := p.placed(); placed != nil {
		t.Fatalf("the body was placed at %+v despite its blob never being stored", placed)
	}
}

// A content hash that is not the body's is refused where it enters, not where
// it is read.
func TestBlobWriterRejectsAMismatchedContentHash(t *testing.T) {
	w := newBlobWriter(NewMockStore(), nil)
	if _, err := w.Add(context.Background(), core.ComputeHash([]byte("other")), []byte("body")); err == nil {
		t.Fatal("Add accepted a hash that is not the body's")
	}
}

// A format-v3 manager built with nowhere to put blobs must say so rather than
// dereferencing nil partway through an operation.
func TestBlobWriterAndReaderAreNilWithoutAStore(t *testing.T) {
	if w := newBlobWriter(nil, nil); w != nil {
		t.Error("newBlobWriter returned a writer with no store to write to")
	}
	d := Deps{FormatV3: true}
	if r := d.blobReader(); r != nil {
		t.Error("blobReader returned a reader with no store to read from")
	}

	var r *blobReader
	_, err := r.Body(context.Background(), &hamt.Payload{}, "")
	if err == nil || !strings.Contains(err.Error(), "no blob store") {
		t.Fatalf("a nil blob reader reported %v, want a message naming the missing store", err)
	}
}

// An entry with no body reference is a caller error, not an empty file.
func TestBlobReaderRefusesAnEntryWithNoBodyReference(t *testing.T) {
	r := newBlobReader(NewMockStore(), nil)
	if _, err := r.Body(context.Background(), &hamt.Payload{}, ""); err == nil {
		t.Fatal("Body accepted an entry carrying no body reference")
	}
}

// An unencrypted repository authenticates nothing, so the content hash is the
// only thing standing between a reader and a body someone else substituted.
// A sealed repository catches the same substitution through the AEAD.
func TestBlobReaderChecksTheHashWhenNothingIsSealed(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()
	w := newBlobWriter(dest, nil)

	body := []byte("the body this entry really has")
	p, err := w.Add(ctx, core.ComputeHash(body), body)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	payload := &hamt.Payload{Body: p.placed(), Size: int64(len(body))}
	r := newBlobReader(dest, nil)

	got, err := r.Body(ctx, payload, core.ComputeHash(body))
	if err != nil {
		t.Fatalf("reading a body under its own hash: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("read %q, want %q", got, body)
	}

	if _, err := r.Body(ctx, payload, core.ComputeHash([]byte("a different body"))); err == nil {
		t.Fatal("a body opened under a content hash that is not its own")
	}
}

// A blob with no members is never written: an empty Flush is a no-op, not an
// object.
func TestBlobWriterFlushWithNothingPendingWritesNothing(t *testing.T) {
	dest := NewMockStore()
	if err := newBlobWriter(dest, nil).Flush(context.Background()); err != nil {
		t.Fatalf("Flush with nothing pending: %v", err)
	}
	if n := dest.CountPrefix("blob/"); n != 0 {
		t.Fatalf("an empty flush wrote %d blobs", n)
	}
}
