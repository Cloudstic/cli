package source

import (
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"google.golang.org/api/drive/v3"
)

func TestToFileMeta_RegularFile(t *testing.T) {
	s := &GDriveSource{exclude: NewExcludeMatcher(nil)}
	f := &drive.File{
		Id:             "file1",
		Name:           "photo.jpg",
		MimeType:       "image/jpeg",
		Size:           1024,
		ModifiedTime:   "2024-01-15T10:30:00Z",
		Sha256Checksum: "abc123",
		Parents:        []string{"folder1"},
		Owners:         []*drive.User{{EmailAddress: "user@example.com"}},
		HeadRevisionId: "rev42",
	}

	meta := s.toFileMeta(f)

	if meta.FileID != "file1" {
		t.Errorf("FileID = %q, want %q", meta.FileID, "file1")
	}
	if meta.Name != "photo.jpg" {
		t.Errorf("Name = %q, want %q", meta.Name, "photo.jpg")
	}
	if meta.Type != core.FileTypeFile {
		t.Errorf("Type = %v, want FileTypeFile", meta.Type)
	}
	if meta.Size != 1024 {
		t.Errorf("Size = %d, want 1024", meta.Size)
	}
	if meta.ContentHash != "abc123" {
		t.Errorf("ContentHash = %q, want %q", meta.ContentHash, "abc123")
	}
	if meta.Owner != "user@example.com" {
		t.Errorf("Owner = %q, want %q", meta.Owner, "user@example.com")
	}
	if meta.Extra["mimeType"] != "image/jpeg" {
		t.Errorf("Extra[mimeType] = %v, want %q", meta.Extra["mimeType"], "image/jpeg")
	}
	// Regular files should not have exportMimeType.
	if _, ok := meta.Extra["exportMimeType"]; ok {
		t.Error("Regular file should not have exportMimeType in Extra")
	}
	if meta.Extra["headRevisionId"] != "rev42" {
		t.Errorf("Extra[headRevisionId] = %v, want %q", meta.Extra["headRevisionId"], "rev42")
	}
}

func TestToFileMeta_NativeDocument(t *testing.T) {
	s := &GDriveSource{exclude: NewExcludeMatcher(nil)}
	f := &drive.File{
		Id:             "doc1",
		Name:           "My Document",
		MimeType:       "application/vnd.google-apps.document",
		HeadRevisionId: "rev5",
		Parents:        []string{"folder1"},
	}

	meta := s.toFileMeta(f)

	if meta.Name != "My Document.docx" {
		t.Errorf("Name = %q, want %q (with .docx extension)", meta.Name, "My Document.docx")
	}
	if meta.Extra["exportMimeType"] != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Errorf("exportMimeType = %v, want docx MIME", meta.Extra["exportMimeType"])
	}
	if meta.Extra["headRevisionId"] != "rev5" {
		t.Errorf("headRevisionId = %v, want %q", meta.Extra["headRevisionId"], "rev5")
	}
}

func TestToFileMeta_NativeSpreadsheet(t *testing.T) {
	s := &GDriveSource{exclude: NewExcludeMatcher(nil)}
	f := &drive.File{
		Id:       "sheet1",
		Name:     "Budget",
		MimeType: "application/vnd.google-apps.spreadsheet",
	}

	meta := s.toFileMeta(f)

	if meta.Name != "Budget.xlsx" {
		t.Errorf("Name = %q, want %q", meta.Name, "Budget.xlsx")
	}
	if meta.Extra["exportMimeType"] != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Errorf("exportMimeType = %v, want xlsx MIME", meta.Extra["exportMimeType"])
	}
}

func TestToFileMeta_NativePresentation(t *testing.T) {
	s := &GDriveSource{exclude: NewExcludeMatcher(nil)}
	f := &drive.File{
		Id:       "slide1",
		Name:     "Deck",
		MimeType: "application/vnd.google-apps.presentation",
	}

	meta := s.toFileMeta(f)

	if meta.Name != "Deck.pptx" {
		t.Errorf("Name = %q, want %q", meta.Name, "Deck.pptx")
	}
	if meta.Extra["exportMimeType"] != "application/vnd.openxmlformats-officedocument.presentationml.presentation" {
		t.Errorf("exportMimeType = %v, want pptx MIME", meta.Extra["exportMimeType"])
	}
}

func TestToFileMeta_Folder(t *testing.T) {
	s := &GDriveSource{exclude: NewExcludeMatcher(nil)}
	f := &drive.File{
		Id:       "folder1",
		Name:     "My Folder",
		MimeType: "application/vnd.google-apps.folder",
	}

	meta := s.toFileMeta(f)

	if meta.Type != core.FileTypeFolder {
		t.Errorf("Type = %v, want FileTypeFolder", meta.Type)
	}
	if meta.Name != "My Folder" {
		t.Errorf("Name = %q, want %q (no extension for folders)", meta.Name, "My Folder")
	}
	if _, ok := meta.Extra["exportMimeType"]; ok {
		t.Error("Folder should not have exportMimeType")
	}
}

func TestToFileMeta_NoHeadRevisionId(t *testing.T) {
	s := &GDriveSource{exclude: NewExcludeMatcher(nil)}
	f := &drive.File{
		Id:       "file1",
		Name:     "test.txt",
		MimeType: "text/plain",
	}

	meta := s.toFileMeta(f)

	if _, ok := meta.Extra["headRevisionId"]; ok {
		t.Error("Should not have headRevisionId when empty")
	}
}

func TestToFileMeta_NoOwners(t *testing.T) {
	s := &GDriveSource{exclude: NewExcludeMatcher(nil)}
	f := &drive.File{
		Id:       "file1",
		Name:     "test.txt",
		MimeType: "text/plain",
	}

	meta := s.toFileMeta(f)
	if meta.Owner != "" {
		t.Errorf("Owner = %q, want empty", meta.Owner)
	}
}

func TestVisitEntryWithPath_SkipNativeFiles(t *testing.T) {
	s := &GDriveSource{
		exclude:         NewExcludeMatcher(nil),
		skipNativeFiles: true,
		mimeTypes:       make(map[string]string),
	}

	var visited []string
	callback := func(meta core.FileMeta) error {
		visited = append(visited, meta.FileID)
		return nil
	}

	pathMap := make(map[string]string)
	excludedPaths := make(map[string]bool)

	// Native file should be skipped.
	err := s.visitEntryWithPath(&drive.File{
		Id:       "doc1",
		Name:     "Report",
		MimeType: "application/vnd.google-apps.document",
	}, pathMap, excludedPaths, callback)
	if err != nil {
		t.Fatal(err)
	}

	// Regular file should be visited.
	err = s.visitEntryWithPath(&drive.File{
		Id:       "file1",
		Name:     "photo.jpg",
		MimeType: "image/jpeg",
	}, pathMap, excludedPaths, callback)
	if err != nil {
		t.Fatal(err)
	}

	if len(visited) != 1 || visited[0] != "file1" {
		t.Errorf("visited = %v, want [file1]", visited)
	}
}

func TestVisitEntryWithPath_NativeFilesIncludedByDefault(t *testing.T) {
	s := &GDriveSource{
		exclude:         NewExcludeMatcher(nil),
		skipNativeFiles: false,
		mimeTypes:       make(map[string]string),
	}

	var visited []string
	callback := func(meta core.FileMeta) error {
		visited = append(visited, meta.FileID)
		return nil
	}

	pathMap := make(map[string]string)
	excludedPaths := make(map[string]bool)

	err := s.visitEntryWithPath(&drive.File{
		Id:       "doc1",
		Name:     "Report",
		MimeType: "application/vnd.google-apps.document",
	}, pathMap, excludedPaths, callback)
	if err != nil {
		t.Fatal(err)
	}

	if len(visited) != 1 || visited[0] != "doc1" {
		t.Errorf("visited = %v, want [doc1]", visited)
	}
}

func TestVisitEntryWithPath_RecordsMimeType(t *testing.T) {
	s := &GDriveSource{
		exclude:   NewExcludeMatcher(nil),
		mimeTypes: make(map[string]string),
	}

	callback := func(meta core.FileMeta) error { return nil }
	pathMap := make(map[string]string)
	excludedPaths := make(map[string]bool)

	// Regular file should record mimeType.
	_ = s.visitEntryWithPath(&drive.File{
		Id:       "file1",
		Name:     "photo.jpg",
		MimeType: "image/jpeg",
	}, pathMap, excludedPaths, callback)

	if s.mimeTypes["file1"] != "image/jpeg" {
		t.Errorf("mimeTypes[file1] = %q, want %q", s.mimeTypes["file1"], "image/jpeg")
	}

	// Native file should also record mimeType.
	_ = s.visitEntryWithPath(&drive.File{
		Id:       "doc1",
		Name:     "Report",
		MimeType: "application/vnd.google-apps.document",
	}, pathMap, excludedPaths, callback)

	if s.mimeTypes["doc1"] != "application/vnd.google-apps.document" {
		t.Errorf("mimeTypes[doc1] = %q, want native MIME", s.mimeTypes["doc1"])
	}

	// Folder should NOT record mimeType.
	_ = s.visitEntryWithPath(&drive.File{
		Id:       "folder1",
		Name:     "Stuff",
		MimeType: "application/vnd.google-apps.folder",
	}, pathMap, excludedPaths, callback)

	if _, ok := s.mimeTypes["folder1"]; ok {
		t.Error("Folders should not be recorded in mimeTypes")
	}
}

func TestVisitEntryWithPath_SkipNativeStillRecordsMimeType(t *testing.T) {
	s := &GDriveSource{
		exclude:         NewExcludeMatcher(nil),
		skipNativeFiles: true,
		mimeTypes:       make(map[string]string),
	}

	callback := func(meta core.FileMeta) error { return nil }
	pathMap := make(map[string]string)
	excludedPaths := make(map[string]bool)

	// Even when skipping native files, mimeType should still be recorded
	// (the recording happens before the skip check).
	_ = s.visitEntryWithPath(&drive.File{
		Id:       "doc1",
		Name:     "Report",
		MimeType: "application/vnd.google-apps.document",
	}, pathMap, excludedPaths, callback)

	if s.mimeTypes["doc1"] != "application/vnd.google-apps.document" {
		t.Errorf("mimeTypes[doc1] = %q, want native MIME even when skipping", s.mimeTypes["doc1"])
	}
}

func TestVisitEntryWithPath_PathComputation(t *testing.T) {
	s := &GDriveSource{
		exclude:   NewExcludeMatcher(nil),
		mimeTypes: make(map[string]string),
	}

	var paths []string
	callback := func(meta core.FileMeta) error {
		if len(meta.Paths) > 0 {
			paths = append(paths, meta.Paths[0])
		}
		return nil
	}

	pathMap := map[string]string{"parentID": "Documents"}
	excludedPaths := make(map[string]bool)

	// File with parent in pathMap.
	_ = s.visitEntryWithPath(&drive.File{
		Id:       "file1",
		Name:     "notes.txt",
		MimeType: "text/plain",
		Parents:  []string{"parentID"},
	}, pathMap, excludedPaths, callback)

	// Native file with parent — name should have extension appended.
	_ = s.visitEntryWithPath(&drive.File{
		Id:       "doc1",
		Name:     "Report",
		MimeType: "application/vnd.google-apps.document",
		Parents:  []string{"parentID"},
	}, pathMap, excludedPaths, callback)

	if len(paths) != 2 {
		t.Fatalf("got %d paths, want 2", len(paths))
	}
	if paths[0] != "Documents/notes.txt" {
		t.Errorf("paths[0] = %q, want %q", paths[0], "Documents/notes.txt")
	}
	if paths[1] != "Documents/Report.docx" {
		t.Errorf("paths[1] = %q, want %q", paths[1], "Documents/Report.docx")
	}
}

func TestVisitEntryWithPath_RootRelativePath_NoLeadingSlash(t *testing.T) {
	s := &GDriveSource{
		exclude:   NewExcludeMatcher(nil),
		mimeTypes: make(map[string]string),
	}

	var got string
	callback := func(meta core.FileMeta) error {
		if len(meta.Paths) > 0 {
			got = meta.Paths[0]
		}
		return nil
	}

	pathMap := map[string]string{"rootFolderID": ""}
	excludedPaths := make(map[string]bool)

	err := s.visitEntryWithPath(&drive.File{
		Id:       "file1",
		Name:     "child.txt",
		MimeType: "text/plain",
		Parents:  []string{"rootFolderID"},
	}, pathMap, excludedPaths, callback)
	if err != nil {
		t.Fatalf("visitEntryWithPath: %v", err)
	}

	if got != "child.txt" {
		t.Fatalf("path = %q, want %q", got, "child.txt")
	}
}

func TestChangeToFileChange_RecordsMimeType(t *testing.T) {
	s := &GDriveChangeSource{
		GDriveSource: GDriveSource{
			exclude:   NewExcludeMatcher(nil),
			mimeTypes: make(map[string]string),
		},
	}

	// Upsert of a regular file.
	fc := s.changeToFileChange(&drive.Change{
		FileId: "file1",
		File: &drive.File{
			Id:       "file1",
			Name:     "photo.jpg",
			MimeType: "image/jpeg",
		},
	})

	if fc.Type != ChangeUpsert {
		t.Errorf("Type = %v, want ChangeUpsert", fc.Type)
	}
	if s.mimeTypes["file1"] != "image/jpeg" {
		t.Errorf("mimeTypes[file1] = %q, want %q", s.mimeTypes["file1"], "image/jpeg")
	}

	// Upsert of a native doc.
	fc2 := s.changeToFileChange(&drive.Change{
		FileId: "doc1",
		File: &drive.File{
			Id:       "doc1",
			Name:     "Report",
			MimeType: "application/vnd.google-apps.document",
		},
	})

	if fc2.Type != ChangeUpsert {
		t.Errorf("Type = %v, want ChangeUpsert", fc2.Type)
	}
	if s.mimeTypes["doc1"] != "application/vnd.google-apps.document" {
		t.Errorf("mimeTypes[doc1] = %q, want native MIME", s.mimeTypes["doc1"])
	}
	if fc2.Meta.Name != "Report.docx" {
		t.Errorf("Name = %q, want %q", fc2.Meta.Name, "Report.docx")
	}

	// Folder should not be recorded in mimeTypes.
	s.changeToFileChange(&drive.Change{
		FileId: "folder1",
		File: &drive.File{
			Id:       "folder1",
			Name:     "Stuff",
			MimeType: "application/vnd.google-apps.folder",
		},
	})
	if _, ok := s.mimeTypes["folder1"]; ok {
		t.Error("Folders should not be recorded in mimeTypes")
	}
}

func TestChangeToFileChange_DeletedFile(t *testing.T) {
	s := &GDriveChangeSource{
		GDriveSource: GDriveSource{
			exclude:   NewExcludeMatcher(nil),
			mimeTypes: make(map[string]string),
		},
	}

	// Removed file.
	fc := s.changeToFileChange(&drive.Change{
		FileId:  "file1",
		Removed: true,
	})

	if fc.Type != ChangeDelete {
		t.Errorf("Type = %v, want ChangeDelete", fc.Type)
	}
	if fc.Meta.FileID != "file1" {
		t.Errorf("FileID = %q, want %q", fc.Meta.FileID, "file1")
	}
}

func TestChangeToFileChange_NilFilePayload(t *testing.T) {
	s := &GDriveChangeSource{
		GDriveSource: GDriveSource{
			exclude:   NewExcludeMatcher(nil),
			mimeTypes: make(map[string]string),
		},
	}

	fc := s.changeToFileChange(&drive.Change{
		FileId:  "file1",
		Removed: false,
		File:    nil,
	})

	if fc.Type != ChangeDelete {
		t.Errorf("Type = %v, want ChangeDelete", fc.Type)
	}
	if fc.Meta.FileID != "file1" {
		t.Errorf("FileID = %q, want %q", fc.Meta.FileID, "file1")
	}
}

func TestChangeToFileChange_TrashedFile(t *testing.T) {
	s := &GDriveChangeSource{
		GDriveSource: GDriveSource{
			exclude:   NewExcludeMatcher(nil),
			mimeTypes: make(map[string]string),
		},
	}

	fc := s.changeToFileChange(&drive.Change{
		FileId: "file1",
		File: &drive.File{
			Id:      "file1",
			Name:    "old.txt",
			Trashed: true,
		},
	})

	if fc.Type != ChangeDelete {
		t.Errorf("Type = %v, want ChangeDelete", fc.Type)
	}
}

func TestGetFileStream_MimeTypeRouting(t *testing.T) {
	// Verify the mimeTypes map is used to determine export vs download.
	// We can't do full HTTP calls without a mock server, but we verify
	// the routing decision by checking what's in the map.
	s := &GDriveSource{
		mimeTypes: map[string]string{
			"doc1":  "application/vnd.google-apps.document",
			"file1": "image/jpeg",
		},
	}

	// Native file: mimeType present and is google-native → should export.
	mimeType, ok := s.mimeTypes["doc1"]
	if !ok || !isGoogleNativeMimeType(mimeType) {
		t.Error("doc1 should be routed to export")
	}

	// Regular file: mimeType present but not native → should download.
	mimeType, ok = s.mimeTypes["file1"]
	if !ok || isGoogleNativeMimeType(mimeType) {
		t.Error("file1 should be routed to download")
	}

	// Unknown file: not in mimeTypes → should download (default path).
	_, ok = s.mimeTypes["unknown"]
	if ok {
		t.Error("unknown file should not be in mimeTypes")
	}
}

func TestWithSkipNativeFiles(t *testing.T) {
	var cfg gDriveOptions
	opt := WithSkipNativeFiles()
	opt(&cfg)
	if !cfg.skipNativeFiles {
		t.Error("WithSkipNativeFiles should set skipNativeFiles to true")
	}
}

func TestGDriveInfo_MyDrive_Root(t *testing.T) {
	s := &GDriveSource{account: "user@gmail.com", rootPath: "/"}
	info := s.Info()

	if info.Type != "gdrive" {
		t.Errorf("Type = %q, want gdrive", info.Type)
	}
	if info.Account != "user@gmail.com" {
		t.Errorf("Account = %q, want user@gmail.com", info.Account)
	}
	if info.Path != "/" {
		t.Errorf("Path = %q, want /", info.Path)
	}
	if info.Identity != "user@gmail.com" {
		t.Errorf("Identity = %q, want user@gmail.com", info.Identity)
	}
	if info.DriveName != "My Drive" {
		t.Errorf("DriveName = %q, want My Drive", info.DriveName)
	}
	if info.PathID != "root" {
		t.Errorf("PathID = %q, want root", info.PathID)
	}
}

func TestGDriveInfo_MyDrive_Subfolder(t *testing.T) {
	s := &GDriveSource{account: "user@gmail.com", rootFolderID: "folder123", rootPath: "/myfolder"}
	info := s.Info()

	if info.Path != "/myfolder" {
		t.Errorf("Path = %q, want /myfolder", info.Path)
	}
	if info.Identity != "user@gmail.com" {
		t.Errorf("Identity = %q, want user@gmail.com", info.Identity)
	}
	if info.DriveName != "My Drive" {
		t.Errorf("DriveName = %q, want My Drive", info.DriveName)
	}
	if info.PathID != "folder123" {
		t.Errorf("PathID = %q, want folder123", info.PathID)
	}
}

func TestGDriveInfo_SharedDrive_Root(t *testing.T) {
	s := &GDriveSource{
		account:   "user@gmail.com",
		driveID:   "shared-drive-abc",
		driveName: "Team Photos",
		rootPath:  "/",
	}
	info := s.Info()

	if info.Path != "/" {
		t.Errorf("Path = %q, want /", info.Path)
	}
	if info.Identity != "shared-drive-abc" {
		t.Errorf("Identity = %q, want shared-drive-abc", info.Identity)
	}
	if info.DriveName != "Team Photos" {
		t.Errorf("DriveName = %q, want Team Photos", info.DriveName)
	}
	if info.PathID != "shared-drive-abc" {
		t.Errorf("PathID = %q, want shared-drive-abc", info.PathID)
	}
}

func TestGDriveInfo_SharedDrive_Subfolder(t *testing.T) {
	s := &GDriveSource{
		account:      "user@gmail.com",
		driveID:      "shared-drive-abc",
		driveName:    "Team Photos",
		rootFolderID: "folder456",
		rootPath:     "/team/folder456",
	}
	info := s.Info()

	if info.Path != "/team/folder456" {
		t.Errorf("Path = %q, want /team/folder456", info.Path)
	}
	if info.Identity != "shared-drive-abc" {
		t.Errorf("Identity = %q, want shared-drive-abc", info.Identity)
	}
	if info.DriveName != "Team Photos" {
		t.Errorf("DriveName = %q, want Team Photos", info.DriveName)
	}
	if info.PathID != "folder456" {
		t.Errorf("PathID = %q, want folder456", info.PathID)
	}
}

func TestGDriveChangesInfo_Type(t *testing.T) {
	s := &GDriveChangeSource{
		GDriveSource: GDriveSource{account: "user@gmail.com"},
	}
	info := s.Info()

	if info.Type != "gdrive-changes" {
		t.Errorf("Type = %q, want gdrive-changes", info.Type)
	}
	if info.DriveName != "My Drive" {
		t.Errorf("DriveName = %q, want My Drive", info.DriveName)
	}
}
