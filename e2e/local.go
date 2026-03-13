package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

type localSource struct {
	dir string
}

func newLocalSource(t *testing.T) *localSource {
	return &localSource{dir: t.TempDir()}
}

func (s *localSource) Name() string { return "local" }
func (s *localSource) Env() TestEnv { return Hermetic }
func (s *localSource) Setup(t *testing.T) []string {
	return []string{"-source", "local:" + s.dir}
}
func (s *localSource) WriteFile(t *testing.T, relPath, content string) {
	t.Helper()
	fullPath := filepath.Join(s.dir, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

type localStore struct {
	dir string
}

func newLocalStore(t *testing.T) *localStore {
	return &localStore{dir: t.TempDir()}
}

func (s *localStore) Name() string { return "local" }
func (s *localStore) Env() TestEnv { return Hermetic }
func (s *localStore) Setup(t *testing.T) []string {
	return []string{"-store", "local:" + s.dir}
}
