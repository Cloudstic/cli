package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExcludeMatcher_BasicGlob(t *testing.T) {
	m := NewExcludeMatcher([]string{"*.tmp", "*.log"})

	tests := []struct {
		path    string
		isDir   bool
		exclude bool
	}{
		{"file.tmp", false, true},
		{"file.log", false, true},
		{"file.txt", false, false},
		{"sub/file.tmp", false, true},
		{"deep/nested/file.log", false, true},
		{"deep/nested/file.go", false, false},
	}
	for _, tc := range tests {
		got := m.Excludes(tc.path, tc.isDir)
		if got != tc.exclude {
			t.Errorf("Excludes(%q, %v) = %v, want %v", tc.path, tc.isDir, got, tc.exclude)
		}
	}
}

func TestExcludeMatcher_DirectoryOnly(t *testing.T) {
	m := NewExcludeMatcher([]string{".git/", "node_modules/"})

	tests := []struct {
		path    string
		isDir   bool
		exclude bool
	}{
		{".git", true, true},
		{".git", false, false},   // not a dir → not matched
		{"sub/.git", true, true}, // unanchored matches in subdirs
		{"node_modules", true, true},
		{"node_modules", false, false},
		{"src/node_modules", true, true},
	}
	for _, tc := range tests {
		got := m.Excludes(tc.path, tc.isDir)
		if got != tc.exclude {
			t.Errorf("Excludes(%q, %v) = %v, want %v", tc.path, tc.isDir, got, tc.exclude)
		}
	}
}

func TestExcludeMatcher_Doublestar(t *testing.T) {
	m := NewExcludeMatcher([]string{"**/*.log", "docs/**/*.pdf"})

	tests := []struct {
		path    string
		isDir   bool
		exclude bool
	}{
		{"app.log", false, true},
		{"sub/app.log", false, true},
		{"a/b/c/app.log", false, true},
		{"app.txt", false, false},
		{"docs/guide.pdf", false, true},
		{"docs/v2/guide.pdf", false, true},
		{"docs/v2/sub/guide.pdf", false, true},
		{"src/guide.pdf", false, false}, // not under docs/
	}
	for _, tc := range tests {
		got := m.Excludes(tc.path, tc.isDir)
		if got != tc.exclude {
			t.Errorf("Excludes(%q, %v) = %v, want %v", tc.path, tc.isDir, got, tc.exclude)
		}
	}
}

func TestExcludeMatcher_Negation(t *testing.T) {
	m := NewExcludeMatcher([]string{"*.log", "!important.log"})

	tests := []struct {
		path    string
		isDir   bool
		exclude bool
	}{
		{"debug.log", false, true},
		{"important.log", false, false},
		{"sub/debug.log", false, true},
		{"sub/important.log", false, false},
	}
	for _, tc := range tests {
		got := m.Excludes(tc.path, tc.isDir)
		if got != tc.exclude {
			t.Errorf("Excludes(%q, %v) = %v, want %v", tc.path, tc.isDir, got, tc.exclude)
		}
	}
}

func TestExcludeMatcher_AnchoredPattern(t *testing.T) {
	// Pattern with '/' is anchored to root.
	m := NewExcludeMatcher([]string{"build/output"})

	tests := []struct {
		path    string
		isDir   bool
		exclude bool
	}{
		{"build/output", false, true},
		{"build/output", true, true},
		{"src/build/output", false, false}, // anchored — must start at root
	}
	for _, tc := range tests {
		got := m.Excludes(tc.path, tc.isDir)
		if got != tc.exclude {
			t.Errorf("Excludes(%q, %v) = %v, want %v", tc.path, tc.isDir, got, tc.exclude)
		}
	}
}

func TestExcludeMatcher_CommentsAndBlankLines(t *testing.T) {
	m := NewExcludeMatcher([]string{
		"# This is a comment",
		"",
		"   ",
		"*.tmp",
		"# Another comment",
	})

	if m.Excludes("file.txt", false) {
		t.Error("file.txt should not be excluded")
	}
	if !m.Excludes("file.tmp", false) {
		t.Error("file.tmp should be excluded")
	}
}

func TestExcludeMatcher_Empty(t *testing.T) {
	m := NewExcludeMatcher(nil)
	if !m.Empty() {
		t.Error("expected Empty() to be true for nil patterns")
	}
	if m.Excludes("anything", false) {
		t.Error("empty matcher should not exclude anything")
	}

	m2 := NewExcludeMatcher([]string{"# only comments", "", "  "})
	if !m2.Empty() {
		t.Error("expected Empty() to be true for comment-only patterns")
	}
}

func TestExcludeMatcher_ExactName(t *testing.T) {
	m := NewExcludeMatcher([]string{"Thumbs.db", ".DS_Store"})

	tests := []struct {
		path    string
		exclude bool
	}{
		{"Thumbs.db", true},
		{"sub/Thumbs.db", true},
		{".DS_Store", true},
		{"a/b/.DS_Store", true},
		{"other.db", false},
	}
	for _, tc := range tests {
		got := m.Excludes(tc.path, false)
		if got != tc.exclude {
			t.Errorf("Excludes(%q, false) = %v, want %v", tc.path, got, tc.exclude)
		}
	}
}

func TestParseExcludeFile(t *testing.T) {
	dir := t.TempDir()
	excludeFile := filepath.Join(dir, ".backupignore")
	content := "# Comment\n*.tmp\n\nnode_modules/\n!important.tmp\n"
	if err := os.WriteFile(excludeFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	patterns, err := ParseExcludeFile(excludeFile)
	if err != nil {
		t.Fatalf("ParseExcludeFile failed: %v", err)
	}
	if len(patterns) != 5 {
		t.Fatalf("expected 5 lines, got %d: %v", len(patterns), patterns)
	}

	m := NewExcludeMatcher(patterns)
	if !m.Excludes("file.tmp", false) {
		t.Error("file.tmp should be excluded")
	}
	if m.Excludes("important.tmp", false) {
		t.Error("important.tmp should NOT be excluded (negated)")
	}
	if !m.Excludes("node_modules", true) {
		t.Error("node_modules dir should be excluded")
	}
	if m.Excludes("node_modules", false) {
		t.Error("node_modules file should NOT be excluded (dir-only pattern)")
	}
}

func TestParseExcludeFile_NotFound(t *testing.T) {
	_, err := ParseExcludeFile("/nonexistent/path/.backupignore")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestIsUnderExcludedDir(t *testing.T) {
	dirs := []string{".git/", "node_modules/"}
	tests := []struct {
		path string
		want bool
	}{
		{".git/config", true},
		{".git/objects/abc", true},
		{"node_modules/pkg/index.js", true},
		{"src/main.go", false},
		{".gitignore", false},
	}
	for _, tc := range tests {
		got := isUnderExcludedDir(tc.path, dirs)
		if got != tc.want {
			t.Errorf("isUnderExcludedDir(%q, %v) = %v, want %v", tc.path, dirs, got, tc.want)
		}
	}
}
