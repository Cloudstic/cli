package store

import (
	"os"
	"path/filepath"
	"strings"
)

// LocalStore implements ObjectStore for the local filesystem.
type LocalStore struct {
	BasePath string
}

func NewLocalStore(basePath string) (*LocalStore, error) {
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, err
	}
	return &LocalStore{BasePath: basePath}, nil
}

func (s *LocalStore) getPath(key string) string {
	parts := strings.Split(key, "/")
	return filepath.Join(s.BasePath, filepath.Join(parts...))
}

func (s *LocalStore) Put(key string, data []byte) error {
	fullPath := s.getPath(key)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmpFile := fullPath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpFile, fullPath)
}

func (s *LocalStore) Get(key string) ([]byte, error) {
	return os.ReadFile(s.getPath(key))
}

func (s *LocalStore) Exists(key string) (bool, error) {
	_, err := os.Stat(s.getPath(key))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (s *LocalStore) Delete(key string) error {
	return os.Remove(s.getPath(key))
}

func (s *LocalStore) Size(key string) (int64, error) {
	info, err := os.Stat(s.getPath(key))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s *LocalStore) TotalSize() (int64, error) {
	var total int64
	err := filepath.Walk(s.BasePath, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// List returns all keys matching the given prefix. When a prefix is provided
// the walk is scoped to just that subdirectory for efficiency.
func (s *LocalStore) List(prefix string) ([]string, error) {
	startPath := s.BasePath
	if prefix != "" {
		candidate := filepath.Join(s.BasePath, filepath.FromSlash(prefix))
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			startPath = candidate
		} else {
			dir := filepath.Dir(candidate)
			if _, err := os.Stat(dir); err == nil {
				startPath = dir
			}
		}
	}

	var keys []string
	err := filepath.Walk(startPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(s.BasePath, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(relPath)
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	return keys, err
}
