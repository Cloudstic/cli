package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cloudstic/cli/pkg/store"
)

// tempFilePattern names in-progress writes. List skips anything matching it:
// a partial write is not an object, and returning one lets a caller act on a
// name that is about to disappear — or, for prune, delete another writer's
// half-finished pack as an orphan.
const tempFilePattern = ".cloudstic-tmp-*"

// Store implements store.ObjectStore for the local filesystem.
type Store struct {
	BasePath  string
	knownDirs sync.Map
}

func New(basePath string) (*Store, error) {
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, err
	}
	return &Store{BasePath: basePath}, nil
}

func (s *Store) getPath(key string) string {
	parts := strings.Split(key, "/")
	return filepath.Join(s.BasePath, filepath.Join(parts...))
}

func (s *Store) Put(_ context.Context, key string, data []byte) error {
	fullPath := s.getPath(key)
	dir := filepath.Dir(fullPath)

	if _, ok := s.knownDirs.Load(dir); !ok {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		s.knownDirs.Store(dir, struct{}{})
	}

	// A unique temporary name per write. A fixed "<path>.tmp" is shared by every
	// writer of the same key, so two concurrent writers would interleave into
	// one file and each rename a possibly torn result into place. Keys are
	// content-addressed, so both are writing identical bytes — but only if
	// neither corrupts the other's buffer first.
	tmp, err := os.CreateTemp(dir, tempFilePattern)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // No-op once renamed.

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0644); err != nil {
		return err
	}
	return os.Rename(tmpName, fullPath)
}

func (s *Store) Get(_ context.Context, key string) ([]byte, error) {
	data, err := os.ReadFile(s.getPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s: %w", key, store.ErrNotFound)
		}
		return nil, err
	}
	return data, nil
}

// GetRange implements RangeGetter, letting callers read a packfile footer
// without loading the whole object.
func (s *Store) GetRange(_ context.Context, key string, offset, length int64) ([]byte, error) {
	f, err := os.Open(s.getPath(key))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, length)
	if _, err := io.ReadFull(io.NewSectionReader(f, offset, length), buf); err != nil {
		return nil, fmt.Errorf("read %s at %d+%d: %w", key, offset, length, err)
	}
	return buf, nil
}

func (s *Store) Exists(_ context.Context, key string) (bool, error) {
	_, err := os.Stat(s.getPath(key))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (s *Store) Delete(_ context.Context, key string) error {
	return os.Remove(s.getPath(key))
}

func (s *Store) Size(_ context.Context, key string) (int64, error) {
	info, err := os.Stat(s.getPath(key))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s *Store) TotalSize(_ context.Context) (int64, error) {
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

func (s *Store) Flush(ctx context.Context) error {
	return nil
}

// List returns all keys matching the given prefix. When a prefix is provided
// the walk is scoped to just that subdirectory for efficiency.
func (s *Store) List(_ context.Context, prefix string) ([]string, error) {
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
		// An in-progress write is not an object. Returning one hands the caller
		// a name that may vanish before it is read, or that prune would treat
		// as an orphan and delete out from under the writer.
		if strings.HasPrefix(info.Name(), ".cloudstic-tmp-") {
			return nil
		}

		key := filepath.ToSlash(relPath)
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	return keys, err
}
