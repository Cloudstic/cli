// Package crypto provides authenticated encryption primitives for backup data.
//
// Ciphertext format: version(1) || nonce(12) || ciphertext || GCM_tag(16)
// Version 0x01 = AES-256-GCM with 12-byte random nonce.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/hkdf"
)

const (
	Version1  byte = 0x01
	NonceSize      = 12
	TagSize        = 16
	KeySize        = 32
	Overhead       = 1 + NonceSize + TagSize // version + nonce + tag
)

var (
	ErrInvalidCiphertext = errors.New("crypto: invalid ciphertext")
	ErrDecryptFailed     = errors.New("crypto: decryption failed (wrong key or tampered data)")
)

// Encrypt encrypts plaintext using AES-256-GCM with a random nonce.
// Returns version(1) || nonce(12) || ciphertext || tag(16).
func Encrypt(plaintext, key []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: generate nonce: %w", err)
	}

	out := make([]byte, 1, Overhead+len(plaintext))
	out[0] = Version1
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// Decrypt decrypts ciphertext produced by Encrypt.
// Returns ErrInvalidCiphertext if the data is too short or has an unknown
// version, and ErrDecryptFailed if authentication fails.
func Decrypt(ciphertext, key []byte) ([]byte, error) {
	if len(ciphertext) < Overhead {
		return nil, ErrInvalidCiphertext
	}
	if ciphertext[0] != Version1 {
		return nil, fmt.Errorf("%w: unknown version 0x%02x", ErrInvalidCiphertext, ciphertext[0])
	}

	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}

	nonce := ciphertext[1 : 1+NonceSize]
	sealed := ciphertext[1+NonceSize:]

	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, ErrDecryptFailed
	}
	return plaintext, nil
}

// IsEncrypted reports whether data starts with a known encryption version byte
// and is long enough to be a valid ciphertext.
func IsEncrypted(data []byte) bool {
	return len(data) >= Overhead && data[0] == Version1
}

// DeriveKey derives a 256-bit encryption key from a master key using
// HKDF-SHA256. The info string should be unique per purpose.
func DeriveKey(masterKey []byte, info string) ([]byte, error) {
	r := hkdf.New(sha256.New, masterKey, nil, []byte(info))
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("crypto: derive key: %w", err)
	}
	return key, nil
}

// GenerateKey generates a cryptographically random 256-bit key.
func GenerateKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("crypto: generate key: %w", err)
	}
	return key, nil
}

// WrapKey encrypts a key with a wrapping key using AES-256-GCM.
// The output format is the same as Encrypt.
func WrapKey(key, wrappingKey []byte) ([]byte, error) {
	return Encrypt(key, wrappingKey)
}

// UnwrapKey decrypts a wrapped key using a wrapping key.
func UnwrapKey(wrapped, wrappingKey []byte) ([]byte, error) {
	return Decrypt(wrapped, wrappingKey)
}

// HKDFInfoBackupV1 is the info string used for deriving the AES-256 backup
// encryption key from a master key. Shared by web and CLI.
const HKDFInfoBackupV1 = "cloudstic-backup-v1"

// HKDFInfoDedupV1 is the info string used for deriving the HMAC-SHA256 key
// for chunk deduplication hashing.
const HKDFInfoDedupV1 = "cloudstic-dedup-mac-v1"

// ComputeHMAC computes an HMAC-SHA256 hash of the given data and returns it as a hex string.
func ComputeHMAC(key, data []byte) string {
	h := hmac.New(sha256.New, key)
	// hmac.Write never returns an error, but handle it for errcheck.
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// NewHMACHash returns a new HMAC-SHA256 hash.Hash initialized with the given key.
func NewHMACHash(key []byte) hash.Hash {
	return hmac.New(sha256.New, key)
}

// Argon2Params controls the cost of Argon2id password hashing.
type Argon2Params struct {
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"`
	Threads uint8  `json:"threads"`
}

// DefaultArgon2Params provides reasonable defaults (~1s on modern hardware).
var DefaultArgon2Params = Argon2Params{
	Time:    3,
	Memory:  64 * 1024, // 64 MiB
	Threads: 4,
}

// DeriveKeyFromPassword derives a 256-bit key from a password using Argon2id.
func DeriveKeyFromPassword(password string, salt []byte, params Argon2Params) []byte {
	return argon2.IDKey([]byte(password), salt, params.Time, params.Memory, params.Threads, KeySize)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("crypto: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: create gcm: %w", err)
	}
	return gcm, nil
}
