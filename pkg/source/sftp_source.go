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
	sftpConfig      intsftp.Config
	excludePatterns []string
}

// SFTPOption configures an SFTP filesystem source.
type SFTPOption func(*sftpOptions)

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
	cfg      intsftp.Config
	exclude  *ExcludeMatcher
}

// NewSFTPSource connects to the SFTP server described by cfg and returns a
// source rooted at cfg.BasePath.
func NewSFTPSource(ctx context.Context, cfg intsftp.Config, opts ...SFTPOption) (*SFTPSource, error) {
	options := sftpOptions{sftpConfig: cfg}
	for _, opt := range opts {
		opt(&options)
	}

	client, err := intsftp.Dial(cfg)
	if err != nil {
		return nil, fmt.Errorf("sftp source connect: %w", err)
	}
	return &SFTPSource{client: client, rootPath: cfg.BasePath, cfg: cfg, exclude: NewExcludeMatcher(options.excludePatterns)}, nil
}

// Close releases the underlying SFTP and SSH connections.
func (s *SFTPSource) Close() error {
	return s.client.Close()
}

func (s *SFTPSource) Info() core.SourceInfo {
	return core.SourceInfo{
		Type:    "sftp",
		Account: fmt.Sprintf("%s@%s", s.cfg.User, s.cfg.Host),
		Path:    s.rootPath,
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
