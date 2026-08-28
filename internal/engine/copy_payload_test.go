package engine

import (
	"bytes"
	"context"
	"encoding/json"
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

// TestCopyEntry_RebuildsAnElidedInlinePayload pins the one rule that makes the
// remap table safe to shrink.
//
// An entry whose content is inline is cached without its payload, because
// keeping every inlined file of a run resident is how a copy of a tree of small
// files exhausts memory. The cost of that is that a second visit must rebuild
// rather than reuse — and getting it wrong is silent: a v3 entry inserted with
// no payload commits, hashes, and passes check, but its metadata and its bytes
// are simply not there to restore.
func TestCopyEntry_RebuildsAnElidedInlinePayload(t *testing.T) {
	ctx := context.Background()
	src, dst := NewMockStore(), NewMockStore()
	data := []byte("small enough to be stored inline")
	srcRef := putV2Entry(t, src, "small.txt", data)

	cm := NewCopyManager(
		Deps{Store: dst, Reporter: ui.NewNoOpReporter(), FormatV3: true},
		CopySide{Store: src},
		"dest-repo",
	)

	first, err := cm.copyEntry(ctx, srcRef, nil)
	if err != nil {
		t.Fatalf("first copyEntry: %v", err)
	}
	if first.payload == nil || !bytes.Equal(first.payload.Inline, data) {
		t.Fatalf("first copy produced payload %+v, want one carrying the inline content", first.payload)
	}
	if !first.payloadElided {
		t.Fatal("an inline payload should be marked elided, so a cache hit rebuilds it")
	}

	second, err := cm.copyEntry(ctx, srcRef, nil)
	if err != nil {
		t.Fatalf("second copyEntry: %v", err)
	}
	if second.payload == nil {
		t.Fatal("cache hit returned no payload: the entry would be inserted with nothing to restore")
	}
	if !bytes.Equal(second.payload.Inline, data) {
		t.Fatalf("rebuilt payload carries %q, want %q", second.payload.Inline, data)
	}
	if second.ref != first.ref {
		t.Fatalf("rebuilt entry names %s, want %s", second.ref, first.ref)
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
		Deps{Store: dst, Reporter: ui.NewNoOpReporter(), FormatV3: true},
		CopySide{Store: src, FormatV3: true},
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
