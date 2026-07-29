package e2e

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestCLI_Feature_BackupCrashConsistency simulates the backup process
// crashing at a range of points during a second, incremental backup, via
// CLOUDSTIC_TEST_CRASH_AFTER_PUTS — a test-only knob that hard-exits the
// binary (os.Exit, skipping every deferred flush) after its Nth physical
// store write. Whatever point the crash lands at, the repository must come
// out either fully at the old snapshot or fully at the new one: `check`
// must report it healthy, `list` must show exactly the snapshots that
// implies, and restoring the latest snapshot must reproduce one of the two
// known-good file sets, never a mix.
//
// This is the acceptance test for the backup flush-ordering fix (writing
// the snapshot object and flushing packs before updating index/latest) —
// crashing between those steps is exactly the scenario that fix protects
// against.
func TestCLI_Feature_BackupCrashConsistency(t *testing.T) {
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}
	t.Parallel()
	bin := buildBinary(t)

	// Files well over the 512KiB chunk-size floor so each one's content
	// chunks bypass PackStore's small-object bundling and land as their own
	// physical store write. That's what gives early/mid/late crash points
	// inside a single backup, rather than everything landing in one packfile
	// flushed atomically at the end.
	const (
		fileCount = 20
		fileSize  = 3 * 1024 * 1024
	)

	v1 := seedContent(fileCount, fileSize)
	// A different size, not just different bytes: local source scanning
	// records mtime at one-second resolution, and v1/v2 are written well
	// within the same wall-clock second, so a same-size rewrite would be
	// indistinguishable from an untouched file and get skipped by
	// incremental change detection — masking the crash window entirely.
	v2 := seedContent(fileCount, fileSize+4096)

	writeContent := func(h *harness, content map[string]string) {
		for relPath, data := range content {
			h.writeFile(relPath, data)
		}
	}

	newRepoAtV1 := func(t *testing.T) (*repo, *harness) {
		t.Helper()
		h := newHarness(t, bin, newLocalSource(t), newLocalStore(t))
		r := h.MustInitUnencrypted()
		writeContent(h, v1)
		r.Backup()
		return r, h
	}

	// Establish how many physical store writes the v1 -> v2 change produces,
	// so a "one before the very last write" crash point (which should land
	// between the snapshot/pack flush and the index/latest update) doesn't
	// have to hardcode an assumption about tree shape.
	control, controlH := newRepoAtV1(t)
	writeContent(controlH, v2)
	totalPuts := control.BackupPutCount()
	if totalPuts < 2 {
		t.Fatalf("expected the v1 -> v2 change to produce at least 2 store writes, got %d", totalPuts)
	}

	for _, tc := range []struct {
		name string
		n    int
	}{
		{"early", 5},
		{"mid", 20},
		{"late", 50},
		{"last-1", totalPuts - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.n <= 0 || tc.n > totalPuts {
				t.Skipf("crash point %d is out of range for a %d-put backup", tc.n, totalPuts)
			}

			r, h := newRepoAtV1(t)
			writeContent(h, v2)

			env := []string{"CLOUDSTIC_TEST_CRASH_AFTER_PUTS=" + strconv.Itoa(tc.n)}
			out, exitCode := r.BackupWithEnv(env)
			switch exitCode {
			case 0:
				// The change had fewer than tc.n writes after all (e.g. a
				// dedup or catalog write moved); the backup ran to completion.
			case crashInjectedExitCode:
				// The simulated crash fired, mid-backup.
			default:
				t.Fatalf("backup exited %d, want 0 or %d (simulated crash):\n%s", exitCode, crashInjectedExitCode, out)
			}

			r.Check("--read-data").MustContain("repository is healthy")

			rows := r.List().SnapshotRows()
			if len(rows) != 1 && len(rows) != 2 {
				t.Fatalf("expected 1 (crash undid the new snapshot) or 2 (new snapshot committed) snapshots, got %d:\n%s", len(rows), out)
			}

			restored := r.RestoreDir("latest")
			switch len(rows) {
			case 1:
				assertRestoredContent(t, restored.Path(), v1)
			case 2:
				assertRestoredContent(t, restored.Path(), v2)
			}
		})
	}
}

// crashInjectedExitCode mirrors cmd/cloudstic's exitCrashInjected constant.
// It is not imported directly since e2e drives the compiled binary as a
// subprocess rather than linking against cmd/cloudstic.
const crashInjectedExitCode = 137

// seedContent returns fileCount files under "dir/", each fileSize bytes of
// random (incompressible, undedupable) data.
func seedContent(fileCount, fileSize int) map[string]string {
	content := make(map[string]string, fileCount)
	for i := range fileCount {
		relPath := filepath.Join("dir", fmt.Sprintf("file%02d.txt", i))
		data := make([]byte, fileSize)
		if _, err := rand.Read(data); err != nil {
			panic(fmt.Sprintf("generate test content: %v", err))
		}
		content[relPath] = string(data)
	}
	return content
}

// assertRestoredContent fails the test unless every file in want is present
// under root with exactly the expected content.
func assertRestoredContent(t *testing.T, root string, want map[string]string) {
	t.Helper()
	for relPath, wantData := range want {
		fullPath := filepath.Join(root, filepath.FromSlash(relPath))
		got, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("restored dir missing %s: %v", relPath, err)
		}
		if string(got) != wantData {
			t.Fatalf("restored content mismatch for %s: got %d bytes, want %d bytes", relPath, len(got), len(wantData))
		}
	}
}
