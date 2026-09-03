package sftp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

// startSFTPContainer spins up an OpenSSH SFTP server using the atmoz/sftp
// image. It creates a user "test" with password "test" and returns the
// container, host, and port. Callers must terminate the container.
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

func writeKnownHosts(t *testing.T, ctx context.Context, container testcontainers.Container, host, port string) string {
	t.Helper()

	var lines []string
	// Get all generated public host keys.
	for _, keyType := range []string{"ed25519", "rsa", "ecdsa"} {
		path := fmt.Sprintf("/etc/ssh/ssh_host_%s_key.pub", keyType)
		exitCode, reader, err := container.Exec(ctx, []string{"cat", path}, tcexec.Multiplexed())
		if err == nil && exitCode == 0 {
			var buf bytes.Buffer
			if _, err := buf.ReadFrom(reader); err == nil {
				pubKeyLine := strings.TrimSpace(buf.String())
				if pubKeyLine == "" {
					continue
				}
				pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubKeyLine))
				if err != nil {
					continue
				}

				hosts := []string{fmt.Sprintf("[%s]:%s", host, port)}
				if host != "127.0.0.1" {
					hosts = append(hosts, fmt.Sprintf("[127.0.0.1]:%s", port))
				}
				if host != "::1" {
					hosts = append(hosts, fmt.Sprintf("[::1]:%s", port))
				}
				if host != "localhost" {
					hosts = append(hosts, fmt.Sprintf("[localhost]:%s", port))
				}
				lines = append(lines, knownhosts.Line(hosts, pubKey))
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

func TestStore(t *testing.T) {
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

	st, err := New(
		host,
		WithPort(port),
		WithUser("test"),
		WithPassword("test"),
		WithBasePath("/upload/store"),
		WithKnownHosts(knownHostsPath),
	)
	if err != nil {
		t.Fatalf("New failed: %v", err)
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

	// Get (not found)
	if _, err := st.Get(ctx, "nonexistent/key"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(nonexistent) error = %v, want errors.Is(err, store.ErrNotFound)", err)
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

	// Ranged reads are what let PackStore fetch a footer without pulling the
	// whole packfile, so hold SFTP to the same contract as every other backend.
	t.Run("RangeGetter", func(t *testing.T) {
		storetest.AssertRangeGetterConformance(t, st)
	})

	// SFTP has no bulk delete, so its DeleteAll is a loop — held to the same
	// contract regardless, since that is what lets a caller batch without a
	// fallback branch.
	t.Run("BatchDeleter", func(t *testing.T) {
		storetest.AssertBatchDeleterConformance(t, st)
	})

	// The listing carries every object's size, which is what lets prune's
	// sweep account for what it deletes without a request per object.
	t.Run("SizedLister", func(t *testing.T) {
		storetest.AssertSizedListerConformance(t, st)
	})
}
