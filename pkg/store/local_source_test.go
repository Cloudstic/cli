package store

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

func TestLocalSource_WithExcludes(t *testing.T) {
	ctx := context.Background()
	tmpDir, err := os.MkdirTemp("", "cloudstic-exclude-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create filesystem structure.
	for _, dir := range []string{"src", ".git/objects", "node_modules/pkg", "build"} {
		if err := os.MkdirAll(filepath.Join(tmpDir, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []struct{ path, data string }{
		{"src/main.go", "package main"},
		{"src/app.log", "log data"},
		{".git/config", "[core]"},
		{".git/objects/abc", "blob"},
		{"node_modules/pkg/index.js", "module.exports = {}"},
		{"build/output.bin", "binary"},
		{"README.md", "hello"},
		{"notes.tmp", "temp"},
	} {
		if err := os.WriteFile(filepath.Join(tmpDir, f.path), []byte(f.data), 0644); err != nil {
			t.Fatal(err)
		}
	}

	src := NewLocalSource(LocalSourceConfig{
		RootPath:        tmpDir,
		ExcludePatterns: []string{".git/", "node_modules/", "*.tmp", "*.log"},
	})

	// Test Walk() — excluded files/dirs should not appear.
	var walked []string
	err = src.Walk(ctx, func(fm core.FileMeta) error {
		walked = append(walked, fm.FileID)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk() failed: %v", err)
	}

	// Should see: src, src/main.go, build, build/output.bin, README.md
	// Should NOT see: .git/*, node_modules/*, notes.tmp, src/app.log
	excluded := map[string]bool{
		".git":                        true,
		".git/config":                  true,
		".git/objects":                 true,
		".git/objects/abc":             true,
		"node_modules":                 true,
		"node_modules/pkg":             true,
		"node_modules/pkg/index.js":    true,
		"notes.tmp":                    true,
		"src/app.log":                  true,
	}
	for _, w := range walked {
		if excluded[w] {
			t.Errorf("Walk() should have excluded %q", w)
		}
	}
	expected := map[string]bool{
		"src":              true,
		"src/main.go":      true,
		"build":            true,
		"build/output.bin": true,
		"README.md":        true,
	}
	for _, w := range walked {
		delete(expected, w)
	}
	for missing := range expected {
		t.Errorf("Walk() missing expected entry %q", missing)
	}

	// Test Size() — should only count non-excluded files.
	size, err := src.Size(ctx)
	if err != nil {
		t.Fatalf("Size() failed: %v", err)
	}
	// Expected files: src/main.go (12), build/output.bin (6), README.md (5) = 3 files, 23 bytes
	if size.Files != 3 {
		t.Errorf("Size().Files = %d, want 3", size.Files)
	}
	if size.Bytes != 23 {
		t.Errorf("Size().Bytes = %d, want 23", size.Bytes)
	}
}

func TestLocalSource(t *testing.T) {
	ctx := context.Background()
	tmpDir, err := os.MkdirTemp("", "cloudstic-source-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a nested filesystem structure
	nestedDir := filepath.Join(tmpDir, "folder")
	if err := os.Mkdir(nestedDir, 0755); err != nil {
		t.Fatalf("Failed to create nested dir: %v", err)
	}

	fileData := []byte("hello world")
	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), fileData, 0644); err != nil {
		t.Fatalf("Failed to write file1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "file2.txt"), fileData, 0644); err != nil {
		t.Fatalf("Failed to write file2: %v", err)
	}

	src := NewLocalSource(LocalSourceConfig{RootPath: tmpDir})

	// Test Info()
	info := src.Info()
	if info.Type != "local" {
		t.Errorf("Expected type 'local', got %s", info.Type)
	}
	if info.Path != tmpDir {
		t.Errorf("Expected Path '%s', got %s", tmpDir, info.Path)
	}

	// Test Size()
	size, err := src.Size(ctx)
	if err != nil {
		t.Fatalf("Size() failed: %v", err)
	}
	if size.Files != 2 {
		t.Errorf("Expected 2 files, got %d", size.Files)
	}
	if size.Bytes != int64(len(fileData)*2) {
		t.Errorf("Expected %d bytes, got %d", len(fileData)*2, size.Bytes)
	}

	// Test Walk()
	var walkedFiles []string
	var walkedFolders []string
	err = src.Walk(ctx, func(fm core.FileMeta) error {
		switch fm.Type {
		case core.FileTypeFile:
			walkedFiles = append(walkedFiles, fm.FileID)
		case core.FileTypeFolder:
			walkedFolders = append(walkedFolders, fm.FileID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk() failed: %v", err)
	}

	if len(walkedFiles) != 2 {
		t.Errorf("Expected to walk 2 files, got %d", len(walkedFiles))
	}
	if len(walkedFolders) != 1 {
		t.Errorf("Expected to walk 1 folder, got %d", len(walkedFolders))
	}

	// Test GetFileStream
	stream, err := src.GetFileStream("file1.txt")
	if err != nil {
		t.Fatalf("GetFileStream() failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	data, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll() failed: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("Expected 'hello world', got '%s'", string(data))
	}
}
