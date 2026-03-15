package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

func TestIsGoogleNativeMeta(t *testing.T) {
	tests := []struct {
		name string
		meta core.FileMeta
		want bool
	}{
		{"google doc", core.FileMeta{Extra: map[string]interface{}{"mimeType": "application/vnd.google-apps.document"}}, true},
		{"google sheet", core.FileMeta{Extra: map[string]interface{}{"mimeType": "application/vnd.google-apps.spreadsheet"}}, true},
		{"folder", core.FileMeta{Extra: map[string]interface{}{"mimeType": "application/vnd.google-apps.folder"}}, false},
		{"regular file", core.FileMeta{Extra: map[string]interface{}{"mimeType": "application/pdf"}}, false},
		{"no extra", core.FileMeta{}, false},
		{"nil extra", core.FileMeta{Extra: nil}, false},
		{"no mimeType key", core.FileMeta{Extra: map[string]interface{}{"other": "value"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isGoogleNativeMeta(&tt.meta); got != tt.want {
				t.Errorf("isGoogleNativeMeta() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectChange_NativeFileFastPath(t *testing.T) {
	ctx := context.Background()
	src := NewMockSource()
	dest := NewMockStore()

	// First backup: a Google Doc with headRevisionId "rev1".
	src.Files["DOC_1"] = MockFile{
		Meta: core.FileMeta{
			FileID: "DOC_1",
			Name:   "Notes.docx",
			Type:   core.FileTypeFile,
			Size:   0, // native files report 0 from Walk
			Mtime:  1000,
			Extra: map[string]interface{}{
				"mimeType":       "application/vnd.google-apps.document",
				"exportMimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
				"headRevisionId": "rev1",
			},
		},
		Content: []byte("exported docx content"),
	}

	mgr := NewBackupManager(src, dest, ui.NewNoOpReporter(), nil)
	result1, err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("First backup failed: %v", err)
	}

	// Verify the file was stored.
	readStore := store.NewCompressedStore(dest)
	tree := hamt.NewTree(readStore)
	ref1, err := tree.Lookup(result1.Root, "", "DOC_1")
	if err != nil || ref1 == "" {
		t.Fatalf("DOC_1 not found in first snapshot: ref=%q err=%v", ref1, err)
	}

	// Second backup: same headRevisionId → should detect as unchanged.
	mgr2 := NewBackupManager(src, dest, ui.NewNoOpReporter(), nil)
	result2, err := mgr2.Run(ctx)
	if err != nil {
		t.Fatalf("Second backup failed: %v", err)
	}

	if result2.FilesChanged != 0 {
		t.Errorf("Expected 0 changed files (same headRevisionId), got %d", result2.FilesChanged)
	}
	if result2.FilesUnmodified != 1 {
		t.Errorf("Expected 1 unmodified file, got %d", result2.FilesUnmodified)
	}

	// Third backup: different headRevisionId → should detect as changed.
	src.Files["DOC_1"] = MockFile{
		Meta: core.FileMeta{
			FileID: "DOC_1",
			Name:   "Notes.docx",
			Type:   core.FileTypeFile,
			Size:   0,
			Mtime:  2000,
			Extra: map[string]interface{}{
				"mimeType":       "application/vnd.google-apps.document",
				"exportMimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
				"headRevisionId": "rev2",
			},
		},
		Content: []byte("new exported docx content"),
	}

	mgr3 := NewBackupManager(src, dest, ui.NewNoOpReporter(), nil)
	result3, err := mgr3.Run(ctx)
	if err != nil {
		t.Fatalf("Third backup failed: %v", err)
	}

	if result3.FilesChanged != 1 {
		t.Errorf("Expected 1 changed file (different headRevisionId), got %d", result3.FilesChanged)
	}

	// Verify the stored content changed.
	ref3, err := tree.Lookup(result3.Root, "", "DOC_1")
	if err != nil || ref3 == "" {
		t.Fatalf("DOC_1 not found in third snapshot: ref=%q err=%v", ref3, err)
	}
	if ref3 == ref1 {
		t.Error("Expected different ref after headRevisionId change")
	}
}

func TestDetectChange_NativeFileEmptyRevID(t *testing.T) {
	ctx := context.Background()
	src := NewMockSource()
	dest := NewMockStore()

	// Native file without headRevisionId should always be treated as changed.
	src.Files["DOC_1"] = MockFile{
		Meta: core.FileMeta{
			FileID: "DOC_1",
			Name:   "Notes.docx",
			Type:   core.FileTypeFile,
			Size:   0,
			Mtime:  1000,
			Extra: map[string]interface{}{
				"mimeType":       "application/vnd.google-apps.document",
				"exportMimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			},
		},
		Content: []byte("exported docx content"),
	}

	mgr := NewBackupManager(src, dest, ui.NewNoOpReporter(), nil)
	result1, err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("First backup failed: %v", err)
	}
	if result1.FilesNew != 1 {
		t.Errorf("Expected 1 new file, got %d", result1.FilesNew)
	}

	// Second backup: still no headRevisionId → should be treated as changed.
	mgr2 := NewBackupManager(src, dest, ui.NewNoOpReporter(), nil)
	result2, err := mgr2.Run(ctx)
	if err != nil {
		t.Fatalf("Second backup failed: %v", err)
	}
	if result2.FilesChanged != 1 {
		t.Errorf("Expected 1 changed file (empty headRevisionId), got %d", result2.FilesChanged)
	}
}

func TestDetectChange_NativeFileCarriesForwardMetadata(t *testing.T) {
	ctx := context.Background()
	src := NewMockSource()
	dest := NewMockStore()

	src.Files["DOC_1"] = MockFile{
		Meta: core.FileMeta{
			FileID: "DOC_1",
			Name:   "Notes.docx",
			Type:   core.FileTypeFile,
			Size:   0,
			Extra: map[string]interface{}{
				"mimeType":       "application/vnd.google-apps.document",
				"headRevisionId": "rev1",
			},
		},
		Content: []byte("content"),
	}

	mgr := NewBackupManager(src, dest, ui.NewNoOpReporter(), nil)
	result1, err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("First backup failed: %v", err)
	}

	// Read the stored meta to get the ContentHash and Size set by the upload.
	readStore := store.NewCompressedStore(dest)
	ref, err := hamt.NewTree(readStore).Lookup(result1.Root, "", "DOC_1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	data, err := readStore.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var storedMeta core.FileMeta
	if err := json.Unmarshal(data, &storedMeta); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if storedMeta.ContentHash == "" {
		t.Fatal("Expected ContentHash to be set after upload")
	}
	if storedMeta.Size == 0 {
		t.Fatal("Expected Size to be set after upload")
	}

	// Second backup with same revID: the unchanged path should carry forward
	// ContentHash and Size from the first backup.
	mgr2 := NewBackupManager(src, dest, ui.NewNoOpReporter(), nil)
	result2, err := mgr2.Run(ctx)
	if err != nil {
		t.Fatalf("Second backup failed: %v", err)
	}
	if result2.FilesUnmodified != 1 {
		t.Errorf("Expected 1 unmodified, got %d", result2.FilesUnmodified)
	}

	// Verify the ref is the same (metadata carried forward correctly).
	ref2, err := hamt.NewTree(readStore).Lookup(result2.Root, "", "DOC_1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if ref2 != ref {
		t.Errorf("Expected same ref (metadata carried forward), got %q vs %q", ref2, ref)
	}
}

func TestMetadataEqual_ExtendedFields(t *testing.T) {
	base := core.FileMeta{
		Name:  "test.txt",
		Size:  100,
		Mtime: 1000,
		Type:  core.FileTypeFile,
		Mode:  0755,
		Uid:   501,
		Gid:   20,
		Btime: 900,
		Flags: 0x10,
		Xattrs: map[string][]byte{
			"user.tag": []byte("v1"),
		},
	}

	t.Run("identical", func(t *testing.T) {
		b := base
		b.Xattrs = map[string][]byte{"user.tag": []byte("v1")}
		if !metadataEqual(base, b) {
			t.Error("expected equal")
		}
	})

	tests := []struct {
		name   string
		modify func(m *core.FileMeta)
	}{
		{"mode", func(m *core.FileMeta) { m.Mode = 0644 }},
		{"uid", func(m *core.FileMeta) { m.Uid = 0 }},
		{"gid", func(m *core.FileMeta) { m.Gid = 100 }},
		{"btime", func(m *core.FileMeta) { m.Btime = 800 }},
		{"flags", func(m *core.FileMeta) { m.Flags = 0 }},
		{"xattrs_value", func(m *core.FileMeta) { m.Xattrs = map[string][]byte{"user.tag": []byte("v2")} }},
		{"xattrs_extra_key", func(m *core.FileMeta) { m.Xattrs = map[string][]byte{"user.tag": []byte("v1"), "user.other": []byte("x")} }},
		{"xattrs_missing", func(m *core.FileMeta) { m.Xattrs = nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := base
			b.Xattrs = map[string][]byte{"user.tag": []byte("v1")} // fresh copy
			tt.modify(&b)
			if metadataEqual(base, b) {
				t.Errorf("expected not equal after modifying %s", tt.name)
			}
		})
	}
}

func TestXattrsEqual(t *testing.T) {
	t.Run("both_nil", func(t *testing.T) {
		if !xattrsEqual(nil, nil) {
			t.Error("expected equal")
		}
	})
	t.Run("one_nil", func(t *testing.T) {
		if xattrsEqual(nil, map[string][]byte{"k": {}}) {
			t.Error("expected not equal")
		}
	})
	t.Run("empty_maps", func(t *testing.T) {
		if !xattrsEqual(map[string][]byte{}, map[string][]byte{}) {
			t.Error("expected equal")
		}
	})
}

func TestBytesEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []byte
		want bool
	}{
		{"both nil", nil, nil, true},
		{"both empty", []byte{}, []byte{}, true},
		{"equal", []byte{1, 2, 3}, []byte{1, 2, 3}, true},
		{"different length", []byte{1, 2}, []byte{1, 2, 3}, false},
		{"different content", []byte{1, 2, 3}, []byte{1, 2, 4}, false},
		{"one nil", nil, []byte{1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bytesEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("bytesEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
