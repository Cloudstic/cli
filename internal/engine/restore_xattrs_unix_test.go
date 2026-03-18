//go:build linux || darwin

package engine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
	"golang.org/x/sys/unix"
)

func TestApplyRestoreXattrs_SetsAllXattrs(t *testing.T) {
	orig := setRestoreXattr
	defer func() { setRestoreXattr = orig }()

	got := map[string][]byte{}
	setRestoreXattr = func(path, name string, value []byte, flags int) error {
		if path != "/tmp/file" {
			t.Fatalf("path=%q", path)
		}
		if flags != 0 {
			t.Fatalf("flags=%d", flags)
		}
		got[name] = append([]byte(nil), value...)
		return nil
	}

	err := applyRestoreXattrs("/tmp/file", core.FileMeta{Xattrs: map[string][]byte{
		"user.a": []byte("one"),
		"user.b": []byte("two"),
	}}, nil)
	if err != nil {
		t.Fatalf("applyRestoreXattrs: %v", err)
	}
	if string(got["user.a"]) != "one" || string(got["user.b"]) != "two" {
		t.Fatalf("unexpected xattrs: %#v", got)
	}
}

func TestApplyRestoreXattrs_IgnoresBestEffortErrors(t *testing.T) {
	orig := setRestoreXattr
	defer func() { setRestoreXattr = orig }()

	setRestoreXattr = func(path, name string, value []byte, flags int) error {
		return unix.EPERM
	}

	err := applyRestoreXattrs("/tmp/file", core.FileMeta{Xattrs: map[string][]byte{"user.a": []byte("one")}}, nil)
	if err != nil {
		t.Fatalf("applyRestoreXattrs: %v", err)
	}
}

func TestApplyRestoreXattrs_ReturnsUnexpectedErrors(t *testing.T) {
	orig := setRestoreXattr
	defer func() { setRestoreXattr = orig }()

	setRestoreXattr = func(path, name string, value []byte, flags int) error {
		return errors.New("boom")
	}

	err := applyRestoreXattrs("/tmp/file", core.FileMeta{Xattrs: map[string][]byte{"user.a": []byte("one")}}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestApplyRestoreXattrs_SkipsKnownNonRestorableXattrs(t *testing.T) {
	orig := setRestoreXattr
	defer func() { setRestoreXattr = orig }()

	called := false
	setRestoreXattr = func(path, name string, value []byte, flags int) error {
		called = true
		return nil
	}

	err := applyRestoreXattrs("/tmp/file", core.FileMeta{Xattrs: map[string][]byte{"com.apple.provenance": []byte("x")}}, nil)
	if err != nil {
		t.Fatalf("applyRestoreXattrs: %v", err)
	}
	if called {
		t.Fatal("expected known non-restorable xattr to be skipped")
	}
}

func TestRestoreManager_RunToDir_ReplaysXattrs(t *testing.T) {
	orig := setRestoreXattr
	defer func() { setRestoreXattr = orig }()

	seen := map[string][]byte{}
	setRestoreXattr = func(path, name string, value []byte, flags int) error {
		seen[path+"::"+name] = append([]byte(nil), value...)
		return nil
	}

	src := NewMockSource()
	dest := NewMockStore()
	src.Files["id_dir"] = MockFile{
		Meta: core.FileMeta{FileID: "id_dir", Name: "dir", Type: core.FileTypeFolder, Xattrs: map[string][]byte{"user.dir": []byte("d")}},
	}
	src.Files["id_file"] = MockFile{
		Meta:    core.FileMeta{FileID: "id_file", Name: "file.txt", Parents: []string{"id_dir"}, Xattrs: map[string][]byte{"user.file": []byte("f")}},
		Content: []byte("content"),
	}
	bkMgr := NewBackupManager(src, dest, ui.NewNoOpReporter(), nil)
	if _, err := bkMgr.Run(t.Context()); err != nil {
		t.Fatalf("backup setup failed: %v", err)
	}

	outDir := filepath.Join(t.TempDir(), "restore")
	writer, err := NewFSRestoreWriter(outDir)
	if err != nil {
		t.Fatalf("NewFSRestoreWriter: %v", err)
	}
	rsMgr := NewRestoreManager(store.NewCompressedStore(dest), ui.NewNoOpReporter())
	if _, err := rsMgr.Run(t.Context(), writer, ""); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if string(seen[filepath.Join(outDir, "dir")+"::user.dir"]) != "d" {
		t.Fatalf("directory xattr not replayed: %#v", seen)
	}
	if string(seen[filepath.Join(outDir, "dir", "file.txt")+"::user.file"]) != "f" {
		t.Fatalf("file xattr not replayed: %#v", seen)
	}
	if b, err := os.ReadFile(filepath.Join(outDir, "dir", "file.txt")); err != nil || string(b) != "content" {
		t.Fatalf("restored file mismatch: %q %v", string(b), err)
	}
}
