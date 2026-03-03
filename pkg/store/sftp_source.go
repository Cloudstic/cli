package store

import (
	"context"
	"fmt"
	"io"
	"path"

	"github.com/cloudstic/cli/internal/core"
	"github.com/pkg/sftp"
)

// SFTPSource implements Source for a remote SFTP filesystem.
type SFTPSource struct {
	client   *sftp.Client
	rootPath string
	cfg      SFTPConfig
}

// NewSFTPSource connects to the SFTP server described by cfg and returns a
// source rooted at rootPath.
func NewSFTPSource(cfg SFTPConfig, rootPath string) (*SFTPSource, error) {
	client, err := dialSFTP(cfg)
	if err != nil {
		return nil, fmt.Errorf("sftp source connect: %w", err)
	}
	return &SFTPSource{client: client, rootPath: rootPath, cfg: cfg}, nil
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
	walker := s.client.Walk(s.rootPath)
	for walker.Step() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err := walker.Err(); err != nil {
			return nil, err
		}
		if !walker.Stat().IsDir() {
			totalBytes += walker.Stat().Size()
			totalFiles++
		}
	}
	return &SourceSize{Bytes: totalBytes, Files: totalFiles}, nil
}

func (s *SFTPSource) GetFileStream(fileID string) (io.ReadCloser, error) {
	fullPath := path.Join(s.rootPath, fileID)
	return s.client.Open(fullPath)
}
