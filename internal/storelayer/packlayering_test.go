package storelayer

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store/local"
)

// Sealing the pack index must not encrypt anything a second time. Object bytes
// are already ciphertext when PackStore receives them, and the index is sealed
// exactly once. These tests assert that layering directly rather than inferring
// it from the store chain's shape.

// Object bytes inside a packfile must be exactly what EncryptedStore produced —
// PackStore concatenates, it does not re-encrypt.
func TestPackStore_DoesNotReEncryptObjectBytes(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	base, err := local.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	masterKey := testIndexKey(t, 1)
	indexKey, err := crypto.DeriveKey(masterKey, crypto.HKDFInfoPackIndexV1)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := NewPackStore(base, WithPackIndexKey(indexKey))
	if err != nil {
		t.Fatal(err)
	}
	encrypted := NewEncryptedStore(NewMeteredStore(pack), masterKey)

	payload := []byte("the quick brown fox")
	if err := encrypted.Put(ctx, "filemeta/a", payload); err != nil {
		t.Fatal(err)
	}
	if err := pack.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	pack.mu.RLock()
	entry := pack.catalog["filemeta/a"]
	pack.mu.RUnlock()

	raw, err := base.Get(ctx, entry.PackRef)
	if err != nil {
		t.Fatal(err)
	}
	objectBytes := raw[entry.Offset : entry.Offset+entry.Length]

	// Exactly one layer of encryption: one Decrypt yields the plaintext.
	if !crypto.IsEncrypted(objectBytes) {
		t.Fatal("object bytes in the pack are not encrypted at all")
	}
	opened, err := crypto.Decrypt(objectBytes, masterKey)
	if err != nil {
		t.Fatalf("object bytes did not decrypt with the master key: %v", err)
	}
	if !bytes.Equal(opened, payload) {
		t.Errorf("decrypted once = %q, want %q", opened, payload)
	}
	if crypto.IsEncrypted(opened) {
		t.Error("object bytes appear to be encrypted twice")
	}
}

// The catalog is sealed exactly once: one Decrypt yields JSON, not another
// ciphertext.
func TestPackStore_CatalogIsSealedExactlyOnce(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	base, err := local.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	indexKey := testIndexKey(t, 9)
	pack, err := NewPackStore(base, WithPackIndexKey(indexKey))
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Put(ctx, "filemeta/a", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := pack.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	objects := indexObjects(t, base)
	if len(objects) != 1 {
		t.Fatalf("expected a single index object, got %d", len(objects))
	}
	raw := objects[0]
	opened, err := crypto.Decrypt(raw, indexKey)
	if err != nil {
		t.Fatalf("catalog did not decrypt: %v", err)
	}
	if crypto.IsEncrypted(opened) {
		t.Fatal("catalog appears to be sealed twice")
	}

	var catalog map[string]PackEntry
	if err := json.Unmarshal(opened, &catalog); err != nil {
		t.Fatalf("one decrypt should yield catalog JSON: %v", err)
	}
	if _, ok := catalog["filemeta/a"]; !ok {
		t.Errorf("catalog missing the entry, got %v", catalog)
	}
}

// Likewise the footer.
func TestPackStore_FooterIsSealedExactlyOnce(t *testing.T) {
	indexKey := testIndexKey(t, 11)
	entries := map[string]PackEntry{"filemeta/a": {Offset: 0, Length: 4}}

	footer, err := buildPackFooter(entries, indexKey)
	if err != nil {
		t.Fatal(err)
	}
	payload := footer[:len(footer)-packTrailerLen]

	opened, err := crypto.Decrypt(payload, indexKey)
	if err != nil {
		t.Fatalf("footer payload did not decrypt: %v", err)
	}
	if crypto.IsEncrypted(opened) {
		t.Fatal("footer appears to be sealed twice")
	}
	var decoded packFooter
	if err := json.Unmarshal(opened, &decoded); err != nil {
		t.Fatalf("one decrypt should yield footer JSON: %v", err)
	}
	if len(decoded.Entries) != 1 || decoded.Entries[0].Key != "filemeta/a" {
		t.Errorf("unexpected footer contents: %+v", decoded)
	}
}

// Sealing must add exactly one AEAD overhead per artefact, not one per entry.
func TestPackStore_SealingOverheadIsPerArtefactNotPerEntry(t *testing.T) {
	entries := make(map[string]PackEntry, 500)
	for i := range 500 {
		entries[string(rune('a'+i%26))+string(rune('a'+i/26))+"/x"] = PackEntry{Offset: int64(i), Length: 1}
	}

	plain, err := buildPackFooter(entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := buildPackFooter(entries, testIndexKey(t, 13))
	if err != nil {
		t.Fatal(err)
	}

	growth := len(sealed) - len(plain)
	if growth != crypto.Overhead {
		t.Errorf("sealing %d entries grew the footer by %d bytes, want exactly %d (one AEAD overhead)",
			len(entries), growth, crypto.Overhead)
	}
}
