package crypto

import "context"

// KMSClient represents a KMS client capable of both encrypting and decrypting.
//
// The interface lives here, alongside the encryption primitives it is used
// with, while implementations live in their own packages — pkg/crypto/kms
// provides the AWS one. That split is what keeps pkg/crypto (and everything
// importing it: pkg/keychain, pkg/secretref/backends, the root client) free
// of a cloud SDK unless a caller actually wraps a key with KMS. See RFC 0022
// §6.
type KMSClient interface {
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
}
