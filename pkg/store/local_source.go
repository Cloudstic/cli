package store

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/cloudstic/cli/pkg/core"
)

func (s *LocalSource) Info() core.SourceInfo {
	hostname, _ := os.Hostname()
	absPath, _ := filepath.Abs(s.RootPath)
	return core.SourceInfo{
		Type:    "local",
		Account: hostname,
		Path:    absPath,
	}
}

// LocalSource implements Source for local filesystem
type LocalSource struct {
	RootPath string
}

func NewLocalSource(rootPath string) *LocalSource {
	return &LocalSource{RootPath: rootPath}
}

func (s *LocalSource) Walk(ctx context.Context, callback func(core.FileMeta) error) error {
	return filepath.Walk(s.RootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(s.RootPath, path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		var fileType core.FileType
		if info.IsDir() {
			fileType = core.FileTypeFolder
		} else {
			fileType = core.FileTypeFile
		}

		var parents []string
		if dir := filepath.Dir(relPath); dir != "." {
			parents = []string{dir}
		}

		meta := core.FileMeta{
			FileID:  relPath,
			Name:    filepath.Base(path),
			Type:    fileType,
			Parents: parents,
			Size:    info.Size(),
			Mtime:   info.ModTime().Unix(),
		}

		return callback(meta)
	})
}

func (s *LocalSource) Size(ctx context.Context) (*SourceSize, error) {
	var totalBytes, totalFiles int64
	err := filepath.Walk(s.RootPath, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !info.IsDir() {
			totalBytes += info.Size()
			totalFiles++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &SourceSize{Bytes: totalBytes, Files: totalFiles}, nil
}

func (s *LocalSource) GetFileStream(fileID string) (io.ReadCloser, error) {
	// fileID is relPath
	fullPath := filepath.Join(s.RootPath, fileID)
	return os.Open(fullPath)
}
