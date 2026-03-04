package store

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// SFTPStore implements ObjectStore backed by an SFTP server.
type SFTPStore struct {
	client   *sftp.Client
	basePath string
}

// SFTPConfig holds the parameters needed to connect to an SFTP server.
type SFTPConfig struct {
	Host           string
	Port           string // default "22"
	User           string
	Password       string // password auth (optional if key is set)
	PrivateKeyPath string // path to PEM-encoded private key (optional if password is set)
	BasePath       string
}

// NewSFTPStore connects to the SFTP server described by cfg and returns a
// store rooted at basePath. The directory is created if it does not exist.
func NewSFTPStore(cfg SFTPConfig) (*SFTPStore, error) {
	client, err := dialSFTP(cfg)
	if err != nil {
		return nil, fmt.Errorf("sftp connect: %w", err)
	}
	if err := mkdirAllSFTP(client, cfg.BasePath); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("sftp mkdir %s: %w", cfg.BasePath, err)
	}
	return &SFTPStore{client: client, basePath: cfg.BasePath}, nil
}

// Close releases the underlying SFTP and SSH connections.
func (s *SFTPStore) Close() error {
	return s.client.Close()
}

func (s *SFTPStore) key(k string) string {
	return path.Join(s.basePath, k)
}

func (s *SFTPStore) Put(_ context.Context, key string, data []byte) error {
	fullPath := s.key(key)
	dir := path.Dir(fullPath)
	if err := mkdirAllSFTP(s.client, dir); err != nil {
		return fmt.Errorf("sftp mkdir %s: %w", dir, err)
	}
	tmpPath := fullPath + ".tmp"
	f, err := s.client.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("sftp create %s: %w", tmpPath, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = s.client.Remove(tmpPath)
		return fmt.Errorf("sftp write %s: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		_ = s.client.Remove(tmpPath)
		return fmt.Errorf("sftp close %s: %w", tmpPath, err)
	}
	if err := s.client.PosixRename(tmpPath, fullPath); err != nil {
		_ = s.client.Remove(tmpPath)
		return fmt.Errorf("sftp rename %s → %s: %w", tmpPath, fullPath, err)
	}
	return nil
}

func (s *SFTPStore) Get(_ context.Context, key string) ([]byte, error) {
	f, err := s.client.Open(s.key(key))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

func (s *SFTPStore) Exists(_ context.Context, key string) (bool, error) {
	_, err := s.client.Stat(s.key(key))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (s *SFTPStore) Delete(_ context.Context, key string) error {
	return s.client.Remove(s.key(key))
}

func (s *SFTPStore) Size(_ context.Context, key string) (int64, error) {
	info, err := s.client.Stat(s.key(key))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s *SFTPStore) TotalSize(_ context.Context) (int64, error) {
	var total int64
	walker := s.client.Walk(s.basePath)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			return 0, err
		}
		if !walker.Stat().IsDir() {
			total += walker.Stat().Size()
		}
	}
	return total, nil
}

func (s *SFTPStore) Flush(ctx context.Context) error {
	return nil
}

func (s *SFTPStore) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	walker := s.client.Walk(s.basePath)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			return nil, err
		}
		if walker.Stat().IsDir() {
			continue
		}
		rel, err := relPath(s.basePath, walker.Path())
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(rel, prefix) {
			keys = append(keys, rel)
		}
	}
	return keys, nil
}

// mkdirAllSFTP creates dir and all parents, tolerating "permission denied" on
// path components that already exist (e.g. in SFTP chroot environments where
// /home/user is read-only).
func mkdirAllSFTP(c *sftp.Client, dir string) error {
	dir = path.Clean(dir)
	if dir == "/" || dir == "." {
		return nil
	}

	// Fast path: dir already exists.
	if fi, err := c.Stat(dir); err == nil && fi.IsDir() {
		return nil
	}

	// Ensure parent exists first.
	parent := path.Dir(dir)
	if parent != dir {
		if err := mkdirAllSFTP(c, parent); err != nil {
			return err
		}
	}

	// Create this level; ignore error if it already exists.
	if err := c.Mkdir(dir); err != nil {
		// Double-check: if it now exists as a dir, that's fine.
		if fi, statErr := c.Stat(dir); statErr == nil && fi.IsDir() {
			return nil
		}
		return err
	}
	return nil
}

// relPath returns p relative to base using pure path manipulation (no OS
// dependency). Both paths must be absolute or both relative.
func relPath(base, p string) (string, error) {
	base = path.Clean(base) + "/"
	p = path.Clean(p)
	if !strings.HasPrefix(p, base) {
		return "", fmt.Errorf("%s is not under %s", p, base)
	}
	return strings.TrimPrefix(p, base), nil
}

// ---------------------------------------------------------------------------
// SSH / SFTP dial helpers
// ---------------------------------------------------------------------------

func dialSFTP(cfg SFTPConfig) (*sftp.Client, error) {
	port := cfg.Port
	if port == "" {
		port = "22"
	}

	authMethods, err := buildAuthMethods(cfg)
	if err != nil {
		return nil, err
	}

	sshCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // users may override via SSH config
	}

	conn, err := ssh.Dial("tcp", net.JoinHostPort(cfg.Host, port), sshCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s:%s: %w", cfg.Host, port, err)
	}

	client, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sftp client: %w", err)
	}
	return client, nil
}

func buildAuthMethods(cfg SFTPConfig) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	// 1. Private key
	if cfg.PrivateKeyPath != "" {
		pemBytes, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read private key %s: %w", cfg.PrivateKeyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("parse private key %s: %w", cfg.PrivateKeyPath, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	// 2. Password
	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}

	// 3. SSH agent (fallback)
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no SFTP authentication method available: provide --sftp-password, --sftp-key, or SSH_AUTH_SOCK")
	}
	return methods, nil
}
