package storelayer

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"

	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/local"
)

// buildFullChain assembles the exact decorator chain Client.NewClient builds
// for an encrypted, packfile-enabled repository:
// Compressed(Encrypted(Metered(Pack(Local)))). pack is returned alongside so
// callers can Flush it, since PackStore only lands packed objects on the
// backend at flush time.
func buildFullChain(t *testing.T) (chain store.ObjectStore, pack *PackStore) {
	t.Helper()
	base, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	masterKey := testIndexKey(t, 7)
	indexKey, err := crypto.DeriveKey(masterKey, crypto.HKDFInfoPackIndexV1)
	if err != nil {
		t.Fatal(err)
	}
	pack, err = NewPackStore(base, WithPackIndexKey(indexKey))
	if err != nil {
		t.Fatal(err)
	}
	encrypted := NewEncryptedStore(NewMeteredStore(pack), masterKey)
	compressed := NewCompressedStore(encrypted, WithFramedWrites(true))
	return compressed, pack
}

// TestFullStoreChain_RoundTripsAdversarialPayloads exercises the actual chain
// Client.NewClient assembles rather than each decorator in isolation. Every
// layer here has its own tests, but nothing previously round-tripped a
// payload through all of them together — and a bug in how two layers
// interact (compression framing vs. encryption vs. pack bundling) would not
// show up in either layer's tests alone. Framed writes are the mode any
// current repository uses, so this is the regression guard for the
// compression-sniffing corruption fix (see compressed.go's frame doc
// comment): a valid gzip/zstd stream that zstd cannot shrink further, stored
// verbatim, must come back byte-for-byte rather than being sniffed and
// wrongly inflated on read.
func TestFullStoreChain_RoundTripsAdversarialPayloads(t *testing.T) {
	ctx := context.Background()

	incompressible := make([]byte, 8192)
	if _, err := rand.Read(incompressible); err != nil {
		t.Fatal(err)
	}

	payloads := map[string][]byte{
		"already-gzip":       gzipBytes(t, []byte("a payload that happens to look like gzip once compressed")),
		"already-zstd":       zstdBytes(t, []byte("a payload that happens to look like zstd once compressed")),
		"frame-magic-prefix": append([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a}, incompressible...),
		"empty":              {},
		"incompressible":     incompressible,
	}

	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			chain, pack := buildFullChain(t)
			key := "content/" + name

			if err := chain.Put(ctx, key, payload); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if err := pack.Flush(ctx); err != nil {
				t.Fatalf("Flush: %v", err)
			}

			got, err := chain.Get(ctx, key)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("round trip through the full chain mismatched: got %d bytes, want %d bytes", len(got), len(payload))
			}

			if ok, err := chain.Exists(ctx, key); err != nil || !ok {
				t.Errorf("Exists = %v, %v, want true, nil", ok, err)
			}
		})
	}
}
