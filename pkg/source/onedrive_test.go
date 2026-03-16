package source

import (
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

func TestOneDriveInfo(t *testing.T) {
	s := &OneDriveSource{account: "user@outlook.com", rootPath: "/"}
	info := s.Info()

	if info.Type != "onedrive" {
		t.Errorf("Type = %q, want onedrive", info.Type)
	}
	if info.Account != "user@outlook.com" {
		t.Errorf("Account = %q, want user@outlook.com", info.Account)
	}
	if info.Path != "/" {
		t.Errorf("Path = %q, want /", info.Path)
	}
	if info.Identity != "user@outlook.com" {
		t.Errorf("Identity = %q, want user@outlook.com", info.Identity)
	}
	if info.DriveName != "My Drive" {
		t.Errorf("DriveName = %q, want My Drive", info.DriveName)
	}
	if info.PathID != "/" {
		t.Errorf("PathID = %q, want /", info.PathID)
	}
}

func TestOneDriveChangesInfo_Type(t *testing.T) {
	s := &OneDriveChangeSource{
		OneDriveSource: OneDriveSource{account: "user@outlook.com", rootPath: "/"},
	}
	info := s.Info()

	if info.Type != "onedrive-changes" {
		t.Errorf("Type = %q, want onedrive-changes", info.Type)
	}
	if info.DriveName != "My Drive" {
		t.Errorf("DriveName = %q, want My Drive", info.DriveName)
	}
	if info.Path != "/" {
		t.Errorf("Path = %q, want /", info.Path)
	}
}

func TestOneDriveFilterChangesByRootPath(t *testing.T) {
	s := &OneDriveChangeSource{
		OneDriveSource: OneDriveSource{rootPath: "/my/root/path"},
	}

	changes := []FileChange{
		{
			Type: ChangeUpsert,
			Meta: core.FileMeta{
				Name:  "file1.txt",
				Paths: []string{"my/root/path/file1.txt"},
			},
		},
		{
			Type: ChangeUpsert,
			Meta: core.FileMeta{
				Name:  "file2.txt",
				Paths: []string{"my/root/path/sub/file2.txt"},
			},
		},
		{
			Type: ChangeUpsert,
			Meta: core.FileMeta{
				Name:  "file3.txt",
				Paths: []string{"other/path/file3.txt"}, // Should be filtered out
			},
		},
		{
			Type: ChangeUpsert,
			Meta: core.FileMeta{
				Name:  "my",
				Paths: []string{"my/root/path"}, // The root path itself
			},
		},
		{
			Type: ChangeDelete,
			Meta: core.FileMeta{
				Name:  "file4.txt",
				Paths: nil, // Deletes don't have paths, should not be filtered out
			},
		},
		{
			Type: ChangeUpsert,
			Meta: core.FileMeta{
				Name:  "file5.txt",
				Paths: nil, // Upserts without paths are filtered out if not at root
			},
		},
	}

	filtered := s.filterChangesByRootPath(changes)

	if len(filtered) != 3 {
		t.Fatalf("expected 3 filtered changes, got %d", len(filtered))
	}

	if filtered[0].Meta.Paths[0] != "file1.txt" {
		t.Errorf("expected stripped path file1.txt, got %s", filtered[0].Meta.Paths[0])
	}
	if filtered[1].Meta.Paths[0] != "sub/file2.txt" {
		t.Errorf("expected stripped path sub/file2.txt, got %s", filtered[1].Meta.Paths[0])
	}
	if filtered[2].Type != ChangeDelete {
		t.Errorf("expected delete change, got %v", filtered[2].Type)
	}

	// Test with rootPath = ""
	s2 := &OneDriveChangeSource{
		OneDriveSource: OneDriveSource{rootPath: ""},
	}
	filtered2 := s2.filterChangesByRootPath(changes)
	if len(filtered2) != len(changes) {
		t.Errorf("expected %d changes with empty rootPath, got %d", len(changes), len(filtered2))
	}
}

func TestNormalizeOneDriveRootPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "/"},
		{"/", "/"},
		{"docs", "/docs"},
		{"/docs/", "/docs"},
		{"  /Team Files/  ", "/Team Files"},
	}

	for _, tc := range tests {
		if got := normalizeOneDriveRootPath(tc.in); got != tc.want {
			t.Errorf("normalizeOneDriveRootPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestOneDriveGetRootURL_EncodesPath(t *testing.T) {
	s := &OneDriveSource{rootPath: "/Team Files/R?D"}
	got := s.getRootURL()
	want := "https://graph.microsoft.com/v1.0/me/drive/root:/Team%20Files/R%3FD"
	if got != want {
		t.Errorf("getRootURL() = %q, want %q", got, want)
	}
}
