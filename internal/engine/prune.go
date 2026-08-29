package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/objkey"
	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/internal/ui"
)

type PruneOption func(*pruneConfig)

type pruneConfig struct {
	dryRun bool
}

func WithPruneDryRun() PruneOption {
	return func(cfg *pruneConfig) { cfg.dryRun = true }
}

type PruneResult struct {
	BytesReclaimed int64
	ObjectsDeleted int
	ObjectsScanned int
	DryRun         bool
}

var objectPrefixes = []string{"chunk/", "content/", "filemeta/", "node/", "snapshot/"}

// objectPrefixesV3 are the namespaces a format-v3 repository actually stores
// in. filemeta/ and content/ do not exist there — an entry's metadata and its
// small content live in the leaf — so listing them would spend a request per
// prune to enumerate nothing, on every backend, forever.
//
// Sweeping a prefix that cannot exist is not merely wasteful, it is a claim
// about the format that stops being true if it is ever wrong: a listing that
// failed would fail the prune (docs/compatibility.md requires it), so the
// honest set is the one the format writes.
var objectPrefixesV3 = []string{"chunk/", "node/", "snapshot/"}

// prefixes returns the namespaces this repository's format stores objects in.
func (pm *PruneManager) prefixes() []string {
	if pm.v3 {
		return objectPrefixesV3
	}
	return objectPrefixes
}

// PruneManager implements mark-and-sweep garbage collection over the object store.
type PruneManager struct {
	store    *storelayer.MeteredStore
	tree     *hamt.Tree
	reporter ui.Reporter
	metas    *metaLoader
	// v3 is the repository's recorded format (Deps.FormatV3): an entry's chunk
	// refs are read from its leaf payload, and there are no filemeta/ or
	// content/ objects to mark or sweep.
	v3 bool
}

func NewPruneManager(d Deps) *PruneManager {
	meteredStore := storelayer.NewMeteredStore(d.Store)
	return &PruneManager{
		store:    meteredStore,
		tree:     hamt.NewTree(meteredStore, d.treeOptions()...),
		reporter: d.Reporter,
		// Uncached: markFileMeta guards every load behind the reachable set, so
		// a ref is read at most once per run and a cache could never hit. Holding
		// one would cost a core.FileMeta per object in the repository for nothing.
		metas: newUncachedMetaLoader(meteredStore),
		v3:    d.FormatV3,
	}
}

func (pm *PruneManager) Run(ctx context.Context, opts ...PruneOption) (*PruneResult, error) {
	var cfg pruneConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	if !cfg.dryRun {
		lock, lockedCtx, err := AcquireRepoLock(ctx, pm.store, "prune")
		if err != nil {
			return nil, err
		}
		defer lock.Release()
		ctx = lockedCtx
	}

	markPhase := pm.reporter.StartPhase("Marking reachable objects", 0, false)
	reachable, err := pm.mark(ctx, markPhase)
	if err != nil {
		markPhase.Error()
		return nil, err
	}
	markPhase.Done()

	result, err := pm.sweep(ctx, reachable, &cfg)
	if err != nil {
		return nil, err
	}

	// Repacking is a packfile-layer operation; a v3 repository has no packs to
	// fragment, and its garbage is ordinary unreferenced objects the sweep
	// above already removed.
	if !cfg.dryRun && !pm.v3 {
		// Attempt to repack fragmented packfiles, if this repository packs at all.
		packStore := findPackStore(pm.store)

		if packStore != nil {
			repackPhase := pm.reporter.StartPhase("Repacking fragmented index files", 0, false)
			// Threshold: Repack any packfile that is more than 30% empty
			bytesReclaimed, packsDeleted, err := packStore.Repack(ctx, 0.3)
			if err != nil {
				repackPhase.Error()
				return nil, fmt.Errorf("repack: %w", err)
			}
			repackPhase.Logf(ui.DetailVerbose, "Repacked/deleted %d packs, reclaimed %d bytes", packsDeleted, bytesReclaimed)
			result.BytesReclaimed += bytesReclaimed
			result.ObjectsDeleted += packsDeleted
			repackPhase.Done()

			// The pack index is append-only, so the sweep's deletions exist
			// only in memory until the index is rewritten wholesale. Compaction
			// is what makes them durable — it is required here, not an
			// optimisation. prune holds the exclusive lock, which is the
			// condition compaction needs.
			compactPhase := pm.reporter.StartPhase("Compacting the pack index", 0, false)
			removed, err := packStore.CompactCatalog(ctx)
			if err != nil {
				compactPhase.Error()
				return nil, fmt.Errorf("compact pack index: %w", err)
			}
			compactPhase.Logf(ui.DetailVerbose, "Consolidated %d index objects", removed)
			compactPhase.Done()
		}

		// Not discarded. The sweep's deletions and the compaction above exist
		// only in memory until this lands, so a failure here means a prune that
		// reported reclaimed space and durably reclaimed none of it.
		if err := pm.store.Flush(ctx); err != nil {
			return nil, fmt.Errorf("flush after prune: %w", err)
		}
	}

	return result, nil
}

// mark walks every snapshot and records the key of every object reachable from
// one. Its result is the largest structure prune holds — one entry per live
// object — so it is an objkey.Set rather than the map[string]bool it reads as.
//
// The representation is load-bearing beyond memory. A key missing from this set
// is an object the sweep below deletes, so the set must be total over the keys
// that reach it: objkey.Set keeps a key it cannot encode compactly rather than
// dropping it, which is docs/compatibility.md's rule that a garbage collector
// must never read "cannot represent" as "not referenced".
func (pm *PruneManager) mark(ctx context.Context, phase ui.Phase) (*objkey.Set, error) {
	reachable := objkey.NewSet()

	snapRefs, err := pm.collectSnapshots(ctx, reachable)
	if err != nil {
		return nil, err
	}

	// A repository holding objects but no listable snapshots would make the
	// sweep below delete everything. That is never a legitimate state: it means
	// the snapshot listing is incomplete, most often because an index could not
	// be read. Refuse rather than act on it.
	if len(snapRefs) == 0 {
		orphan, err := pm.firstObject(ctx)
		if err != nil {
			return nil, err
		}
		if orphan != "" {
			return nil, fmt.Errorf(
				"prune aborted: no snapshots found, but the repository still contains objects (e.g. %s). "+
					"This usually means a repository index could not be read; "+
					"re-run once the store is reachable, or run 'cloudstic check' to inspect the repository",
				orphan,
			)
		}
	}

	phase.Log(fmt.Sprintf("Found %d unique snapshots", len(snapRefs)))

	for ref := range snapRefs {
		if err := pm.markSnapshot(ctx, ref, reachable); err != nil {
			return nil, fmt.Errorf("mark snapshot %s: %w", ref, err)
		}
		phase.Increment(1)
	}

	return reachable, nil
}

// firstObject returns any one key under the sweepable prefixes, or "" when the
// repository holds none. Errors are propagated: an unreadable listing must not
// be mistaken for an empty repository.
func (pm *PruneManager) firstObject(ctx context.Context) (string, error) {
	for _, prefix := range pm.prefixes() {
		keys, err := pm.store.List(ctx, prefix)
		if err != nil {
			return "", fmt.Errorf("list %s: %w", prefix, err)
		}
		if len(keys) > 0 {
			return keys[0], nil
		}
	}
	return "", nil
}

func (pm *PruneManager) collectSnapshots(ctx context.Context, reachable *objkey.Set) (map[string]bool, error) {
	keys, err := pm.store.List(ctx, "snapshot/")
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	snapRefs := make(map[string]bool, len(keys))
	for _, key := range keys {
		snapRefs[key] = true
	}

	if exists, _ := pm.store.Exists(ctx, "index/latest"); exists {
		reachable.Add("index/latest")
	}
	// index/packs belongs to the packfile layer, which a v3 repository does
	// not have: asking would cost a request to learn it is absent.
	if !pm.v3 {
		if exists, _ := pm.store.Exists(ctx, "index/packs"); exists {
			reachable.Add("index/packs")
		}
	}

	return snapRefs, nil
}

func (pm *PruneManager) markSnapshot(ctx context.Context, ref string, reachable *objkey.Set) error {
	if !reachable.Add(ref) {
		return nil
	}

	snap, err := pm.loadSnapshot(ctx, ref)
	if err != nil {
		return err
	}

	// v3: an entry's whole chain lives in its leaf, so marking is one
	// traversal that records node refs and reads each entry's chunk refs from
	// the payload as it passes — no per-entry reads at all. Enumerating nodes
	// and then walking entries separately would read every leaf twice, and a
	// v3 leaf is the data (see hamt.WalkTree).
	//
	// A payload-less entry fails the prune rather than being skipped: its
	// chunk refs are unknowable, and docs/compatibility.md forbids collecting
	// garbage over data that could not be fully read.
	if pm.v3 {
		// WalkChunkRefs rather than WalkTree: marking reads an entry's chunk
		// refs and nothing else, and a leaf's Meta and Inline are almost all of
		// its bytes. Decoding them cost 45% of the allocation of marking one
		// 357 MB repository.
		return pm.tree.WalkChunkRefs(ctx, snap.Root,
			func(r string) error {
				reachable.Add(r)
				return nil
			},
			func(key, ref string, chunks []string, hasPayload bool) error {
				if !reachable.Add(ref) {
					return nil
				}
				if !hasPayload {
					return fmt.Errorf("v3 leaf entry %s (%s) carries no payload; refusing to prune", key, ref)
				}
				for _, c := range chunks {
					reachable.Add(c)
				}
				return nil
			})
	}

	if err := pm.tree.NodeRefs(ctx, snap.Root, func(r string) error {
		reachable.Add(r)
		return nil
	}); err != nil {
		return err
	}

	// Two grouped passes per batch rather than one interleaved pass. Marking an
	// entry needs its filemeta and then the content object that filemeta names,
	// and doing both inline alternates between two namespaces that live in
	// different packs — so a batch of grouped filemeta reads is punctuated by
	// content reads that evict the very bodies the grouping arranged. Measured
	// at 82 packs, grouping the walk alone moved prune essentially nothing.
	//
	// One namespace per pass is the rule. Doing the second pass per batch rather
	// than per snapshot is what keeps the carried keys bounded: a snapshot-wide
	// collection is O(entries), and this phase is already the one that builds an
	// O(objects) reachable set, so there is no reason to add a second.
	return walkEntriesBatched(ctx, pm.tree, snap.Root, func(entries []treeEntry) error {
		refs := make([]string, len(entries))
		for i, e := range entries {
			refs[i] = e.ref
		}
		contentRefs := make([]string, 0, len(refs))
		if err := readGrouped(ctx, pm.store, refs, func(ref string) error {
			key, err := pm.markFileMeta(ctx, ref, reachable)
			if err != nil {
				return err
			}
			if key != "" {
				contentRefs = append(contentRefs, key)
			}
			return nil
		}); err != nil {
			return err
		}
		return readGrouped(ctx, pm.store, contentRefs, func(key string) error {
			return pm.markContent(ctx, key, reachable)
		})
	})
}

// markFileMeta marks an entry's filemeta and returns the content key it names,
// leaving that content unread so the caller can batch it.
//
// It returns "" when there is nothing to read: the entry has no content, or its
// filemeta was already marked by another snapshot.
func (pm *PruneManager) markFileMeta(ctx context.Context, ref string, reachable *objkey.Set) (string, error) {
	if !reachable.Add(ref) {
		return "", nil
	}

	meta, err := pm.metas.load(ctx, ref)
	if err != nil {
		return "", err
	}
	if meta.ContentHash == "" {
		return "", nil
	}
	contentKey := meta.ContentRef
	if contentKey == "" {
		contentKey = meta.ContentHash
	}
	return "content/" + contentKey, nil
}

func (pm *PruneManager) markContent(ctx context.Context, ref string, reachable *objkey.Set) error {
	if !reachable.Add(ref) {
		return nil
	}

	data, err := pm.store.Get(ctx, ref)
	if err != nil {
		return fmt.Errorf("get content %s: %w", ref, err)
	}
	var content core.Content
	if err := json.Unmarshal(data, &content); err != nil {
		return err
	}

	for _, c := range content.Chunks {
		reachable.Add(c)
	}
	return nil
}

// sweep deletes every object the mark phase did not reach.
//
// A listing that fails is fatal, and that is the rule in docs/compatibility.md
// rather than caution: prune must not proceed on data it could not fully read.
// Skipping the prefix instead — which is what this did — leaves the operation
// silently partial. It errs safe in the sense that an unlisted prefix is one
// nothing is deleted from, but prune then reports a success and an object count
// covering a repository it only partly looked at, and the next run has no idea
// a prefix was missed.
//
// A delete that fails is fatal too, at the end rather than on the spot. The
// sweep goes on to attempt every other object — one unreachable object it could
// not remove is not a reason to leave the rest — but it must not then hand back
// a success. `prune` reports objects deleted and space reclaimed, and a report
// saying it collected garbage that is still sitting in the repository is the
// misreport docs/compatibility.md's rule exists to prevent. The next run
// re-marks and re-sweeps, so failing is safe as well as honest.
//
// Deletions are issued in batches through the store's BatchDeleter capability
// (store.DeleteAll), which is one DeleteObjects request per 1,000 keys on an
// S3-family backend and a loop everywhere else. The batching is why the failure
// rule has to be stated in terms of individual keys: one response carries a
// verdict per key, and only the keys it confirms gone may be counted.
func (pm *PruneManager) sweep(ctx context.Context, reachable *objkey.Set, cfg *pruneConfig) (*PruneResult, error) {
	listing := make(map[string][]string, len(pm.prefixes()))
	var totalKeys int
	for _, prefix := range pm.prefixes() {
		keys, err := pm.store.List(ctx, prefix)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, err)
		}
		listing[prefix] = keys
		totalKeys += len(keys)
	}

	label := "Sweeping unreachable objects"
	if cfg.dryRun {
		label = "Scanning unreachable objects (dry run)"
	}
	phase := pm.reporter.StartPhase(label, int64(totalKeys), true)
	result := &PruneResult{DryRun: cfg.dryRun}

	var batch []string
	var failed sweepFailures

	// The listing taken above is the one swept, rather than being taken a second
	// time. Two passes could disagree, and the progress total would then describe
	// a different set of objects than the one being deleted.
	for _, prefix := range pm.prefixes() {
		for _, key := range listing[prefix] {
			result.ObjectsScanned++
			if reachable.Has(key) {
				phase.Increment(0)
				continue
			}
			if cfg.dryRun {
				phase.Logf(ui.DetailVerbose, "Would delete: %s", key)
				result.ObjectsDeleted++
				phase.Increment(0)
				continue
			}
			batch = append(batch, key)
			if len(batch) == sweepDeleteBatch {
				pm.deleteBatch(ctx, batch, result, phase, &failed)
				batch = batch[:0]
			}
		}
	}
	if len(batch) > 0 {
		pm.deleteBatch(ctx, batch, result, phase, &failed)
	}

	if !cfg.dryRun {
		result.BytesReclaimed = -pm.store.BytesWritten()
		pm.store.Reset()
	}
	if failed.count > 0 {
		phase.Error()
		return nil, fmt.Errorf("sweep: %d of %d unreachable objects could not be deleted: %w",
			failed.count, failed.count+result.ObjectsDeleted, failed.first)
	}
	phase.Done()
	return result, nil
}

// sweepDeleteBatch is how many unreachable keys the sweep hands to the store at
// once. It matches the 1,000-key ceiling of S3's DeleteObjects, which is the
// largest batch any backend this tool targets accepts; a store free to split
// further does, and one that cannot batch at all loops. Batching at the sweep
// rather than handing over the whole listing keeps progress reporting and the
// memory held for one batch's sizes bounded by this number instead of by the
// repository.
const sweepDeleteBatch = 1000

// sweepFailures records that deletions failed, without holding one error per
// object: a sweep over a repository that has lost its credentials would
// otherwise accumulate an error for every key it lists.
type sweepFailures struct {
	count int
	first error
}

// deleteBatch deletes one batch of unreachable keys and folds the outcome into
// result, the progress phase, and failed.
//
// Only the keys the store confirms gone are counted and logged. That is the
// whole reason DeleteAllReturnSizes reports sizes per key rather than a total:
// a batch is not all-or-nothing, and neither the object count nor the reclaimed
// bytes may include a key the store did not say it deleted.
func (pm *PruneManager) deleteBatch(ctx context.Context, keys []string, result *PruneResult, phase ui.Phase, failed *sweepFailures) {
	sizes, err := pm.store.DeleteAllReturnSizes(ctx, keys)
	for _, key := range keys {
		size, deleted := sizes[key]
		if !deleted {
			continue
		}
		phase.Logf(ui.DetailVerbose, "Deleted: %s", key)
		result.ObjectsDeleted++
		phase.Increment(size)
	}
	if err == nil {
		return
	}
	failed.count += len(keys) - len(sizes)
	if failed.first == nil {
		failed.first = err
	}
}

func (pm *PruneManager) loadSnapshot(ctx context.Context, ref string) (*core.Snapshot, error) {
	data, err := getVerified(ctx, pm.store, ref)
	if err != nil {
		return nil, fmt.Errorf("get snapshot %s: %w", ref, err)
	}
	var s core.Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
