//go:build linux || darwin

package source

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"golang.org/x/sys/unix"
)

func TestLocalSource_Walk_ExtendedMeta(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file with specific permissions.
	filePath := filepath.Join(tmpDir, "script.sh")
	if err := os.WriteFile(filePath, []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	s := NewLocalSource(tmpDir)

	var files []core.FileMeta
	err := s.Walk(context.Background(), func(fm core.FileMeta) error {
		files = append(files, fm)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	fm := files[0]

	// Mode should have execute bit set.
	if fm.Mode == 0 {
		t.Error("expected Mode to be populated")
	}
	if fm.Mode&0111 == 0 {
		t.Errorf("expected execute bits in mode %04o", fm.Mode)
	}

	// Uid/Gid should be populated (at least on local filesystem).
	// We just check they're not both zero on a non-root system.
	if os.Getuid() != 0 && fm.Uid == 0 {
		t.Error("expected non-zero Uid for non-root user")
	}

	// Btime may or may not be available depending on fs/kernel.
	// On macOS it should always be present.
	if runtime.GOOS == "darwin" && fm.Btime == 0 {
		t.Error("expected Btime to be populated on macOS")
	}
}

func TestLocalSource_Walk_SkipMode(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	s := NewLocalSource(tmpDir, WithSkipMode())

	var files []core.FileMeta
	err := s.Walk(context.Background(), func(fm core.FileMeta) error {
		files = append(files, fm)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	fm := files[0]
	if fm.Mode != 0 || fm.Uid != 0 || fm.Gid != 0 || fm.Btime != 0 || fm.Flags != 0 {
		t.Errorf("expected all extended fields to be zero with SkipMode, got mode=%o uid=%d gid=%d btime=%d flags=%d",
			fm.Mode, fm.Uid, fm.Gid, fm.Btime, fm.Flags)
	}
}

func TestLocalSource_Walk_SkipXattrs(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	s := NewLocalSource(tmpDir, WithSkipXattrs())

	var files []core.FileMeta
	err := s.Walk(context.Background(), func(fm core.FileMeta) error {
		files = append(files, fm)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	if files[0].Xattrs != nil {
		t.Error("expected Xattrs to be nil with SkipXattrs")
	}
}

func TestLocalSource_Walk_SkipFlags(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	s := NewLocalSource(tmpDir, WithSkipFlags())

	var files []core.FileMeta
	err := s.Walk(context.Background(), func(fm core.FileMeta) error {
		files = append(files, fm)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	// Mode/Uid/Gid should still be populated (only flags are skipped).
	fm := files[0]
	if fm.Mode == 0 {
		t.Error("expected Mode to be populated even with SkipFlags")
	}
	// Flags should be zero (skipped).
	if fm.Flags != 0 {
		t.Errorf("expected Flags=0 with SkipFlags, got %d", fm.Flags)
	}
}

func TestLocalSource_Walk_XattrNamespaces(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "file.txt")

	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Set an xattr to test namespace filtering.
	if runtime.GOOS == "darwin" {
		// macOS: xattrs don't have namespace prefixes, but we use com.test.
		if err := unix.Setxattr(filePath, "com.test.tag", []byte("val"), 0); err != nil {
			t.Skip("cannot set xattr:", err)
		}
	} else {
		if err := unix.Setxattr(filePath, "user.test.tag", []byte("val"), 0); err != nil {
			t.Skip("cannot set xattr:", err)
		}
	}

	// Use a namespace that doesn't match the xattr we set.
	s := NewLocalSource(tmpDir, WithXattrNamespaces([]string{"nomatch."}))

	var files []core.FileMeta
	err := s.Walk(context.Background(), func(fm core.FileMeta) error {
		files = append(files, fm)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	if files[0].Xattrs != nil {
		t.Error("expected Xattrs to be nil when namespace doesn't match")
	}
}

func TestLocalSource_Walk_Xattr_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "file.txt")

	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Set an xattr.
	var attrName string
	if runtime.GOOS == "darwin" {
		attrName = "com.test.tag"
	} else {
		attrName = "user.test.tag"
	}
	if err := unix.Setxattr(filePath, attrName, []byte("hello"), 0); err != nil {
		t.Skip("cannot set xattr:", err)
	}

	s := NewLocalSource(tmpDir)

	var files []core.FileMeta
	err := s.Walk(context.Background(), func(fm core.FileMeta) error {
		files = append(files, fm)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	fm := files[0]
	if fm.Xattrs == nil {
		t.Fatal("expected Xattrs to be populated")
	}

	val, ok := fm.Xattrs[attrName]
	if !ok {
		t.Fatalf("expected xattr %q to be present, got keys: %v", attrName, fm.Xattrs)
	}
	if string(val) != "hello" {
		t.Errorf("expected xattr value %q, got %q", "hello", string(val))
	}
}

func TestListXattrs_NoAttrs(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "file.txt")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	// File with no xattrs should return nil.
	result := listXattrs(filePath, nil)
	// May or may not be nil depending on macOS quarantine attrs.
	// Just verify it doesn't error/panic.
	_ = result
}

func TestListXattrs_NonexistentPath(t *testing.T) {
	result := listXattrs("/nonexistent/path/xyz", nil)
	if result != nil {
		t.Error("expected nil for nonexistent path")
	}
}

func TestGetXattr_NonexistentAttr(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "file.txt")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := getXattr(filePath, "user.nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent xattr")
	}
}

func TestLocalSource_Info_FsType(t *testing.T) {
	tmpDir := t.TempDir()
	s := NewLocalSource(tmpDir)
	info := s.Info()

	if info.FsType == "" {
		t.Error("expected FsType to be populated on local filesystem")
	}
}

func TestSplitXattrNames(t *testing.T) {
	tests := []struct {
		name string
		buf  []byte
		want []string
	}{
		{"empty", nil, nil},
		{"single", []byte("user.tag\x00"), []string{"user.tag"}},
		{"multiple", []byte("user.a\x00user.b\x00"), []string{"user.a", "user.b"}},
		{"no trailing null", []byte("user.a"), []string{"user.a"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitXattrNames(tt.buf)
			if len(got) != len(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestHasPrefix(t *testing.T) {
	if !hasPrefix("user.tag", []string{"user."}) {
		t.Error("expected match")
	}
	if hasPrefix("security.label", []string{"user.", "com.apple."}) {
		t.Error("expected no match")
	}
	if !hasPrefix("com.apple.quarantine", []string{"com.apple."}) {
		t.Error("expected match")
	}
}
