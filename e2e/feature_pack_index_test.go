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

// readPackArtefacts returns the raw bytes of every pack index object and every
// packfile on disk. The index is sharded, so this collects the shards plus the
// pre-shard monolithic catalog if the repository still has one.
func readPackArtefacts(t *testing.T, dir string) (index [][]byte, packs [][]byte) {
	t.Helper()

	indexPaths, err := filepath.Glob(filepath.Join(dir, "index", "packmap", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if legacy := filepath.Join(dir, "index", "packs"); fileExists(legacy) {
		indexPaths = append(indexPaths, legacy)
	}
	if len(indexPaths) == 0 {
		t.Fatal("no pack index objects were written; the test is not exercising the index path")
	}
	for _, p := range indexPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		index = append(index, data)
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
	return index, packs
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A real backup through the CLI must leave no object keys readable in the pack
// catalog or in any packfile footer, and must still restore byte for byte.
func TestCLI_Feature_PackIndexIsSealedOnDisk(t *testing.T) {
	t.Parallel()
	runFeatureMatrix(t, featureSpec{
		name:         "pack_index_sealed_on_disk",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			dir := storeDir(t, h)

			r := h.
				WithFile("invoice.txt", "classified invoice data").
				WithFile("subdir/nested.txt", "nested content").
				MustInitEncrypted("-format", "2")
			r.Backup()

			index, packs := readPackArtefacts(t, dir)

			for i, obj := range index {
				if !crypto.IsEncrypted(obj) {
					t.Errorf("pack index object %d is not sealed; first bytes: %x", i, obj[:min(16, len(obj))])
				}
				for _, marker := range []string{"filemeta/", "node/", "snapshot/", "packs/"} {
					if strings.Contains(string(obj), marker) {
						t.Errorf("pack index object %d leaks %q in plaintext", i, marker)
					}
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
	t.Parallel()
	runFeatureMatrix(t, featureSpec{
		name:         "pack_index_unsealed_without_encryption",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			dir := storeDir(t, h)

			r := h.WithFile("plain.txt", "not secret").MustInitUnencrypted("-format", "2")
			r.Backup()

			index, _ := readPackArtefacts(t, dir)
			plaintextEntries := false
			for _, obj := range index {
				if crypto.IsEncrypted(obj) {
					t.Error("an unencrypted repository should not seal its pack index")
				}
				if strings.Contains(string(obj), "filemeta/") {
					plaintextEntries = true
				}
			}
			if !plaintextEntries {
				t.Error("expected a plaintext pack index naming its entries")
			}

			r.Check()
			r.RestoreDir("restore-plain").MustHaveFileContent("plain.txt", "not secret")
		},
	})
}
