package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/source"
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

type mockIncrementalSource struct {
	*MockSource
	startToken string
	changes    []source.FileChange
	newToken   string
}

func (s *mockIncrementalSource) GetStartPageToken() (string, error) {
	return s.startToken, nil
}

func (s *mockIncrementalSource) WalkChanges(_ context.Context, _ string, callback func(source.FileChange) error) (string, error) {
	for _, ch := range s.changes {
		if err := callback(ch); err != nil {
			return "", err
		}
	}
	return s.newToken, nil
}

func TestScanIncremental_DeleteWithoutParentUsesExistingMetadataParent(t *testing.T) {
	ctx := context.Background()
	base := NewMockSource()
	base.Files["FOLDER_1"] = MockFile{Meta: core.FileMeta{FileID: "FOLDER_1", Name: "folder", Type: core.FileTypeFolder}}
	base.Files["FILE_1"] = MockFile{
		Meta: core.FileMeta{
			FileID:  "FILE_1",
			Name:    "a.txt",
			Type:    core.FileTypeFile,
			Parents: []string{"FOLDER_1"},
			Size:    3,
		},
		Content: []byte("abc"),
	}

	inc := &mockIncrementalSource{
		MockSource: base,
		startToken: "tok-1",
		newToken:   "tok-2",
	}

	dest := NewMockStore()
	mgr := NewBackupManager(inc, dest, ui.NewNoOpReporter(), nil)
	_, err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("first backup failed: %v", err)
	}

	deleteOnly := []source.FileChange{{
		Type: source.ChangeDelete,
		Meta: core.FileMeta{FileID: "FILE_1", Type: core.FileTypeFile},
	}}
	inc.changes = deleteOnly
	delete(base.Files, "FILE_1")

	mgr2 := NewBackupManager(inc, dest, ui.NewNoOpReporter(), nil)
	second, err := mgr2.Run(ctx)
	if err != nil {
		t.Fatalf("second backup failed: %v", err)
	}

	tree := hamt.NewTree(store.NewCompressedStore(dest))
	ref, err := tree.Lookup(second.Root, "FOLDER_1", "FILE_1")
	if err != nil {
		t.Fatalf("lookup failed: %v", err)
	}
	if ref != "" {
		t.Fatalf("expected FILE_1 to be deleted, got ref %q", ref)
	}
}
