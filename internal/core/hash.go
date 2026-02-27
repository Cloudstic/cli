package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

