package secretref

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

	// 2. Verify permissions (0600) - skip on Windows
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat failed: %v", err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("expected permissions 0600, got %v", info.Mode().Perm())
		}
	}

	// 3. Load
	got, err := backend.LoadBlob(ctx, ref)
	if err != nil {
		t.Fatalf("LoadBlob failed: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("expected %q, got %q", string(data), string(got))
	}

	// 4. Resolve (string)
	val, err := backend.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if val != string(data) {
		t.Errorf("expected %q, got %q", string(data), val)
	}

	// 5. Exists
	exists, err := backend.Exists(ctx, ref)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Errorf("expected file to exist")
	}

	// 6. Store
	if err := backend.Store(ctx, ref, "new value"); err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	val, _ = backend.Resolve(ctx, ref)
	if val != "new value" {
		t.Errorf("expected 'new value', got %q", val)
	}

	// 7. Delete
	if err := backend.DeleteBlob(ctx, ref); err != nil {
		t.Fatalf("DeleteBlob failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still exists after DeleteBlob")
	}

	// 8. Not found
	_, err = backend.LoadBlob(ctx, ref)
	if err == nil {
		t.Errorf("expected error for non-existent file")
	}
}

func TestFileBackend_Metadata(t *testing.T) {
	backend := NewFileBackend()
	if backend.Scheme() != "file" {
		t.Errorf("expected scheme 'file', got %q", backend.Scheme())
	}
	if !strings.Contains(backend.DisplayName(), "file") {
		t.Errorf("expected display name to contain 'file', got %q", backend.DisplayName())
	}
	if !backend.WriteSupported() {
		t.Errorf("expected WriteSupported to be true")
	}
	def := backend.DefaultRef("test", "acc")
	if !strings.HasPrefix(def, "file://") {
		t.Errorf("expected default ref to start with 'file://', got %q", def)
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

	// 4. Resolve
	val, err := backend.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if val != string(data) {
		t.Errorf("expected %q, got %q", string(data), val)
	}

	// 5. Exists
	exists, err := backend.Exists(ctx, ref)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Errorf("expected token to exist")
	}

	// 6. Delete
	if err := backend.DeleteBlob(ctx, ref); err != nil {
		t.Fatalf("DeleteBlob failed: %v", err)
	}
	exists, _ = backend.Exists(ctx, ref)
	if exists {
		t.Errorf("expected token to be deleted")
	}

	// 7. Store
	if err := backend.Store(ctx, ref, "test-store"); err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	val, _ = backend.Resolve(ctx, ref)
	if val != "test-store" {
		t.Errorf("expected 'test-store', got %q", val)
	}
}

func TestConfigTokenBackend_Metadata(t *testing.T) {
	backend := NewConfigTokenBackend()
	if backend.Scheme() != "config-token" {
		t.Errorf("expected scheme 'config-token', got %q", backend.Scheme())
	}
	if !backend.WriteSupported() {
		t.Errorf("expected WriteSupported to be true")
	}
	def := backend.DefaultRef("myname", "myprovider")
	if def != "config-token://myprovider/myname" {
		t.Errorf("unexpected default ref: %q", def)
	}
	// Default provider
	def2 := backend.DefaultRef("myname", "")
	if def2 != "config-token://google/myname" {
		t.Errorf("unexpected default ref for empty account: %q", def2)
	}
}

func TestConfigTokenBackend_DecryptionFallback(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDSTIC_CONFIG_DIR", tmpDir)

	backend := NewConfigTokenBackend()
	ctx := context.Background()
	ref, _ := Parse("config-token://google/legacy")

	// Manually write unencrypted data
	tokenDir := filepath.Join(tmpDir, "tokens", "google")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	rawPath := filepath.Join(tokenDir, "legacy.json")
	data := []byte("plain text token")
	if err := os.WriteFile(rawPath, data, 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Load should fall back to plaintext
	got, err := backend.LoadBlob(ctx, ref)
	if err != nil {
		t.Fatalf("LoadBlob failed: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("expected %q, got %q", string(data), string(got))
	}
}

func TestConfigTokenBackend_InvalidRef(t *testing.T) {
	backend := NewConfigTokenBackend()
	ctx := context.Background()

	ref, _ := Parse("config-token://") // Parse won't actually allow this but let's be sure
	ref.Path = ""

	_, err := backend.LoadBlob(ctx, ref)
	if err == nil {
		t.Errorf("expected error for empty path")
	}
}

func TestConfigTokenBackend_RejectsPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDSTIC_CONFIG_DIR", tmpDir)

	backend := NewConfigTokenBackend()
	ctx := context.Background()
	ref, _ := Parse("config-token://google/../escape")

	if err := backend.SaveBlob(ctx, ref, []byte("secret")); err == nil {
		t.Fatal("expected traversal ref to fail")
	}
}
