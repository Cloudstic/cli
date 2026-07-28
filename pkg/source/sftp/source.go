package sftp

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	intsftp "github.com/cloudstic/cli/internal/sftp"
	"github.com/cloudstic/cli/pkg/source"
)

// relPath returns p relative to base using pure path manipulation
func relPath(base, p string) (string, error) {
	base = path.Clean(base) + "/"
	p = path.Clean(p)
	if !strings.HasPrefix(p, base) {
		return "", fmt.Errorf("%s is not under %s", p, base)
	}
	return strings.TrimPrefix(p, base), nil
}

// sftpOptions holds configuration for an SFTP filesystem source.
type sftpOptions struct {
	port, user      string
	password        string
	privateKeyPath  string
	basePath        string
	hostKeyCallback ssh.HostKeyCallback
	knownHostsPath  string
	client          *sftp.Client
	excludePatterns []string
}

// Option configures an SFTP filesystem source.
type Option func(*sftpOptions)

// WithPort sets the SSH port. Defaults to "22" when empty.
func WithPort(port string) Option {
	return func(o *sftpOptions) {
		o.port = port
	}
}

// WithUser sets the SSH user for authentication.
func WithUser(user string) Option {
	return func(o *sftpOptions) {
		o.user = user
	}
}

// WithPassword sets password authentication.
func WithPassword(password string) Option {
	return func(o *sftpOptions) {
		o.password = password
	}
}

// WithKey sets the path to a PEM-encoded private key for authentication.
func WithKey(keyPath string) Option {
	return func(o *sftpOptions) {
		o.privateKeyPath = keyPath
	}
}

// WithHostKeyCallback sets the host key verification callback.
func WithHostKeyCallback(cb ssh.HostKeyCallback) Option {
	return func(o *sftpOptions) {
		o.hostKeyCallback = cb
	}
}

// WithKnownHosts sets the path to the known_hosts file.
func WithKnownHosts(path string) Option {
	return func(o *sftpOptions) {
		o.knownHostsPath = path
	}
}

// WithBasePath sets the root directory on the SFTP server.
func WithBasePath(basePath string) Option {
	return func(o *sftpOptions) {
		o.basePath = basePath
	}
}

// WithClient provides a pre-configured SFTP client, skipping
// internal connection setup. When set, server and auth options are ignored.
func WithClient(client *sftp.Client) Option {
	return func(o *sftpOptions) {
		o.client = client
	}
}

// WithExcludePatterns sets the patterns used to exclude files and folders.
func WithExcludePatterns(patterns []string) Option {
	return func(o *sftpOptions) {
		o.excludePatterns = patterns
	}
}

// Source implements Source for a remote SFTP filesystem.
type Source struct {
	client   *sftp.Client
	rootPath string
	host     string
	user     string
	exclude  *source.ExcludeMatcher
}

// New creates an SFTP-backed source for the given host.
// Either WithClient or authentication options must be provided,
// along with WithBasePath.
func New(host string, opts ...Option) (*Source, error) {
	var o sftpOptions
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
			return nil, fmt.Errorf("sftp source connect: %w", err)
		}
	}

	return &Source{
		client:   client,
		rootPath: o.basePath,
		host:     host,
		user:     o.user,
		exclude:  source.NewExcludeMatcher(o.excludePatterns),
	}, nil
}

// Close releases the underlying SFTP and SSH connections.
func (s *Source) Close() error {
	return s.client.Close()
}

func (s *Source) Info() source.SourceInfo {
	identity := fmt.Sprintf("%s@%s", s.user, s.host)
	return source.SourceInfo{
		Type:     "sftp",
		Account:  identity,
		Path:     s.rootPath,
		Identity: identity,
		PathID:   s.rootPath,
		FsType:   "sftp",
	}
}

func (s *Source) Walk(ctx context.Context, callback func(source.FileMeta) error) error {
	walker := s.client.Walk(s.rootPath)
	// Track excluded directory prefixes so we can skip their children
	// (the sftp walker does not support SkipDir).
	var excludedDirs []string
	for walker.Step() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := walker.Err(); err != nil {
			return err
		}

		info := walker.Stat()
		p := walker.Path()

		rel, err := relPath(s.rootPath, p)
		if err != nil {
			continue // skip root itself
		}
		if rel == "" {
			continue
		}

		// Check if this entry is inside a previously excluded directory.
		if source.IsUnderExcludedDir(rel, excludedDirs) {
			continue
		}

		// Apply exclude patterns.
		if !s.exclude.Empty() && s.exclude.Excludes(rel, info.IsDir()) {
			if info.IsDir() {
				excludedDirs = append(excludedDirs, rel+"/")
			}
			continue
		}

		var fileType source.FileType
		if info.IsDir() {
			fileType = source.FileTypeFolder
		} else {
			fileType = source.FileTypeFile
		}

		var parents []string
		if dir := path.Dir(rel); dir != "." {
			parents = []string{dir}
		}

		meta := source.FileMeta{
			FileID:  rel,
			Name:    path.Base(p),
			Type:    fileType,
			Parents: parents,
			Paths:   []string{rel},
			Size:    info.Size(),
			Mtime:   info.ModTime().Unix(),
		}

		// Extract POSIX metadata from SFTPv3 Attrs.
		if fs, ok := info.Sys().(*sftp.FileStat); ok {
			meta.Mode = fs.Mode & 0xFFF
			meta.Uid = fs.UID
			meta.Gid = fs.GID
		}

		if err := callback(meta); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) Size(ctx context.Context) (*source.SourceSize, error) {
	var totalBytes, totalFiles int64
	var excludedDirs []string
	walker := s.client.Walk(s.rootPath)
	for walker.Step() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err := walker.Err(); err != nil {
			return nil, err
		}
		info := walker.Stat()
		p := walker.Path()
		if !s.exclude.Empty() {
			rel, relErr := relPath(s.rootPath, p)
			if relErr == nil && rel != "" {
				if source.IsUnderExcludedDir(rel, excludedDirs) {
					continue
				}
				if s.exclude.Excludes(rel, info.IsDir()) {
					if info.IsDir() {
						excludedDirs = append(excludedDirs, rel+"/")
					}
					continue
				}
			}
		}
		if !info.IsDir() {
			totalBytes += info.Size()
			totalFiles++
		}
	}
	return &source.SourceSize{Bytes: totalBytes, Files: totalFiles}, nil
}

func (s *Source) GetFileStream(fileID string) (io.ReadCloser, error) {
	fullPath := path.Join(s.rootPath, fileID)
	return s.client.Open(fullPath)
}
