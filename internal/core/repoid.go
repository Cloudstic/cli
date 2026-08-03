package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// RepoIDBytes is the length of a repository identifier before hex encoding.
//
// 16 bytes is the same size as a UUID and far past the point where a birthday
// collision between two repositories is worth reasoning about. The identifier
// is not a secret and is not derived from one; it only has to be unique.
const RepoIDBytes = 16

// NewRepoID returns a fresh random repository identifier, hex encoded.
//
// It is called once, when a repository is initialized. Adopting an existing
// repository must carry the stored identifier forward rather than mint a new
// one — see RepoConfig.ID.
func NewRepoID() (string, error) {
	buf := make([]byte, RepoIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate repository id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
