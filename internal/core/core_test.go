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

func TestFileMetaHashStability_WithXattrs(t *testing.T) {
	// Xattrs map keys must be sorted for deterministic hashing.
	meta := FileMeta{
		Version: 1,
		FileID:  "test.txt",
		Name:    "test.txt",
		Type:    FileTypeFile,
		Size:    100,
		Mtime:   1000,
		Mode:    0755,
		Uid:     501,
		Gid:     20,
		Btime:   900,
		Xattrs: map[string][]byte{
			"user.zeta":  []byte("last"),
			"user.alpha": []byte("first"),
		},
	}

	hash1, _, err := ComputeJSONHash(&meta)
	if err != nil {
		t.Fatal(err)
	}

	// Compute again — must be identical.
	hash2, _, err := ComputeJSONHash(&meta)
	if err != nil {
		t.Fatal(err)
	}

	if hash1 != hash2 {
		t.Errorf("hash not stable: %s vs %s", hash1, hash2)
	}
}

func TestFileMetaOmitempty(t *testing.T) {
	// Fields with zero values should be omitted from JSON.
	meta := FileMeta{
		Version: 1,
		FileID:  "test.txt",
		Name:    "test.txt",
		Type:    FileTypeFile,
		Size:    100,
		Mtime:   1000,
	}

	_, data, err := ComputeJSONHash(&meta)
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"mode", "uid", "gid", "btime", "flags", "xattrs"} {
		if _, ok := decoded[field]; ok {
			t.Errorf("expected %q to be omitted from JSON when zero, but it was present", field)
		}
	}
}
