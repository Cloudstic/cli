package store

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/cloudstic/cli/pkg/crypto"
)

// OpenWithKMS finds a kms-platform slot, unwraps the master key using the
// given KMS decrypter, and returns the derived encryption key.
func OpenWithKMS(ctx context.Context, slots []KeySlot, decrypter crypto.KMSDecrypter) ([]byte, error) {
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
// kms-platform slots via a crypto.KMSDecrypter.
func ExtractMasterKeyWithKMS(ctx context.Context, slots []KeySlot, decrypter crypto.KMSDecrypter, platformKey []byte, password string) ([]byte, error) {
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
