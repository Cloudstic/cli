package core

import (
	"encoding/json"
	"testing"
)

func TestComputeHash(t *testing.T) {
	input := []byte("hello world")
	// SHA-256 of "hello world"
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

	hash := ComputeHash(input)
	if hash != expected {
		t.Errorf("ComputeHash(%s) = %s; want %s", input, hash, expected)
	}
}

func TestComputeJSONHash(t *testing.T) {
	type TestStruct struct {
		Field string `json:"field"`
		Num   int    `json:"num"`
	}

	obj := TestStruct{Field: "test", Num: 123}

	hash, data, err := ComputeJSONHash(&obj)
	if err != nil {
		t.Fatalf("ComputeJSONHash failed: %v", err)
	}

	// Verify data is valid JSON
	var decoded TestStruct
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Errorf("Failed to unmarshal returned data: %v", err)
	}
	if decoded.Field != "test" || decoded.Num != 123 {
		t.Errorf("Unmarshaled data mismatch")
	}

	// Verify hash matches hash of data
	expectedHash := ComputeHash(data)
	if hash != expectedHash {
		t.Errorf("Hash mismatch: got %s, calculated from data %s", hash, expectedHash)
	}
}
