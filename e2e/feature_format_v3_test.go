package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Format v3 (RFC 0026) stores file metadata and small-file content inside the
// snapshot tree and has no packfile layer. Everything below drives it through
// the real binary, which is the level the unit and client tests do not reach:
// the `-format 3` flag, the store chain the CLI builds from a v3 marker, and
// the whole pipeline against a repository written that way.
//
// The physical assertions are the point. A v3 repository that quietly grew a
// filemeta/, content/ or packs/ namespace would still pass every behavioural
// check — it would just be a v2 repository wearing a v3 marker — so the test
// looks at what is actually on disk rather than only at what comes back out.

// localStoreDir returns the filesystem path behind a harness's local store,
// read from the -store argument it was set up with. The physical assertions
// below need the directory itself, which is why these tests are pinned to a
// local store.
func localStoreDir(t *testing.T, h *harness) string {
	t.Helper()
	for i, arg := range h.storeArgs {
		if arg == "-store" && i+1 < len(h.storeArgs) {
			return strings.TrimPrefix(h.storeArgs[i+1], "local:")
		}
	}
	t.Fatalf("harness has no -store argument: %v", h.storeArgs)
	return ""
}

// storeObjectKeys lists the object keys physically present under the local
// store directory, relative to it and slash-separated.
func storeObjectKeys(t *testing.T, storeDir string) []string {
	t.Helper()
	var keys []string
	err := filepath.WalkDir(storeDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(storeDir, path)
		if err != nil {
			return err
		}
		keys = append(keys, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk store %s: %v", storeDir, err)
	}
	return keys
}

// mustHaveNoV2Namespaces fails when a repository holds any object that only a
// packfile-era format writes.
func mustHaveNoV2Namespaces(t *testing.T, h *harness) {
	t.Helper()
	storeDir := localStoreDir(t, h)
	forbidden := []string{"filemeta/", "content/", "packs/", "index/packs", "index/packmap/"}
	for _, key := range storeObjectKeys(t, storeDir) {
		for _, prefix := range forbidden {
			if strings.HasPrefix(key, prefix) {
				t.Errorf("format-v3 repository holds %s, which belongs to the packfile format", key)
			}
		}
	}
}

func TestCLI_Feature_FormatV3RoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		encrypted bool
	}{
		{"unencrypted", false},
		{"encrypted", true},
	} {
		runFeatureMatrix(t, featureSpec{
			name: "format_v3_" + tc.name,
			// The format is a property of the repository, not of the source,
			// and the fixtures below must clear the 512 KiB inline threshold —
			// which the portable source's small RAM disk cannot hold.
			sourceFilter: localOnlySource,
			storeFilter:  localOnlyStore,
			test: func(t *testing.T, h *harness, _ matrixEntry) {
				// One file per storage shape v3 has: inlined into a leaf, and
				// chunked with only its refs in the leaf.
				h.WithFile("small.txt", "inlined into the leaf")
				h.WithFile("nested/also-small.txt", "also inlined")
				h.WithFile("big.bin", string(incompressibleBytes(768*1024)))

				var r *repo
				if tc.encrypted {
					r = h.MustInitEncrypted("-format", "3")
				} else {
					// InitUnencrypted takes no extra arguments, so the v3
					// unencrypted repository is initialized directly.
					args := append([]string{"init", "--no-encryption", "-format", "3"}, h.storeArgs...)
					run(t, h.bin, args...)
					r = &repo{h: h, authArgs: append([]string{}, h.storeArgs...)}
				}

				r.Backup()
				mustHaveNoV2Namespaces(t, h)

				// Reading it back through the CLI: the tree, one file's bytes,
				// and a full integrity check including the data itself.
				r.Ls("latest").MustContain("small.txt")
				r.Check("-read-data")

				restored := r.RestoreDir("v3-restore")
				restored.MustHaveFileContent("small.txt", "inlined into the leaf")
				restored.MustHaveFileContent("nested/also-small.txt", "also inlined")
				mustHaveExactBytes(t, restored.Path(), "big.bin", string(incompressibleBytes(768*1024)))

				// An incremental over the same repository exercises change
				// detection reading its metadata out of leaf payloads.
				r.WithFile("small.txt", "changed").WithFile("added.txt", "new").Backup()
				r.List().MustHaveSnapshotCount(2)
				mustHaveNoV2Namespaces(t, h)

				// Forget plus prune is the destructive path: it must collect
				// the first snapshot's garbage and leave the survivor intact.
				r.Forget("--keep-last", "1", "--prune").MustRemove(1)
				r.Check("-read-data")
				after := r.RestoreDir("v3-after-prune")
				after.MustHaveFileContent("small.txt", "changed")
				after.MustHaveFileContent("added.txt", "new")
				mustHaveNoV2Namespaces(t, h)
			},
		})
	}
}

// A repository created without -format keeps the packfile format, so the flag
// is genuinely opt-in and the default path is unchanged.
func TestCLI_Feature_DefaultFormatStillPacks(t *testing.T) {
	t.Parallel()
	runFeatureMatrix(t, featureSpec{
		name:         "default_format_packs",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, _ matrixEntry) {
			r := h.WithFile("a.txt", "alpha").MustInitEncrypted()
			r.Backup()

			var packed bool
			for _, key := range storeObjectKeys(t, localStoreDir(t, h)) {
				if strings.HasPrefix(key, "packs/") {
					packed = true
					break
				}
			}
			if !packed {
				t.Error("a default-format repository wrote no packfiles")
			}
		},
	})
}

// Re-initializing a packfile-era repository as v3 must be refused: rewriting
// the marker cannot convert the structures it describes (RFC 0026).
func TestCLI_Feature_FormatV3RefusesReinit(t *testing.T) {
	t.Parallel()
	runFeatureMatrix(t, featureSpec{
		name:         "format_v3_refuses_reinit",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, _ matrixEntry) {
			r := h.WithFile("a.txt", "alpha").MustInitEncrypted()
			r.Backup()

			args := append([]string{"init", "-format", "3", "-adopt-slots"}, h.storeArgs...)
			args = append(args, "-password", "test-matrix-passphrase")
			out := runExpectFail(t, h.bin, args...)
			if !strings.Contains(out, "migrate") {
				t.Errorf("refusal should point at migration, got:\n%s", out)
			}
		},
	})
}
