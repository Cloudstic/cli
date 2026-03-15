package source

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/cloudstic/cli/internal/core"
	intsftp "github.com/cloudstic/cli/internal/sftp"
	"github.com/pkg/sftp"
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
	client          *sftp.Client
	excludePatterns []string
}

// SFTPOption configures an SFTP filesystem source.
type SFTPOption func(*sftpOptions)

// WithSFTPSourcePort sets the SSH port. Defaults to "22" when empty.
func WithSFTPSourcePort(port string) SFTPOption {
	return func(o *sftpOptions) {
		o.port = port
	}
}

// WithSFTPSourceUser sets the SSH user for authentication.
func WithSFTPSourceUser(user string) SFTPOption {
	return func(o *sftpOptions) {
		o.user = user
	}
}

// WithSFTPSourcePassword sets password authentication.
func WithSFTPSourcePassword(password string) SFTPOption {
	return func(o *sftpOptions) {
		o.password = password
	}
}

// WithSFTPSourceKey sets the path to a PEM-encoded private key for authentication.
func WithSFTPSourceKey(keyPath string) SFTPOption {
	return func(o *sftpOptions) {
		o.privateKeyPath = keyPath
	}
}

// WithSFTPSourceBasePath sets the root directory on the SFTP server.
func WithSFTPSourceBasePath(basePath string) SFTPOption {
	return func(o *sftpOptions) {
		o.basePath = basePath
	}
}

// WithSFTPSourceClient provides a pre-configured SFTP client, skipping
// internal connection setup. When set, server and auth options are ignored.
func WithSFTPSourceClient(client *sftp.Client) SFTPOption {
	return func(o *sftpOptions) {
		o.client = client
	}
}

// WithSFTPExcludePatterns sets the patterns used to exclude files and folders.
func WithSFTPExcludePatterns(patterns []string) SFTPOption {
	return func(o *sftpOptions) {
		o.excludePatterns = patterns
	}
}

// SFTPSource implements Source for a remote SFTP filesystem.
type SFTPSource struct {
	client   *sftp.Client
	rootPath string
	host     string
	user     string
	exclude  *ExcludeMatcher
}

// NewSFTPSource creates an SFTP-backed source for the given host.
// Either WithSFTPSourceClient or authentication options must be provided,
// along with WithSFTPSourceBasePath.
func NewSFTPSource(host string, opts ...SFTPOption) (*SFTPSource, error) {
	var o sftpOptions
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
			return nil, fmt.Errorf("sftp source connect: %w", err)
		}
	}

	return &SFTPSource{
		client:   client,
		rootPath: o.basePath,
		host:     host,
		user:     o.user,
		exclude:  NewExcludeMatcher(o.excludePatterns),
	}, nil
}

// Close releases the underlying SFTP and SSH connections.
func (s *SFTPSource) Close() error {
	return s.client.Close()
}

func (s *SFTPSource) Info() core.SourceInfo {
	identity := fmt.Sprintf("%s@%s", s.user, s.host)
	return core.SourceInfo{
		Type:     "sftp",
		Account:  identity,
		Path:     s.rootPath,
		Identity: identity,
		PathID:   s.rootPath,
		FsType:   "sftp",
	}
}

func (s *SFTPSource) Walk(ctx context.Context, callback func(core.FileMeta) error) error {
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
		if isUnderExcludedDir(rel, excludedDirs) {
			continue
		}

		// Apply exclude patterns.
		if !s.exclude.Empty() && s.exclude.Excludes(rel, info.IsDir()) {
			if info.IsDir() {
				excludedDirs = append(excludedDirs, rel+"/")
			}
			continue
		}

		var fileType core.FileType
		if info.IsDir() {
			fileType = core.FileTypeFolder
		} else {
			fileType = core.FileTypeFile
		}

		var parents []string
		if dir := path.Dir(rel); dir != "." {
			parents = []string{dir}
		}

		meta := core.FileMeta{
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

func (s *SFTPSource) Size(ctx context.Context) (*SourceSize, error) {
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
				if isUnderExcludedDir(rel, excludedDirs) {
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
	return &SourceSize{Bytes: totalBytes, Files: totalFiles}, nil
}

func (s *SFTPSource) GetFileStream(fileID string) (io.ReadCloser, error) {
	fullPath := path.Join(s.rootPath, fileID)
	return s.client.Open(fullPath)
}
