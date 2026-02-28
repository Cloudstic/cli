package store

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startSFTPContainer spins up an OpenSSH SFTP server using the atmoz/sftp
// image. It creates a user "test" with password "test" and returns the
// container, mapped host, and mapped port. Callers must terminate the container.
func startSFTPContainer(t *testing.T, ctx context.Context) (testcontainers.Container, string, string) {
	t.Helper()

	req := testcontainers.ContainerRequest{
		Image:        "atmoz/sftp:latest",
		ExposedPorts: []string{"22/tcp"},
		Cmd:          []string{"test:test:::upload"},
		WaitingFor:   wait.ForListeningPort("22/tcp"),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start sftp container: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("failed to get container host: %v", err)
	}
	mappedPort, err := container.MappedPort(ctx, "22")
	if err != nil {
		t.Fatalf("failed to get mapped port: %v", err)
	}

	return container, host, mappedPort.Port()
}

func sftpTestConfig(host, port string) SFTPConfig {
	return SFTPConfig{
		Host:     host,
		Port:     port,
		User:     "test",
		Password: "test",
	}
}

func TestSFTPStore(t *testing.T) {
	// Check if docker is available
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		t.Skipf("docker is not available or not running, skipping test: %v", err)
	}

	ctx := context.Background()

	container, host, port := startSFTPContainer(t, ctx)
	defer func() {
		if err := container.Terminate(ctx); err != nil {
			t.Fatalf("failed to terminate sftp container: %v", err)
		}
	}()

	cfg := sftpTestConfig(host, port)
	basePath := "/upload/store"

	st, err := NewSFTPStore(cfg, basePath)
	if err != nil {
		t.Fatalf("NewSFTPStore failed: %v", err)
	}
	defer func() { _ = st.Close() }()

	key := "test/file.txt"
	data := []byte("hello sftp!")

	// Put
	if err := st.Put(ctx, key, data); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get
	fetched, err := st.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(fetched) != string(data) {
		t.Fatalf("Get mismatch: want %q, got %q", string(data), string(fetched))
	}

	// Exists (true)
	exists, err := st.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Fatalf("Expected key to exist")
	}

	// Exists (false)
	exists, err = st.Exists(ctx, "nonexistent/key")
	if err != nil {
		t.Fatalf("Exists(nonexistent) failed: %v", err)
	}
	if exists {
		t.Fatalf("Expected nonexistent key to report false")
	}

	// Size
	size, err := st.Size(ctx, key)
	if err != nil {
		t.Fatalf("Size failed: %v", err)
	}
	if size != int64(len(data)) {
		t.Fatalf("Expected size %d, got %d", len(data), size)
	}

	// Put another key for List/TotalSize coverage
	if err := st.Put(ctx, "test/another.txt", data); err != nil {
		t.Fatalf("Put another.txt failed: %v", err)
	}

	// List
	keys, err := st.List(ctx, "test/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("Expected 2 keys in list, got %d: %v", len(keys), keys)
	}

	// TotalSize
	total, err := st.TotalSize(ctx)
	if err != nil {
		t.Fatalf("TotalSize failed: %v", err)
	}
	if total != int64(len(data)*2) {
		t.Fatalf("Expected TotalSize %d, got %d", len(data)*2, total)
	}

	// Delete
	if err := st.Delete(ctx, key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	exists, _ = st.Exists(ctx, key)
	if exists {
		t.Fatalf("Expected key to be deleted")
	}
}

func TestSFTPSource(t *testing.T) {
	// Check if docker is available
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		t.Skipf("docker is not available or not running, skipping test: %v", err)
	}

	ctx := context.Background()

	container, host, port := startSFTPContainer(t, ctx)
	defer func() {
		if err := container.Terminate(ctx); err != nil {
			t.Fatalf("failed to terminate sftp container: %v", err)
		}
	}()

	cfg := sftpTestConfig(host, port)

	// Seed some files via a direct SFTP connection.
	seedClient, err := dialSFTP(cfg)
	if err != nil {
		t.Fatalf("seed dialSFTP: %v", err)
	}

	rootPath := "/upload/source"
	if err := seedClient.MkdirAll(rootPath + "/subdir"); err != nil {
		t.Fatalf("seed mkdir: %v", err)
	}

	for _, rel := range []string{"file1.txt", "subdir/file2.txt"} {
		f, err := seedClient.Create(fmt.Sprintf("%s/%s", rootPath, rel))
		if err != nil {
			t.Fatalf("seed create %s: %v", rel, err)
		}
		if _, err := f.Write([]byte("hello world")); err != nil {
			_ = f.Close()
			t.Fatalf("seed write %s: %v", rel, err)
		}
		_ = f.Close()
	}
	_ = seedClient.Close()

	// Create SFTPSource
	src, err := NewSFTPSource(cfg, rootPath)
	if err != nil {
		t.Fatalf("NewSFTPSource failed: %v", err)
	}
	defer func() { _ = src.Close() }()

	// Test Info()
	info := src.Info()
	if info.Type != "sftp" {
		t.Errorf("Expected type 'sftp', got %s", info.Type)
	}
	if info.Path != rootPath {
		t.Errorf("Expected Path %q, got %q", rootPath, info.Path)
	}

	// Test Size()
	sz, err := src.Size(ctx)
	if err != nil {
		t.Fatalf("Size() failed: %v", err)
	}
	if sz.Files != 2 {
		t.Errorf("Expected 2 files, got %d", sz.Files)
	}
	if sz.Bytes != int64(len("hello world")*2) {
		t.Errorf("Expected %d bytes, got %d", len("hello world")*2, sz.Bytes)
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
		t.Errorf("Expected to walk 2 files, got %d: %v", len(walkedFiles), walkedFiles)
	}
	if len(walkedFolders) != 1 {
		t.Errorf("Expected to walk 1 folder, got %d: %v", len(walkedFolders), walkedFolders)
	}

	// Test GetFileStream
	stream, err := src.GetFileStream("file1.txt")
	if err != nil {
		t.Fatalf("GetFileStream() failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll() failed: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("Expected 'hello world', got %q", string(got))
	}
}
