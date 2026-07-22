package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/crypto"
)

// storeDir returns the on-disk root of a local store, so a test can inspect the
// bytes a real `cloudstic` process actually wrote.
func storeDir(t *testing.T, h *harness) string {
	t.Helper()
	local, ok := h.store.(*localStore)
	if !ok {
		t.Skipf("store %T is not inspectable on the host filesystem", h.store)
	}
	return local.dir
}

// readPackArtefacts returns the raw catalog and packfile bytes on disk.
func readPackArtefacts(t *testing.T, dir string) (catalog []byte, packs [][]byte) {
	t.Helper()

	catalog, err := os.ReadFile(filepath.Join(dir, "index", "packs"))
	if err != nil {
		t.Fatalf("read index/packs: %v", err)
	}

	paths, err := filepath.Glob(filepath.Join(dir, "packs", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no packfiles were written; the test is not exercising the pack path")
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		packs = append(packs, data)
	}
	return catalog, packs
}

// A real backup through the CLI must leave no object keys readable in the pack
// catalog or in any packfile footer, and must still restore byte for byte.
func TestCLI_Feature_PackIndexIsSealedOnDisk(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "pack_index_sealed_on_disk",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			dir := storeDir(t, h)

			r := h.
				WithFile("invoice.txt", "classified invoice data").
				WithFile("subdir/nested.txt", "nested content").
				MustInitEncrypted()
			r.Backup()

			catalog, packs := readPackArtefacts(t, dir)

			if !crypto.IsEncrypted(catalog) {
				t.Errorf("index/packs is not sealed; first bytes: %x", catalog[:min(16, len(catalog))])
			}
			for _, marker := range []string{"filemeta/", "node/", "snapshot/", "packs/"} {
				if strings.Contains(string(catalog), marker) {
					t.Errorf("index/packs leaks %q in plaintext", marker)
				}
			}

			for i, pack := range packs {
				for _, marker := range []string{"filemeta/", "node/", "snapshot/"} {
					if strings.Contains(string(pack), marker) {
						t.Errorf("packfile %d leaks %q in plaintext", i, marker)
					}
				}
			}

			// Sealing must not have broken anything the user can observe.
			r.Check()
			r.List().MustHaveSnapshotCount(1)
			dirOut := r.RestoreDir("restore-sealed")
			dirOut.MustHaveFileContent("invoice.txt", "classified invoice data")
			dirOut.MustHaveFileContent("subdir/nested.txt", "nested content")
		},
	})
}

// An unencrypted repository has no key to seal with; it must keep working and
// keep its indexes readable, rather than failing or half-sealing.
func TestCLI_Feature_PackIndexUnsealedWithoutEncryption(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "pack_index_unsealed_without_encryption",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			dir := storeDir(t, h)

			r := h.WithFile("plain.txt", "not secret").MustInitUnencrypted()
			r.Backup()

			catalog, _ := readPackArtefacts(t, dir)
			if crypto.IsEncrypted(catalog) {
				t.Error("an unencrypted repository should not seal its pack catalog")
			}
			if !strings.Contains(string(catalog), "filemeta/") {
				t.Errorf("expected a plaintext catalog, got: %.80s", catalog)
			}

			r.Check()
			r.RestoreDir("restore-plain").MustHaveFileContent("plain.txt", "not secret")
		},
	})
}
