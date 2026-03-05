package store

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	intsftp "github.com/cloudstic/cli/internal/sftp"
	"github.com/pkg/sftp"
)

type sftpStoreOptions struct {
	port, user     string
	password       string
	privateKeyPath string
	basePath       string
	client         *sftp.Client
}

// SFTPStoreOption configures an SFTP store.
type SFTPStoreOption func(*sftpStoreOptions)

// WithSFTPPort sets the SSH port. Defaults to "22" when empty.
func WithSFTPPort(port string) SFTPStoreOption {
	return func(o *sftpStoreOptions) {
		o.port = port
	}
}

// WithSFTPUser sets the SSH user for authentication.
func WithSFTPUser(user string) SFTPStoreOption {
	return func(o *sftpStoreOptions) {
		o.user = user
	}
}

// WithSFTPPassword sets password authentication.
func WithSFTPPassword(password string) SFTPStoreOption {
	return func(o *sftpStoreOptions) {
		o.password = password
	}
}

// WithSFTPKey sets the path to a PEM-encoded private key for authentication.
func WithSFTPKey(keyPath string) SFTPStoreOption {
	return func(o *sftpStoreOptions) {
		o.privateKeyPath = keyPath
	}
}

// WithSFTPBasePath sets the root directory on the SFTP server.
func WithSFTPBasePath(basePath string) SFTPStoreOption {
	return func(o *sftpStoreOptions) {
		o.basePath = basePath
	}
}

// WithSFTPClient provides a pre-configured SFTP client, skipping
// internal connection setup. When set, server and auth options are ignored.
func WithSFTPClient(client *sftp.Client) SFTPStoreOption {
	return func(o *sftpStoreOptions) {
		o.client = client
	}
}

// SFTPStore implements ObjectStore backed by an SFTP server.
type SFTPStore struct {
	client   *sftp.Client
	basePath string
}

// NewSFTPStore creates an SFTP-backed store for the given host.
// Either WithSFTPClient or authentication options must be provided.
// The base path directory is created if it does not exist.
func NewSFTPStore(host string, opts ...SFTPStoreOption) (*SFTPStore, error) {
	var o sftpStoreOptions
	for _, opt := range opts {
		opt(&o)
	}

	client := o.client
	if client == nil {
		cfg := intsftp.Config{
			Host:           host,
			Port:           o.port,
			User:           o.user,
			Password:       o.password,
			PrivateKeyPath: o.privateKeyPath,
			BasePath:       o.basePath,
		}
		var err error
		client, err = intsftp.Dial(cfg)
		if err != nil {
			return nil, fmt.Errorf("sftp connect: %w", err)
		}
	}

	if o.basePath != "" {
		if err := mkdirAllSFTP(client, o.basePath); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("sftp mkdir %s: %w", o.basePath, err)
		}
	}
	return &SFTPStore{client: client, basePath: o.basePath}, nil
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
