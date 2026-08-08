// Command gentree builds a backup source with the statistics a real one has.
//
// The memory benchmark used to generate a uniform tree: every file the same
// size, every directory the same width, every byte unique. That shape cannot
// answer the questions the numbers were being used to settle. Deduplication is
// invisible when nothing is duplicated. Write amplification depends on whether
// changes cluster, and uniform churn spreads them perfectly evenly. Leaf
// occupancy depends on directory fan-out, which was constant.
//
// Every distribution here is drawn from a seeded PRNG, so a given
// (profile, files, seed) always produces byte-identical output. That is not a
// nicety: comparing two builds of cloudstic requires the input to be the same,
// and a benchmark whose dataset drifts measures nothing.
//
// Usage:
//
//	gentree -out DIR -profile mixed -files 50000 [-seed 1] [-stats FILE]
//	gentree -churn DIR -profile mixed [-seed 1] [-fraction 0.05]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// profile is the shape of one kind of backup source.
//
// The parameters are deliberately few. Each one exists because it changes a
// conclusion the memory work drew: size spread decides how many files are
// inlined rather than chunked, fan-out decides leaf occupancy, duplication
// decides what content addressing actually saves, and incompressibility decides
// whether the compression layer is doing anything.
type profile struct {
	name string
	desc string

	// File sizes are log-normal, the standard model for filesystem contents:
	// a median in the low kilobytes with a long tail of large files, so the
	// mean sits far above the median.
	sizeMedian float64
	sizeSigma  float64
	sizeMax    int64

	// Directory fan-out is heavy-tailed too. Most directories hold a handful of
	// files; a few hold thousands. A uniform 100-per-directory tree hides the
	// case that matters, which is the outlier.
	fanoutAlpha float64 // Pareto shape; lower is heavier-tailed
	fanoutMin   int
	fanoutMax   int

	// Tree depth, geometric about depthMean.
	depthMean float64
	depthMax  int

	// dupFraction of files reuse the content of an earlier file. Which earlier
	// file follows a Zipf law, so a few contents appear very often — a vendored
	// dependency copied into fifty projects — and most duplicates are pairs.
	dupFraction float64
	dupZipfS    float64

	// incompressibleFraction of distinct contents are random bytes, standing in
	// for media and archives. The rest are English-like text, which is what the
	// compression layer is for.
	incompressibleFraction float64

	// churnDirZipfS controls how much an incremental concentrates. Real churn is
	// not spread evenly: a few working directories change constantly and most of
	// the tree is untouched for months. This is the parameter the write
	// amplification analysis is most sensitive to.
	churnDirZipfS float64
}

var profiles = map[string]profile{
	// Kept so historical benchmark numbers stay comparable. Every file the same
	// size, every directory the same width, nothing duplicated.
	"uniform": {
		name: "uniform", desc: "the original flat tree; no duplication, constant fan-out",
		sizeMedian: 35, sizeSigma: 0, sizeMax: 35,
		fanoutAlpha: 0, fanoutMin: 100, fanoutMax: 100,
		depthMean: 1, depthMax: 1,
		dupFraction: 0, dupZipfS: 1.1,
		incompressibleFraction: 0,
		churnDirZipfS:          0,
	},
	// A developer machine: many small text files, deep trees, heavy duplication
	// from vendored dependencies, a few large build artifacts.
	"source": {
		name: "source", desc: "source trees: small text files, deep nesting, vendored duplicates",
		sizeMedian: 3 * 1024, sizeSigma: 1.9, sizeMax: 64 << 20,
		fanoutAlpha: 1.3, fanoutMin: 4, fanoutMax: 4000,
		depthMean: 5, depthMax: 12,
		dupFraction: 0.35, dupZipfS: 1.2,
		incompressibleFraction: 0.05,
		churnDirZipfS:          1.6,
	},
	// A photo and video library: large incompressible files, shallow wide
	// directories, some duplication from exports and edits.
	"media": {
		name: "media", desc: "photo/video libraries: large incompressible files, wide shallow dirs",
		sizeMedian: 2 << 20, sizeSigma: 1.3, sizeMax: 2 << 30,
		fanoutAlpha: 1.5, fanoutMin: 20, fanoutMax: 8000,
		depthMean: 2, depthMax: 4,
		dupFraction: 0.08, dupZipfS: 1.4,
		incompressibleFraction: 0.92,
		churnDirZipfS:          2.2,
	},
	// The default: a home directory, which is all of the above at once.
	"mixed": {
		name: "mixed", desc: "a home directory: documents, code and media together",
		sizeMedian: 12 * 1024, sizeSigma: 2.4, sizeMax: 512 << 20,
		fanoutAlpha: 1.4, fanoutMin: 5, fanoutMax: 5000,
		depthMean: 4, depthMax: 10,
		dupFraction: 0.18, dupZipfS: 1.25,
		incompressibleFraction: 0.45,
		churnDirZipfS:          1.8,
	},
}

func main() {
	var (
		out      = flag.String("out", "", "directory to generate into")
		churn    = flag.String("churn", "", "directory to mutate, as an incremental backup would find it")
		name     = flag.String("profile", "mixed", "workload profile: "+profileNames())
		files    = flag.Int("files", 50000, "number of files to generate")
		seed     = flag.Int64("seed", 1, "PRNG seed; the same seed always produces the same tree")
		fraction = flag.Float64("fraction", 0.05, "churn: fraction of files to touch")
		statsOut = flag.String("stats", "", "write a JSON summary of what was generated")
		maxBytes = flag.Int64("max-bytes", 2<<30, "scale file sizes so the tree fits this budget; 0 disables")
	)
	flag.Parse()

	p, ok := profiles[*name]
	if !ok {
		fatalf("unknown profile %q; have %s", *name, profileNames())
	}
	if (*out == "") == (*churn == "") {
		fatalf("exactly one of -out or -churn is required")
	}

	var (
		st  stats
		err error
	)
	if *out != "" {
		if *maxBytes > 0 {
			p = fitToBudget(p, *files, *maxBytes)
		}
		st, err = generate(*out, p, *files, *seed)
	} else {
		// Scale against the tree actually on disk. -files describes a tree being
		// generated and says nothing about one being mutated, so using it here
		// would size a churn's writes against a file count that is merely the
		// flag's default — writing megabyte files into a tree scaled to
		// kilobytes, and quietly changing what the benchmark measures.
		n, cerr := countFiles(*churn)
		if cerr != nil {
			fatalf("count files under %s: %v", *churn, cerr)
		}
		if *maxBytes > 0 {
			p = fitToBudget(p, n, *maxBytes)
		}
		st, err = applyChurn(*churn, p, *seed, *fraction)
	}
	if err != nil {
		fatalf("%v", err)
	}

	st.Profile = p.name
	st.Seed = *seed
	report(st)
	if *statsOut != "" {
		if err := writeJSON(*statsOut, st); err != nil {
			fatalf("write stats: %v", err)
		}
	}
}

// stats is what the run produced, so a benchmark result can be interpreted
// against the dataset that produced it rather than against a profile name.
type stats struct {
	Profile        string  `json:"profile"`
	Seed           int64   `json:"seed"`
	Files          int     `json:"files"`
	Dirs           int     `json:"dirs"`
	TotalBytes     int64   `json:"total_bytes"`
	DistinctBytes  int64   `json:"distinct_bytes"`
	DuplicateFiles int     `json:"duplicate_files"`
	DedupRatio     float64 `json:"dedup_ratio"`
	MedianSize     int64   `json:"median_file_size"`
	P99Size        int64   `json:"p99_file_size"`
	MaxSize        int64   `json:"max_file_size"`
	MaxFanout      int     `json:"max_dir_fanout"`
	MedianFanout   int     `json:"median_dir_fanout"`
	MaxDepth       int     `json:"max_depth"`
	// Churn only.
	Modified int `json:"modified,omitempty"`
	Created  int `json:"created,omitempty"`
	Deleted  int `json:"deleted,omitempty"`
	Renamed  int `json:"renamed_dirs,omitempty"`
	// Directories containing at least one modify, create or delete. Renames are
	// excluded: moving a directory changes every path beneath it without any of
	// its files being touched, which would overstate how spread the churn was.
	TouchedDirs int `json:"touched_dirs,omitempty"`
}

// generate builds the tree.
//
// Directories are laid out first so that fan-out is a property of the tree
// rather than an artefact of the order files happen to be created.
func generate(root string, p profile, files int, seed int64) (stats, error) {
	rng := rand.New(rand.NewSource(seed))

	dirs := layoutDirs(rng, p, files)
	st := stats{Dirs: len(dirs), MaxFanout: 0}

	pool := newContentPool(rng, p)
	var sizes []int64
	var fanouts []int

	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d.path), 0o755); err != nil {
			return st, err
		}
		fanouts = append(fanouts, d.files)
		if d.files > st.MaxFanout {
			st.MaxFanout = d.files
		}
		if d.depth > st.MaxDepth {
			st.MaxDepth = d.depth
		}

		for i := 0; i < d.files && st.Files < files; i++ {
			size := sampleSize(rng, p)
			body, distinct := pool.body(rng, size)
			name := fileName(rng, d.kind, i, len(body))

			if err := os.WriteFile(filepath.Join(root, d.path, name), body, 0o644); err != nil {
				return st, err
			}
			st.Files++
			st.TotalBytes += int64(len(body))
			if distinct {
				st.DistinctBytes += int64(len(body))
			} else {
				st.DuplicateFiles++
			}
			sizes = append(sizes, int64(len(body)))
		}
		if st.Files >= files {
			break
		}
	}

	summarise(&st, sizes, fanouts)
	return st, nil
}

type dirSpec struct {
	path  string
	depth int
	files int
	kind  string
}

// layoutDirs picks a directory tree whose fan-out follows the profile, then
// assigns each directory a file count from the same distribution.
func layoutDirs(rng *rand.Rand, p profile, files int) []dirSpec {
	if p.fanoutAlpha == 0 { // uniform profile: the original flat tree
		n := (files + p.fanoutMin - 1) / p.fanoutMin
		out := make([]dirSpec, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, dirSpec{path: fmt.Sprintf("dir-%d", i), depth: 1, files: p.fanoutMin, kind: "flat"})
		}
		return out
	}

	kinds := []string{"docs", "src", "media", "archive", "work"}
	var out []dirSpec
	// Paths are drawn at random and can collide. Two specs with the same path
	// would merge into one directory, so the tree would hold fewer, wider
	// directories than the distribution asked for and fewer files than
	// requested — silently, since nothing errors.
	seen := make(map[string]bool)
	placed := 0
	for placed < files {
		depth := sampleDepth(rng, p)
		parts := make([]string, 0, depth)
		kind := kinds[rng.Intn(len(kinds))]
		parts = append(parts, kind)
		for d := 1; d < depth; d++ {
			parts = append(parts, fmt.Sprintf("%s-%d", segmentWord(rng), rng.Intn(64)))
		}
		path := filepath.Join(parts...)
		if seen[path] {
			// Disambiguate rather than skip, so the requested file count is
			// still reached and the loop cannot spin on a saturated name space.
			path = fmt.Sprintf("%s-%d", path, len(out))
		}
		seen[path] = true

		n := sampleFanout(rng, p)
		if placed+n > files {
			n = files - placed
		}
		out = append(out, dirSpec{path: path, depth: depth, files: n, kind: kind})
		placed += n
	}
	return out
}

// contentPool makes duplication real.
//
// A fraction of files reuse an earlier body, chosen by a Zipf law so a few
// contents recur constantly and most repeats are pairs. Without this the
// repository's deduplication does nothing measurable, which is how a uniform
// tree makes content addressing look free.
type contentPool struct {
	bodies [][]byte
	p      profile
	zipf   *rand.Zipf
}

func newContentPool(rng *rand.Rand, p profile) *contentPool {
	// The Zipf table is sized for the pool's eventual length; it is only used
	// once there is something in it.
	z := rand.NewZipf(rng, math.Max(p.dupZipfS, 1.01), 1, 1<<20)
	return &contentPool{p: p, zipf: z}
}

func (c *contentPool) body(rng *rand.Rand, size int64) ([]byte, bool) {
	if len(c.bodies) > 0 && rng.Float64() < c.p.dupFraction {
		idx := int(c.zipf.Uint64()) % len(c.bodies)
		return c.bodies[idx], false
	}
	b := makeBody(rng, size, rng.Float64() < c.p.incompressibleFraction)
	// Only bodies small enough to be worth holding go in the pool; a duplicated
	// 2 GB video is not a case worth simulating in memory.
	if len(b) <= 1<<20 {
		c.bodies = append(c.bodies, b)
	}
	return b, true
}

// makeBody returns either incompressible bytes or English-like text, because
// the compression layer behaves very differently on the two and a tree of one
// kind misrepresents what the store does.
func makeBody(rng *rand.Rand, size int64, incompressible bool) []byte {
	if size < 0 {
		size = 0
	}
	b := make([]byte, size)
	if incompressible {
		rng.Read(b)
		return b
	}
	var sb strings.Builder
	sb.Grow(int(size) + 16)
	for sb.Len() < int(size) {
		sb.WriteString(words[rng.Intn(len(words))])
		sb.WriteByte(' ')
	}
	return []byte(sb.String()[:size])
}

// fitToBudget rescales the size distribution so the tree fits in budget bytes,
// keeping its shape.
//
// A realistic media library is terabytes, which is true and useless: the memory
// benchmark cares about the number of files and the spread of their sizes, not
// about moving real video. Scaling the median leaves the log-normal shape — and
// therefore the mix of inlined, packed and chunked files — intact while making
// the dataset something CI can generate in a minute.
//
// The mean of a log-normal is exp(mu + sigma^2/2), so the median that lands a
// given expected total follows directly.
func fitToBudget(p profile, files int, budget int64) profile {
	if p.sizeSigma == 0 || files == 0 {
		return p
	}
	mean := math.Exp(math.Log(p.sizeMedian) + p.sizeSigma*p.sizeSigma/2)
	expected := mean * float64(files)
	if expected <= float64(budget) {
		return p
	}
	p.sizeMedian *= float64(budget) / expected
	if p.sizeMedian < 1 {
		p.sizeMedian = 1
	}
	// The cap has to come down with the median or the tail alone blows the
	// budget back open.
	if capped := int64(p.sizeMedian * 4096); capped < p.sizeMax {
		p.sizeMax = capped
	}
	return p
}

func sampleSize(rng *rand.Rand, p profile) int64 {
	if p.sizeSigma == 0 {
		return int64(p.sizeMedian)
	}
	v := math.Exp(math.Log(p.sizeMedian) + p.sizeSigma*rng.NormFloat64())
	if v > float64(p.sizeMax) {
		v = float64(p.sizeMax)
	}
	if v < 1 {
		v = 1
	}
	return int64(v)
}

// sampleFanout draws from a bounded Pareto, which is the usual model for
// directory width: many narrow directories and a thin tail of very wide ones.
func sampleFanout(rng *rand.Rand, p profile) int {
	u := rng.Float64()
	v := float64(p.fanoutMin) / math.Pow(1-u, 1/p.fanoutAlpha)
	if v > float64(p.fanoutMax) {
		v = float64(p.fanoutMax)
	}
	return int(v)
}

func sampleDepth(rng *rand.Rand, p profile) int {
	d := 1 + int(rng.ExpFloat64()*(p.depthMean-1))
	if d > p.depthMax {
		d = p.depthMax
	}
	if d < 1 {
		d = 1
	}
	return d
}

func fileName(rng *rand.Rand, kind string, i, size int) string {
	// Long shared prefixes within a directory are the norm — IMG_0001.jpg and
	// its four thousand siblings — and they are what a metadata string arena
	// would compress. A generator emitting unique random names would make that
	// saving invisible.
	ext := map[string]string{"docs": ".md", "src": ".go", "media": ".jpg", "archive": ".tar", "work": ".txt"}[kind]
	if ext == "" {
		ext = ".dat"
	}
	switch kind {
	case "media":
		return fmt.Sprintf("IMG_%05d%s", i, ext)
	case "src":
		return fmt.Sprintf("%s_%03d%s", segmentWord(rng), i, ext)
	default:
		return fmt.Sprintf("%s-%04d%s", kind, i, ext)
	}
}

func summarise(st *stats, sizes []int64, fanouts []int) {
	if len(sizes) > 0 {
		sort.Slice(sizes, func(i, j int) bool { return sizes[i] < sizes[j] })
		st.MedianSize = sizes[len(sizes)/2]
		st.P99Size = sizes[min(len(sizes)-1, len(sizes)*99/100)]
		st.MaxSize = sizes[len(sizes)-1]
	}
	if len(fanouts) > 0 {
		sort.Ints(fanouts)
		st.MedianFanout = fanouts[len(fanouts)/2]
	}
	if st.TotalBytes > 0 {
		st.DedupRatio = 1 - float64(st.DistinctBytes)/float64(st.TotalBytes)
	}
}

func report(st stats) {
	fmt.Printf("profile=%s seed=%d\n", st.Profile, st.Seed)
	if st.Modified+st.Created+st.Deleted+st.Renamed > 0 {
		fmt.Printf("  churn: %d modified, %d created, %d deleted, %d dirs renamed, across %d dirs\n",
			st.Modified, st.Created, st.Deleted, st.Renamed, st.TouchedDirs)
		return
	}
	fmt.Printf("  %d files in %d dirs, %.1f MB (%.1f MB distinct, %.0f%% duplicate bytes)\n",
		st.Files, st.Dirs, float64(st.TotalBytes)/(1<<20), float64(st.DistinctBytes)/(1<<20), st.DedupRatio*100)
	fmt.Printf("  %d files (%.0f%%) reuse another file's content\n",
		st.DuplicateFiles, 100*float64(st.DuplicateFiles)/math.Max(float64(st.Files), 1))
	fmt.Printf("  size    median %s, p99 %s, max %s\n", human(st.MedianSize), human(st.P99Size), human(st.MaxSize))
	fmt.Printf("  fan-out median %d, max %d, depth max %d\n", st.MedianFanout, st.MaxFanout, st.MaxDepth)
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func profileNames() string {
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gentree: "+format+"\n", args...)
	os.Exit(1)
}

var words = []string{
	"backup", "restore", "snapshot", "chunk", "content", "index", "repository",
	"encrypt", "compress", "manifest", "pack", "catalog", "object", "store",
	"the", "a", "of", "and", "to", "in", "that", "it", "with", "for", "as",
}

func segmentWord(rng *rand.Rand) string { return words[rng.Intn(len(words))] }
