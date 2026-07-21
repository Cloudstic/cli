package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/crypto"
)

func testIndexKey(t *testing.T, seed byte) []byte {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

// buildRealChain mirrors the store chain assembled in client.go:
// Compressed(Encrypted(Metered(Pack(base)))).
func buildRealChain(t *testing.T, dir string, sealed bool) (ObjectStore, *PackStore) {
	t.Helper()

	base, err := NewLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	masterKey := testIndexKey(t, 1)
	var opts []PackOption
	if sealed {
		indexKey, err := crypto.DeriveKey(masterKey, crypto.HKDFInfoPackIndexV1)
		if err != nil {
			t.Fatal(err)
		}
		opts = append(opts, WithPackIndexKey(indexKey))
	}

	pack, err := NewPackStore(base, opts...)
	if err != nil {
		t.Fatal(err)
	}
	top := NewCompressedStore(NewEncryptedStore(NewMeteredStore(pack), masterKey))
	return top, pack
}

// The exposure reported in #295: index/packs named every packed object and its
// exact size, in plaintext, inside an encrypted repository.
func TestPackStore_SealedCatalogIsNotPlaintextOnDisk(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	top, pack := buildRealChain(t, tmp, true)
	if err := top.Put(ctx, "filemeta/deadbeefcafe", []byte(`{"name":"secret-invoice.pdf"}`)); err != nil {
		t.Fatal(err)
	}
	if err := pack.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(tmp, "index", "packs"))
	if err != nil {
		t.Fatalf("read raw catalog: %v", err)
	}
	if bytes.Contains(raw, []byte("filemeta/deadbeefcafe")) {
		t.Error("catalog still exposes object keys in plaintext")
	}
	if !crypto.IsEncrypted(raw) {
		t.Errorf("catalog should be sealed, first bytes: %x", raw[:min(8, len(raw))])
	}
}

// The footer carries the same information as the catalog, so it must be sealed
// on the same terms.
func TestPackStore_SealedFooterIsNotPlaintextOnDisk(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	top, pack := buildRealChain(t, tmp, true)
	if err := top.Put(ctx, "filemeta/deadbeefcafe", []byte(`{"name":"secret-invoice.pdf"}`)); err != nil {
		t.Fatal(err)
	}
	if err := pack.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	packs, err := filepath.Glob(filepath.Join(tmp, "packs", "*"))
	if err != nil || len(packs) != 1 {
		t.Fatalf("expected 1 pack, got %v (err %v)", packs, err)
	}
	raw, err := os.ReadFile(packs[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("filemeta/deadbeefcafe")) {
		t.Error("packfile footer still exposes object keys in plaintext")
	}
	if !bytes.HasSuffix(raw, []byte(packMagic)) {
		t.Error("packfile should still end with the trailer magic")
	}
}

// Sealing must not break reads, self-healing, or the recovery path.
func TestPackStore_SealedRoundTripAndSelfHeal(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	indexKey := testIndexKey(t, 7)

	base, err := NewLocalStore(tmp)
	if err != nil {
		t.Fatal(err)
	}

	writer, err := NewPackStore(base, WithPackIndexKey(indexKey))
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"filemeta/a", "node/b", "snapshot/c"}
	for _, key := range keys {
		if err := writer.Put(ctx, key, []byte("payload for "+key)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	reader, err := NewPackStore(base, WithPackIndexKey(indexKey))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		data, err := reader.Get(ctx, key)
		if err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
		if string(data) != "payload for "+key {
			t.Errorf("%s = %q", key, data)
		}
	}

	// Self-healing must still work when the footers are sealed.
	if err := base.Delete(ctx, indexPacksKey); err != nil {
		t.Fatal(err)
	}
	healed, err := NewPackStore(base, WithPackIndexKey(indexKey))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if _, err := healed.Get(ctx, key); err != nil {
			t.Errorf("self-heal failed for %s: %v", key, err)
		}
	}
}

// A wrong key must fail loudly. Silently yielding an empty catalog is the
// failure mode that made #287 destructive.
func TestPackStore_WrongIndexKeyFailsLoudly(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	base, err := NewLocalStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewPackStore(base, WithPackIndexKey(testIndexKey(t, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Put(ctx, "filemeta/a", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	wrong, err := NewPackStore(base, WithPackIndexKey(testIndexKey(t, 99)))
	if err != nil {
		t.Fatal(err)
	}
	keys, listErr := wrong.List(ctx, "filemeta/")
	if listErr == nil {
		t.Fatalf("List succeeded with %v; want a decryption failure", keys)
	}

	// And with no key at all.
	none, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := none.List(ctx, "filemeta/"); err == nil {
		t.Fatal("List succeeded without a key; want an error")
	} else if !strings.Contains(err.Error(), "no index key") {
		t.Errorf("error should explain the missing key, got: %v", err)
	}
}

// Migration: a repository written before sealing stays readable, and its
// catalog is sealed on the next flush.
func TestPackStore_MigratesPlaintextIndexOnNextFlush(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	indexKey := testIndexKey(t, 3)

	base, err := NewLocalStore(tmp)
	if err != nil {
		t.Fatal(err)
	}

	// Written by a client with no sealing.
	legacy, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Put(ctx, "filemeta/old", []byte("legacy payload")); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	rawBefore, err := base.Get(ctx, indexPacksKey)
	if err != nil {
		t.Fatal(err)
	}
	if crypto.IsEncrypted(rawBefore) {
		t.Fatal("precondition: legacy catalog should be plaintext")
	}

	// A sealing client reads it, adds to it, and flushes.
	upgraded, err := NewPackStore(base, WithPackIndexKey(indexKey))
	if err != nil {
		t.Fatal(err)
	}
	data, err := upgraded.Get(ctx, "filemeta/old")
	if err != nil {
		t.Fatalf("legacy object unreadable after upgrade: %v", err)
	}
	if string(data) != "legacy payload" {
		t.Errorf("got %q", data)
	}
	if err := upgraded.Put(ctx, "filemeta/new", []byte("new payload")); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	rawAfter, err := base.Get(ctx, indexPacksKey)
	if err != nil {
		t.Fatal(err)
	}
	if !crypto.IsEncrypted(rawAfter) {
		t.Error("catalog should be sealed after a flush by a keyed store")
	}

	// Both the legacy and the new object survive the migration. The legacy
	// pack's footer is still plaintext, which is expected and harmless.
	final, err := NewPackStore(base, WithPackIndexKey(indexKey))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"filemeta/old": "legacy payload",
		"filemeta/new": "new payload",
	} {
		got, err := final.Get(ctx, key)
		if err != nil {
			t.Errorf("get %s: %v", key, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// A repository with no encryption keeps plaintext indexes and must keep working.
func TestPackStore_UnencryptedRepositoryStaysPlaintext(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	base, err := NewLocalStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.Put(ctx, "filemeta/a", []byte("plain")); err != nil {
		t.Fatal(err)
	}
	if err := ps.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	raw, err := base.Get(ctx, indexPacksKey)
	if err != nil {
		t.Fatal(err)
	}
	if crypto.IsEncrypted(raw) {
		t.Error("an unencrypted repository should not seal its catalog")
	}

	fresh, err := NewPackStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Get(ctx, "filemeta/a"); err != nil {
		t.Errorf("read back: %v", err)
	}
}

// A sealed footer is not byte-reproducible, by design. Note this does not cost
// reproducible *pack names* in an encrypted repository: those were already
// nondeterministic, because every object reaching PackStore carries its own
// random nonce. See TestPackStore_UnsealedPackNamesStayReproducible.
func TestPackFooter_SealedFooterIsNotDeterministic(t *testing.T) {
	entries := map[string]PackEntry{"filemeta/a": {Offset: 0, Length: 10}}
	key := testIndexKey(t, 5)

	first, err := buildPackFooter(entries, key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildPackFooter(entries, key)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Error("sealed footers repeated a nonce")
	}

	// Both still decode to the same entries.
	for _, footer := range [][]byte{first, second} {
		pack := append(make([]byte, 10), footer...)
		info, err := decodePackFooter("packs/x", pack, key)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got := info.entries["filemeta/a"]; got.Offset != 0 || got.Length != 10 {
			t.Errorf("entry = %+v", got)
		}
	}
}

// Pins the reproducibility facts, so the claim in the sealing PR cannot drift
// again: an encrypted repository never had reproducible pack names, and an
// unencrypted one still does.
func TestPackStore_UnsealedPackNamesStayReproducible(t *testing.T) {
	ctx := context.Background()

	writeOnce := func(t *testing.T, encrypted bool) string {
		t.Helper()
		base, err := NewLocalStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		pack, err := NewPackStore(base)
		if err != nil {
			t.Fatal(err)
		}
		var target ObjectStore = pack
		if encrypted {
			target = NewEncryptedStore(NewMeteredStore(pack), testIndexKey(t, 1))
		}
		if err := target.Put(ctx, "filemeta/a", []byte("identical payload")); err != nil {
			t.Fatal(err)
		}
		if err := pack.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		packs, err := base.List(ctx, packPrefix)
		if err != nil || len(packs) != 1 {
			t.Fatalf("want 1 pack, got %v (err %v)", packs, err)
		}
		return packs[0]
	}

	t.Run("unencrypted repositories stay reproducible", func(t *testing.T) {
		if a, b := writeOnce(t, false), writeOnce(t, false); a != b {
			t.Errorf("pack names diverged without encryption: %s vs %s", a, b)
		}
	})

	t.Run("encrypted repositories were never reproducible", func(t *testing.T) {
		if a, b := writeOnce(t, true), writeOnce(t, true); a == b {
			t.Errorf("expected per-object nonces to make pack names differ, both were %s", a)
		}
	})
}
