package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
)

// putV2Entry writes the standalone objects a format-v2 repository holds for one
// small file, and returns the filemeta ref a tree entry would name.
func putV2Entry(t *testing.T, s *MockStore, name string, data []byte) string {
	t.Helper()
	ctx := context.Background()

	hash := core.ComputeHash(data)
	content, err := json.Marshal(core.Content{
		Type:          core.ObjectTypeContent,
		Size:          int64(len(data)),
		DataInlineB64: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "content/"+hash, content); err != nil {
		t.Fatal(err)
	}

	meta := core.FileMeta{
		Version:     1,
		FileID:      name,
		Name:        name,
		Type:        core.FileTypeFile,
		Parents:     []string{"root"},
		ContentHash: hash,
		ContentRef:  hash,
		Size:        int64(len(data)),
	}
	ref, encoded, err := core.FileMetaRef(&meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, ref, encoded); err != nil {
		t.Fatal(err)
	}
	return ref
}

// TestCopyEntry_CachedEntryKeepsItsBodyReference pins what replaced the
// payload-elision rule.
//
// A payload used to be dropped from the remap table when it carried inline
// content, because keeping every inlined file of a run resident is how a copy
// of a tree of small files exhausted memory. A payload is now metadata and a
// body reference, so it is cached whole — and the thing that must hold is that
// a second visit reuses the *same* body, rather than packing it into the
// destination twice.
//
// Getting it wrong is silent in both directions: a cache hit that lost its
// reference inserts an entry with nothing to restore, and one that re-added
// the body would store it twice under two references that both work.
func TestCopyEntry_CachedEntryKeepsItsBodyReference(t *testing.T) {
	ctx := context.Background()
	src, dst := NewMockStore(), NewMockStore()
	data := []byte("small enough to travel in a blob")
	srcRef := putV2Entry(t, src, "small.txt", data)

	cm := NewCopyManager(
		Deps{Store: dst, Reporter: ui.NewNoOpReporter(), FormatV3: true, BlobStore: dst},
		CopySide{Store: src},
		"dest-repo",
	)

	first, err := cm.copyEntry(ctx, srcRef, nil)
	if err != nil {
		t.Fatalf("first copyEntry: %v", err)
	}
	if first.payload == nil {
		t.Fatal("first copy produced no payload")
	}
	if first.promise == nil {
		t.Fatal("first copy produced no body promise: the content would never reach a blob")
	}

	second, err := cm.copyEntry(ctx, srcRef, nil)
	if err != nil {
		t.Fatalf("second copyEntry: %v", err)
	}
	if second.payload == nil {
		t.Fatal("cache hit returned no payload: the entry would be inserted with nothing to restore")
	}
	if second.ref != first.ref {
		t.Fatalf("rebuilt entry names %s, want %s", second.ref, first.ref)
	}

	if err := cm.dstBlobs.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if first.promise.ref == nil {
		t.Fatal("the body was never placed in a blob")
	}
	if second.promise == nil || *second.promise.ref != *first.promise.ref {
		t.Fatalf("the cache hit points at a different body: %+v vs %+v", second.promise, first.promise)
	}

	// One body, stored once: a second copy of the same content must not have
	// produced a second blob.
	blobs := 0
	for key := range dst.Data {
		if strings.HasPrefix(key, "blob/") {
			blobs++
		}
	}
	if blobs != 1 {
		t.Fatalf("destination holds %d blobs for one body, want 1", blobs)
	}
}

// TestCopyEntry_ReadsMetadataFromAV3Payload covers the other side of the seam:
// a v3 source hands its metadata over in the leaf, and there is no filemeta/
// object behind the value to fall back on. A source that claims v3 and delivers
// no payload is a corrupt tree, and must be refused rather than copied as an
// entry with no name, type or timestamps.
func TestCopyEntry_ReadsMetadataFromAV3Payload(t *testing.T) {
	ctx := context.Background()
	src, dst := NewMockStore(), NewMockStore()

	meta := core.FileMeta{
		Version: 1,
		FileID:  "dir",
		Name:    "dir",
		Type:    core.FileTypeFolder,
		Parents: []string{"root"},
	}
	_, encoded, err := core.FileMetaRef(&meta)
	if err != nil {
		t.Fatal(err)
	}

	cm := NewCopyManager(
		Deps{Store: dst, Reporter: ui.NewNoOpReporter(), FormatV3: true, BlobStore: dst},
		CopySide{Store: src, FormatV3: true, BlobStore: src},
		"dest-repo",
	)

	// Nothing was written to the source store: the payload is the only place
	// this entry's metadata exists, which is the point of the format.
	copied, err := cm.copyEntry(ctx, "filemeta/absent", &hamt.Payload{Meta: encoded})
	if err != nil {
		t.Fatalf("copyEntry from payload: %v", err)
	}
	if copied.payload == nil || !bytes.Equal(copied.payload.Meta, encoded) {
		t.Fatalf("destination payload does not carry the source metadata: %+v", copied.payload)
	}
	if copied.affinityKey != AffinityKey("root", "dir") {
		t.Fatalf("affinity key %s, want %s", copied.affinityKey, AffinityKey("root", "dir"))
	}

	// A different ref, so this misses the remap table and takes the read path.
	if _, err := cm.copyEntry(ctx, "filemeta/nowhere", nil); err == nil {
		t.Fatal("a v3 source entry with no payload should be refused, not read as an object")
	}
}
