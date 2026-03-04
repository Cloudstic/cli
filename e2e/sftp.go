package e2e

import (
	"context"
	"net"
	"path"
	"testing"

	"github.com/pkg/sftp"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh"
)

// sftpTestStore implements TestStore for SFTP.
type sftpTestStore struct {
	host     string
	port     string
	user     string
	password string
	basePath string
}

var _ TestStore = (*sftpTestStore)(nil)

func newSFTPTestStore(t *testing.T) *sftpTestStore {
	ctx := context.Background()
	container, host, port := startSFTPContainer(t, ctx)

	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("failed to terminate sftp container: %v", err)
		}
	})

	return &sftpTestStore{
		host:     host,
		port:     port,
		user:     "test",
		password: "test",
		basePath: "/upload/store",
	}
}

func (s *sftpTestStore) Name() string { return "sftp" }
func (s *sftpTestStore) Env() TestEnv { return Hermetic }
func (s *sftpTestStore) Setup(t *testing.T) []string {
	return []string{
		"-store", "sftp",
		"-store-path", s.basePath,
		"-store-sftp-host", s.host,
		"-store-sftp-port", s.port,
		"-store-sftp-user", s.user,
		"-store-sftp-password", s.password,
	}
}

// sftpTestSource implements TestSource for SFTP.
type sftpTestSource struct {
	host       string
	port       string
	user       string
	password   string
	rootPath   string
	sftpClient *sftp.Client
}

var _ TestSource = (*sftpTestSource)(nil)

func newSFTPTestSource(t *testing.T) *sftpTestSource {
	ctx := context.Background()
	container, host, port := startSFTPContainer(t, ctx)

	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("failed to terminate sftp container: %v", err)
		}
	})

	// Setup a seeding client
	sshCfg := &ssh.ClientConfig{
		User: "test",
		Auth: []ssh.AuthMethod{
			ssh.Password("test"),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	conn, err := ssh.Dial("tcp", net.JoinHostPort(host, port), sshCfg)
	if err != nil {
		t.Fatalf("failed to dial sftp for seeding: %v", err)
	}
	client, err := sftp.NewClient(conn)
	if err != nil {
		t.Fatalf("failed to create sftp client for seeding: %v", err)
	}

	return &sftpTestSource{
		host:       host,
		port:       port,
		user:       "test",
		password:   "test",
		rootPath:   "/upload/source",
		sftpClient: client,
	}
}

func (s *sftpTestSource) Name() string { return "sftp" }
func (s *sftpTestSource) Env() TestEnv { return Hermetic }
func (s *sftpTestSource) Setup(t *testing.T) []string {
	return []string{
		"-source", "sftp",
		"-source-path", s.rootPath,
		"-source-sftp-host", s.host,
		"-source-sftp-port", s.port,
		"-source-sftp-user", s.user,
		"-source-sftp-password", s.password,
	}
}

func (s *sftpTestSource) WriteFile(t *testing.T, relPath, content string) {
	fullPath := path.Join(s.rootPath, relPath)
	dir := path.Dir(fullPath)

	// Create directory recursion
	if err := mkdirAllSFTP(s.sftpClient, dir); err != nil {
		t.Fatalf("failed to create remote dir %s: %v", dir, err)
	}

	f, err := s.sftpClient.Create(fullPath)
	if err != nil {
		t.Fatalf("failed to create remote file %s: %v", fullPath, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatalf("failed to write remote file %s: %v", fullPath, err)
	}
}

// startSFTPContainer helper copied and adapted for E2E tests
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

// mkdirAllSFTP helper copied and adapted for E2E tests
func mkdirAllSFTP(c *sftp.Client, dir string) error {
	dir = path.Clean(dir)
	if dir == "/" || dir == "." || dir == "" {
		return nil
	}

	if fi, err := c.Stat(dir); err == nil && fi.IsDir() {
		return nil
	}

	parent := path.Dir(dir)
	if parent != dir {
		if err := mkdirAllSFTP(c, parent); err != nil {
			return err
		}
	}

	if err := c.Mkdir(dir); err != nil {
		if fi, statErr := c.Stat(dir); statErr == nil && fi.IsDir() {
			return nil
		}
		return err
	}
	return nil
}
