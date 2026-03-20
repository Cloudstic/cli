package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCLI_Feature_BackupExcludePatterns verifies that -exclude, -exclude *.glob,
// and -exclude-file patterns correctly suppress files from the backup.
// This is a source-level feature; running on every store backend adds no value.
func TestCLI_Feature_BackupExcludePatterns(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "backup_exclude_patterns",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			// Lay out a directory tree with several exclusion candidates.
			for _, d := range []string{"src", ".git/objects", "node_modules/pkg", "logs"} {
				h.source.WriteFile(t, filepath.Join(d, ".keep"), "")
			}
			h.WithFile("src/main.go", "package main").
				WithFile("src/debug.log", "log line").
				WithFile(".git/config", "[core]").
				WithFile(".git/objects/abc", "blob").
				WithFile("node_modules/pkg/index.js", "exports").
				WithFile("logs/app.log", "log").
				WithFile("README.md", "hello").
				WithFile("notes.tmp", "temp")

			// Write exclude file into a temp dir (not inside the source).
			excludeFilePath := filepath.Join(t.TempDir(), ".backupignore")
			if err := os.WriteFile(excludeFilePath, []byte("# Skip logs dir\nlogs/\n"), 0644); err != nil {
				t.Fatal(err)
			}

			r := h.MustInitEncrypted()
			r.Backup(
				"-exclude", ".git/",
				"-exclude", "node_modules/",
				"-exclude", "*.tmp",
				"-exclude", "*.log",
				"-exclude-file", excludeFilePath,
			)

			restore := r.RestoreZip("exclude_restore.zip")
			restore.
				MustContainFile("src/main.go").
				MustContainFile("README.md").
				MustNotContainFile(".git/config").
				MustNotContainFile(".git/objects/abc").
				MustNotContainFile("node_modules/pkg/index.js").
				MustNotContainFile("notes.tmp").
				MustNotContainFile("src/debug.log").
				MustNotContainFile("logs/app.log").
				MustHaveFileContent("src/main.go", "package main").
				MustHaveFileContent("README.md", "hello")
		},
	})
}
