package source

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	intsftp "github.com/cloudstic/cli/internal/sftp"
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
		WaitingFor:   wait.ForLog("Server listening on"),
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

func sftpTestConfig(host, port, basePath string) intsftp.Config {
	return intsftp.Config{
		Host:     host,
		Port:     port,
		User:     "test",
		Password: "test",
		BasePath: basePath,
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

	cfg := sftpTestConfig(host, port, "/upload/source")

	// Seed some files via a direct SFTP connection.
	seedClient, err := intsftp.Dial(cfg)
	if err != nil {
		t.Fatalf("seed dialSFTP: %v", err)
	}

	if err := seedClient.MkdirAll(cfg.BasePath + "/subdir"); err != nil {
		t.Fatalf("seed mkdir: %v", err)
	}

	for _, rel := range []string{"file1.txt", "subdir/file2.txt"} {
		f, err := seedClient.Create(fmt.Sprintf("%s/%s", cfg.BasePath, rel))
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
	src, err := NewSFTPSource(ctx, cfg)
	if err != nil {
		t.Fatalf("NewSFTPSource failed: %v", err)
	}
	defer func() { _ = src.Close() }()

	// Test Info()
	info := src.Info()
	if info.Type != "sftp" {
		t.Errorf("Expected type 'sftp', got %s", info.Type)
	}
	if info.Path != cfg.BasePath {
		t.Errorf("Expected Path %q, got %q", cfg.BasePath, info.Path)
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
