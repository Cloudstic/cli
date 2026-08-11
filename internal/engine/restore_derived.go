package engine

import (
	"context"
	"path"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/store"
)

// The derived walk (RFC 0025 §1).
//
// AffinityKey takes a routing key's leading 4 hex characters from the parent, so
// every entry naming the same primary parent shares a 16-bit prefix and lands in
// the same HAMT subtree. Listing a directory is therefore a descent to that
// prefix and a scan, not a search — which means a parent-before-child order is
// *recoverable from the tree* instead of having to be reconstructed from a
// materialised copy of it.
//
// That is the whole point. The order restore writes in used to be computed by
// loading every entry in the snapshot, indexing it by FileID, and running three
// passes over the result; six O(files) structures were live before the first
// byte was written. Descending the tree supplies the same order with a stack of
// directories and one batch of children.
//
//	children(D) = scan(subtree under hash(D)[:4]) where Parents[0] == D
//
// **This enumerates by primary parent only.** An entry whose Parents[1] is D
// does not appear under D, and an entry whose Parents[0] is missing, is not a
// folder, or was routed by a pre-affinity build does not appear at all. Those
// are exactly the cases restoreOrder's fallback passes exist for, and they are
// not dropped: the walk counts what it reached, the caller compares that against
// the tree's own entry count, and anything short falls back to the materialised
// plan. See RestoreManager.restoreUnreached.

// derivedScanBatch is how many child refs the derived walk gathers before
// reading any of them.
//
// A directory on its own is a poor unit to read by: a handful of keys tells the
// store almost nothing, and the grouping it can do with them is worth almost
// nothing. Gathering several directories' children first restores the batch that
// walkEntriesBatched gives the streaming traversals, and costs one string header
// per pending ref rather than a FileMeta.
//
// It is smaller than entryBatch because these refs are gathered by descending
// the tree once per directory rather than by one linear pass: a larger batch
// buys diminishing grouping and delays the first write, which is the thing this
// walk exists to bring forward.
const derivedScanBatch = 2048

// restoreDir is one directory on the derived walk: what it is called, where the
// walk came from, and whether it has been created yet.
//
// It holds a FileMeta only until it is created. Directories are the one thing
// this walk retains — a snapshot's interior, not its leaves — and a path filter
// can defer creating one indefinitely, so the meta is released as soon as it has
// been used rather than being held for the length of the walk.
type restoreDir struct {
	id     string
	name   string
	stored string // path the entry itself recorded, if any (pre-RFC 0015 snapshots)
	rel    string // this directory's restore path
	parent *restoreDir
	meta   *core.FileMeta
	made   bool
	// charged records that this directory has already been counted against the
	// progress total — because a path filter passed over it — so creating it
	// later on a descendant's behalf does not count it a second time.
	charged bool
}

// derivedWalk streams a snapshot in derived order.
type derivedWalk struct {
	tree  *hamt.Tree
	store store.ObjectStore
	metas *metaLoader

	filter restorePathFilter
	out    *restoreEmitter

	// reached counts every entry the walk claimed, filtered or not. It is what
	// the completeness check compares against the tree's entry count, so it must
	// count what the walk *found* rather than what it wrote.
	reached int64
}

// run walks the snapshot rooted at root and returns how many entries it reached.
func (w *derivedWalk) run(ctx context.Context, root string) (int64, error) {
	// The synthetic root stands for the empty parent ID that top-level entries
	// name. It is already "made": there is no directory to create for it.
	stack := []*restoreDir{{made: true}}

	for len(stack) > 0 {
		scans, refs, err := w.gather(ctx, root, &stack)
		if err != nil {
			return w.reached, err
		}
		if len(refs) == 0 {
			continue
		}
		metas, err := w.read(ctx, refs)
		if err != nil {
			return w.reached, err
		}
		found, err := w.emit(ctx, scans, metas)
		if err != nil {
			return w.reached, err
		}
		// Depth first, in the order the entries were listed: the last child
		// pushed is the first popped, so the slice is reversed on the way in.
		for i := len(found) - 1; i >= 0; i-- {
			stack = append(stack, found[i])
		}
	}
	return w.reached, nil
}

// dirScan is one directory's listing: the refs its prefix descent yielded,
// before any of them have been read.
type dirScan struct {
	dir  *restoreDir
	refs []string
}

// gather pops directories off the stack and lists each one, until it has
// accumulated a batch worth reading or the stack is empty.
//
// Listing is a descent through the HAMT and touches no filemeta, so the cost of
// gathering more than one directory is node reads the node cache mostly already
// holds. What it buys is a batch the store can group.
func (w *derivedWalk) gather(ctx context.Context, root string, stack *[]*restoreDir) ([]dirScan, []string, error) {
	var (
		scans []dirScan
		refs  []string
	)
	for len(*stack) > 0 && len(refs) < derivedScanBatch {
		s := *stack
		dir := s[len(s)-1]
		*stack = s[:len(s)-1]

		scan := dirScan{dir: dir}
		err := w.tree.ScanPrefix(ctx, root, childRoutingPrefix(dir.id), func(_, ref string) error {
			scan.refs = append(scan.refs, ref)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		if len(scan.refs) == 0 {
			continue
		}
		refs = append(refs, scan.refs...)
		scans = append(scans, scan)
	}
	return scans, refs, nil
}

// read fetches a batch of filemetas in the groups the store nominates.
//
// The unit of concurrency is the group rather than the ref, for the reason
// collectMetadata gives: one worker per group reads a bundle's objects
// consecutively, where spreading refs across workers interleaves the bundles
// again. Results land in a map because the walk consumes them in listing order,
// which is not the order they are fetched in.
func (w *derivedWalk) read(ctx context.Context, refs []string) (map[string]core.FileMeta, error) {
	plan := store.PlanReads(ctx, w.store, refs)
	out := make(map[string]core.FileMeta, len(refs))
	var mu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(plan.Concurrency)
	for _, group := range plan.Groups {
		g.Go(func() error {
			for _, ref := range group {
				fm, err := w.metas.load(gctx, ref)
				if err != nil {
					return err
				}
				mu.Lock()
				out[ref] = *fm
				mu.Unlock()
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// emit writes a batch's entries and returns the directories it discovered.
func (w *derivedWalk) emit(ctx context.Context, scans []dirScan, metas map[string]core.FileMeta) ([]*restoreDir, error) {
	var found []*restoreDir

	w.declareContent(ctx, scans, metas)

	for _, scan := range scans {
		for _, ref := range scan.refs {
			meta, ok := metas[ref]
			if !ok {
				continue
			}
			// A 16-bit prefix is shared by every directory whose ID hashes to
			// it, so a listing sees its neighbours as well as its children.
			// Those belong to another directory and are read again when that
			// one is listed; discarding them here is what makes the scan a
			// listing rather than a neighbourhood.
			if primaryParentID(&meta) != scan.dir.id {
				continue
			}
			w.reached++

			rel := derivedPath(meta, scan.dir)
			if meta.Type != core.FileTypeFolder {
				if !w.filter.matches(rel) {
					w.out.skip()
					continue
				}
				w.out.ensureDir(scan.dir)
				w.out.file(meta, rel)
				continue
			}

			dir := &restoreDir{
				id:     meta.FileID,
				name:   meta.Name,
				stored: storedDirPath(meta),
				rel:    rel,
				parent: scan.dir,
				meta:   &meta,
			}
			found = append(found, dir)
			// A directory inside the selection is created whether or not
			// anything under it survives the filter, which is what an
			// unfiltered restore does for an empty directory too. One merely
			// on the way to the selection is created only if something below
			// it is — that is filterByPath's "ancestors of matched entries",
			// arrived at by deferring rather than by a second pass.
			if !w.filter.matches(rel) {
				dir.charged = true
				w.out.skip()
				continue
			}
			w.out.ensureDir(dir)
		}
	}
	return found, nil
}

// declareContent tells the store which content objects this batch is about to be
// written from, without reordering them.
//
// It is the streaming form of what declareContentReads does for the materialised
// plan, and it carries that function's constraint: the declaration is a statement
// about what will be read, not about when, so the returned grouping is discarded
// and the walk keeps listing order — which is the order backup wrote these
// objects and therefore the order they sit in packfiles.
//
// **The unit is the batch, not the directory.** A directory's files are a
// handful of keys, and a plan that small tells the store almost nothing: it sees
// two objects wanted from a bundle and correctly declines to transfer it, over
// and over. Declaring the whole batch is what keeps the demand statement close to
// the one an unbounded plan makes.
func (w *derivedWalk) declareContent(ctx context.Context, scans []dirScan, metas map[string]core.FileMeta) {
	var keys []string
	for _, scan := range scans {
		for _, ref := range scan.refs {
			meta, ok := metas[ref]
			if !ok || meta.Type == core.FileTypeFolder || primaryParentID(&meta) != scan.dir.id {
				continue
			}
			if key := contentKeyOf(meta); key != "" {
				keys = append(keys, "content/"+key)
			}
		}
	}
	if len(keys) < 2 {
		return
	}
	store.PlanReads(ctx, w.store, keys)
}

func contentKeyOf(meta core.FileMeta) string {
	if meta.ContentRef != "" {
		return meta.ContentRef
	}
	return meta.ContentHash
}

// derivedPrefixLen is how much of a routing key comes from the parent, in hex
// characters. It is AffinityKey's own split and cannot be chosen here: widening
// it would make the descent look in a subtree entries are not in.
const derivedPrefixLen = 4

// childRoutingPrefix is the routing prefix every entry naming parentID as its
// primary parent is stored under.
//
// It is AffinityKey's leading characters, and it is the whole derivation: the
// routing key already carries the parent, so the tree already holds the index
// that a stored order list would have had to record and rewrite on every backup.
func childRoutingPrefix(parentID string) string {
	return core.ComputeHash([]byte(parentID))[:derivedPrefixLen]
}

// derivedPath is fileMetaPath along the chain the walk descended.
//
// It reproduces collectMetaPaths rather than approximating it: a persisted path
// wins over anything reconstructed, the chain is folded with path.Join one
// segment at a time, and it stops after maxParentDepth links — so a snapshot
// whose ancestry is deeper than that resolves to the same truncated path it
// resolved to before.
func derivedPath(meta core.FileMeta, parent *restoreDir) string {
	if stored := storedMetaPaths(meta); len(stored) > 0 {
		return stored[0]
	}

	segments := []string{meta.Name}
	for d, depth := parent, 1; d != nil && d.id != ""; d, depth = d.parent, depth+1 {
		if d.stored != "" {
			segments = append(segments, d.stored)
			break
		}
		segments = append(segments, d.name)
		if depth >= maxParentDepth {
			break
		}
	}

	p := segments[len(segments)-1]
	for i := len(segments) - 2; i >= 0; i-- {
		p = path.Join(p, segments[i])
	}
	return p
}

// storedDirPath returns the path a directory recorded for itself, or "".
func storedDirPath(meta core.FileMeta) string {
	if stored := storedMetaPaths(meta); len(stored) > 0 {
		return stored[0]
	}
	return ""
}

// restorePathFilter is -path, expressed so that one rule covers both spellings.
// "a/b" and "a/b/" select the same set: the entry itself, and everything under
// it. filterByPath spelled that as two branches with identical bodies.
//
// The selection is unchanged, and so is the cost: every entry is still read and
// tested, because a stored path (pre-RFC 0015 snapshots record one) need not
// agree with where the entry sits in the tree, so a subtree cannot be pruned on
// its directory's path alone. Descending straight to the selected subtree is
// open question 7 in RFC 0025 and wants that question answered first.
//
// One narrowing against filterByPath, in the multi-parent case only: it pulled
// in the ancestors of a match through *every* Parents entry, so a secondary
// parent directory appeared in the output as an empty directory. This follows
// the primary chain, which is the one the matched file is written under.
type restorePathFilter struct {
	active bool
	target string
}

func newRestorePathFilter(pathFilter string) restorePathFilter {
	if pathFilter == "" {
		return restorePathFilter{}
	}
	return restorePathFilter{active: true, target: trimTrailingSlash(pathFilter)}
}

func (f restorePathFilter) matches(rel string) bool {
	if !f.active {
		return true
	}
	return rel == f.target || (len(rel) > len(f.target) && rel[:len(f.target)] == f.target && rel[len(f.target)] == '/')
}

func trimTrailingSlash(s string) string {
	if len(s) > 0 && s[len(s)-1] == '/' {
		return s[:len(s)-1]
	}
	return s
}
