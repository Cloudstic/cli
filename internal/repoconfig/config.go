// Package repoconfig encodes and decodes the repository config marker.
//
// An encrypted repository's marker is sealed with the repository encryption
// key, so its contents can be neither read nor edited by anyone who cannot
// derive that key. Sealing rather than authenticating is deliberate: AES-GCM
// covers every byte it seals, so a new field is protected the moment it is
// added, with no separate list of authenticated fields to keep in sync.
//
// The cost is that the format version moves inside the sealed blob, so it can
// only be read after the key is resolved. Restic makes the same trade.
//
// An unencrypted repository has no key, so its marker stays plaintext. So does
// every repository written before sealing existed: backward compatibility is
// permanent, and a plaintext marker must remain readable forever. The bytes are
// self-describing, so a reader tells the two apart without being told which to
// expect.
package repoconfig

import (
	"encoding/json"
	"fmt"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/crypto"
)

// IsSealed reports whether raw marker bytes are sealed.
//
// This is a length and version-byte check that needs no key, which is what
// lets a reader decide whether it must unlock before it can decode. A sealed
// marker also implies an encrypted repository, so callers that only need that
// much can answer from this alone.
func IsSealed(raw []byte) bool {
	return crypto.IsEncrypted(raw)
}

// Encode returns the bytes to store for cfg, sealing them when encryptionKey
// is non-empty. An encrypted repository must always pass its key: writing its
// marker in plaintext would strip the protection from a repository that had it.
func Encode(cfg core.RepoConfig, encryptionKey []byte) ([]byte, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal repository config: %w", err)
	}
	if len(encryptionKey) == 0 {
		if cfg.Encrypted {
			return nil, fmt.Errorf("seal repository config: encryption key is required")
		}
		return data, nil
	}
	sealed, err := crypto.Encrypt(data, encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("seal repository config: %w", err)
	}
	return sealed, nil
}

// Decode decodes raw marker bytes, unsealing them first when they are sealed.
//
// encryptionKey is required for a sealed marker and ignored for a plaintext
// one. A sealed marker that does not open is reported as tampering rather than
// as a parse failure: GCM authenticates the whole object, so the only ways to
// get here are the wrong key or modified bytes.
func Decode(raw []byte, encryptionKey []byte) (*core.RepoConfig, error) {
	data := raw
	if IsSealed(raw) {
		if len(encryptionKey) == 0 {
			return nil, fmt.Errorf("repository config is sealed: the encryption key is required to read it")
		}
		opened, err := crypto.Decrypt(raw, encryptionKey)
		if err != nil {
			return nil, fmt.Errorf(
				"repository config could not be opened: it was sealed with a different key, or has been "+
					"modified since it was written: %w", err,
			)
		}
		data = opened
	}

	var cfg core.RepoConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse repository config: %w", err)
	}
	return &cfg, nil
}
