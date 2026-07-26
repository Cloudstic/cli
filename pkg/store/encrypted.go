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
// prefix is not a ciphertext in a repository that guarantees it is.
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
// Objects under the "keys/" prefix are passed through unencrypted because
// they hold the wrapped master key needed to derive the encryption key.
//
// An object that is not a ciphertext is refused rather than returned. The
// passthrough that used to apply — legacy plaintext data returned as-is,
// enabling gradual migration — is what let anyone with write access to the
// backing store have a client *holding the correct key* read attacker-written
// plaintext as repository content, with no key and no tampering with config or
// key slots required. It survives only for repositories recorded below
// core.CiphertextOnlyFormat, which may genuinely contain plaintext objects,
// and is selected with WithLegacyPlaintext.
type EncryptedStore struct {
	ObjectStore
	key []byte
	// legacyPlaintext keeps the pre-format-3 fallback: a non-ciphertext object
	// under a content-addressed prefix is returned as-is instead of refused.
	legacyPlaintext bool
}

func (s *EncryptedStore) Unwrap() ObjectStore { return s.ObjectStore }

// EncryptedOption configures an EncryptedStore.
type EncryptedOption func(*EncryptedStore)

// WithLegacyPlaintext allows Get to return a non-ciphertext object under a
// content-addressed prefix as-is, which is how repositories below
// core.CiphertextOnlyFormat must be read: one converted from an unencrypted
// repository holds real plaintext objects, and backward compatibility is
// permanent.
//
// It is a policy value derived from the repository format, passed in rather
// than probed, so a store cannot decide on its own to accept unauthenticated
// bytes. The default — allow=false — refuses them.
func WithLegacyPlaintext(allow bool) EncryptedOption {
	return func(s *EncryptedStore) { s.legacyPlaintext = allow }
}

// NewEncryptedStore creates an EncryptedStore that encrypts all Put operations
// and decrypts Get operations. The key must be 32 bytes (AES-256).
func NewEncryptedStore(inner ObjectStore, key []byte, opts ...EncryptedOption) *EncryptedStore {
	s := &EncryptedStore{ObjectStore: inner, key: key}
	for _, opt := range opts {
		opt(s)
	}
	return s
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
		if s.legacyPlaintext || !isContentAddressed(key) {
			return data, nil
		}
		return nil, fmt.Errorf(
			"%w: %s was not written by this repository's encryption key. "+
				"Refusing to use it as repository content; the backing store may have been written to by someone else. "+
				"If this repository was converted from an unencrypted one by an earlier release, "+
				"set CLOUDSTIC_ALLOW_LEGACY_PLAINTEXT=1 to read its pre-conversion objects",
			ErrPlaintextObject, key,
		)
	}
	return crypto.Decrypt(data, s.key)
}
