package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cloudstic/cli/pkg/crypto"
)

type memStore struct {
	data map[string][]byte
}

func newMemStore() *memStore {
	return &memStore{data: make(map[string][]byte)}
}

func (m *memStore) Put(_ context.Context, key string, data []byte) error {
	m.data[key] = append([]byte(nil), data...)
	return nil
}

func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	d, ok := m.data[key]
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return d, nil
}

func (m *memStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := m.data[key]
	return ok, nil
}

func (m *memStore) Delete(_ context.Context, key string) error {
	delete(m.data, key)
	return nil
}

func (m *memStore) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.data {
		if len(prefix) == 0 || k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memStore) Size(_ context.Context, key string) (int64, error) {
	d, ok := m.data[key]
	if !ok {
		return 0, fmt.Errorf("not found: %s", key)
	}
	return int64(len(d)), nil
}

func (m *memStore) TotalSize(_ context.Context) (int64, error) {
	var total int64
	for _, d := range m.data {
		total += int64(len(d))
	}
	return total, nil
}

func (m *memStore) Flush(_ context.Context) error {
	return nil
}

func testKey(t *testing.T) []byte {
	t.Helper()
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestEncryptedStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	inner := newMemStore()
	key := testKey(t)
	store := NewEncryptedStore(inner, key)

	plain := []byte(`{"root":"abc123","created":"2026-01-01"}`)
	if err := store.Put(ctx, "snapshot/abc", plain); err != nil {
		t.Fatal(err)
	}

	raw := inner.data["snapshot/abc"]
	if bytes.Equal(raw, plain) {
		t.Fatal("data in inner store should be encrypted, not plaintext")
	}
	if !crypto.IsEncrypted(raw) {
		t.Fatal("data in inner store should have encryption header")
	}

	got, err := store.Get(ctx, "snapshot/abc")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q, want %q", got, plain)
	}
}

// A gzip-framed legacy object looks nothing like a ciphertext (no version
// byte), so it is refused the same as any other plaintext content.
func TestEncryptedStore_RefusesLegacyGzip(t *testing.T) {
	ctx := context.Background()
	inner := newMemStore()
	key := testKey(t)

	gzipData := []byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00}
	inner.data["chunk/abc"] = gzipData

	store := NewEncryptedStore(inner, key)
	if _, err := store.Get(ctx, "chunk/abc"); !errors.Is(err, ErrPlaintextObject) {
		t.Fatalf("error = %v, want ErrPlaintextObject", err)
	}
}

// Anyone who can write to the backing store can put plaintext under a
// content-addressed key. A client holding the correct encryption key must not
// read it back as repository content: that is a forged object, accepted without
// the key and without touching config or the key slots.
func TestEncryptedStore_RefusesPlaintextContent(t *testing.T) {
	ctx := context.Background()

	for _, key := range []string{
		"chunk/forged",
		"content/forged",
		"filemeta/forged",
		"node/forged",
		"snapshot/forged",
	} {
		t.Run(key, func(t *testing.T) {
			inner := newMemStore()
			forged := []byte(`{"root":"node/attacker"}`)
			inner.data[key] = forged

			store := NewEncryptedStore(inner, testKey(t))
			got, err := store.Get(ctx, key)
			if err == nil {
				t.Fatalf("Get(%q) returned %q, want an error", key, got)
			}
			if !errors.Is(err, ErrPlaintextObject) {
				t.Fatalf("Get(%q) error = %v, want ErrPlaintextObject", key, err)
			}
			if got != nil {
				t.Errorf("Get(%q) returned %d bytes alongside the error", key, len(got))
			}
		})
	}
}

// Refusal covers content, not the whole keyspace. Key slots are deliberately
// plaintext, "config" is the plaintext repository marker, and the pack index
// under "index/" is written below this layer — refusing those would break
// reading a repository rather than protect it.
func TestEncryptedStore_RefusalIsScopedToContent(t *testing.T) {
	ctx := context.Background()
	inner := newMemStore()

	plain := map[string][]byte{
		"keys/platform-default": []byte(`{"slot_type":"platform"}`),
		"config":                []byte(`{"version":2,"encrypted":true}`),
		"index/packs":           []byte(`{"entries":{}}`),
	}
	for key, data := range plain {
		inner.data[key] = data
	}

	store := NewEncryptedStore(inner, testKey(t))
	for key, want := range plain {
		got, err := store.Get(ctx, key)
		if err != nil {
			t.Errorf("Get(%q): %v", key, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("Get(%q) = %q, want %q", key, got, want)
		}
	}
}

// A truncated ciphertext is shorter than the AEAD overhead, so it fails the
// header check the same way plaintext does. It must not slip through as data.
func TestEncryptedStore_RefusesTruncatedCiphertext(t *testing.T) {
	ctx := context.Background()
	inner := newMemStore()
	key := testKey(t)
	store := NewEncryptedStore(inner, key)

	if err := store.Put(ctx, "chunk/abc", []byte("real content")); err != nil {
		t.Fatal(err)
	}
	inner.data["chunk/abc"] = inner.data["chunk/abc"][:crypto.Overhead-1]

	if _, err := store.Get(ctx, "chunk/abc"); !errors.Is(err, ErrPlaintextObject) {
		t.Fatalf("error = %v, want ErrPlaintextObject", err)
	}
}

func TestEncryptedStore_WrongKey(t *testing.T) {
	ctx := context.Background()
	inner := newMemStore()
	key1 := testKey(t)
	key2 := testKey(t)

	store1 := NewEncryptedStore(inner, key1)
	if err := store1.Put(ctx, "data/x", []byte("secret")); err != nil {
		t.Fatal(err)
	}

	store2 := NewEncryptedStore(inner, key2)
	if _, err := store2.Get(ctx, "data/x"); err == nil {
		t.Fatal("expected error reading with wrong key")
	}
}

func TestEncryptedStore_PassthroughOps(t *testing.T) {
	ctx := context.Background()
	inner := newMemStore()
	key := testKey(t)
	store := NewEncryptedStore(inner, key)

	_ = store.Put(ctx, "a/1", []byte("one"))
	_ = store.Put(ctx, "a/2", []byte("two"))
	_ = store.Put(ctx, "b/1", []byte("three"))

	exists, _ := store.Exists(ctx, "a/1")
	if !exists {
		t.Fatal("Exists should return true")
	}
	exists, _ = store.Exists(ctx, "c/1")
	if exists {
		t.Fatal("Exists should return false for missing key")
	}

	keys, _ := store.List(ctx, "a/")
	if len(keys) != 2 {
		t.Fatalf("List(a/) = %d keys, want 2", len(keys))
	}

	_ = store.Delete(ctx, "a/1")
	exists, _ = store.Exists(ctx, "a/1")
	if exists {
		t.Fatal("key should be deleted")
	}
}

func TestEncryptedStore_EmptyValue(t *testing.T) {
	ctx := context.Background()
	inner := newMemStore()
	key := testKey(t)
	store := NewEncryptedStore(inner, key)

	if err := store.Put(ctx, "empty", []byte{}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty value, got %d bytes", len(got))
	}
}

func TestEncryptedStore_KeySlotPassthrough(t *testing.T) {
	ctx := context.Background()
	inner := newMemStore()
	key := testKey(t)
	store := NewEncryptedStore(inner, key)

	slotData := []byte(`{"slot_type":"platform","wrapped_key":"base64data"}`)
	if err := store.Put(ctx, "keys/platform-default", slotData); err != nil {
		t.Fatal(err)
	}

	raw := inner.data["keys/platform-default"]
	if !bytes.Equal(raw, slotData) {
		t.Fatal("keys/ objects should be stored as plaintext, not encrypted")
	}

	got, err := store.Get(ctx, "keys/platform-default")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, slotData) {
		t.Fatalf("got %q, want %q", got, slotData)
	}
}

func TestEncryptedStore_LargeChunk(t *testing.T) {
	ctx := context.Background()
	inner := newMemStore()
	key := testKey(t)
	store := NewEncryptedStore(inner, key)

	plain := make([]byte, 1024*1024)
	for i := range plain {
		plain[i] = byte(i % 251)
	}

	if err := store.Put(ctx, "chunk/big", plain); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "chunk/big")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("large chunk round-trip failed")
	}
}
