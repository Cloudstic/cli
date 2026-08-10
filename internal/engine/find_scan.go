package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/store"
)

// findScanner walks snapshots and emits observations of matching entries.
//
// The naive implementation of find — for each snapshot, walk the HAMT, Get every
// filemeta, evaluate — costs the sum of every snapshot's entries and makes
// searching a whole repository untenable on a remote backend. The delta scan
// exploits the persistence of the HAMT instead: within a lineage it walks the
// oldest root once and diffs thereafter, so an unchanged subtree costs one
// pointer comparison. Both scanners emit the same observations, which is what
// makes them comparable in tests.
type findScanner struct {
	store store.ObjectStore
	tree  *hamt.Tree
	pred  *findPredicate
	out   *findCollector
	// metas reads through on every call rather than memoizing: a full scan
	// crosses every snapshot in the repository, and holding each filemeta it
	// decodes would grow without bound. The evaluated map below is the bounded
	// memo that makes the repeats cheap instead.
	metas *metaLoader
	// log receives the scan heartbeat. It replaced a verbose flag writing
	// straight to os.Stderr, which no library caller could redirect.
	log *logger.Logger

	// evaluated memoizes the verdict for a filemeta ref. Those objects are
	// content-addressed and immutable, so a ref evaluated once never needs
	// evaluating again — including across lineages, which is what makes an
	// identical file backed up from two machines cost one evaluation.
	evaluated map[[16]byte]findRefEval

	entriesScanned int
	metaFetched    int
}

// findRefEval is a memoized verdict. meta is retained only when something later
// needs it — a match, or a folder the path index depends on — so the memo of a
// million-entry repository stays a set of verdicts rather than a second copy of
// the tree.
type findRefEval struct {
	matched bool
	meta    *core.FileMeta
}

// findCandidate is an entry that passed every predicate not needing a path.
type findCandidate struct {
	key  string
	ref  string
	meta core.FileMeta
}

// findRun is a matched entry's uninterrupted presence, at one ref and one set of
// paths, over a contiguous range of a lineage's snapshots.
type findRun struct {
	key   string
	ref   string
	meta  core.FileMeta
	paths []string
	from  int // index into the lineage's snapshots
}

// lineageState is the live tree state the scan maintains as it advances through
// a lineage. It is deliberately not a key→ref map of the whole tree: at a
// million files that map is the dominant memory cost for no benefit, since
// snapshot attribution needs only the matched entries.
type lineageState struct {
	// folders maps FileID → metadata for folders only. Directories are a small
	// fraction of entries, and having them all indexed turns path resolution
	// into O(depth) map lookups with no additional object reads — which matters
	// because hamt.LookupByKey is O(N).
	folders map[string]core.FileMeta
	// active holds the run currently open for each matched file.
	active map[string]*findRun
}

func newLineageState() *lineageState {
	return &lineageState{
		folders: make(map[string]core.FileMeta),
		active:  make(map[string]*findRun),
	}
}

func newFindScanner(s store.ObjectStore, tree *hamt.Tree, pred *findPredicate, out *findCollector, log *logger.Logger) *findScanner {
	return &findScanner{
		store:     s,
		tree:      tree,
		pred:      pred,
		out:       out,
		log:       log,
		metas:     newUncachedMetaLoader(s),
		evaluated: make(map[[16]byte]findRefEval),
	}
}

func (s *findScanner) scanLineage(ctx context.Context, lineage findLineage, noDelta bool) error {
	if len(lineage.snapshots) == 0 {
		return nil
	}
	if noDelta {
		return s.scanLineageFull(ctx, lineage)
	}
	return s.scanLineageDelta(ctx, lineage)
}

// ---------------------------------------------------------------------------
// Straightforward scan
// ---------------------------------------------------------------------------

// scanLineageFull walks every selected snapshot in full. It is what -no-delta
// selects: slower by a factor of the snapshot count, but with nowhere for a bug
// to hide, so the delta scan can be checked against it.
func (s *findScanner) scanLineageFull(ctx context.Context, lineage findLineage) error {
	for i, entry := range lineage.snapshots {
		state := newLineageState()
		candidates, err := s.walkSnapshot(ctx, entry.Snap.Root, state)
		if err != nil {
			return err
		}
		for _, c := range candidates {
			paths := resolveCandidatePaths(state, c.meta)
			if !s.pred.matchPaths(paths) {
				continue
			}
			s.observe(lineage, i, &findRun{key: c.key, ref: c.ref, meta: c.meta, paths: paths})
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Delta scan
// ---------------------------------------------------------------------------

// scanLineageDelta walks the oldest root once and diffs consecutive roots
// thereafter, so the cost of searching a whole lineage is roughly one full walk
// plus the repository's actual churn — rather than one full walk per snapshot.
func (s *findScanner) scanLineageDelta(ctx context.Context, lineage findLineage) error {
	snaps := lineage.snapshots
	state := newLineageState()

	candidates, err := s.walkSnapshot(ctx, snaps[0].Snap.Root, state)
	if err != nil {
		return err
	}
	s.openRuns(state, candidates, 0)

	for i := 1; i < len(snaps); i++ {
		added, removed, err := s.diffSnapshots(ctx, snaps[i-1].Snap.Root, snaps[i].Snap.Root)
		if err != nil {
			return err
		}

		// A key that disappeared or changed ends whatever run it had; the run's
		// last snapshot is the previous one. A replacement ref, if it matches,
		// opens a fresh run below.
		for _, key := range removed {
			s.closeRun(lineage, state, key, i-1)
		}

		foldersChanged := s.applyFolderRemovals(state, removed)
		candidates, changed, err := s.applyAdditions(ctx, state, added)
		if err != nil {
			return err
		}
		foldersChanged = foldersChanged || changed

		// A folder rename changes its descendants' paths without changing their
		// own metadata objects, so an active run's path can go stale even though
		// its ref did not. Re-resolving only when the folder index actually moved
		// keeps this off the common path.
		if foldersChanged {
			s.reresolveActiveRuns(lineage, state, i)
		}
		s.openRuns(state, candidates, i)
	}

	// Flush in a fixed order. Runs live in a map, so iterating it directly would
	// make the order — and therefore which matches survive -max-results — differ
	// between two runs of the same query.
	last := len(snaps) - 1
	remaining := make([]string, 0, len(state.active))
	for key := range state.active {
		remaining = append(remaining, key)
	}
	sort.Strings(remaining)
	for _, key := range remaining {
		s.closeRun(lineage, state, key, last)
	}
	return nil
}

// diffSnapshots collects one snapshot step's changes. The diff is buffered
// because the folder index has to be brought fully up to date before any of the
// step's entries can have their paths resolved.
func (s *findScanner) diffSnapshots(ctx context.Context, oldRoot, newRoot string) (added []hamt.DiffEntry, removed []string, err error) {
	err = s.tree.Diff(ctx, oldRoot, newRoot, func(e hamt.DiffEntry) error {
		if e.OldValue != "" {
			removed = append(removed, e.Key)
		}
		if e.NewValue != "" {
			added = append(added, e)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("diff snapshot roots: %w", err)
	}
	return added, removed, nil
}

func (s *findScanner) applyFolderRemovals(state *lineageState, removed []string) bool {
	var changed bool
	for _, key := range removed {
		if _, ok := state.folders[key]; ok {
			delete(state.folders, key)
			changed = true
		}
	}
	return changed
}

func (s *findScanner) applyAdditions(ctx context.Context, state *lineageState, added []hamt.DiffEntry) ([]findCandidate, bool, error) {
	var candidates []findCandidate
	var foldersChanged bool

	for _, e := range added {
		s.entriesScanned++
		eval, err := s.eval(ctx, e.NewValue)
		if err != nil {
			return nil, false, err
		}
		if eval.meta != nil && metaFileType(eval.meta) == core.FileTypeFolder {
			state.folders[e.Key] = *eval.meta
			foldersChanged = true
		}
		if eval.matched && s.pred.matchKey(e.Key) {
			candidates = append(candidates, findCandidate{key: e.Key, ref: e.NewValue, meta: *eval.meta})
		}
	}
	return candidates, foldersChanged, nil
}

// reresolveActiveRuns re-derives the paths of every open run after the folder
// index moved. A run whose paths changed is closed at the previous snapshot and
// reopened, so each reported path is one the file genuinely had over the
// snapshots credited to it.
func (s *findScanner) reresolveActiveRuns(lineage findLineage, state *lineageState, idx int) {
	// Collect the keys first: the loop reopens runs under the same keys it
	// closes, and re-adding a key mid-range is exactly the case Go leaves
	// unspecified.
	keys := make([]string, 0, len(state.active))
	for key := range state.active {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		run := state.active[key]
		paths := resolveCandidatePaths(state, run.meta)
		if equalPaths(paths, run.paths) {
			continue
		}
		s.closeRun(lineage, state, key, idx-1)
		if !s.pred.matchPaths(paths) {
			continue
		}
		state.active[key] = &findRun{key: run.key, ref: run.ref, meta: run.meta, paths: paths, from: idx}
	}
}

func (s *findScanner) openRuns(state *lineageState, candidates []findCandidate, idx int) {
	for _, c := range candidates {
		paths := resolveCandidatePaths(state, c.meta)
		if !s.pred.matchPaths(paths) {
			continue
		}
		state.active[c.key] = &findRun{key: c.key, ref: c.ref, meta: c.meta, paths: paths, from: idx}
	}
}

// closeRun credits an open run with every snapshot it spanned and removes it.
func (s *findScanner) closeRun(lineage findLineage, state *lineageState, key string, endIdx int) {
	run, ok := state.active[key]
	if !ok {
		return
	}
	delete(state.active, key)
	for i := run.from; i <= endIdx; i++ {
		s.observe(lineage, i, run)
	}
}

// ---------------------------------------------------------------------------
// Shared machinery
// ---------------------------------------------------------------------------

// walkSnapshot walks one root in full, building the folder index as it goes and
// returning the entries that passed every predicate not needing a path.
//
// Candidates are buffered rather than resolved inline because a child can be
// visited before its parent: paths are only reliable once the walk has seen the
// whole tree.
func (s *findScanner) walkSnapshot(ctx context.Context, root string, state *lineageState) ([]findCandidate, error) {
	var candidates []findCandidate
	err := walkEntriesBatched(ctx, s.tree, root, func(entries []treeEntry) error {
		byRef := make(map[string][]string, len(entries))
		refs := make([]string, len(entries))
		for i, e := range entries {
			refs[i] = e.ref
			byRef[e.ref] = append(byRef[e.ref], e.key)
		}
		// Candidates come out in pack order rather than walk order; collectMatches
		// sorts by path before returning, so the result is unaffected.
		return readGrouped(ctx, s.store, refs, func(ref string) error {
			keys := byRef[ref]
			if len(keys) == 0 {
				return nil
			}
			key := keys[0]
			byRef[ref] = keys[1:]

			s.entriesScanned++
			eval, err := s.eval(ctx, ref)
			if err != nil {
				return err
			}
			if eval.meta != nil && metaFileType(eval.meta) == core.FileTypeFolder {
				state.folders[key] = *eval.meta
			}
			if eval.matched && s.pred.matchKey(key) {
				candidates = append(candidates, findCandidate{key: key, ref: ref, meta: *eval.meta})
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("walk snapshot root %s: %w", root, err)
	}
	return candidates, nil
}

func (s *findScanner) observe(lineage findLineage, idx int, run *findRun) {
	entry := lineage.snapshots[idx]
	s.out.observe(findObservation{
		fileID: run.key,
		ref:    run.ref,
		meta:   run.meta,
		paths:  run.paths,
		snapshot: SnapshotRef{
			Ref:     entry.Ref,
			Seq:     entry.Snap.Seq,
			Created: entry.Snap.Created,
		},
		source: entry.Snap.Source,
	})
}

func (s *findScanner) eval(ctx context.Context, ref string) (findRefEval, error) {
	key := findRefKey(ref)
	if cached, ok := s.evaluated[key]; ok {
		return cached, nil
	}

	meta, err := s.loadMeta(ctx, ref)
	if err != nil {
		return findRefEval{}, err
	}
	eval := findRefEval{matched: s.pred.matchMeta(ref, meta)}
	if eval.matched || metaFileType(meta) == core.FileTypeFolder {
		eval.meta = meta
	}
	s.evaluated[key] = eval
	return eval, nil
}

func (s *findScanner) loadMeta(ctx context.Context, ref string) (*core.FileMeta, error) {
	meta, err := s.metas.load(ctx, ref)
	if err != nil {
		return nil, err
	}
	s.metaFetched++
	if s.metaFetched%10000 == 0 {
		s.log.Debugf("  read %d metadata objects", s.metaFetched)
	}
	return meta, nil
}

func resolveCandidatePaths(state *lineageState, meta core.FileMeta) []string {
	return fileMetaPaths(meta, func(id string) (core.FileMeta, bool) {
		parent, ok := state.folders[id]
		return parent, ok
	})
}

func equalPaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// findRefKey compresses a content-addressed ref into a fixed-size map key.
//
// Refs are SHA-256 hex, so their leading 128 bits identify an object as surely
// as the whole string does — while keeping the memo of a million-entry
// repository in the tens of megabytes rather than the hundreds a map keyed by
// the full ref string would need.
func findRefKey(ref string) [16]byte {
	var key [16]byte
	if i := strings.IndexByte(ref, '/'); i >= 0 && len(ref)-i-1 >= 32 {
		if _, err := hex.Decode(key[:], []byte(ref[i+1:i+33])); err == nil {
			return key
		}
	}
	// Not the expected shape (a legacy or hand-written ref); hashing keeps the
	// key well distributed rather than colliding on a shared prefix.
	sum := sha256.Sum256([]byte(ref))
	copy(key[:], sum[:16])
	return key
}
