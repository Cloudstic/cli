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

// configObjectKey is the repository marker. It is always read and written
// through the raw, undecorated store (LoadRepoConfig, UpgradeRepoFormat) — the
// version gate it carries has to be readable before any key is resolved — so
// this layer never sees it Put through it in practice. It reaches Get here
// only via Client.Cat, which lets a caller fetch any key, "config" included,
// through the full decorated store for inspection.
const configObjectKey = "config"

// ErrPlaintextObject is returned when an object is not a ciphertext in an
// encrypted repository, outside what is plaintext by design.
var ErrPlaintextObject = errors.New("store: unencrypted object in an encrypted repository")

// EncryptedStore wraps an ObjectStore and transparently encrypts data on Put
// and decrypts on Get using AES-256-GCM.
//
// Objects under "keys/" are exempt from encryption entirely — they hold the
// wrapped master key needed to derive the encryption key, so Put never
// encrypts them and Get never expects ciphertext there. "config", the
// repository marker read before any key is resolved, is a narrower exemption:
// Get returns it as-is when it is not a ciphertext (which is how it is always
// actually written, directly through the raw store), but Put still encrypts
// it like any other key, and Get still decrypts it if it ever does arrive as
// one.
//
// A non-ciphertext object anywhere else is refused rather than returned. The
// passthrough that used to apply unconditionally here — legacy plaintext data
// returned as-is, documented as a gradual-migration affordance — is what let
// anyone with write access to the backing store have a client *holding the
// correct key* read attacker-written plaintext as repository content, with no
// key and no tampering with config or key slots required.
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
	if crypto.IsEncrypted(data) {
		return crypto.Decrypt(data, s.key)
	}
	// Not a ciphertext. "config" is plaintext by design — the repository
	// marker read before any key is resolved — so it passes through as-is;
	// anything else unauthenticated is refused.
	if key == configObjectKey {
		return data, nil
	}
	return nil, fmt.Errorf(
		"%w: %s was not written by this repository's encryption key. "+
			"Refusing to use it as repository content; the backing store may have been written to by someone else",
		ErrPlaintextObject, key,
	)
}
