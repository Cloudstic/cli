package externalmod

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/cloudstic/cli/pkg/source"
)

// Source implements source.Source using only github.com/cloudstic/cli/pkg/source.
// That single import is the whole point: no internal/ package, and no provider
// SDK pulled in transitively.
type Source struct{ files map[string]string }

var (
	_ source.Source            = (*Source)(nil)
	_ source.IncrementalSource = (*Source)(nil)
)

func (s *Source) Walk(ctx context.Context, cb func(source.FileMeta) error) error {
	// Parents before children, per the contract.
	if err := cb(source.FileMeta{FileID: "docs", Name: "docs", Type: source.FileTypeFolder}); err != nil {
		return err
	}
	for name, body := range s.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := cb(source.FileMeta{
			FileID:  "docs/" + name,
			Name:    name,
			Type:    source.FileTypeFile,
			Parents: []string{"docs"},
			Size:    int64(len(body)),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) GetFileStream(fileID string) (io.ReadCloser, error) {
	body, ok := s.files[strings.TrimPrefix(fileID, "docs/")]
	if !ok {
		return nil, fmt.Errorf("no such file: %s", fileID)
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func (s *Source) Info() source.SourceInfo {
	return source.SourceInfo{
		Type:     "com.example.externalmod",
		Identity: "fixture",
		PathID:   "/docs",
		Path:     "/docs",
	}
}

func (s *Source) Size(context.Context) (*source.SourceSize, error) {
	var total int64
	for _, b := range s.files {
		total += int64(len(b))
	}
	return &source.SourceSize{Bytes: total, Files: int64(len(s.files))}, nil
}

func (s *Source) GetStartPageToken() (string, error) { return "t0", nil }

func (s *Source) WalkChanges(ctx context.Context, token string, cb func(source.FileChange) error) (string, error) {
	err := cb(source.FileChange{
		Type: source.ChangeUpsert,
		Meta: source.FileMeta{FileID: "docs/new.txt", Name: "new.txt", Type: source.FileTypeFile},
	})
	return "t1", err
}
