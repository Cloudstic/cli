package sftp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	intsftp "github.com/cloudstic/cli/internal/sftp"
	"github.com/cloudstic/cli/pkg/store"
)

type options struct {
	port, user      string
	password        string
	privateKeyPath  string
	basePath        string
	hostKeyCallback ssh.HostKeyCallback
	knownHostsPath  string
	client          *sftp.Client
}

// Option configures an SFTP store.
type Option func(*options)

// WithPort sets the SSH port. Defaults to "22" when empty.
func WithPort(port string) Option {
	return func(o *options) {
		o.port = port
	}
}

// WithUser sets the SSH user for authentication.
func WithUser(user string) Option {
	return func(o *options) {
		o.user = user
	}
}

// WithPassword sets password authentication.
func WithPassword(password string) Option {
	return func(o *options) {
		o.password = password
	}
}

// WithKey sets the path to a PEM-encoded private key for authentication.
func WithKey(keyPath string) Option {
	return func(o *options) {
		o.privateKeyPath = keyPath
	}
}

// WithHostKeyCallback sets the host key verification callback.
func WithHostKeyCallback(cb ssh.HostKeyCallback) Option {
	return func(o *options) {
		o.hostKeyCallback = cb
	}
}

// WithKnownHosts sets the path to the known_hosts file.
func WithKnownHosts(path string) Option {
	return func(o *options) {
		o.knownHostsPath = path
	}
}

// WithBasePath sets the root directory on the SFTP server.
func WithBasePath(basePath string) Option {
	return func(o *options) {
		o.basePath = basePath
	}
}

// WithSFTPClient provides a pre-configured SFTP client, skipping
// internal connection setup. When set, server and auth options are ignored.
func WithSFTPClient(client *sftp.Client) Option {
	return func(o *options) {
		o.client = client
	}
}

// Store implements store.ObjectStore backed by an SFTP server.
type Store struct {
	client   *sftp.Client
	basePath string
}

// New creates an SFTP-backed store for the given host.
// Either WithSFTPClient or authentication options must be provided.
// The base path directory is created if it does not exist.
func New(host string, opts ...Option) (*Store, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	client := o.client
	if client == nil {
		cfg := intsftp.Config{
			Host:            host,
			Port:            o.port,
			User:            o.user,
			Password:        o.password,
			PrivateKeyPath:  o.privateKeyPath,
			BasePath:        o.basePath,
			HostKeyCallback: o.hostKeyCallback,
			KnownHostsPath:  o.knownHostsPath,
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
	return &Store{client: client, basePath: o.basePath}, nil
}

// Close releases the underlying SFTP and SSH connections.
func (s *Store) Close() error {
	return s.client.Close()
}

func (s *Store) key(k string) string {
	return path.Join(s.basePath, k)
}

func (s *Store) Put(_ context.Context, key string, data []byte) error {
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

func (s *Store) Get(_ context.Context, key string) ([]byte, error) {
	f, err := s.client.Open(s.key(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s: %w", key, store.ErrNotFound)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// GetRange implements RangeGetter by seeking, which sftp supports natively, so
// a caller reading a packfile footer does not transfer the whole object.
func (s *Store) GetRange(_ context.Context, key string, offset, length int64) ([]byte, error) {
	if offset < 0 || length < 0 {
		return nil, fmt.Errorf("invalid range %d+%d for %s", offset, length, key)
	}
	if length == 0 {
		return []byte{}, nil
	}

	f, err := s.client.Open(s.key(key))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek %s to %d: %w", key, offset, err)
	}

	// Short reads mean the object ended early; the caller asked for bytes that
	// are not there, which is an error rather than a truncated slice.
	buf, err := store.ReadExactly(f, length)
	if err != nil {
		return nil, fmt.Errorf("read %s at %d+%d: %w", key, offset, length, err)
	}
	return buf, nil
}

func (s *Store) Exists(_ context.Context, key string) (bool, error) {
	_, err := s.client.Stat(s.key(key))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (s *Store) Delete(_ context.Context, key string) error {
	return s.client.Remove(s.key(key))
}

func (s *Store) Size(_ context.Context, key string) (int64, error) {
	info, err := s.client.Stat(s.key(key))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s *Store) TotalSize(_ context.Context) (int64, error) {
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

func (s *Store) Flush(ctx context.Context) error {
	return nil
}

func (s *Store) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	err := s.walk(prefix, func(key string, _ os.FileInfo) error {
		keys = append(keys, key)
		return nil
	})
	return keys, err
}

// ListSized implements store.SizedLister. The walk stats every entry to tell
// a file from a directory, so the size comes with the key.
func (s *Store) ListSized(_ context.Context, prefix string, fn func(key string, size int64) error) error {
	return s.walk(prefix, func(key string, info os.FileInfo) error {
		return fn(key, info.Size())
	})
}

// walk visits every object under prefix with the FileInfo the walk stat'd it
// with.
func (s *Store) walk(prefix string, fn func(key string, info os.FileInfo) error) error {
	startPath := s.basePath
	if prefix != "" {
		candidate := path.Join(s.basePath, prefix)
		if info, err := s.client.Stat(candidate); err == nil && info.IsDir() {
			startPath = candidate
		} else {
			dir := path.Dir(candidate)
			if _, err := s.client.Stat(dir); err == nil {
				startPath = dir
			}
		}
	}

	walker := s.client.Walk(startPath)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			return err
		}
		if walker.Stat().IsDir() {
			continue
		}
		rel, err := relPath(s.basePath, walker.Path())
		if err != nil {
			return err
		}
		if strings.HasPrefix(rel, prefix) {
			if err := fn(rel, walker.Stat()); err != nil {
				return err
			}
		}
	}
	return nil
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

// DeleteAll implements store.BatchDeleter as a loop: SFTP removes one path per
// request and has no bulk form.
//
// It exists so callers need no fallback branch and so a failed remove is
// reported per key, exactly as it is on a backend that really does batch. A
// path that is already gone counts as deleted, which store.DeleteEach supplies.
func (s *Store) DeleteAll(ctx context.Context, keys []string) error {
	return store.DeleteEach(ctx, keys, s.Delete)
}
