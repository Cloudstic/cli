package store

import (
	"context"
	"strings"

	"github.com/cloudstic/cli/pkg/crypto"
)

// KeySlotPrefix is the object key prefix for encryption key slot objects.
// These objects are stored unencrypted (they contain already-wrapped keys)
// so they can be read without the encryption key — avoiding a chicken-and-egg
// problem during key loading.
const KeySlotPrefix = "keys/"

// EncryptedStore wraps an ObjectStore and transparently encrypts data on Put
// and decrypts on Get using AES-256-GCM. Unencrypted (legacy) data is
// returned as-is on Get, enabling gradual migration.
//
// Objects under the "keys/" prefix are passed through unencrypted because
// they hold the wrapped master key needed to derive the encryption key.
type EncryptedStore struct {
	ObjectStore
	key []byte
}

// NewEncryptedStore creates an EncryptedStore that encrypts all Put operations
// and decrypts Get operations. The key must be 32 bytes (AES-256).
func NewEncryptedStore(inner ObjectStore, key []byte) *EncryptedStore {
	return &EncryptedStore{ObjectStore: inner, key: key}
}

func (s *EncryptedStore) Put(ctx context.Context, key string, data []byte) error {
	if strings.HasPrefix(key, KeySlotPrefix) {
		return s.ObjectStore.Put(ctx, key, data)
	}
	ct, err := crypto.Encrypt(data, s.key)
	if err != nil {
		return err
	}
	return s.ObjectStore.Put(ctx, key, ct)
}

func (s *EncryptedStore) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.ObjectStore.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(key, KeySlotPrefix) {
		return data, nil
	}
	if !crypto.IsEncrypted(data) {
		return data, nil
	}
	return crypto.Decrypt(data, s.key)
}
