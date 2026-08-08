package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// fingerprint hashes every path and its contents, so two trees compare equal
// only if they are byte-identical.
func fingerprint(t *testing.T, root string) string {
	t.Helper()

	var paths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		rel, _ := filepath.Rel(root, p)
		h.Write([]byte(rel))
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// The property the whole benchmark rests on. Comparing two builds of cloudstic
// requires the input to be identical; a dataset that drifts between runs turns
// every measurement into noise.
func TestGenerateIsDeterministic(t *testing.T) {
	for name := range profiles {
		t.Run(name, func(t *testing.T) {
			p := fitToBudget(profiles[name], 400, 16<<20)

			a, b := t.TempDir(), t.TempDir()
			if _, err := generate(a, p, 400, 42); err != nil {
				t.Fatal(err)
			}
			if _, err := generate(b, p, 400, 42); err != nil {
				t.Fatal(err)
			}
			if fingerprint(t, a) != fingerprint(t, b) {
				t.Error("the same seed produced different trees")
			}
		})
	}
}

func TestGenerateDiffersBySeed(t *testing.T) {
	p := fitToBudget(profiles["mixed"], 400, 16<<20)
	a, b := t.TempDir(), t.TempDir()
	if _, err := generate(a, p, 400, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := generate(b, p, 400, 2); err != nil {
		t.Fatal(err)
	}
	if fingerprint(t, a) == fingerprint(t, b) {
		t.Error("different seeds produced the same tree")
	}
}

// Duplication has to be real, or the repository's content addressing looks free.
func TestRealisticProfilesDuplicateContent(t *testing.T) {
	for _, name := range []string{"source", "mixed", "media"} {
		t.Run(name, func(t *testing.T) {
			p := fitToBudget(profiles[name], 800, 32<<20)
			st, err := generate(t.TempDir(), p, 800, 3)
			if err != nil {
				t.Fatal(err)
			}
			if st.DuplicateFiles == 0 {
				t.Errorf("%s produced no duplicate files; dedup would be unmeasurable", name)
			}
			if st.DedupRatio <= 0 {
				t.Errorf("%s dedup ratio is %.3f, want > 0", name, st.DedupRatio)
			}
		})
	}
}

// The uniform profile is the historical baseline and must stay flat and
// duplicate-free, or previously recorded numbers stop being comparable.
func TestUniformProfileStaysUniform(t *testing.T) {
	st, err := generate(t.TempDir(), profiles["uniform"], 500, 1)
	if err != nil {
		t.Fatal(err)
	}
	if st.DuplicateFiles != 0 {
		t.Errorf("uniform produced %d duplicate files, want 0", st.DuplicateFiles)
	}
	if st.MedianSize != st.MaxSize {
		t.Errorf("uniform sizes vary: median %d, max %d", st.MedianSize, st.MaxSize)
	}
	if st.MaxDepth != 1 {
		t.Errorf("uniform depth is %d, want 1", st.MaxDepth)
	}
}

// Sizes and fan-out must actually be spread. A "realistic" profile that
// collapses to a constant would reintroduce the flaw it exists to fix.
func TestRealisticProfilesAreHeavyTailed(t *testing.T) {
	p := fitToBudget(profiles["mixed"], 1500, 64<<20)
	st, err := generate(t.TempDir(), p, 1500, 5)
	if err != nil {
		t.Fatal(err)
	}
	if st.P99Size <= st.MedianSize*4 {
		t.Errorf("p99 size %d is not far above median %d; the tail is missing", st.P99Size, st.MedianSize)
	}
	if st.MaxFanout <= st.MedianFanout*3 {
		t.Errorf("max fan-out %d is not far above median %d", st.MaxFanout, st.MedianFanout)
	}
	if st.MaxDepth < 3 {
		t.Errorf("max depth %d; the tree is too shallow to be a real one", st.MaxDepth)
	}
}

// Churn must concentrate. Spread evenly it touches nearly every metadata leaf,
// which is the assumption that made the uniform tree misleading about write
// amplification.
//
// The count comes from the churn itself rather than from diffing paths before
// and after: one directory rename moves every path beneath it without touching
// a single file, and a path diff would score all of those as churn.
func TestChurnConcentratesInFewDirectories(t *testing.T) {
	root := t.TempDir()
	p := fitToBudget(profiles["source"], 2000, 64<<20)
	if _, err := generate(root, p, 2000, 9); err != nil {
		t.Fatal(err)
	}

	st, err := applyChurn(root, p, 9, 0.05, 0)
	if err != nil {
		t.Fatal(err)
	}
	changed := st.Modified + st.Created + st.Deleted
	if changed == 0 {
		t.Fatal("churn changed nothing")
	}

	dirs, err := collectDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	// Uniform churn of this many files would touch roughly one directory per
	// changed file. Concentrated, it should touch far fewer.
	if st.TouchedDirs >= changed {
		t.Errorf("churn touched %d directories for %d changes; it is not concentrating",
			st.TouchedDirs, changed)
	}
	t.Logf("%d changes across %d of %d directories", changed, st.TouchedDirs, len(dirs))
}

// An exact count must hold regardless of tree size: "1000 changed" has to mean
// 1000 files whether the tree has 2000 or 50000, which -fraction cannot
// express since it scales with the tree.
func TestChurnCountIsExactAcrossTreeSizes(t *testing.T) {
	const count = 50
	for _, files := range []int{200, 2000} {
		t.Run(fmt.Sprintf("%d files", files), func(t *testing.T) {
			root := t.TempDir()
			p := fitToBudget(profiles["source"], files, 64<<20)
			if _, err := generate(root, p, files, 9); err != nil {
				t.Fatal(err)
			}
			st, err := applyChurn(root, p, 9, 0.05, count)
			if err != nil {
				t.Fatal(err)
			}
			changed := st.Modified + st.Created + st.Deleted
			if changed != count {
				t.Errorf("changed %d files out of %d in tree, want exactly %d", changed, files, count)
			}
		})
	}
}

// A count larger than the tree must not be requested forever: it should be
// capped at the number of files that actually exist, and reached exactly —
// not just approached. A single share-weighted pass over each directory
// (20-80% of its files) never reaches every file on its own, so hitting the
// cap requires revisiting directories across passes.
func TestChurnCountIsCappedByTreeSize(t *testing.T) {
	root := t.TempDir()
	p := fitToBudget(profiles["source"], 100, 16<<20)
	if _, err := generate(root, p, 100, 3); err != nil {
		t.Fatal(err)
	}
	st, err := applyChurn(root, p, 3, 0.05, 10000)
	if err != nil {
		t.Fatal(err)
	}
	changed := st.Modified + st.Created + st.Deleted
	if changed != 100 {
		t.Errorf("changed %d files in a 100-file tree, want exactly 100 (the capped budget)", changed)
	}
}

// A rename must move a directory inside the tree, never the tree itself.
//
// Asserted on the candidate set rather than by running churn: renameOneDir picks
// one candidate at random, so a test that only watches the root survive passes
// by luck whenever the draw misses it.
func TestRenameCandidatesExcludeTheRoot(t *testing.T) {
	// A root nested deep enough that its absolute path clears any separator
	// count, which is what the previous check measured.
	root := filepath.Join(t.TempDir(), "a", "b", "c", "source")
	dirs := []string{
		root,
		filepath.Join(root, "one"),
		filepath.Join(root, "one", "two"),
	}

	got := renameCandidates(root, dirs)
	for _, c := range got {
		if c == root {
			t.Fatalf("the source root is a rename candidate: %v", got)
		}
	}
	if len(got) != 1 || got[0] != filepath.Join(root, "one", "two") {
		t.Fatalf("candidates = %v, want only the two-deep directory", got)
	}
}

// A delete must stay deleted. Paths are drawn with replacement, so a path
// removed and drawn again would be recreated by the modify branch, leaving the
// reported counts describing a tree that never existed.
func TestChurnDeletionsAreNotRecreated(t *testing.T) {
	root := t.TempDir()
	p := fitToBudget(profiles["source"], 1200, 32<<20)
	if _, err := generate(root, p, 1200, 6); err != nil {
		t.Fatal(err)
	}

	// Many seeds at a high churn fraction: a delete followed by a redraw of the
	// same path is a collision, and one run may not produce it.
	for seed := int64(0); seed < 20; seed++ {
		before := snapshotFiles(t, root)
		st, err := applyChurn(root, p, seed, 0.30, 0)
		if err != nil {
			t.Fatal(err)
		}
		after := snapshotFiles(t, root)

		gone := 0
		for path := range before {
			if _, ok := after[path]; !ok {
				gone++
			}
		}
		if gone < st.Deleted {
			t.Fatalf("seed %d: reported %d deletions but only %d paths are gone; some were recreated",
				seed, st.Deleted, gone)
		}
	}
}

func snapshotFiles(t *testing.T, root string) map[string]string {
	t.Helper()

	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[path] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A budget must actually bound the tree, or a realistic media profile is
// unrunnable in CI.
func TestFitToBudgetBoundsTheTree(t *testing.T) {
	const budget = 8 << 20
	p := fitToBudget(profiles["media"], 500, budget)
	st, err := generate(t.TempDir(), p, 500, 11)
	if err != nil {
		t.Fatal(err)
	}
	// The budget sets the expected total; sampling puts the realised total near
	// it, not exactly on it. A wide factor still catches a budget that does
	// nothing, which is what this guards.
	if st.TotalBytes > budget*4 {
		t.Errorf("generated %d bytes against a %d budget", st.TotalBytes, budget)
	}
}
