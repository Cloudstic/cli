package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/crypto"
)

// Backward compatibility is a permanent guarantee: a repository written by any
// released version stays readable by every later one. See docs/compatibility.md.
//
// It is enforced here against real repositories produced by the releases
// themselves, not simulations of them. Each fixture directory under
// testdata/legacy-repo-*/ carries a manifest describing what the repository
// should contain, so adding a new baseline is a directory drop with no code
// change.

const legacyFixturePrefix = "legacy-repo-"

// fixtureManifest describes a committed legacy repository. Files that are not
// part of the repository itself (this manifest, the README) are excluded when
// the fixture is materialised.
type fixtureManifest struct {
	Release     string            `json:"release"`
	Password    string            `json:"password"`
	Snapshot    string            `json:"snapshot"`
	Files       map[string]string `json:"files"`
	SealedIndex bool              `json:"sealed_index"`
	Notes       string            `json:"notes"`
}

var fixtureNonRepoFiles = map[string]bool{
	"manifest.json": true,
	"README.md":     true,
}

// legacyFixtures returns every committed baseline, newest-name last.
func legacyFixtures(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), legacyFixturePrefix) {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	if len(dirs) == 0 {
		t.Fatal("no legacy repository fixtures found; the backward-compatibility guarantee is unenforced")
	}
	return dirs
}

func readFixtureManifest(t *testing.T, dir string) fixtureManifest {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", dir, "manifest.json"))
	if err != nil {
		t.Fatalf("%s: every fixture needs a manifest.json: %v", dir, err)
	}
	var m fixtureManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s: parse manifest: %v", dir, err)
	}
	if m.Release == "" || m.Password == "" || len(m.Files) == 0 {
		t.Fatalf("%s: manifest must set release, password, and files", dir)
	}
	return m
}

// materialiseFixture copies a fixture into a writable temp directory, since the
// commands under test write to it.
func materialiseFixture(t *testing.T, dir string) string {
	t.Helper()

	src := filepath.Join("testdata", dir)
	dst := t.TempDir()

	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." || fixtureNonRepoFiles[rel] {
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
		t.Fatalf("copy fixture %s: %v", dir, err)
	}
	return dst
}

// Every committed baseline must remain fully usable by the current build:
// listable, checkable, restorable, and writable.
func TestCLI_Feature_ReadsLegacyRepositories(t *testing.T) {
	bin := buildBinary(t)

	for _, dir := range legacyFixtures(t) {
		t.Run(dir, func(t *testing.T) {
			m := readFixtureManifest(t, dir)
			storeDir := materialiseFixture(t, dir)
			auth := []string{"-store", "local:" + storeDir, "-password", m.Password}

			t.Run("list", func(t *testing.T) {
				out := run(t, bin, append([]string{"list"}, auth...)...)
				if m.Snapshot != "" && !strings.Contains(out, m.Snapshot[:16]) {
					t.Errorf("expected snapshot %s in list output, got:\n%s", m.Snapshot[:16], out)
				}
			})

			t.Run("check", func(t *testing.T) {
				out := run(t, bin, append([]string{"check"}, auth...)...)
				if !strings.Contains(out, "No errors found") {
					t.Errorf("check should pass on %s, got:\n%s", m.Release, out)
				}
			})

			t.Run("restore", func(t *testing.T) {
				outDir := filepath.Join(t.TempDir(), "restored")
				args := append([]string{"restore"}, auth...)
				args = append(args, "-format", "dir", "-output", outDir)
				run(t, bin, args...)
				assertFixtureFiles(t, outDir, m.Files)
			})

			t.Run("backup onto it", func(t *testing.T) {
				sourceDir := t.TempDir()
				if err := os.WriteFile(filepath.Join(sourceDir, "added.txt"), []byte("added later\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				args := append([]string{"backup"}, auth...)
				args = append(args, "-source", "local:"+sourceDir)
				run(t, bin, args...)

				// New entries land in a sealed shard. The pre-shard monolithic
				// catalog is left as it is — it is still read, and a prune
				// folds it in — so this asserts the shard, not the monolith.
				shards, err := filepath.Glob(filepath.Join(storeDir, "index", "packmap", "*"))
				if err != nil {
					t.Fatal(err)
				}
				if len(shards) == 0 {
					t.Fatal("a backup should have written a pack index shard")
				}
				for _, path := range shards {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if !crypto.IsEncrypted(data) {
						t.Errorf("shard %s should be sealed, got: %.60s", filepath.Base(path), data)
					}
				}

				// The original snapshot must survive and still restore.
				if m.Snapshot != "" {
					outDir := filepath.Join(t.TempDir(), "after-backup")
					restoreArgs := append([]string{"restore"}, auth...)
					restoreArgs = append(restoreArgs, "-format", "dir", "-output", outDir, m.Snapshot)
					run(t, bin, restoreArgs...)
					assertFixtureFiles(t, outDir, m.Files)
				}
			})

			// A repository below the framing format must be raised before the
			// first object is written, not after. An object written unframed is
			// unframed permanently — content-addressed objects are never
			// rewritten, so a later framed backup skips them on the Exists
			// check — and an already-compressed file stored that way is
			// unrestorable forever.
			//
			// This needs its own copy of the fixture: it has to be the *first*
			// backup onto the repository, while it still records the old
			// format. Sharing storeDir with the subtest above would run it
			// against a repository that backup had already stamped, which is
			// exactly the case that was never broken.
			//
			// Every released repository is below the framing format, so the
			// window this covers is every user's first backup after upgrading.
			t.Run("already-compressed file survives the format upgrade", func(t *testing.T) {
				freshStore := materialiseFixture(t, dir)
				freshAuth := []string{"-store", "local:" + freshStore, "-password", m.Password}

				archive := gzipFixture(t, incompressibleBytes(768*1024))
				gzDir := t.TempDir()
				if err := os.WriteFile(filepath.Join(gzDir, "archive.gz"), []byte(archive), 0o644); err != nil {
					t.Fatal(err)
				}

				backupArgs := append([]string{"backup"}, freshAuth...)
				backupArgs = append(backupArgs, "-source", "local:"+gzDir)
				snapshot := snapshotIDFrom(t, run(t, bin, backupArgs...))

				// Restore by id. The fixture holds snapshots from another
				// source, and "latest" resolves per source identity, so it
				// would not name the one just written.
				outDir := filepath.Join(t.TempDir(), "compressed")
				restoreArgs := append([]string{"restore"}, freshAuth...)
				restoreArgs = append(restoreArgs, "-format", "dir", "-output", outDir, snapshot)
				run(t, bin, restoreArgs...)
				mustHaveExactBytes(t, outDir, "archive.gz", archive)
			})
		})
	}
}

// snapshotIDFrom extracts the id a backup reported writing.
func snapshotIDFrom(t *testing.T, backupOutput string) string {
	t.Helper()
	for _, line := range strings.Split(backupOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "Snapshot" && fields[2] == "saved" {
			return fields[1]
		}
	}
	t.Fatalf("no snapshot id in backup output:\n%s", backupOutput)
	return ""
}

func assertFixtureFiles(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	for rel, content := range want {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("restore %s: %v", rel, err)
			continue
		}
		if string(got) != content {
			t.Errorf("restored %s = %q, want %q", rel, got, content)
		}
	}
}

// The compatibility contract names the baselines it guarantees. A fixture that
// is not documented, or a documented baseline with no fixture, means the
// written guarantee and the enforced one have drifted apart.
func TestCompatibilityDocListsEveryFixture(t *testing.T) {
	table := guaranteedBaselinesTable(t)

	documented := documentedBaselines(table)
	fixtured := make(map[string]bool)

	for _, dir := range legacyFixtures(t) {
		m := readFixtureManifest(t, dir)
		fixtured[m.Release] = true
		// Match the table-row form, not a bare mention: releases are named in
		// the prose too, and searching the whole document would let an
		// undocumented baseline pass.
		if !documented[m.Release] {
			t.Errorf("the Guaranteed baselines table in docs/compatibility.md does not list %s (fixture %s)",
				m.Release, dir)
		}
	}

	// The other direction. A documented baseline with no fixture is the more
	// dangerous drift of the two: the contract promises a guarantee that
	// nothing enforces, and its absence is invisible precisely because no
	// fixture exists to notice.
	for release := range documented {
		if !fixtured[release] {
			t.Errorf("docs/compatibility.md guarantees baseline %s, but there is no %s%s fixture enforcing it",
				release, legacyFixturePrefix, release)
		}
	}
}

// documentedBaselines returns the releases named in the first column of the
// Guaranteed baselines table.
func documentedBaselines(table string) map[string]bool {
	out := make(map[string]bool)
	for _, line := range strings.Split(table, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) == 0 {
			continue
		}
		release := strings.TrimSpace(cells[0])
		// Skip the header row and the separator beneath it; only the data
		// rows carry a release, and they carry it in backticks.
		if !strings.HasPrefix(release, "`") || !strings.HasSuffix(release, "`") {
			continue
		}
		out[strings.Trim(release, "`")] = true
	}
	return out
}

// guaranteedBaselinesTable returns only the "Guaranteed baselines" section of
// the compatibility doc.
func guaranteedBaselinesTable(t *testing.T) string {
	t.Helper()

	const heading = "### Guaranteed baselines"

	data, err := os.ReadFile(filepath.Join("..", "docs", "compatibility.md"))
	if err != nil {
		t.Fatalf("read compatibility doc: %v", err)
	}
	doc := string(data)

	start := strings.Index(doc, heading)
	if start < 0 {
		t.Fatalf("docs/compatibility.md is missing the %q section", heading)
	}
	rest := doc[start+len(heading):]

	// The section runs until the next heading of the same level or higher.
	end := len(rest)
	for _, next := range []string{"\n## ", "\n### "} {
		if i := strings.Index(rest, next); i >= 0 && i < end {
			end = i
		}
	}
	return rest[:end]
}
