package source

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

func TestIsDescendantOfRoot(t *testing.T) {
	ctx := context.Background()
	s := &GDriveChangeSource{
		GDriveSource: GDriveSource{
			rootFolderID: "root123",
		},
	}

	tests := []struct {
		name     string
		meta     core.FileMeta
		expected bool
	}{
		{
			name: "no parents",
			meta: core.FileMeta{
				Parents: nil,
			},
			expected: false,
		},
		{
			name: "direct child of rootFolderID",
			meta: core.FileMeta{
				Parents: []string{"root123"},
			},
			expected: true,
		},
		{
			name: "some other parent",
			meta: core.FileMeta{
				Parents: []string{"other456"},
			},
			expected: true, // Optimistic true
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.isDescendantOfRoot(ctx, tt.meta)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestResolveChangePath_WithRootFolder(t *testing.T) {
	ctx := context.Background()
	s := &GDriveChangeSource{
		GDriveSource: GDriveSource{
			rootFolderID: "root123",
		},
	}

	pathMap := map[string]string{
		"folder1": "folder1",
		"folder2": "folder1/folder2",
	}

	tests := []struct {
		name        string
		meta        core.FileMeta
		expected    string
		expectError bool
	}{
		{
			name: "no parents",
			meta: core.FileMeta{
				Name:    "file.txt",
				Parents: nil,
			},
			expected:    "",
			expectError: false, // returns empty string to be skipped
		},
		{
			name: "direct child of root",
			meta: core.FileMeta{
				Name:    "file.txt",
				Parents: []string{"root123"},
			},
			expected:    "file.txt",
			expectError: false,
		},
		{
			name: "child of known folder",
			meta: core.FileMeta{
				Name:    "file.txt",
				Parents: []string{"folder2"},
			},
			expected:    "folder1/folder2/file.txt",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := s.resolveChangePath(ctx, tt.meta, pathMap)
			if (err != nil) != tt.expectError {
				t.Errorf("expected error %v, got %v", tt.expectError, err)
			}
			if p != tt.expected {
				t.Errorf("expected path %q, got %q", tt.expected, p)
			}
		})
	}
}

func TestProcessChanges(t *testing.T) {
	ctx := context.Background()
	s := &GDriveChangeSource{
		GDriveSource: GDriveSource{
			rootFolderID: "root123",
		},
	}

	pathMap := map[string]string{
		"folder1": "folder1",
	}

	changes := []FileChange{
		{
			Type: ChangeUpsert,
			Meta: core.FileMeta{
				FileID:  "file1",
				Name:    "file1.txt",
				Parents: []string{"root123"},
			},
		},
		{
			Type: ChangeUpsert,
			Meta: core.FileMeta{
				FileID:  "file2",
				Name:    "file2.txt",
				Parents: []string{"folder1"},
			},
		},
		{
			Type: ChangeUpsert,
			Meta: core.FileMeta{
				FileID:  "file3",
				Name:    "file3.txt",
				Parents: nil, // Should be skipped
			},
		},
		{
			Type: ChangeDelete, // Deletes should be passed through
			Meta: core.FileMeta{
				FileID: "file4",
			},
		},
	}

	valid, err := s.processChanges(ctx, changes, pathMap, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(valid) != 3 {
		t.Fatalf("expected 3 valid changes, got %d", len(valid))
	}

	if valid[0].Meta.FileID != "file1" || len(valid[0].Meta.Paths) == 0 || valid[0].Meta.Paths[0] != "file1.txt" {
		t.Errorf("unexpected valid[0]: %+v", valid[0])
	}
	if valid[1].Meta.FileID != "file2" || len(valid[1].Meta.Paths) == 0 || valid[1].Meta.Paths[0] != "folder1/file2.txt" {
		t.Errorf("unexpected valid[1]: %+v", valid[1])
	}
	if valid[2].Meta.FileID != "file4" || valid[2].Type != ChangeDelete {
		t.Errorf("unexpected valid[2]: %+v", valid[2])
	}
}
