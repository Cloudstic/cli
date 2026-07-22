package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/crypto"
)

// legacyFixtureVersion is the release that produced the committed fixture.
const legacyFixtureVersion = "v1.14.0"

const legacyFixturePassword = "fixture-password"

// copyLegacyFixture materialises the committed pre-sealing repository into a
// writable temp directory, since the commands under test may write to it.
func copyLegacyFixture(t *testing.T) string {
	t.Helper()

	src := filepath.Join("testdata", "legacy-repo-"+legacyFixtureVersion)
	dst := t.TempDir()

	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." || rel == "README.md" {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy legacy fixture: %v", err)
	}
	return dst
}

// A repository written by the released v1.14.0 binary must remain fully
// readable. The fixture predates packfile footers and index sealing, and stores
// index/latest inside a packfile, so it exercises every legacy shape at once.
//
// This is deliberately tested against real historical output rather than a
// simulated old repository: the simulations in the unit and integration tests
// encode our belief about what an old client wrote, and this pins what one
// actually did write.
func TestCLI_Feature_ReadsLegacyRepository(t *testing.T) {
	bin := buildBinary(t)
	storeDir := copyLegacyFixture(t)
	storeArg := "local:" + storeDir

	auth := []string{"-store", storeArg, "-password", legacyFixturePassword}

	t.Run("list", func(t *testing.T) {
		out := run(t, bin, append([]string{"list"}, auth...)...)
		if !strings.Contains(out, "e05649fd8d4af2dc") {
			t.Errorf("expected the legacy snapshot in list output, got:\n%s", out)
		}
	})

	t.Run("check", func(t *testing.T) {
		out := run(t, bin, append([]string{"check"}, auth...)...)
		if !strings.Contains(out, "No errors found") {
			t.Errorf("check should pass on the legacy repository, got:\n%s", out)
		}
	})

	t.Run("restore", func(t *testing.T) {
		outDir := filepath.Join(t.TempDir(), "restored")
		args := append([]string{"restore"}, auth...)
		args = append(args, "-format", "dir", "-output", outDir)
		run(t, bin, args...)

		for path, want := range map[string]string{
			"doc.txt":        "legacy document contents\n",
			"sub/nested.txt": "nested legacy content\n",
		} {
			got, err := os.ReadFile(filepath.Join(outDir, path))
			if err != nil {
				t.Errorf("restore %s from legacy repository: %v", path, err)
				continue
			}
			if string(got) != want {
				t.Errorf("restored %s = %q, want %q", path, got, want)
			}
		}
	})

	t.Run("backup onto the legacy repository", func(t *testing.T) {
		sourceDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(sourceDir, "added.txt"), []byte("added later\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		args := append([]string{"backup"}, auth...)
		args = append(args, "-source", "local:"+sourceDir)
		run(t, bin, args...)

		// The legacy plaintext catalog must be sealed once a current binary
		// flushes it, so an upgraded repository does not stay exposed.
		catalog, err := os.ReadFile(filepath.Join(storeDir, "index", "packs"))
		if err != nil {
			t.Fatalf("read catalog after backup: %v", err)
		}
		if !crypto.IsEncrypted(catalog) {
			t.Errorf("catalog should be sealed after a backup by a current binary, got: %.60s", catalog)
		}

		out := run(t, bin, append([]string{"list"}, auth...)...)
		if !strings.Contains(out, "e05649fd8d4af2dc") {
			t.Errorf("the legacy snapshot should survive a new backup, got:\n%s", out)
		}

		// And the legacy snapshot must still restore afterwards.
		outDir := filepath.Join(t.TempDir(), "after-backup")
		restoreArgs := append([]string{"restore"}, auth...)
		restoreArgs = append(restoreArgs, "-format", "dir", "-output", outDir)
		restoreArgs = append(restoreArgs, "e05649fd8d4af2dc476cb4108e57d94118730d06a788fcedddd55c09cdee5456")
		run(t, bin, restoreArgs...)

		got, err := os.ReadFile(filepath.Join(outDir, "doc.txt"))
		if err != nil {
			t.Fatalf("legacy snapshot unreadable after a new backup: %v", err)
		}
		if string(got) != "legacy document contents\n" {
			t.Errorf("doc.txt = %q", got)
		}
	})
}
