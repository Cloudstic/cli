package local

import (
	"path/filepath"
	"testing"
)

func TestSourceInfo_UsesRelativePathOnlyWithinMount(t *testing.T) {
	abs := filepath.Join(string(filepath.Separator), "Users", "alice", "workspace", "repo")
	s := &Source{
		rootPath:         ".",
		volumeUUID:       "UUID-123",
		volumeMountPoint: filepath.Join(string(filepath.Separator), "System", "Volumes", "Data"),
	}

	oldAbs := filepathAbs
	defer func() { filepathAbs = oldAbs }()
	filepathAbs = func(string) (string, error) { return abs, nil }

	info := s.Info()
	if info.Path != abs {
		t.Fatalf("Path = %q, want %q", info.Path, abs)
	}
	if info.PathID != abs {
		t.Fatalf("PathID = %q, want %q", info.PathID, abs)
	}
}

func TestPathWithinRoot(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "Users", "alice")
	if !pathWithinRoot(root, filepath.Join(root, "workspace")) {
		t.Fatal("expected child path to be within root")
	}
	if pathWithinRoot(root, filepath.Join(string(filepath.Separator), "Users", "bob")) {
		t.Fatal("unexpected unrelated path within root")
	}
}
