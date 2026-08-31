package engine

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

// RestoreOption configures a restore operation.
type RestoreOption func(*restoreConfig)

type restoreConfig struct {
	dryRun     bool
	pathFilter string
	noVerify   bool
}

// restorePlan is what a restore walks. byID owns the metadata; sorted is an
// ordering over it, naming entries by FileID rather than repeating them.
//
// The two used to be independent copies of the same tree. A core.FileMeta is 216
// bytes, so a snapshot's metadata was held twice over before a byte was written.
// An ordering of IDs costs one string header each and shares the ID already
// stored as byID's key.
type restorePlan struct {
	cfg         restoreConfig
	sorted      []string
	byID        map[string]core.FileMeta
	snapshotRef string
	root        string

	// dirsOnly marks a plan that holds the snapshot's directories and nothing
	// else — the shape a streaming v3 restore uses. byID is then the directory
	// index a path is resolved through, and sorted their topological order;
	// files never enter either, because the write walk meets each one once and
	// releases it (see runWithWriterV3).
	dirsOnly bool
}

// WithRestoreDryRun resolves the snapshot and reports what would be restored without writing output.
func WithRestoreDryRun() RestoreOption {
	return func(cfg *restoreConfig) { cfg.dryRun = true }
}

// WithRestoreNoVerify skips the content-hash check that restore normally runs
// over every file it writes. Verification is on by default; this exists as an
// escape hatch for the case where a snapshot records a hash that disagrees with
// its own content, so that the data can still be recovered.
func WithRestoreNoVerify() RestoreOption {
	return func(cfg *restoreConfig) { cfg.noVerify = true }
}

// WithRestorePath limits the restore to files matching the given path.
// If the path ends with "/", all files under that subtree are included.
// Otherwise, only the file with the exact path is restored.
func WithRestorePath(p string) RestoreOption {
	return func(cfg *restoreConfig) { cfg.pathFilter = p }
}

// RestoreResult holds the outcome of a restore operation.
type RestoreResult struct {
	SnapshotRef  string
	Root         string
	FilesWritten int
	DirsWritten  int
	BytesWritten int64
	Errors       int
	Warnings     int
	DryRun       bool
}

// RestoreWriter is the output abstraction for restore formats.
type RestoreWriter interface {
	MkdirAll(path string, meta core.FileMeta) error
	WriteFile(path string, meta core.FileMeta, writeContent func(io.Writer) error) error
	BytesWritten() int64
	Close() error
}

// concurrentRestoreWriter is the optional opt-in a RestoreWriter uses to
// declare that WriteFile is safe to call from several goroutines at once.
// Without it runWithWriter restores files one at a time.
//
// It is an opt-in rather than the default because it is not universally
// achievable: a zip archive is a single sequential stream with one open entry
// at a time, so zipRestoreWriter deliberately does not implement it. A
// directory tree has no such constraint — distinct files are distinct fds —
// so fsRestoreWriter does.
type concurrentRestoreWriter interface {
	SupportsConcurrentWrites() bool
}

// restoreMemoryBudget caps the total bytes of chunk data that have been
// fetched but not yet written, across every file being restored at once.
// Restore fans out on two axes there — several files in flight, several chunks
// per file — so neither axis can be the memory bound on its own; this is.
// It mirrors the equivalent cap on the backup side (see backup_upload.go).
//
// It governs the chunked path only, and deliberately still does. A v3 file
// under inlineThreshold is a member of a blob rather than a sequence of chunk
// objects, and blob reads are bounded by restoreSpansInFlight instead. The two
// are separate because they bound different shapes: this one a pipeline of
// independent objects per file, where the right unit is bytes; that one a
// shared buffer many files are cut out of, where the right unit is buffers.
const restoreMemoryBudget = 128 * 1024 * 1024

// restoreSpansInFlight is how many coalesced blob reads a v3 restore has
// outstanding at once, counting a read as outstanding until the last file cut
// out of it has been written — which is exactly how long its buffer lives.
//
// It is the price coalescing pays. Reading a blob once instead of once per
// file means holding that blob while its files are written, so this number
// times a blob is the memory the request reduction costs, and it is the number
// that has to stay small.
//
// Two, from the bandwidth-delay product that sizes a blob in the first place.
// A blob is written at about one round trip's worth of bytes (blobBudget, and
// see restoreIOGap), so one in flight already keeps the link busy; the second
// is the one being decoded and written while the first arrives. A third buys
// no more utilization — the link was busy at one — and is simply another blob
// resident. Measurement on a 20,000-file repository agrees from the other
// side: wall time is flat from two spans upward, while peak RSS climbs about
// 2.5 MB for every further span held, and holding eight cost 15 MB.
//
// A count rather than a byte budget, because the quantity the argument is
// about is blobs. A byte budget has to be written down in *stored* bytes while
// blobBudget is plaintext, so the same figure admits four blobs on a tree that
// compresses 4x and one on a tree that does not — it varies the parallelism
// with the compression ratio, which is the one thing it should not depend on.
// A span cannot exceed restoreSpanBytes, so the bytes are bounded regardless.
const restoreSpansInFlight = 2

// restoreBatchEntries is how many files a v3 restore plans its reads over
// before writing any of them.
//
// A batch is the window a coalescing planner can see, so it wants to be large;
// it is also what the phase holds, so it wants to be small. 2048 is the same
// figure the metadata pass buffers at (metaDecodeBatch) and for the same
// reason: an entry in the batch is a pointer into a leaf that is live anyway,
// so a batch costs a few slice headers each rather than a copy of anything.
const restoreBatchEntries = 2048

// restoreSpanWindowBytes is roughly how much blob one batch reaches into, and
// so the second thing that closes a batch.
//
// An entry count alone is the wrong cut, because what a batch has to contain
// is not files but *blobs*: the walk does not arrive strictly blob by blob —
// upload concurrency shuffles bodies into blobs, so a stretch of walk order
// interleaves several — and a batch narrower than that interleaving splits a
// blob's members across batches and reads the blob once per batch. Measured on
// a 20,000-file repository of 32 blobs, restoring everything: a 4 MB window
// costs 3,656 blob reads, 8 MB costs 78, 16 MB and 32 MB both cost 61. The
// curve flattens once a batch spans more blobs than the walk interleaves, and
// 61 against 32 blobs is that floor.
//
// It does not bound memory. How much blob is resident is restoreSpansInFlight,
// which is why this can be set for the request count alone; widening it past
// the flat region buys nothing, and narrowing it below costs requests without
// saving anything.
const restoreSpanWindowBytes = 32 * 1024 * 1024

// RestoreManager recreates a snapshot's file tree using a RestoreWriter output format.
type RestoreManager struct {
	store     store.ObjectStore
	tree      *hamt.Tree
	metas     *metaLoader
	reporter  ui.Reporter
	memBudget *semaphore.Weighted
	// blobs reads a body out of the blob holding it. Nil outside format v3.
	blobs *blobReader
	// v3 is the repository's recorded format (Deps.FormatV3): metadata and
	// content locations are read from leaf payloads, never from filemeta/ or
	// content/ objects.
	v3 bool
}

func NewRestoreManager(d Deps) *RestoreManager {
	return &RestoreManager{
		store:     d.Store,
		tree:      hamt.NewTree(d.Store, d.treeOptions()...),
		metas:     newUncachedMetaLoader(d.Store),
		reporter:  d.Reporter,
		memBudget: semaphore.NewWeighted(restoreMemoryBudget),
		v3:        d.FormatV3,
		blobs:     d.blobReader(),
	}
}

// Run restores the snapshot's file tree to the provided writer format.
// snapshotRef can be "", "latest", a bare hash, or "snapshot/<hash>".
func (rm *RestoreManager) Run(ctx context.Context, writer RestoreWriter, snapshotRef string, opts ...RestoreOption) (*RestoreResult, error) {
	lock, lockedCtx, err := AcquireSharedLock(ctx, rm.store, "restore")
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	ctx = lockedCtx

	plan, err := rm.prepareRestore(ctx, snapshotRef, opts...)
	if err != nil {
		return nil, err
	}

	if plan.cfg.dryRun {
		return rm.dryRunRestore(plan.sorted, plan.byID, plan.snapshotRef, plan.root), nil
	}
	if writer == nil {
		return nil, fmt.Errorf("restore writer is required")
	}
	return rm.runWithWriter(ctx, plan, writer)
}

// runWithWriter walks the topologically sorted entries, creating directories
// and writing file contents.
//
// Directories are handled inline and in order: plan.sorted lists a parent
// before its children, so creating them on this goroutine is what guarantees
// a file's directory exists by the time the file is dispatched.
//
// File contents are the expensive part — each one is at least a content-object
// fetch, and for a large file a whole sequence of chunk fetches. Restoring
// them one after another means exactly one request is ever outstanding, so on
// a high-latency backend the wall time collapses to (file count x round trip)
// no matter how much bandwidth or store concurrency is available. They are
// therefore dispatched to a bounded worker pool whenever the writer says it
// can take concurrent writes; the pool degenerates to sequential execution for
// writers that cannot (zip).
func (rm *RestoreManager) runWithWriter(ctx context.Context, plan restorePlan, writer RestoreWriter) (*RestoreResult, error) {
	if rm.v3 {
		return rm.runWithWriterV3(ctx, plan, writer)
	}
	return rm.runWithWriterV2(ctx, plan, writer)
}

func (rm *RestoreManager) runWithWriterV2(ctx context.Context, plan restorePlan, writer RestoreWriter) (*RestoreResult, error) {
	result := &RestoreResult{SnapshotRef: plan.snapshotRef, Root: plan.root}
	phase := rm.reporter.StartPhase("Restoring", int64(len(plan.sorted)), false)

	// Counters are touched from the file workers as well as this goroutine.
	var mu sync.Mutex
	bump := func(field *int) {
		mu.Lock()
		*field++
		mu.Unlock()
	}

	if setter, ok := writer.(restoreWarningSetter); ok {
		setter.SetWarningFunc(func(msg string) {
			bump(&result.Warnings)
			phase.Log("Warning: " + msg)
		})
	}

	cw, ok := writer.(concurrentRestoreWriter)
	concurrent := ok && cw.SupportsConcurrentWrites()

	g, gCtx := errgroup.WithContext(ctx)
	if concurrent {
		g.SetLimit(restoreFileConcurrency(rm.store))
	}

	// A failure to write one file is reported and counted, not fatal — the
	// rest of the snapshot is still worth recovering. Only a context
	// cancellation stops the walk, which is why the worker returns nil here
	// and errgroup carries just gCtx's error.
	restoreFile := func(meta core.FileMeta, rel string) func() error {
		return func() error {
			err := writer.WriteFile(rel, meta, func(out io.Writer) error {
				return rm.writeFileContent(gCtx, out, meta, !plan.cfg.noVerify)
			})
			defer phase.Increment(1)
			switch {
			case errors.Is(err, errRestoreSkipped):
				return nil
			case err != nil:
				phase.Log(fmt.Sprintf("Failed: %s: %v", rel, err))
				bump(&result.Errors)
				return nil
			}
			phase.Logf(ui.DetailVerbose, "File: %s (%d bytes)", rel, meta.Size)
			bump(&result.FilesWritten)
			return nil
		}
	}

	// The write phase declares nothing, and that is a decision rather than an
	// omission.
	//
	// It used to hand its whole content read set to store.PlanReads (#496), on the
	// reasoning that a declaration is a statement about *what* will be read and so
	// stays true whatever order the reads arrive in. The statement is true; the use
	// admission makes of it is not. PlanReads records how many objects each pack
	// owes this caller, and resolveFromPack promotes a pack to a whole transfer once
	// that count says a transfer beats the ranged reads it replaces — which is only
	// cheaper if the caller reads those objects while the body is still resident.
	//
	// The metadata phase earns that: it reads group by group, one worker per group,
	// with concurrency bounded to the body cache's capacity. This phase cannot. It
	// writes in walk order across up to restoreFileConcurrency files at once, so it
	// touches every pack before returning to any of them, and a declaration that
	// marks them all worth transferring whole turns every read into a whole-pack
	// fetch that is evicted before its next object is wanted.
	//
	// Measured against MinIO on a 20,000-file repository of 11 full packs
	// (RFC 0025 §7): declaring cost 18,416 MB of transfer and 93.5 s where declaring
	// nothing cost 160 MB and 3.1 s, for 2,274 requests against 4,726. Bounding the
	// declared window to 2,048 entries does not help — 19,236 MB — because a window
	// that size still spans every pack. Below roughly 42 packs the whole working set
	// fits the body cache and all three are indistinguishable.
	//
	// So the write phase reads undeclared, and its packs are admitted by the
	// estimator in packadmission.go, whose eviction penalty is exactly the feedback
	// a declaration overrides. Restore's *metadata* phase still declares (see
	// collectMetadata) and is where the measured win lives.
	for _, id := range plan.sorted {
		meta := plan.byID[id]
		rel := buildRestorePath(meta, plan.byID)

		if meta.Type == core.FileTypeFolder {
			if err := writer.MkdirAll(rel, meta); err != nil {
				if !errors.Is(err, errRestoreSkipped) {
					phase.Log(fmt.Sprintf("Failed: %s: %v", rel, err))
					result.Errors++
				}
				phase.Increment(1)
				continue
			}
			phase.Logf(ui.DetailVerbose, "Dir: %s", rel)
			result.DirsWritten++
			phase.Increment(1)
			continue
		}

		if meta.ContentHash == "" {
			phase.Increment(1)
			continue
		}

		// A sequential writer runs inline, not through the pool. Even a pool of
		// one would let a file write overlap the MkdirAll of a later entry on
		// this goroutine, and for a zip that means a directory header written
		// into the middle of an open file entry.
		if !concurrent {
			_ = restoreFile(meta, rel)()
			continue
		}
		g.Go(restoreFile(meta, rel))
	}

	if err := g.Wait(); err != nil {
		phase.Error()
		return nil, err
	}

	phase.Done()
	if err := writer.Close(); err != nil {
		return nil, err
	}
	result.BytesWritten = writer.BytesWritten()
	return result, nil
}

// runWithWriterV3 writes a format-v3 snapshot in **leaf order**, taking each
// file's content from the payload the walk already holds.
//
// The v2 shape does not carry over. There, a file's content is its own object,
// so the write phase can visit files in any order and pay one fetch each. In
// v3 the content lives inside the leaf, and looking each file up separately —
// which is what this did first — costs a leaf read per file whenever the leaf
// set outgrows the node cache: measured at ~20,000 requests for a 22,000-file
// snapshot, an order of magnitude worse than v2 on the same tree.
//
// Walking the leaves once and writing what each holds costs one read per leaf
// instead, whatever the cache does, because nothing is ever revisited. The
// price is that directories can no longer be created lazily on the way past:
// leaf order is affinity-hash order, so a file may be written before its
// parent directory is reached. So directories are created first, in
// topological order, from metadata already collected — they are a small
// minority of entries and carry no content, so the extra pass is cheap.
//
// Chunked files are dispatched to a bounded pool because their cost is
// per-chunk round trips; inline files are written on this goroutine, where
// the bytes are already in hand and a hand-off would only add scheduling.
func (rm *RestoreManager) runWithWriterV3(ctx context.Context, plan restorePlan, writer RestoreWriter) (*RestoreResult, error) {
	result := &RestoreResult{SnapshotRef: plan.snapshotRef, Root: plan.root}

	// A streaming restore does not know how many entries it will write until
	// it has written them — that is the point — so it reports a count rather
	// than a percentage. plan.sorted holds only directories there, and using
	// it as the total would show a bar that reaches 100% after the first few
	// per cent of the work.
	total := int64(len(plan.sorted))
	if plan.dirsOnly {
		total = 0
	}
	phase := rm.reporter.StartPhase("Restoring", total, false)

	var mu sync.Mutex
	bump := func(field *int) {
		mu.Lock()
		*field++
		mu.Unlock()
	}

	if setter, ok := writer.(restoreWarningSetter); ok {
		setter.SetWarningFunc(func(msg string) {
			bump(&result.Warnings)
			phase.Log("Warning: " + msg)
		})
	}

	// Directories first, in the plan's topological order: leaf order does not
	// guarantee a parent precedes its children.
	for _, id := range plan.sorted {
		meta := plan.byID[id]
		if meta.Type != core.FileTypeFolder {
			continue
		}
		rel := buildRestorePath(meta, plan.byID)
		if !restoreNeedsDir(rel, plan.cfg.pathFilter) {
			continue
		}
		if err := writer.MkdirAll(rel, meta); err != nil {
			if !errors.Is(err, errRestoreSkipped) {
				phase.Log(fmt.Sprintf("Failed: %s: %v", rel, err))
				result.Errors++
			}
			phase.Increment(1)
			continue
		}
		phase.Logf(ui.DetailVerbose, "Dir: %s", rel)
		result.DirsWritten++
		phase.Increment(1)
	}

	cw, ok := writer.(concurrentRestoreWriter)
	concurrent := ok && cw.SupportsConcurrentWrites()

	// pendingWrite is one file the walk has met and the batch has not yet
	// written. The payload is carried rather than copied: a decoded node is
	// immutable and shared with the cache, so holding a pointer into one is
	// free and races with nothing.
	type pendingWrite struct {
		meta core.FileMeta
		rel  string
		p    *hamt.Payload
	}

	batch := make([]pendingWrite, 0, restoreBatchEntries)
	batchSpanBytes := int64(0)
	batchBlobs := make(map[string]bool)

	// sealed, when non-nil, is w's member already in hand from a coalesced
	// read, so writing the file costs no request of its own.
	writeOne := func(ctx context.Context, w pendingWrite, sealed []byte) error {
		err := writer.WriteFile(w.rel, w.meta, func(out io.Writer) error {
			return rm.writeContentFromPayload(ctx, out, w.meta, w.p, sealed, !plan.cfg.noVerify)
		})
		defer phase.Increment(1)
		switch {
		case errors.Is(err, errRestoreSkipped):
			return nil
		case err != nil:
			phase.Log(fmt.Sprintf("Failed: %s: %v", w.rel, err))
			bump(&result.Errors)
			return nil
		}
		phase.Logf(ui.DetailVerbose, "File: %s (%d bytes)", w.rel, w.meta.Size)
		bump(&result.FilesWritten)
		return nil
	}

	// flush reads the batch's bodies and writes its files, through two pools
	// that overlap rather than two phases that follow each other.
	//
	// Planning the reads is what collapses them: the batch's body references
	// go through planBlobSpans and each span becomes one request serving every
	// member inside it. Restore issued one ranged read per file before this,
	// which on a tree whose files were backed up together — and so packed
	// together — is N requests for bytes that arrive in one.
	//
	// There are three pools because there are three costs, and the single one
	// this replaced conflated them. While a body was one fetch per file,
	// "files in flight" and "requests in flight" were the same number and one
	// limit served both; a span now serves many files, so:
	//
	//   - the blob reads are bounded by how many blobs are worth being
	//     resident in at once (restoreSpansInFlight);
	//   - the files cut out of those spans by how much decoding and writing
	//     there is room for, which is not a store property at all
	//     (restoreWriteConcurrency);
	//   - and the files that still make a request of their own — chunked, or
	//     fallen back from a span that could not be read — by the store's own
	//     hint, exactly as before (restoreFileConcurrency).
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		defer func() {
			batch = batch[:0]
			batchSpanBytes = 0
			clear(batchBlobs)
		}()

		reads := make([]blobRead, 0, len(batch))
		for i := range batch {
			if b := batch[i].p.Body; b != nil {
				reads = append(reads, blobRead{index: i, ref: b})
			}
		}
		spans := planBlobSpans(reads, restoreIOGap, restoreSpanBytes())

		sealed := make([][]byte, len(batch))
		// dispatched marks the entries a span handed to the write pool as soon
		// as its bytes landed, so the pass below writes only what is left.
		// Each index belongs to exactly one span, and fg.Wait orders these
		// writes before that pass reads them.
		dispatched := make([]bool, len(batch))

		wg, wgCtx := errgroup.WithContext(ctx)
		if concurrent {
			wg.SetLimit(restoreWriteConcurrency)
		}

		// A span holds its slot until every file cut out of it has been
		// written, because that is exactly as long as its buffer is reachable.
		// The slot count is therefore how many blobs a restore is resident in
		// at once, which is the memory this trades for the requests it saves —
		// see restoreSpansInFlight.
		fg, fgCtx := errgroup.WithContext(ctx)
		fg.SetLimit(restoreSpansInFlight)
		for _, s := range spans {
			fg.Go(func() error {
				data, err := rm.blobs.span(fgCtx, s)
				if err != nil {
					// Not fatal, and deliberately not reported here. The
					// members fall back to their own ranged reads below, where
					// a failure is counted against the one file it belongs to
					// and the rest of the snapshot is still recovered — which
					// is what restore did before its reads were coalesced, and
					// what coalescing must not quietly take away.
					return nil
				}
				// pending is what keeps this slot occupied until the span's
				// own files are written.
				var pending sync.WaitGroup
				defer pending.Wait()
				for _, idx := range s.members {
					m, err := s.slice(data, batch[idx].p.Body)
					if err != nil {
						// A member the span does not cover means the plan and
						// the entry disagree, which should be unreachable. It
						// falls back rather than failing the restore, for the
						// same reason a failed span read does — the file is
						// then read on its own and either works or is counted.
						continue
					}
					if !concurrent {
						// The sequential writer takes its bytes from here
						// instead, below, in batch order — so its spans stay
						// resident for the batch rather than for their own
						// files. A zip restore holds one batch's worth, which
						// restoreSpanWindowBytes is what bounds.
						sealed[idx] = m
						continue
					}
					// Handed over the moment it is readable rather than after
					// every span has landed. Two phases in sequence idle the
					// disk while the batch is read and then idle the link
					// while it is written, and the write pool is what has to
					// stay fed.
					//
					// m is the only reference to the span's buffer once this
					// closure holds it, which is why a dispatched member is
					// never also parked in sealed: keeping it there would pin
					// every span the batch read until the batch ended, and
					// cost 15 MB of peak RSS on a 20,000-file tree.
					//
					// Submitting from here cannot deadlock against the slot
					// this goroutine holds. The only writes running while a
					// span is resident are blob members, and a member cut out
					// of a span in hand waits on nothing; the files that do
					// wait — the chunked ones, on restoreMemoryBudget — go to
					// the request pool below, after every span slot is free.
					w := batch[idx]
					dispatched[idx] = true
					pending.Add(1)
					wg.Go(func() error {
						defer pending.Done()
						return writeOne(wgCtx, w, m)
					})
				}
				return nil
			})
		}
		if err := fg.Wait(); err != nil {
			return err
		}

		// What is left is the request-bound work, and it gets its own pool.
		//
		// Under the concurrent writer every span-served file has already gone
		// to wg above, so everything reaching here still has a request to
		// make: a chunked file, which opens a fetch window of its own inside
		// writeChunks, or a body whose span could not be read and which falls
		// back to its own ranged read. Those are exactly the files the single
		// pool used to admit at restoreFileConcurrency, and they still want
		// that width — it is a statement about requests, which they still
		// issue, where restoreWriteConcurrency is a statement about decoding
		// and writing, which is all a span-served file does.
		//
		// Collapsing the two is what would cost: a store hinting above 8 —
		// S3 hints 128 — would have its chunked files throttled to a number
		// measured on a local disk, where the constraint is directory
		// metadata and no request is made at all.
		rg, rgCtx := errgroup.WithContext(ctx)
		if concurrent {
			rg.SetLimit(restoreFileConcurrency(rm.store))
		}
		for i := range batch {
			if dispatched[i] {
				continue
			}
			w, m := batch[i], sealed[i]
			if !concurrent {
				// A zip is one sequential stream with one open entry at a
				// time, so its files are written here, in batch order, rather
				// than as their spans arrive.
				if err := writeOne(rgCtx, w, m); err != nil {
					return err
				}
				continue
			}
			rg.Go(func() error { return writeOne(rgCtx, w, m) })
		}
		if err := rg.Wait(); err != nil {
			return err
		}
		return wg.Wait()
	}

	// Files stream past in batches: each batch is decoded from leaves that are
	// already in hand, read, written and released. What the phase holds is the
	// directory index, one batch of entries, and the spans currently being
	// consumed — not the snapshot.
	//
	// This is the streaming restore RFC 0025 §8 built for the packfile format
	// and withdrew. There it cost 3x the wall time, because metadata and
	// content lived in different objects and interleaving the two evicted each
	// other's pack bodies. Here they are separate objects again, but separated
	// the other way round: a leaf carries every entry's metadata *and* the
	// address of its body, so one pass over the leaves knows a whole batch's
	// reads before issuing any of them — which is exactly what a coalescing
	// planner needs, and what a pack catalog could not supply.
	walkErr := rm.tree.WalkEntries(ctx, plan.root, func(fileID, ref string, p *hamt.Payload) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("v3 leaf entry %s carries no payload", ref)
		}
		if _, isDir := plan.byID[fileID]; isDir && plan.dirsOnly {
			return nil // written above, in topological order
		}

		var meta core.FileMeta
		if plan.dirsOnly {
			fm, err := decodePayloadMeta(ref, p)
			if err != nil {
				return err
			}
			meta = *fm
		} else {
			known, ok := plan.byID[fileID]
			if !ok {
				return nil // filtered out of this restore
			}
			meta = known
		}

		if meta.Type == core.FileTypeFolder {
			return nil
		}
		if meta.ContentHash == "" {
			phase.Increment(1)
			return nil
		}

		rel := buildRestorePath(meta, plan.byID)
		if !matchesRestorePath(rel, plan.cfg.pathFilter) {
			return nil
		}

		// Every file joins the batch, inline ones included. Writing them on
		// this goroutine instead — which this did first — serialises the
		// majority of a snapshot behind one writer, since most files are
		// small enough to inline, and that is what made a v3 restore slower
		// in wall time than v2 while making fewer requests and moving a third
		// of the bytes.
		batch = append(batch, pendingWrite{meta: meta, rel: rel, p: p})

		// The batch closes on whichever comes first: enough entries to plan
		// over, or enough distinct blobs reached into that a wider window
		// would buy no further coalescing. The byte figure is counted once per
		// blob and is deliberately an upper bound, because it decides only how
		// coarsely the walk is cut — what is resident at any moment is
		// restoreSpansInFlight spans, not a batch's worth.
		if b := p.Body; b != nil && !batchBlobs[b.Blob] {
			batchBlobs[b.Blob] = true
			batchSpanBytes += min(max(b.Total, b.Offset+b.Length), restoreSpanBytes())
		}
		if len(batch) < restoreBatchEntries && batchSpanBytes < restoreSpanWindowBytes {
			return nil
		}
		return flush()
	})
	if walkErr == nil {
		walkErr = flush()
	}
	if walkErr != nil {
		phase.Error()
		return nil, walkErr
	}

	phase.Done()
	if err := writer.Close(); err != nil {
		return nil, err
	}
	result.BytesWritten = writer.BytesWritten()
	return result, nil
}

// restoreFileConcurrency picks how many files to reconstruct at once.
//
// It is deliberately far below the store's own concurrency hint (128 for S3):
// each file in flight opens its own chunk window underneath it, so the two
// levels multiply. Memory is bounded by restoreMemoryBudget regardless, but
// dividing the budget across too many files would starve each one's window
// and lose the pipelining this exists to enable. A modest fan-out is enough
// to hide per-file round trips, which is the actual bottleneck for the many
// small-to-medium files that dominate a real snapshot.
func restoreFileConcurrency(s store.ObjectStore) int {
	return min(store.GetConcurrencyHint(s, 8), 16)
}

// restoreWriteConcurrency is how many files a v3 restore decodes and writes at
// once, once their bytes are in hand.
//
// It governs *only* files whose bytes came out of a span. A file that still
// has a request to make — a chunked one, or a body whose span read failed and
// falls back to its own ranged read — goes through restoreFileConcurrency
// instead, where the store's hint is the right input because a request is what
// it is about. Applying this width to those too is a real regression on a
// store that hints above 8, and the reason there are two pools rather than one
// constant.
//
// For the files it does govern, a store concurrency hint says nothing: they
// are a decrypt, a decompress, a hash and a write, and no request at all.
//
// It is a constant rather than a function of GOMAXPROCS, which is what this
// was written as first and what measurement rejected. Sweeping the width over
// one 3,000-file repository on a 14-core machine against a **local** store,
// median of five restores each:
//
//	 2      4      6      8     10     12     14     16
//	0.52s  0.44s  0.45s  0.45s  0.45s  0.46s  0.48s  0.49s
//
// The curve is flat from 4 to 10 and then climbs: the work saturates well
// below the core count — it is a file create, a write and a close, and those
// contend on directory metadata rather than on CPU — so a machine's cores are
// the wrong quantity to scale by, and scaling by them cost 11% on the machine
// that has the most of them. 8 sits in the middle of the flat region, and is
// also where a local restore already ran (GetConcurrencyHint's default), so
// nothing about the write side moves with this change.
//
// The sweep is local-only, and that is a real limit on what it proves: it says
// where filesystem contention sets in, not what a remote restore wants. It is
// allowed to be the whole answer here only because the remote question — how
// many requests to have open — is asked of the other pool.
const restoreWriteConcurrency = 8

func (rm *RestoreManager) prepareRestore(ctx context.Context, snapshotRef string, opts ...RestoreOption) (restorePlan, error) {
	var cfg restoreConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	snap, resolvedRef, err := rm.resolveSnapshot(ctx, snapshotRef)
	if err != nil {
		return restorePlan{}, err
	}

	// A v3 restore that is going to write needs only the directories up
	// front; the files stream past in the write walk. A dry run still
	// collects everything, because what it reports *is* the entry list.
	if rm.v3 && !cfg.dryRun {
		dirs, order, err := rm.collectDirectoriesFromLeaves(ctx, snap.Root)
		if err != nil {
			return restorePlan{}, err
		}
		return restorePlan{
			cfg:         cfg,
			sorted:      restoreOrder(dirs, order),
			byID:        dirs,
			snapshotRef: resolvedRef,
			root:        snap.Root,
			dirsOnly:    true,
		}, nil
	}

	byID, walkOrder, err := rm.collectMetadata(ctx, snap.Root)
	if err != nil {
		return restorePlan{}, err
	}

	sorted := restoreOrder(byID, walkOrder)
	if cfg.pathFilter != "" {
		sorted = filterByPath(sorted, byID, cfg.pathFilter)
	}

	return restorePlan{
		cfg:         cfg,
		sorted:      sorted,
		byID:        byID,
		snapshotRef: resolvedRef,
		root:        snap.Root,
	}, nil
}

func secureRestorePath(root, rel string) (string, error) {
	cleanRel := strings.TrimPrefix(path.Clean("/"+rel), "/")
	if cleanRel == "." || cleanRel == "" {
		return filepath.Clean(root), nil
	}
	joined := filepath.Join(root, filepath.FromSlash(cleanRel))
	rootClean := filepath.Clean(root)
	joinedClean := filepath.Clean(joined)
	if joinedClean != rootClean && !strings.HasPrefix(joinedClean, rootClean+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid restore path: %q", rel)
	}
	return joinedClean, nil
}

type zipRestoreWriter struct {
	cw *countingWriter
	zw *zip.Writer
}

func NewZipRestoreWriter(w io.Writer) RestoreWriter {
	cw := &countingWriter{w: w}
	return &zipRestoreWriter{cw: cw, zw: zip.NewWriter(cw)}
}

func (w *zipRestoreWriter) MkdirAll(path string, meta core.FileMeta) error {
	header := &zip.FileHeader{Name: path + "/", Method: zip.Store}
	if meta.Mtime > 0 {
		header.Modified = time.Unix(meta.Mtime, 0)
	}
	_, err := w.zw.CreateHeader(header)
	return err
}

func (w *zipRestoreWriter) WriteFile(path string, meta core.FileMeta, writeContent func(io.Writer) error) error {
	header := &zip.FileHeader{Name: path, Method: zip.Deflate}
	if meta.Mtime > 0 {
		header.Modified = time.Unix(meta.Mtime, 0)
	}
	fw, err := w.zw.CreateHeader(header)
	if err != nil {
		return err
	}
	return writeContent(fw)
}

func (w *zipRestoreWriter) BytesWritten() int64 { return w.cw.count }

func (w *zipRestoreWriter) Close() error {
	if err := w.zw.Close(); err != nil {
		return fmt.Errorf("finalize zip: %w", err)
	}
	return nil
}

type fsRestoreWriter struct {
	root string

	// mu guards every field below it. WriteFile runs concurrently (see
	// SupportsConcurrentWrites), so the counters, the warn-dedup set, and the
	// deferred-metadata lists are all shared mutable state. The lock is only
	// ever held around bookkeeping — never across the content write itself,
	// which is the whole point of restoring files in parallel.
	mu           sync.Mutex
	bytes        int64
	warn         func(string)
	warned       map[string]struct{}
	deferredDirs []deferredRestoreEntry
	deferredFlag []deferredRestoreEntry
}

type deferredRestoreEntry struct {
	path string
	meta core.FileMeta
}

type restoreWarningSetter interface {
	SetWarningFunc(func(string))
}

var errRestoreSkipped = fmt.Errorf("restore entry skipped")

func NewFSRestoreWriter(root string) (RestoreWriter, error) {
	return &fsRestoreWriter{root: root, warned: map[string]struct{}{}}, nil
}

// SupportsConcurrentWrites implements concurrentRestoreWriter. Two WriteFile
// calls for distinct paths touch disjoint files; the shared bookkeeping they
// do touch is guarded by w.mu.
func (w *fsRestoreWriter) SupportsConcurrentWrites() bool { return true }

func (w *fsRestoreWriter) SetWarningFunc(fn func(string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.warn = fn
}

func (w *fsRestoreWriter) warnf(format string, args ...interface{}) {
	w.mu.Lock()
	fn := w.warn
	w.mu.Unlock()
	if fn != nil {
		fn(fmt.Sprintf(format, args...))
	}
}

func (w *fsRestoreWriter) warnOncef(key, format string, args ...interface{}) {
	w.mu.Lock()
	if _, ok := w.warned[key]; ok {
		w.mu.Unlock()
		return
	}
	w.warned[key] = struct{}{}
	w.mu.Unlock()
	w.warnf(format, args...)
}

func (w *fsRestoreWriter) warnDedupf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	w.warnOncef(msg, "%s", msg)
}

func (w *fsRestoreWriter) MkdirAll(relPath string, meta core.FileMeta) error {
	fullPath, err := secureRestorePath(w.root, relPath)
	if err != nil {
		return err
	}
	if err := ensureNoSymlinkComponents(w.root, fullPath); err != nil {
		return err
	}
	st, err := os.Lstat(fullPath)
	if err == nil {
		if !st.IsDir() {
			w.warnf("skipped existing non-directory: %s", relPath)
			return errRestoreSkipped
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(fullPath, 0o755); err != nil {
		return err
	}
	w.mu.Lock()
	w.deferredDirs = append(w.deferredDirs, deferredRestoreEntry{path: fullPath, meta: meta})
	if meta.Flags != 0 {
		w.deferredFlag = append(w.deferredFlag, deferredRestoreEntry{path: fullPath, meta: meta})
	}
	w.mu.Unlock()
	return nil
}

func (w *fsRestoreWriter) WriteFile(relPath string, meta core.FileMeta, writeContent func(io.Writer) error) error {
	fullPath, err := secureRestorePath(w.root, relPath)
	if err != nil {
		return err
	}
	if err := ensureNoSymlinkComponents(w.root, fullPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return err
	}
	if st, err := os.Lstat(fullPath); err == nil {
		if st.IsDir() {
			w.warnf("skipped existing directory collision: %s", relPath)
			return errRestoreSkipped
		}
		w.warnf("skipped existing file: %s", relPath)
		return errRestoreSkipped
	} else if !os.IsNotExist(err) {
		return err
	}

	f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}

	cw := &countingWriter{w: f}
	writeErr := writeContent(cw)
	closeErr := f.Close()
	if writeErr != nil {
		// Never leave known-bad bytes behind under the user's own filename.
		// This covers a failed content-hash check as well as a stream that
		// died partway, which previously left a truncated file on disk.
		_ = os.Remove(fullPath)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(fullPath)
		return closeErr
	}

	w.mu.Lock()
	w.bytes += cw.count
	w.mu.Unlock()

	if err := applyRestoreFileMetadata(fullPath, meta, w.warnDedupf); err != nil {
		return err
	}
	if meta.Flags != 0 {
		w.mu.Lock()
		w.deferredFlag = append(w.deferredFlag, deferredRestoreEntry{path: fullPath, meta: meta})
		w.mu.Unlock()
	}
	return nil
}

func (w *fsRestoreWriter) BytesWritten() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytes
}

// Close runs after every WriteFile has returned, so it needs no lock of its
// own — but it must stay that way: the deferred lists are ordered, and
// applying directory metadata is what makes parent mtimes survive the writes
// underneath them.
func (w *fsRestoreWriter) Close() error {
	for i := len(w.deferredDirs) - 1; i >= 0; i-- {
		entry := w.deferredDirs[i]
		if err := applyRestoreDirMetadata(entry.path, entry.meta, w.warnDedupf); err != nil {
			return err
		}
	}
	for i := len(w.deferredFlag) - 1; i >= 0; i-- {
		entry := w.deferredFlag[i]
		if err := applyRestoreFlags(entry.path, entry.meta, w.warnDedupf); err != nil {
			return err
		}
	}
	return nil
}

func ensureNoSymlinkComponents(root, target string) error {
	rootClean := filepath.Clean(root)
	targetClean := filepath.Clean(target)

	if err := checkSymlinkPath(rootClean); err != nil {
		return err
	}

	if targetClean == rootClean {
		return nil
	}

	rel, err := filepath.Rel(rootClean, targetClean)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}

	cur := rootClean
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		if err := checkSymlinkPath(cur); err != nil {
			return err
		}
	}
	return nil
}

func checkSymlinkPath(p string) error {
	st, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to restore through symlink path: %s", p)
	}
	return nil
}

func (rm *RestoreManager) dryRunRestore(sorted []string, byID map[string]core.FileMeta, snapshotRef, root string) *RestoreResult {
	result := &RestoreResult{
		SnapshotRef: snapshotRef,
		Root:        root,
		DryRun:      true,
	}
	for _, id := range sorted {
		meta := byID[id]
		if meta.Type == core.FileTypeFolder {
			result.DirsWritten++
		} else if meta.ContentHash != "" {
			result.FilesWritten++
			result.BytesWritten += meta.Size
		}
	}
	return result
}

type countingWriter struct {
	w     io.Writer
	count int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.count += int64(n)
	return n, err
}

// writeFileContent reconstructs a v2 file from its content object. The v3 path
// is writeContentFromPayload, which takes the bytes from the leaf instead.
func (rm *RestoreManager) writeFileContent(ctx context.Context, w io.Writer, meta core.FileMeta, verify bool) error {
	contentKey := meta.ContentRef
	if contentKey == "" {
		contentKey = meta.ContentHash
	}

	content, err := rm.loadContent(ctx, contentKey)
	if err != nil {
		return err
	}

	// Hash the reconstructed stream as it is written. meta.ContentHash is a
	// SHA-256 over the whole plaintext for both storage paths — the chunked
	// path accumulates it in Chunker.ProcessStream, the inline path computes
	// it directly — so one digest of the output is directly comparable.
	// Nothing below this line authenticates the bytes otherwise: an object
	// replaced in the backing store is returned by the store stack without
	// complaint.
	hasher := sha256.New()
	out := w
	if verify {
		out = io.MultiWriter(w, hasher)
	}

	if err := rm.writeChunks(ctx, out, content.Chunks, avgChunkSize(content)); err != nil {
		return err
	}
	if len(content.DataInlineB64) > 0 {
		if _, err := out.Write(content.DataInlineB64); err != nil {
			return err
		}
	}

	if verify {
		got := hex.EncodeToString(hasher.Sum(nil))
		if got != meta.ContentHash {
			return fmt.Errorf("content hash mismatch: expected %s, got %s", meta.ContentHash, got)
		}
	}
	return nil
}

// writeContentFromPayload reconstructs a file from the payload its leaf
// carries (format v3): inline bytes are written as they are, chunked files
// stream through the same pipelined chunk fetcher as v2.
//
// The payload is passed in rather than looked up, which is what keeps the
// write phase at one read per leaf: see runWithWriterV3.
//
// sealed, when non-nil, is the member's own bytes already fetched — the batch
// coalesced this body's read with its neighbours' — and the blob path then
// costs no request. Nil means read the range now, which is both the
// uncoalesced path and the fallback for a span that could not be read.
func (rm *RestoreManager) writeContentFromPayload(ctx context.Context, out io.Writer, meta core.FileMeta, p *hamt.Payload, sealed []byte, verify bool) error {
	if p == nil {
		return fmt.Errorf("leaf entry for %s carries no content payload", meta.FileID)
	}

	hasher := sha256.New()
	dst := out
	if verify {
		dst = io.MultiWriter(out, hasher)
	}

	switch {
	case len(p.Chunks) > 0:
		avg := int64(0)
		if p.Size > 0 {
			avg = p.Size / int64(len(p.Chunks))
		}
		if err := rm.writeChunks(ctx, dst, p.Chunks, avg); err != nil {
			return err
		}
	case p.Body != nil:
		var (
			body []byte
			err  error
		)
		if sealed != nil {
			body, err = rm.blobs.member(sealed, p, meta.ContentHash)
		} else {
			body, err = rm.blobs.Body(ctx, p, meta.ContentHash)
		}
		if err != nil {
			return err
		}
		if _, err := dst.Write(body); err != nil {
			return err
		}
	}

	if verify {
		if got := hex.EncodeToString(hasher.Sum(nil)); got != meta.ContentHash {
			return fmt.Errorf("content hash mismatch: expected %s, got %s", meta.ContentHash, got)
		}
	}
	return nil
}

func buildRestorePath(meta core.FileMeta, byID map[string]core.FileMeta) string {
	return fileMetaPath(meta, func(parentID string) (core.FileMeta, bool) {
		parent, ok := byID[parentID]
		return parent, ok
	})
}

// filterByPath returns only the entries whose restore path matches the given filter.
// If the filter ends with "/", it matches all entries under that subtree.
// Otherwise it matches only the entry with the exact path.
// Ancestor directories of matched entries are always included.
// matchesRestorePath reports whether a restored path is inside the filter —
// the same rule filterByPath applies, expressed for one path at a time so a
// streaming restore can test entries as it meets them instead of building the
// matched set in advance.
func matchesRestorePath(rel, filter string) bool {
	if filter == "" {
		return true
	}
	prefix := strings.TrimSuffix(filter, "/")
	return rel == prefix || strings.HasPrefix(rel, prefix+"/")
}

// restoreNeedsDir reports whether a directory has to be created for a filtered
// restore: either it is inside the filter, or it is an ancestor of it and the
// files below could not otherwise be written.
func restoreNeedsDir(rel, filter string) bool {
	if matchesRestorePath(rel, filter) {
		return true
	}
	return strings.HasPrefix(strings.TrimSuffix(filter, "/"), rel+"/")
}

func filterByPath(sorted []string, byID map[string]core.FileMeta, pathFilter string) []string {
	isSubtree := strings.HasSuffix(pathFilter, "/")
	prefix := pathFilter
	if isSubtree {
		prefix = strings.TrimSuffix(pathFilter, "/")
	}

	// Determine which entries are matched. The path is built once per entry and
	// tested immediately rather than being kept in a map for a second pass: the
	// only thing the second pass needed was the path, and nothing after this
	// reads it.
	matched := make(map[string]bool)
	for _, id := range sorted {
		p := buildRestorePath(byID[id], byID)
		if isSubtree {
			// Match the directory itself and anything under it.
			if p == prefix || strings.HasPrefix(p, prefix+"/") {
				matched[id] = true
			}
		} else {
			// Exact match, or — when the target is a folder — include
			// everything under it so the user doesn't need a trailing "/".
			if p == pathFilter || strings.HasPrefix(p, pathFilter+"/") {
				matched[id] = true
			}
		}
	}

	// Include all ancestor directories of matched entries by walking
	// up the full parent chain (not just immediate parents).
	var walkAncestors func(id string)
	walkAncestors = func(id string) {
		meta, ok := byID[id]
		if !ok {
			return
		}
		for _, parentID := range meta.Parents {
			if matched[parentID] {
				continue
			}
			if _, ok := byID[parentID]; ok {
				matched[parentID] = true
				walkAncestors(parentID)
			}
		}
	}
	for _, id := range sorted {
		if matched[id] {
			walkAncestors(id)
		}
	}

	var filtered []string
	for _, id := range sorted {
		if matched[id] {
			filtered = append(filtered, id)
		}
	}
	return filtered
}

// ---------------------------------------------------------------------------
// Snapshot resolution
// ---------------------------------------------------------------------------

func (rm *RestoreManager) resolveSnapshot(ctx context.Context, ref string) (*core.Snapshot, string, error) {
	ref, err := resolveSnapshotRef(ctx, rm.store, ref)
	if err != nil {
		return nil, "", err
	}

	snap, err := loadSnapshotByRef(ctx, rm.store, ref)
	if err != nil {
		return nil, "", err
	}
	return snap, ref, nil
}

// ---------------------------------------------------------------------------
// Metadata collection
// ---------------------------------------------------------------------------

// collectMetadata loads every entry in the snapshot, and reports the order the
// tree walk yielded them in as well as the entries themselves.
//
// The order matters to what comes after, and the two orders are different.
// Restore *writes* in walk order, because that is the order backup laid entries
// out and writing in any other scatters reads across every pack — measured at
// 55% pack-cache misses against 0.6% (RFC 0023). Restore *fetches* in whatever
// order the store says is cheapest, which on a repository built by several
// backups is not walk order at all: each backup's entries sit in its own packs,
// so the walk interleaves them. See the grouping below.
// collectMetadataFromLeaves is collectMetadata for a v3 repository: every
// entry's metadata arrives in its leaf, so one walk yields the whole plan with
// no per-entry reads, no grouping, and nothing to fetch concurrently. Payloads
// are not retained — the metadata is decoded and the leaf released, which is
// what keeps the plan's memory proportional to metadata rather than to the
// snapshot's inline content.
// collectDirectoriesFromLeaves indexes the snapshot's directories, and only
// those, so a streaming restore can resolve a path without holding the tree.
//
// It exists because of what affinity routing puts where. A routing key is the
// parent's hash prefix followed by the entry's own (see AffinityKey), so every
// entry of one directory shares the leading routing bits and lands in the same
// leaf: a leaf walk is a directory-at-a-time walk. What it does *not* give is
// a parent before its children — a directory is filed under its own parent —
// so paths still need an index, and this is the smallest one that works.
//
// Skipping the files is what makes it cheap, and it is done without decoding
// them: a directory carries no content, so an entry whose payload has inline
// bytes or chunk refs cannot be one. That test is a pointer comparison, so the
// pass reads the leaves and decodes a few per cent of what they hold.
func (rm *RestoreManager) collectDirectoriesFromLeaves(ctx context.Context, root string) (map[string]core.FileMeta, []string, error) {
	phase := rm.reporter.StartPhase("Reading directories", 0, false)

	dirs := make(map[string]core.FileMeta)
	var order []string
	err := rm.tree.WalkEntries(ctx, root, func(_, ref string, p *hamt.Payload) error {
		if p == nil {
			return fmt.Errorf("v3 leaf entry %s carries no metadata payload", ref)
		}
		if p.Body != nil || len(p.Chunks) > 0 {
			return nil // has content, so not a directory
		}
		fm, err := decodePayloadMeta(ref, p)
		if err != nil {
			return err
		}
		if fm.Type != core.FileTypeFolder {
			return nil // an empty file, not a directory
		}
		dirs[fm.FileID] = *fm
		order = append(order, fm.FileID)
		phase.Increment(1)
		return nil
	})
	if err != nil {
		phase.Error()
		return nil, nil, err
	}
	phase.Done()
	return dirs, order, nil
}

// metaDecodeBatch is how many entries the v3 metadata pass buffers before
// decoding them in parallel.
//
// The walk itself is sequential — it is one pass over the leaves, which is the
// point — but decoding is per-entry CPU work with no ordering requirement, and
// a snapshot has as many of them as it has files. Buffering references rather
// than results is what keeps this bounded: an entry in the buffer is a pointer
// into a leaf that is live anyway, so the batch costs a few slice headers each
// and not a copy of the snapshot's inline content.
const metaDecodeBatch = 2048

func (rm *RestoreManager) collectMetadataFromLeaves(ctx context.Context, root string) (map[string]core.FileMeta, []string, error) {
	phase := rm.reporter.StartPhase("Loading metadata", 0, false)

	type pending struct {
		ref     string
		payload *hamt.Payload
	}

	byID := make(map[string]core.FileMeta)
	var walkOrder []string
	var mu sync.Mutex

	batch := make([]pending, 0, metaDecodeBatch)
	workers := min(runtime.GOMAXPROCS(0), 8)

	// flush decodes one batch across workers and appends the results in the
	// order the walk produced them, which is the order restore writes in.
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		decoded := make([]core.FileMeta, len(batch))
		g, gCtx := errgroup.WithContext(ctx)
		g.SetLimit(workers)
		chunk := (len(batch) + workers - 1) / workers
		for start := 0; start < len(batch); start += chunk {
			end := min(start+chunk, len(batch))
			g.Go(func() error {
				for i := start; i < end; i++ {
					if err := gCtx.Err(); err != nil {
						return err
					}
					fm, err := decodePayloadMeta(batch[i].ref, batch[i].payload)
					if err != nil {
						return err
					}
					decoded[i] = *fm
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return err
		}

		mu.Lock()
		for i := range decoded {
			byID[decoded[i].FileID] = decoded[i]
			walkOrder = append(walkOrder, decoded[i].FileID)
		}
		mu.Unlock()
		phase.Increment(int64(len(batch)))
		batch = batch[:0]
		return nil
	}

	err := rm.tree.WalkEntries(ctx, root, func(_, ref string, p *hamt.Payload) error {
		if p == nil {
			return fmt.Errorf("v3 leaf entry %s carries no metadata payload", ref)
		}
		batch = append(batch, pending{ref: ref, payload: p})
		if len(batch) < metaDecodeBatch {
			return nil
		}
		return flush()
	})
	if err == nil {
		err = flush()
	}
	if err != nil {
		phase.Error()
		return nil, nil, err
	}
	phase.Done()
	return byID, walkOrder, nil
}

func (rm *RestoreManager) collectMetadata(ctx context.Context, root string) (map[string]core.FileMeta, []string, error) {
	if rm.v3 {
		return rm.collectMetadataFromLeaves(ctx, root)
	}

	var refs []string
	err := rm.tree.Walk(ctx, root, func(_, valueRef string) error {
		refs = append(refs, valueRef)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	phase := rm.reporter.StartPhase("Loading metadata", int64(len(refs)), false)

	// Fetch concurrently (mirroring fetchSnapshots' pattern): each ref is a
	// small, independent JSON object with no ordering requirement among
	// them, unlike a file's content chunks, so results can land in the map
	// as soon as they arrive rather than needing to be sequenced.
	//
	// Walk order is kept separately, by writing each result to its own index
	// rather than appending as it lands: concurrency decides when a result
	// arrives, and that must not decide the order the restore writes in.
	byID := make(map[string]core.FileMeta, len(refs))
	walkOrder := make([]string, len(refs))
	var mu sync.Mutex

	// Fetch in the groups the store says are cheapest, which is not the order
	// the walk produced. Walk order matches the layout of whichever backup wrote
	// each entry, so on a repository built by several backups it interleaves
	// packs — and a pack read in pieces spread across the fetch is transferred,
	// evicted and transferred again. Handing the whole set over first lets
	// PackStore group it, because it is the layer that knows where objects live
	// and this is the one moment the full set is known in advance.
	//
	// **The unit of concurrency is the group, not the ref**, and that is what
	// makes grouping worth anything. Spreading every ref across the errgroup
	// interleaves the packs again as soon as more than one worker runs, which is
	// the arrangement grouping exists to prevent; one worker per group reads a
	// pack's objects consecutively while other workers read other packs. The
	// concurrency hint therefore bounds how many pack bodies are live at once.
	//
	// walkOrder is unaffected: it is filled by index below, so the order results
	// are *fetched* in is independent of the order restore *writes* in, which
	// must stay walk order (RFC 0023 §5).
	plan := store.PlanReads(ctx, rm.store, refs)
	groups := indexGroups(plan.Groups, refs)

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(plan.Concurrency)
	for _, group := range groups {
		g.Go(func() error {
			for _, f := range group {
				fm, err := rm.metas.load(gCtx, f.ref)
				if err != nil {
					return err
				}
				mu.Lock()
				byID[fm.FileID] = *fm
				walkOrder[f.index] = fm.FileID
				mu.Unlock()
				phase.Increment(1)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		phase.Error()
		return nil, nil, err
	}
	phase.Done()
	return byID, walkOrder, nil
}

// ---------------------------------------------------------------------------
// Ordering
// ---------------------------------------------------------------------------

// restoreOrder returns FileIDs in the order a restore should write them.
//
// The constraint is that an entry's parents exist before it does, so a directory
// is created before anything it contains. That constraint binds only the entries
// that *are* parents — everything else is a leaf, and a leaf may be written at
// any point after the interior of the tree exists.
//
// So the interior is emitted first, in parent-before-child order, and the leaves
// follow in walkOrder. Leaves are the overwhelming majority (50,000 of 50,500 in
// the tree RFC 0023 measured) and walk order is the order backup wrote them,
// which is the order they are laid out in packfiles. Ordering the whole tree
// topologically instead scattered reads across every pack: 55% pack-cache misses
// against 0.6% for the metadata phase, which already reads in walk order.
//
// Interior membership is derived from the data rather than from Type. A regular
// file is a leaf in every source model this supports, but a snapshot is data
// read off a store, and an entry that claims a file as its parent must still be
// ordered after it rather than trusted not to exist.
func restoreOrder(byID map[string]core.FileMeta, walkOrder []string) []string {
	interior := make(map[string]bool)
	for _, meta := range byID {
		for _, parentID := range meta.Parents {
			if _, ok := byID[parentID]; ok {
				interior[parentID] = true
			}
		}
	}

	out := make([]string, 0, len(byID))
	emitted := make(map[string]bool, len(byID))

	var visit func(core.FileMeta)
	visit = func(meta core.FileMeta) {
		if emitted[meta.FileID] {
			return
		}
		// Marked before recursing, not after: a parent cycle would otherwise
		// revisit this entry forever. Marking first makes a cycle terminate with
		// the entry emitted once, in an order the cycle itself does not define.
		emitted[meta.FileID] = true
		for _, parentID := range meta.Parents {
			if parent, ok := byID[parentID]; ok {
				visit(parent)
			}
		}
		out = append(out, meta.FileID)
	}

	// Interior nodes in walk order too, so that sibling directories are created
	// in the order they were backed up rather than in map-iteration order. visit
	// pulls in any ancestor that has not been reached yet.
	for _, id := range walkOrder {
		if interior[id] {
			visit(byID[id])
		}
	}
	// A cycle among interior nodes can leave one unreached from walkOrder if the
	// snapshot is malformed; nothing may be dropped.
	for id := range interior {
		if !emitted[id] {
			visit(byID[id])
		}
	}

	for _, id := range walkOrder {
		if emitted[id] {
			continue
		}
		emitted[id] = true
		out = append(out, id)
	}
	// walkOrder comes from the same walk that built byID, so this adds nothing in
	// practice. It is here because dropping an entry silently loses a file.
	for id := range byID {
		if !emitted[id] {
			emitted[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Content loading
// ---------------------------------------------------------------------------

func (rm *RestoreManager) loadContent(ctx context.Context, hash string) (*core.Content, error) {
	data, err := rm.store.Get(ctx, "content/"+hash)
	if err != nil {
		return nil, err
	}
	var c core.Content
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// avgChunkSize estimates a Content's per-chunk payload so writeChunks can
// charge the memory budget without having to fetch a chunk to find out how
// big one is.
func avgChunkSize(c *core.Content) int64 {
	if c == nil || len(c.Chunks) == 0 || c.Size <= 0 {
		return 0
	}
	return c.Size / int64(len(c.Chunks))
}

func (rm *RestoreManager) writeChunk(ctx context.Context, w io.Writer, ref string) error {
	data, err := rm.store.Get(ctx, ref)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// writeChunks fetches a file's content chunks and writes them to w in order.
//
// The store's Get is the bottleneck for a large file, so fetches run
// concurrently. They run as a *sliding window* rather than as discrete
// batches: a fixed number of fetches stay in flight at all times, and each
// chunk is written the moment the chunk before it has been written. A batched
// version — fetch N, wait for all N, write all N — leaves the network
// completely idle for the whole write phase of every batch and stalls the
// entire batch behind its single slowest Get, which on a high-latency backend
// such as S3 roughly halves throughput.
//
// Chunks still land on w in their original order regardless of which fetch
// finishes first, since the reconstructed stream must be byte-identical to
// what was chunked at backup time.
//
// avgChunkSize is the caller's estimate of the per-chunk payload, used to
// charge the shared memory budget (see RestoreManager.memBudget). Fetches
// block once the budget is exhausted, which is what bounds resident memory
// now that the window is no longer a hard chunk count.
func (rm *RestoreManager) writeChunks(ctx context.Context, w io.Writer, refs []string, avgChunkSize int64) error {
	if len(refs) <= 1 {
		for _, ref := range refs {
			if err := rm.writeChunk(ctx, w, ref); err != nil {
				return err
			}
		}
		return nil
	}

	window := min(store.GetConcurrencyHint(rm.store, 10), len(refs))
	weight := chunkWeight(avgChunkSize)

	type slot struct {
		data  []byte
		err   error
		ready chan struct{}
	}
	slots := make([]slot, window)
	for i := range slots {
		slots[i] = slot{ready: make(chan struct{})}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// fetch runs refs[i] into the slot it will be consumed from. The slot is
	// reused every `window` chunks, so it is re-armed by the consumer before
	// the next fetch targeting it is started — never concurrently with it.
	fetch := func(i int) {
		s := &slots[i%window]
		if err := rm.memBudget.Acquire(ctx, weight); err != nil {
			s.err = err
			close(s.ready)
			return
		}
		d, err := rm.store.Get(ctx, refs[i])
		if err != nil {
			rm.memBudget.Release(weight)
			s.err = fmt.Errorf("fetch chunk %s: %w", refs[i], err)
			close(s.ready)
			return
		}
		s.data = d
		close(s.ready)
	}

	var wg sync.WaitGroup
	start := func(i int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fetch(i)
		}()
	}

	for i := 0; i < window; i++ {
		start(i)
	}

	// Draining every outstanding fetch before returning keeps the memory
	// budget balanced on the error path: an abandoned fetch that later
	// completes would otherwise hold its reservation forever.
	drain := func() {
		cancel()
		wg.Wait()
		for i := range slots {
			select {
			case <-slots[i].ready:
				if slots[i].err == nil && slots[i].data != nil {
					rm.memBudget.Release(weight)
				}
			default:
			}
		}
	}

	for i := 0; i < len(refs); i++ {
		s := &slots[i%window]
		<-s.ready
		if s.err != nil {
			err := s.err
			s.data, s.err = nil, nil // claimed; drain must not double-release
			drain()
			return err
		}

		data := s.data
		_, werr := w.Write(data)
		s.data = nil
		rm.memBudget.Release(weight)
		if werr != nil {
			drain()
			return werr
		}

		// Re-arm this slot and pull the next chunk that will land in it.
		if next := i + window; next < len(refs) {
			s.ready = make(chan struct{})
			start(next)
		}
	}

	wg.Wait()
	return nil
}

// chunkWeight turns a per-chunk size estimate into the amount charged against
// the restore memory budget. The estimate comes from a Content object's own
// size and chunk count, so it is close for the common case; the clamp keeps a
// bogus or missing estimate from either serialising the window (too large) or
// letting it grow without bound (too small).
func chunkWeight(avgChunkSize int64) int64 {
	const minWeight = 64 * 1024
	if avgChunkSize <= 0 {
		return cdcMaxSize
	}
	return max(minWeight, min(avgChunkSize, cdcMaxSize))
}

// metaFetch pairs a ref with the slot its result belongs in. The slot is
// carried because fetch order and walk order are deliberately different: a ref
// fetched third may be the first entry the restore writes.
type metaFetch struct {
	ref   string
	index int
}

// indexGroups pairs each ref in a plan's groups with its original index.
//
// A ref can appear more than once when two entries share a filemeta object, so
// occurrences are handed out in order rather than looked up: the plan names the
// ref once per occurrence, and each occurrence needs its own slot.
func indexGroups(groups [][]string, refs []string) [][]metaFetch {
	byRef := make(map[string][]int, len(refs))
	for i, ref := range refs {
		byRef[ref] = append(byRef[ref], i)
	}
	out := make([][]metaFetch, 0, len(groups))
	for _, group := range groups {
		fetches := make([]metaFetch, 0, len(group))
		for _, ref := range group {
			idx := byRef[ref]
			if len(idx) == 0 {
				continue // a key the store invented; not possible for a partition
			}
			fetches = append(fetches, metaFetch{ref: ref, index: idx[0]})
			byRef[ref] = idx[1:]
		}
		if len(fetches) > 0 {
			out = append(out, fetches)
		}
	}
	return out
}
