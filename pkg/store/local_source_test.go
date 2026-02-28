package store

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

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

	src := NewLocalSource(tmpDir)

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
