package e2e

import "testing"

type localSource struct {
	dir string
}

func newLocalSource(t *testing.T) *localSource {
	return &localSource{dir: t.TempDir()}
}

func (s *localSource) Name() string { return "local" }
func (s *localSource) Env() TestEnv { return Hermetic }
func (s *localSource) Setup(t *testing.T) []string {
	return []string{"-source", "local", "-source-path", s.dir}
}
func (s *localSource) WriteFile(t *testing.T, relPath, content string) {
	writeFile(t, s.dir, relPath, content)
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
	return []string{"-store", "local", "-store-path", s.dir}
}
