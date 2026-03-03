package store

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/cloudstic/cli/internal/core"
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
	exclude  *ExcludeMatcher
}

// NewLocalSource creates a local filesystem source rooted at rootPath.
// Optional exclude patterns use gitignore-style syntax to skip files and
// directories during Walk and Size.
func NewLocalSource(rootPath string, excludePatterns ...string) *LocalSource {
	return &LocalSource{
		RootPath: rootPath,
		exclude:  NewExcludeMatcher(excludePatterns),
	}
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

		// Apply exclude patterns.
		if !s.exclude.Empty() && s.exclude.Excludes(filepath.ToSlash(relPath), info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
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
			Paths:   []string{relPath},
			Size:    info.Size(),
			Mtime:   info.ModTime().Unix(),
		}

		return callback(meta)
	})
}

func (s *LocalSource) Size(ctx context.Context) (*SourceSize, error) {
	var totalBytes, totalFiles int64
	err := filepath.Walk(s.RootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !s.exclude.Empty() {
			relPath, relErr := filepath.Rel(s.RootPath, path)
			if relErr == nil && relPath != "." {
				if s.exclude.Excludes(filepath.ToSlash(relPath), info.IsDir()) {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
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
