package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudstic/cli/pkg/crypto"
)

// KeySlotPrefix is the object key prefix for encryption key slot objects.
// These objects are stored unencrypted (they contain already-wrapped keys)
// so they can be read without the encryption key — avoiding a chicken-and-egg
// problem during key loading.
const KeySlotPrefix = "keys/"

// ErrPlaintextObject is returned when an object under a content-addressed
// prefix in an encrypted repository is not a ciphertext.
var ErrPlaintextObject = errors.New("store: unencrypted object in an encrypted repository")

// contentAddressedPrefixes are the namespaces whose objects carry repository
// *content*: the file data and the metadata tree reachable from a snapshot.
// Everything a restore reconstructs comes from these five.
//
// They are the prefixes the ciphertext-only rule covers. The rest of the
// keyspace is deliberately outside it: "keys/" holds wrapped keys that must be
// readable before any key exists, "config" is the plaintext repository marker,
// and "index/" holds catalogs written below the encryption layer (the pack
// index) or rebuildable pointers. None of them is content, and refusing
// plaintext there would break reading a repository rather than protecting it.
var contentAddressedPrefixes = []string{
	"chunk/",
	"content/",
	"filemeta/",
	"node/",
	"snapshot/",
}

// isContentAddressed reports whether key names repository content, and is
// therefore covered by the ciphertext-only rule.
func isContentAddressed(key string) bool {
	for _, prefix := range contentAddressedPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// EncryptedStore wraps an ObjectStore and transparently encrypts data on Put
// and decrypts on Get using AES-256-GCM.
//
// Objects under the "keys/" prefix are passed through unencrypted because they
// hold the wrapped master key needed to derive the encryption key.
//
// An object under a content-addressed prefix that is not a ciphertext is
// refused rather than returned. The passthrough that used to apply here —
// legacy plaintext data returned as-is, documented as a gradual-migration
// affordance — is what let anyone with write access to the backing store have
// a client *holding the correct key* read attacker-written plaintext as
// repository content, with no key and no tampering with config or key slots
// required.
type EncryptedStore struct {
	ObjectStore
	key []byte
}

func (s *EncryptedStore) Unwrap() ObjectStore { return s.ObjectStore }

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
		if !isContentAddressed(key) {
			return data, nil
		}
		return nil, fmt.Errorf(
			"%w: %s was not written by this repository's encryption key. "+
				"Refusing to use it as repository content; the backing store may have been written to by someone else",
			ErrPlaintextObject, key,
		)
	}
	return crypto.Decrypt(data, s.key)
}
