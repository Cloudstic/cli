package crypto

import (
	"crypto/rand"
	"fmt"
	"io"

	"github.com/tyler-smith/go-bip39"
)

// GenerateRecoveryMnemonic generates a 256-bit recovery key and returns it
// as both a BIP39 24-word mnemonic and raw key bytes. The mnemonic is shown
// to the user once; the raw key is used to wrap the master key.
func GenerateRecoveryMnemonic() (mnemonic string, rawKey []byte, err error) {
	entropy := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, entropy); err != nil {
		return "", nil, fmt.Errorf("crypto: generate entropy: %w", err)
	}
	mnemonic, err = bip39.NewMnemonic(entropy)
	if err != nil {
		return "", nil, fmt.Errorf("crypto: create mnemonic: %w", err)
	}
	return mnemonic, entropy, nil
}

// MnemonicToKey converts a BIP39 24-word mnemonic back to the 256-bit raw
// key. Returns an error if the mnemonic is invalid or has a bad checksum.
func MnemonicToKey(mnemonic string) ([]byte, error) {
	entropy, err := bip39.EntropyFromMnemonic(mnemonic)
	if err != nil {
		return nil, fmt.Errorf("crypto: invalid mnemonic: %w", err)
	}
	if len(entropy) != KeySize {
		return nil, fmt.Errorf("crypto: mnemonic entropy is %d bytes, expected %d", len(entropy), KeySize)
	}
	return entropy, nil
}
