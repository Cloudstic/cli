package storelayer

import (
	"encoding/json"
	"testing"
)

// sealShardFor renders a shard without building a map of the entries first. The
// bytes must match what marshalling the equivalent map would produce, because
// the shard's key is the hash of those bytes.
func TestSealShardFor_MatchesMarshallingAnEquivalentMap(t *testing.T) {
	catalog := map[string]PackEntry{
		"filemeta/b": {PackRef: "packs/one", Offset: 5, Length: 7},
		"filemeta/a": {PackRef: "packs/one", Offset: 0, Length: 5},
		"node/z":     {PackRef: "packs/two", Offset: 3, Length: 9},
		// Present in the catalog but not pending: must not appear in the shard.
		"content/x": {PackRef: "packs/three", Offset: 1, Length: 1},
	}
	pending := map[string]struct{}{
		"filemeta/a": {}, "filemeta/b": {}, "node/z": {},
	}

	s, err := NewPackStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	gotRef, gotBytes, err := s.sealShardFor(catalog, pending)
	if err != nil {
		t.Fatal(err)
	}

	subset := map[string]PackEntry{
		"filemeta/a": catalog["filemeta/a"],
		"filemeta/b": catalog["filemeta/b"],
		"node/z":     catalog["node/z"],
	}
	wantRef, wantBytes, err := s.sealShard(subset)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBytes) != string(wantBytes) {
		t.Fatalf("bytes differ:\n got %s\nwant %s", gotBytes, wantBytes)
	}
	if gotRef != wantRef {
		t.Fatalf("ref = %s, want %s", gotRef, wantRef)
	}

	var decoded map[string]PackEntry
	if err := json.Unmarshal(gotBytes, &decoded); err != nil {
		t.Fatalf("shard does not decode: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("shard has %d entries, want 3: %v", len(decoded), decoded)
	}
	if _, ok := decoded["content/x"]; ok {
		t.Error("a catalog entry that was not pending leaked into the shard")
	}
}

// A pending key whose catalog entry has since been removed names nothing, and
// must not produce a shard entry pointing at a zero location.
func TestSealShardFor_SkipsKeysMissingFromTheCatalog(t *testing.T) {
	s, err := NewPackStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog := map[string]PackEntry{"filemeta/a": {PackRef: "packs/one", Length: 2}}
	pending := map[string]struct{}{"filemeta/a": {}, "filemeta/gone": {}}

	_, data, err := s.sealShardFor(catalog, pending)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]PackEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["filemeta/gone"]; ok {
		t.Error("a pending key with no catalog entry produced a shard entry")
	}
	if len(decoded) != 1 {
		t.Fatalf("shard has %d entries, want 1", len(decoded))
	}
}

// Nothing pending is not an error and names no object.
func TestSealShardFor_EmptyPendingSetWritesNothing(t *testing.T) {
	s, err := NewPackStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	ref, data, err := s.sealShardFor(map[string]PackEntry{"a": {}}, nil)
	if err != nil || ref != "" || data != nil {
		t.Fatalf("got (%q, %v, %v), want (\"\", nil, nil)", ref, data, err)
	}
}
