package main

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// applyChurn mutates a generated tree the way a day between two backups would.
//
// The shape of the churn matters more to a backup than its volume. An
// incremental that rewrites 5% of files spread evenly across every directory
// touches nearly every metadata leaf; the same 5% concentrated in a few working
// directories touches a handful. Those two produce very different write
// amplification from the same headline churn rate, and the uniform generator
// could only express the first.
//
// Four kinds of change, because they stress different parts of the format:
//
//   - modify: same path, new content. New content object, same file identity.
//   - append: same path, content grows. The case chunking is supposed to handle
//     without rewriting what came before.
//   - create/delete: new and vanished paths, which move the tree's shape.
//   - rename a directory: with FileID identity nothing below it should be
//     rewritten, and with path identity everything would be. It is the cheapest
//     way to tell the two apart, so the generator has to be able to produce it.
//
// count, when greater than zero, sets the churn budget directly — "1000
// changed" has to mean 1000 files at every tree size, which a fraction cannot
// express without scaling with the tree. Zero defers to fraction, the
// original size-relative behaviour.
func applyChurn(root string, p profile, seed int64, fraction float64, count int) (stats, error) {
	rng := rand.New(rand.NewSource(seed ^ 0x5eed))
	st := stats{}

	dirs, err := collectDirs(root)
	if err != nil {
		return st, err
	}
	if len(dirs) == 0 {
		return st, fmt.Errorf("no directories under %s", root)
	}

	// Directories are ranked, then chosen by a Zipf law over the ranking, so the
	// same few are hot across successive churns rather than a fresh random set
	// each time. That is what makes repeated incrementals realistic: a working
	// directory stays hot, an archive stays cold.
	sort.Strings(dirs)
	hot := hotOrder(rng, dirs, p)

	total, err := countFiles(root)
	if err != nil {
		return st, err
	}
	var budget int
	if count > 0 {
		budget = count
	} else {
		budget = int(float64(total) * fraction)
	}
	if budget < 1 {
		budget = 1
	}
	if budget > total {
		budget = total
	}

	pool := newContentPool(rng, p)
	touched := map[string]bool{}

	for _, dir := range hot {
		if st.Modified+st.Created+st.Deleted >= budget {
			break
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var files []string
		for _, e := range entries {
			if !e.IsDir() {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
		if len(files) == 0 {
			continue
		}
		sort.Strings(files)

		// How much of this directory changes, weighted so hot directories churn
		// hard rather than every directory churning a little.
		share := 0.2 + 0.6*rng.Float64()
		n := int(float64(len(files)) * share)
		if n < 1 {
			n = 1
		}

		for i := 0; i < n && st.Modified+st.Created+st.Deleted < budget; i++ {
			idx := rng.Intn(len(files))
			path := files[idx]
			touched[dir] = true
			switch r := rng.Float64(); {
			case r < 0.60: // modify in place
				size := sampleSize(rng, p)
				body, _ := pool.body(rng, size)
				if err := os.WriteFile(path, body, 0o644); err == nil {
					st.Modified++
				}
			case r < 0.80: // append, the case incremental chunking exists for
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					continue
				}
				tail := makeBody(rng, sampleSize(rng, p)/8, rng.Float64() < p.incompressibleFraction)
				_, _ = f.Write(tail)
				_ = f.Close()
				st.Modified++
			case r < 0.93: // create
				size := sampleSize(rng, p)
				body, _ := pool.body(rng, size)
				name := fmt.Sprintf("new-%d-%d.dat", seed, rng.Int63())
				if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err == nil {
					st.Created++
				}
			default: // delete
				if err := os.Remove(path); err == nil {
					st.Deleted++
					// Drawing is with replacement, and the modify branch would
					// recreate this path with os.WriteFile — leaving the run
					// reporting a deletion that is not in the tree the next
					// backup sees.
					files = append(files[:idx], files[idx+1:]...)
					if len(files) == 0 {
						break
					}
				}
			}
		}
	}

	st.TouchedDirs = len(touched)

	// One directory rename per churn, deep enough to carry a subtree. This is
	// the case that separates identity models, and it costs nothing to include.
	if renamed := renameOneDir(rng, root, dirs); renamed {
		st.Renamed++
	}
	return st, nil
}

// hotOrder ranks directories so that a Zipf draw concentrates on a few of them.
// A churnDirZipfS of 0 means no concentration, which is the uniform profile.
func hotOrder(rng *rand.Rand, dirs []string, p profile) []string {
	if p.churnDirZipfS == 0 {
		out := append([]string(nil), dirs...)
		rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return out
	}

	z := rand.NewZipf(rng, math.Max(p.churnDirZipfS, 1.01), 1, uint64(len(dirs)-1))
	seen := make(map[string]bool, len(dirs))
	out := make([]string, 0, len(dirs))
	// Draw until most directories have been ranked; the tail is appended in
	// order so nothing is unreachable.
	for i := 0; i < len(dirs)*4 && len(out) < len(dirs); i++ {
		d := dirs[z.Uint64()]
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	for _, d := range dirs {
		if !seen[d] {
			out = append(out, d)
		}
	}
	return out
}

// renameOneDir renames a directory below root, so a subtree moves.
//
// Depth is measured relative to root. Measuring it on the absolute path counted
// the separators of whatever temporary directory the tree happens to live under,
// so the source root itself cleared the threshold and could be renamed out from
// under the backup that was about to read it.
func renameOneDir(rng *rand.Rand, root string, dirs []string) bool {
	candidates := renameCandidates(root, dirs)
	if len(candidates) == 0 {
		return false
	}
	src := candidates[rng.Intn(len(candidates))]
	dst := filepath.Join(filepath.Dir(src), fmt.Sprintf("%s-renamed-%d", filepath.Base(src), rng.Intn(1000)))
	return os.Rename(src, dst) == nil
}

// renameCandidates returns the directories eligible to be renamed: those inside
// root and deep enough to carry a subtree.
//
// Depth is relative to root. Measured on the absolute path it counted the
// separators of whatever temporary directory the tree lives under, so root
// itself cleared the threshold and could be renamed out from under the backup
// about to read it.
func renameCandidates(root string, dirs []string) []string {
	var out []string
	for _, d := range dirs {
		rel, err := filepath.Rel(root, d)
		if err != nil || rel == "." {
			continue
		}
		if strings.Count(rel, string(filepath.Separator)) >= 1 {
			out = append(out, d)
		}
	}
	return out
}

func collectDirs(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func countFiles(root string) (int, error) {
	n := 0
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	return n, err
}
