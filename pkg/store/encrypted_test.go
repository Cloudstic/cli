package store

import (
	"bytes"
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

func (m *memStore) Put(key string, data []byte) error {
	m.data[key] = append([]byte(nil), data...)
	return nil
}

func (m *memStore) Get(key string) ([]byte, error) {
	d, ok := m.data[key]
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return d, nil
}

func (m *memStore) Exists(key string) (bool, error) {
	_, ok := m.data[key]
	return ok, nil
}

func (m *memStore) Delete(key string) error {
	delete(m.data, key)
	return nil
}

func (m *memStore) List(prefix string) ([]string, error) {
	var keys []string
	for k := range m.data {
		if len(prefix) == 0 || k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memStore) Size(key string) (int64, error) {
	d, ok := m.data[key]
	if !ok {
		return 0, fmt.Errorf("not found: %s", key)
	}
	return int64(len(d)), nil
}

func (m *memStore) TotalSize() (int64, error) {
	var total int64
	for _, d := range m.data {
		total += int64(len(d))
	}
	return total, nil
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
	inner := newMemStore()
	key := testKey(t)
	store := NewEncryptedStore(inner, key)

	plain := []byte(`{"root":"abc123","created":"2026-01-01"}`)
	if err := store.Put("snapshot/abc", plain); err != nil {
		t.Fatal(err)
	}

	raw := inner.data["snapshot/abc"]
	if bytes.Equal(raw, plain) {
		t.Fatal("data in inner store should be encrypted, not plaintext")
	}
	if !crypto.IsEncrypted(raw) {
		t.Fatal("data in inner store should have encryption header")
	}

	got, err := store.Get("snapshot/abc")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q, want %q", got, plain)
	}
}

func TestEncryptedStore_LegacyPlaintext(t *testing.T) {
	inner := newMemStore()
	key := testKey(t)

	legacy := []byte(`{"root":"old_unencrypted"}`)
	inner.data["snapshot/old"] = legacy

	store := NewEncryptedStore(inner, key)
	got, err := store.Get("snapshot/old")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, legacy) {
		t.Fatal("legacy plaintext should be returned as-is")
	}
}

func TestEncryptedStore_LegacyGzip(t *testing.T) {
	inner := newMemStore()
	key := testKey(t)

	gzipData := []byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00}
	inner.data["chunk/abc"] = gzipData

	store := NewEncryptedStore(inner, key)
	got, err := store.Get("chunk/abc")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, gzipData) {
		t.Fatal("legacy gzip data should be returned as-is")
	}
}

func TestEncryptedStore_WrongKey(t *testing.T) {
	inner := newMemStore()
	key1 := testKey(t)
	key2 := testKey(t)

	store1 := NewEncryptedStore(inner, key1)
	if err := store1.Put("data/x", []byte("secret")); err != nil {
		t.Fatal(err)
	}

	store2 := NewEncryptedStore(inner, key2)
	if _, err := store2.Get("data/x"); err == nil {
		t.Fatal("expected error reading with wrong key")
	}
}

func TestEncryptedStore_PassthroughOps(t *testing.T) {
	inner := newMemStore()
	key := testKey(t)
	store := NewEncryptedStore(inner, key)

	store.Put("a/1", []byte("one"))
	store.Put("a/2", []byte("two"))
	store.Put("b/1", []byte("three"))

	exists, _ := store.Exists("a/1")
	if !exists {
		t.Fatal("Exists should return true")
	}
	exists, _ = store.Exists("c/1")
	if exists {
		t.Fatal("Exists should return false for missing key")
	}

	keys, _ := store.List("a/")
	if len(keys) != 2 {
		t.Fatalf("List(a/) = %d keys, want 2", len(keys))
	}

	store.Delete("a/1")
	exists, _ = store.Exists("a/1")
	if exists {
		t.Fatal("key should be deleted")
	}
}

func TestEncryptedStore_EmptyValue(t *testing.T) {
	inner := newMemStore()
	key := testKey(t)
	store := NewEncryptedStore(inner, key)

	if err := store.Put("empty", []byte{}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("empty")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty value, got %d bytes", len(got))
	}
}

func TestEncryptedStore_KeySlotPassthrough(t *testing.T) {
	inner := newMemStore()
	key := testKey(t)
	store := NewEncryptedStore(inner, key)

	slotData := []byte(`{"slot_type":"platform","wrapped_key":"base64data"}`)
	if err := store.Put("keys/platform-default", slotData); err != nil {
		t.Fatal(err)
	}

	raw := inner.data["keys/platform-default"]
	if !bytes.Equal(raw, slotData) {
		t.Fatal("keys/ objects should be stored as plaintext, not encrypted")
	}

	got, err := store.Get("keys/platform-default")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, slotData) {
		t.Fatalf("got %q, want %q", got, slotData)
	}
}

func TestEncryptedStore_LargeChunk(t *testing.T) {
	inner := newMemStore()
	key := testKey(t)
	store := NewEncryptedStore(inner, key)

	plain := make([]byte, 1024*1024)
	for i := range plain {
		plain[i] = byte(i % 251)
	}

	if err := store.Put("chunk/big", plain); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("chunk/big")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("large chunk round-trip failed")
	}
}
