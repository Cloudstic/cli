package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// ComputeHash computes the SHA-256 hash of the given data and returns it as a hex string.
func ComputeHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// ComputeJSONHash computes the SHA-256 hash of the JSON representation of the object.
// This uses encoding/json which sorts map keys, providing a canonical form for basic structs.
func ComputeJSONHash(v interface{}) (string, []byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", nil, err
	}
	return ComputeHash(data), data, nil
}

// SelfAddressedPrefixes are the object namespaces whose key is the SHA-256 of
// the bytes stored under it. For these, and only these, the key is a checksum
// the reader can verify without holding any secret.
//
// The other namespaces are deliberately absent. A chunk is named by an HMAC of
// its bytes when the repository is encrypted, so verifying it needs the dedup
// key rather than a bare hash; `check -read-data` does that where the key is
// available. A content object is named by an HMAC of the *file's* hash, not of
// its own bytes, so it is verified through the file it describes — restore
// hashes the reconstructed stream against FileMeta.ContentHash.
var SelfAddressedPrefixes = []string{"filemeta/", "node/", "snapshot/"}

// ErrRefMismatch reports an object whose bytes are not what its key names.
//
// It is deliberately one error for every such object type: whether the cause is
// bit rot, a truncated write, or a store that substituted the object, the
// caller's correct response is the same — refuse the bytes.
type ErrRefMismatch struct {
	Ref  string
	Got  string
	Size int
}

func (e *ErrRefMismatch) Error() string {
	return fmt.Sprintf("object %s failed its integrity check: %d bytes hashing to %s",
		e.Ref, e.Size, e.Got)
}

// VerifyRef checks that data is the object ref names.
//
// Repository objects form a Merkle tree: a snapshot names a root node, nodes
// name child nodes and filemeta refs, and a filemeta names its content. Every
// link in that chain is a SHA-256 of the bytes on the other end, so verifying a
// ref on the way in authenticates the whole tree beneath it against anything
// short of rewriting the snapshot ref itself.
//
// Objects outside SelfAddressedPrefixes are not self-checking, and passing one
// here is a programming error rather than a corrupt repository — hence the
// distinct error. Callers that handle mixed keys should test the prefix first.
func VerifyRef(ref string, data []byte) error {
	for _, prefix := range SelfAddressedPrefixes {
		suffix, ok := strings.CutPrefix(ref, prefix)
		if !ok {
			continue
		}
		if got := ComputeHash(data); got != suffix {
			return &ErrRefMismatch{Ref: ref, Got: got, Size: len(data)}
		}
		return nil
	}
	return fmt.Errorf("object %s is not content-addressed by its own bytes", ref)
}

// IsSelfAddressed reports whether ref lives in a namespace VerifyRef can check.
func IsSelfAddressed(ref string) bool {
	for _, prefix := range SelfAddressedPrefixes {
		if strings.HasPrefix(ref, prefix) {
			return true
		}
	}
	return false
}
