package store

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// KMSDecrypter can decrypt a KMS ciphertext blob.
// Implementations must be safe for concurrent use.
type KMSDecrypter interface {
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}

// AWSKMSDecrypter wraps the AWS KMS SDK v2 client.
type AWSKMSDecrypter struct {
	client *kms.Client
}

// NewAWSKMSDecrypter creates a KMS decrypter using the default AWS credential
// chain. The key ARN is embedded in the ciphertext blob so it does not need
// to be supplied at decryption time.
func NewAWSKMSDecrypter(ctx context.Context) (*AWSKMSDecrypter, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config for kms: %w", err)
	}
	return &AWSKMSDecrypter{client: kms.NewFromConfig(cfg)}, nil
}

func (d *AWSKMSDecrypter) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	out, err := d.client.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob: ciphertext,
	})
	if err != nil {
		return nil, fmt.Errorf("kms decrypt: %w", err)
	}
	return out.Plaintext, nil
}

// OpenWithKMS finds a kms-platform slot, unwraps the master key using the
// given KMS decrypter, and returns the derived encryption key.
func OpenWithKMS(ctx context.Context, slots []KeySlot, decrypter KMSDecrypter) ([]byte, error) {
	for _, slot := range slots {
		if slot.SlotType != "kms-platform" {
			continue
		}
		wrapped, err := base64.StdEncoding.DecodeString(slot.WrappedKey)
		if err != nil {
			continue
		}
		masterKey, err := decrypter.Decrypt(ctx, wrapped)
		if err != nil {
			continue
		}
		return deriveEncryptionKey(masterKey)
	}
	return nil, fmt.Errorf("no compatible kms-platform key slot found")
}

// ExtractMasterKeyWithKMS is like ExtractMasterKey but also supports
// kms-platform slots via a KMSDecrypter.
func ExtractMasterKeyWithKMS(ctx context.Context, slots []KeySlot, decrypter KMSDecrypter, platformKey []byte, password string) ([]byte, error) {
	// Try KMS slots first.
	if decrypter != nil {
		for _, slot := range slots {
			if slot.SlotType != "kms-platform" {
				continue
			}
			wrapped, err := base64.StdEncoding.DecodeString(slot.WrappedKey)
			if err != nil {
				continue
			}
			if mk, err := decrypter.Decrypt(ctx, wrapped); err == nil {
				return mk, nil
			}
		}
	}
	// Fall back to legacy credentials.
	return ExtractMasterKey(slots, platformKey, password)
}
