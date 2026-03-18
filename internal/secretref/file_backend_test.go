package secretref

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileBackend_BlobRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "secret.bin")
	ref, _ := Parse("file://" + path)
	data := []byte("top secret blob")

	backend := NewFileBackend()
	ctx := context.Background()

	// 1. Save
	if err := backend.SaveBlob(ctx, ref, data); err != nil {
		t.Fatalf("SaveBlob failed: %v", err)
	}

	// 2. Verify permissions (0600)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected permissions 0600, got %v", info.Mode().Perm())
	}

	// 3. Load
	got, err := backend.LoadBlob(ctx, ref)
	if err != nil {
		t.Fatalf("LoadBlob failed: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("expected %q, got %q", string(data), string(got))
	}

	// 4. Delete
	if err := backend.DeleteBlob(ctx, ref); err != nil {
		t.Fatalf("DeleteBlob failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still exists after DeleteBlob")
	}
}

func TestConfigTokenBackend_BlobRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDSTIC_CONFIG_DIR", tmpDir)

	backend := NewConfigTokenBackend()
	ctx := context.Background()
	ref, _ := Parse("config-token://google/test-token")
	data := []byte("{\"token\":\"fake\"}")

	// 1. Save
	if err := backend.SaveBlob(ctx, ref, data); err != nil {
		t.Fatalf("SaveBlob failed: %v", err)
	}

	// 2. Verify it's in the right place
	expectedPath := filepath.Join(tmpDir, "tokens", "google/test-token.json")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("expected file at %s: %v", expectedPath, err)
	}

	// 3. Load
	got, err := backend.LoadBlob(ctx, ref)
	if err != nil {
		t.Fatalf("LoadBlob failed: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("expected %q, got %q", string(data), string(got))
	}
}
