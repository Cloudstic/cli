package source

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	intsftp "github.com/cloudstic/cli/internal/sftp"
	"github.com/pkg/sftp"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh/knownhosts"
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

func dialTestSFTP(t *testing.T, host, port, knownHostsPath string) *sftp.Client {
	t.Helper()
	hostKeyCallback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		t.Fatalf("failed to create host key callback for seeding: %v", err)
	}
	cfg := intsftp.Config{
		Host:            host,
		Port:            port,
		User:            "test",
		Password:        "test",
		HostKeyCallback: hostKeyCallback,
	}
	c, err := intsftp.Dial(cfg)
	if err != nil {
		t.Fatalf("dialTestSFTP: %v", err)
	}
	return c
}

func writeKnownHosts(t *testing.T, ctx context.Context, container testcontainers.Container, host, port string) string {
	t.Helper()

	var lines []string
	// Get all generated public host keys.
	for _, keyType := range []string{"ed25519", "rsa", "ecdsa"} {
		path := fmt.Sprintf("/etc/ssh/ssh_host_%s_key.pub", keyType)
		exitCode, reader, err := container.Exec(ctx, []string{"cat", path})
		if err == nil && exitCode == 0 {
			var buf bytes.Buffer
			if _, err := buf.ReadFrom(reader); err == nil {
				pubKeyLine := strings.TrimSpace(buf.String())
				if pubKeyLine == "" {
					continue
				}
				parts := strings.Fields(pubKeyLine)
				if len(parts) < 2 {
					continue
				}
				keyTypeAndKey := parts[0] + " " + parts[1]

				// known_hosts format: host1,host2,... keytype key
				// We include host, 127.0.0.1, and ::1 to be safe against resolution differences.
				hosts := fmt.Sprintf("[%s]:%s", host, port)
				if host != "127.0.0.1" {
					hosts += fmt.Sprintf(",[127.0.0.1]:%s", port)
				}
				if host != "::1" {
					hosts += fmt.Sprintf(",[::1]:%s", port)
				}
				if host != "localhost" {
					hosts += fmt.Sprintf(",[localhost]:%s", port)
				}
				lines = append(lines, fmt.Sprintf("%s %s", hosts, keyTypeAndKey))
			}
		}
	}

	if len(lines) == 0 {
		t.Fatalf("failed to get any host key from container")
	}

	tmpFile := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(tmpFile, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatalf("failed to write known_hosts: %v", err)
	}

	return tmpFile
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

	knownHostsPath := writeKnownHosts(t, ctx, container, host, port)
	basePath := "/upload/source"

	// Seed some files via a direct SFTP connection.
	seedClient := dialTestSFTP(t, host, port, knownHostsPath)

	if err := seedClient.MkdirAll(basePath + "/subdir"); err != nil {
		t.Fatalf("seed mkdir: %v", err)
	}

	for _, rel := range []string{"file1.txt", "subdir/file2.txt"} {
		f, err := seedClient.Create(basePath + "/" + rel)
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
	src, err := NewSFTPSource(
		host,
		WithSFTPSourcePort(port),
		WithSFTPSourceUser("test"),
		WithSFTPSourcePassword("test"),
		WithSFTPSourceBasePath(basePath),
		WithSFTPSourceKnownHosts(knownHostsPath),
	)
	if err != nil {
		t.Fatalf("NewSFTPSource failed: %v", err)
	}
	defer func() { _ = src.Close() }()

	// Test Info()
	info := src.Info()
	if info.Type != "sftp" {
		t.Errorf("Expected type 'sftp', got %s", info.Type)
	}
	if info.Path != basePath {
		t.Errorf("Expected Path %q, got %q", basePath, info.Path)
	}

	// Test Size()
	sz, err := src.Size(ctx)
	if err != nil {
		t.Fatalf("Size() failed: %v", err)
	}
	if sz.Files != 2 {
		t.Errorf("Expected 2 files, got %d", sz.Files)
	}
	if sz.Bytes != 22 { // "hello world" x 2
		t.Errorf("Expected 22 bytes, got %d", sz.Bytes)
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
		t.Fatalf("Walk failed: %v", err)
	}
	// 2 files + 1 subdir = 3 entries
	if len(walkedFiles) != 2 {
		t.Errorf("Expected 2 files walked, got %d", len(walkedFiles))
	}
	if len(walkedFolders) != 1 {
		t.Errorf("Expected 1 folder walked, got %d", len(walkedFolders))
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
