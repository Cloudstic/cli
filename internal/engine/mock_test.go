package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/source"
)

// MockStore implements store.ObjectStore. It is safe for concurrent use.
type MockStore struct {
	mu   sync.RWMutex
	Data map[string][]byte
}

func NewMockStore() *MockStore {
	return &MockStore{
		Data: make(map[string][]byte),
	}
}

func (s *MockStore) Put(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	s.Data[key] = data
	s.mu.Unlock()
	return nil
}

func (s *MockStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	data, ok := s.Data[key]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	return data, nil
}

func (s *MockStore) Exists(_ context.Context, key string) (bool, error) {
	s.mu.RLock()
	_, ok := s.Data[key]
	s.mu.RUnlock()
	return ok, nil
}

func (s *MockStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.Data, key)
	s.mu.Unlock()
	return nil
}

func (s *MockStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var keys []string
	for k := range s.Data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (s *MockStore) Size(_ context.Context, key string) (int64, error) {
	s.mu.RLock()
	data, ok := s.Data[key]
	s.mu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("key not found: %s", key)
	}
	return int64(len(data)), nil
}

func (s *MockStore) TotalSize(_ context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total int64
	for _, d := range s.Data {
		total += int64(len(d))
	}
	return total, nil
}

func (s *MockStore) Flush(_ context.Context) error {
	return nil
}

// MockSource implements source.Source
type MockSource struct {
	Files map[string]MockFile
}

type MockFile struct {
	Meta    core.FileMeta
	Content []byte
}

func NewMockSource() *MockSource {
	return &MockSource{
		Files: make(map[string]MockFile),
	}
}

func (s *MockSource) Info() core.SourceInfo {
	return core.SourceInfo{Type: "mock"}
}

func (s *MockSource) AddFile(name, id string, content []byte) {
	s.Files[id] = MockFile{
		Meta: core.FileMeta{
			FileID: id,
			Name:   name,
			Type:   core.FileTypeFile,
			Size:   int64(len(content)),
			Mtime:  time.Now().Unix(),
		},
		Content: content,
	}
}

func (s *MockSource) Walk(ctx context.Context, callback func(core.FileMeta) error) error {
	processed := make(map[string]bool)

	var process func(id string) error
	process = func(id string) error {
		if processed[id] {
			return nil
		}

		f, ok := s.Files[id]
		if !ok {
			return nil
		}

		for _, pid := range f.Meta.Parents {
			if err := process(pid); err != nil {
				return err
			}
		}

		processed[id] = true
		return callback(f.Meta)
	}

	for id := range s.Files {
		if err := process(id); err != nil {
			return err
		}
	}

	return nil
}

func (s *MockSource) Size(ctx context.Context) (*source.SourceSize, error) {
	var total int64
	var count int64
	for _, f := range s.Files {
		total += int64(len(f.Content))
		count++
	}
	return &source.SourceSize{Bytes: total, Files: count}, nil
}

func (s *MockSource) GetFileStream(fileID string) (io.ReadCloser, error) {
	f, ok := s.Files[fileID]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", fileID)
	}
	return io.NopCloser(bytes.NewReader(f.Content)), nil
}
