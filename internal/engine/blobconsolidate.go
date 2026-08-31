package engine

import (
	"cmp"
	"context"
	"os"
	"slices"
	"strconv"

	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
)

// Consolidating sparse blobs forward.
//
// A blob stays live while any one of its bodies is still referenced, so a
// repository accumulates blobs that are mostly garbage — the defect packfiles
// had, and the one format v3 exists to escape. prune cannot repair it, and
// deliberately does not try: rewriting a blob an old snapshot references
// either breaks that snapshot or needs the indirection layer this format
// exists to avoid.
//
// So the *next backup* consolidates forward, which is History-Aware Rewriting
// (Fu et al., USENIX ATC '14). A backup is already writing, it already holds
// the live-byte count of every blob it walked over, and a body it is passing
// over costs one copy to rewrite and no correctness: the entry keeps its value
// — the content address of its metadata — and only the body reference inside
// the leaf moves. Old snapshots keep naming the blobs they always did, so they
// keep restoring byte for byte; prune collects those blobs once no retained
// snapshot needs them, which is the point at which the waste is actually
// reclaimed.
//
// HAR's measured finding is what makes a bounded budget work: a sparse
// container stays sparse. The set of blobs worth rewriting does not regenerate
// faster than a bounded budget retires it, so a per-backup cap converges
// rather than chases.
//
// # What makes a blob worth rewriting
//
// Two different blobs cost a restore the same misplaced request, and the naive
// measure only catches one of them:
//
//   - a blob most of whose bodies have been superseded — low utilization, the
//     case utilization was invented for;
//   - a blob that was *sealed small*. Every incremental backup seals one for
//     its own churn, and it is 100% utilized the day it is written. A
//     blob-size distribution over a 20,000-file tree measured a median of
//     2,076 KB and a minimum of 36 KB, and those minima are what make restore
//     requests grow with the number of retained backups rather than with the
//     data restored.
//
// One quantity covers both: the *live bytes* a blob delivers, against what a
// full blob delivers in this repository. A blob earns its request by handing
// back a blob's worth of wanted bytes; whether it fails to because half its
// members are dead or because it only ever held a tenth of a blob makes no
// difference to the reader. For a full-size blob the test reduces exactly to
// utilization, which is why utilization is the secondary filter here and not
// the trigger.
//
// "What a full blob delivers" is measured rather than assumed: it is the
// largest blob total this snapshot references. It cannot be computed from
// blobBudget, which counts plaintext, while everything here is stored bytes —
// the two differ by the compression ratio of whatever happened to be in the
// blob, and dividing one by the other would read compression as waste (see
// hamt.BodyRef.Total).

// consolidateFillPercent is the share of a full blob's live bytes below which
// a blob is worth rewriting.
//
// 50% is the starting point rather than a measured optimum, and it has a
// convergence argument behind it: no two blobs under half a blob's worth may
// both survive, since merging them yields at most one full blob. The steady
// state is therefore "as many full blobs as the data needs, plus at most one
// partial", which is what takes the blob count off the backup-count axis.
//
// Raising it consolidates more aggressively and writes more; lowering it
// leaves more small blobs standing. Sweep it with CLOUDSTIC_TEST_BLOB_FILL.
const consolidateFillPercent = 50

// envConsolidateFill overrides consolidateFillPercent, for sweeps and tests
// only. It joins the CLOUDSTIC_TEST_* family described in AGENTS.md.
const envConsolidateFill = "CLOUDSTIC_TEST_BLOB_FILL"

// consolidateRewriteBytes bounds the blob bytes one backup rewrites.
//
// A budget rather than a clock, because docs/compatibility.md's standing rule
// is that work bounded only by maintenance is unbounded: a time limit makes
// what a backup writes a property of the machine it ran on, and a slow store
// would consolidate nothing forever.
//
// It is counted in *stored* bytes read out of the blobs being retired, which
// is within a compression pass of the extra bytes the backup writes — so the
// budget is directly the write amplification a user is agreeing to. 8 MB is
// about four full blobs at the measured ratio; a backup that has less than
// that worth of sparse blobs simply rewrites what there is.
const consolidateRewriteBytes = 8 << 20

// envConsolidateRewrite overrides consolidateRewriteBytes, for sweeps and
// tests only.
const envConsolidateRewrite = "CLOUDSTIC_TEST_BLOB_REWRITE_BYTES"

// consolidateTrackBytes bounds what the accumulator holds.
//
// Deciding which blobs are sparse needs one pass over the tree, and rewriting
// them needs to find the entries again afterwards — so the walk retains a
// routing key and a file ID per entry whose body sits in a blob it inherited.
// That is proportional to the repository, which nothing in a v3 backup is
// allowed to be, hence a cap.
//
// Reaching it costs coverage, never correctness: the blobs whose entries were
// evicted are marked as partially tracked and are then never selected, because
// rewriting *some* of a blob's bodies writes bytes without retiring the blob.
// A backup that cannot hold the whole repository consolidates the part it
// could hold, and the next one starts again from a different set.
const consolidateTrackBytes = 32 << 20

// envConsolidateTrack overrides consolidateTrackBytes, for tests only.
const envConsolidateTrack = "CLOUDSTIC_TEST_BLOB_TRACK_BYTES"

func consolidateFill() int64       { return envInt64(envConsolidateFill, consolidateFillPercent) }
func consolidateBudget() int64     { return envInt64(envConsolidateRewrite, consolidateRewriteBytes) }
func consolidateTrackLimit() int64 { return envInt64(envConsolidateTrack, consolidateTrackBytes) }

// envInt64 reads a positive CLOUDSTIC_TEST_* integer override, falling back to
// def for anything absent, unparseable or non-positive.
func envInt64(name string, def int64) int64 {
	if v, ok := os.LookupEnv(name); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// blobEntry names one leaf entry whose body could be moved: where it sits in
// the tree, and where its body sits today.
//
// It holds the entry's routing key rather than its payload on purpose. A
// payload carries the entry's whole metadata, and retaining one per candidate
// would hold the tree's metadata twice over — and would hold it past the
// intermediate commits that exist to release it. The payload is looked up
// again when the entry is actually rewritten, which is a descent of a leaf the
// rewrite is about to write to anyway.
type blobEntry struct {
	routingKey string
	fileID     string
	// body is the reference the entry carried when it was walked. It is the
	// tree's own *BodyRef, not a copy: BodyRef is a separately allocated
	// struct, so holding it retains nothing else.
	body *hamt.BodyRef
}

// approxSize is what one candidate costs the accumulator, for the byte cap.
func (e blobEntry) approxSize() int64 {
	return int64(len(e.routingKey)+len(e.fileID)) + 96
}

// blobUsage is what the snapshot being written still needs from one blob it
// inherited.
type blobUsage struct {
	// live is the stored bytes of that blob this snapshot still reaches. Its
	// denominator is not kept here: what decides whether a blob is worth
	// retiring is what a *full* blob delivers in this repository, which is a
	// property of the set rather than of one member (see plan).
	live int64

	// members are the offsets already counted towards live. A blob stores one
	// body per content hash, so two entries with identical content share a
	// member; counting it twice would report a half-empty blob as full. Freed
	// once the blob stops being a candidate, after which live over-counts,
	// which biases the trigger below towards doing nothing.
	members map[int64]struct{}

	// refs are the entries to rewrite if this blob is selected.
	refs []blobEntry
	held int64

	// partial marks a blob some of whose entries were dropped by the byte cap.
	// Such a blob can never be selected: rewriting part of one writes bytes
	// without retiring the blob, which is strictly worse than leaving it.
	partial bool
}

// blobConsolidator accumulates, over one backup's walk, what the snapshot
// being written still needs from each blob it inherited, and then decides
// which of those blobs are worth rewriting.
//
// It is only fed by a **full** scan. A change-feed scan visits only what
// changed, so every blob would appear to hold a handful of live bytes and the
// whole repository would look like garbage — the one input that could turn
// this from an optimization into a rewrite of everything.
type blobConsolidator struct {
	blobs map[string]*blobUsage
	// order is the order blobs were first seen, which makes a plan
	// deterministic where two blobs are equally sparse.
	order []string

	held  int64
	limit int64

	// maxTotal is the largest BodyRef.Total seen, from which plan derives what
	// a full blob delivers in this repository.
	maxTotal int64
}

// full is the live bytes a full blob delivers here: the largest one this
// snapshot references, capped at what a blob can physically be.
//
// The cap matters because Total is a value read off a store rather than
// computed: one entry claiming an absurd total would otherwise make every blob
// in the repository look sparse, and every backup would spend its whole budget
// rewriting blobs that were already full. Two budgets' worth is the same slack
// restoreSpanBytes allows for the same reason — a blob seals at one budget of
// plaintext, and incompressible content plus per-member overhead is the only
// way its stored form exceeds that.
func (c *blobConsolidator) full() int64 { return min(c.maxTotal, 2*blobLimit()) }

func newBlobConsolidator() *blobConsolidator {
	return &blobConsolidator{blobs: make(map[string]*blobUsage), limit: consolidateTrackLimit()}
}

// note records that the snapshot being written still reaches body, through the
// entry at (routingKey, fileID).
func (c *blobConsolidator) note(routingKey, fileID string, body *hamt.BodyRef) {
	if c == nil || body == nil {
		return
	}
	// A reference whose extent is not usable is not reasoned about: Total and
	// Length come off a store, and a blob whose size is unknown has no
	// denominator. Skipped rather than rejected — the entry is left pointing
	// where it points, which is the safe outcome for anything this pass
	// cannot understand.
	if body.Total <= 0 || body.Length <= 0 || body.Offset < 0 {
		return
	}

	u := c.blobs[body.Blob]
	if u == nil {
		u = &blobUsage{members: make(map[int64]struct{})}
		c.blobs[body.Blob] = u
		c.order = append(c.order, body.Blob)
	}
	if body.Total > c.maxTotal {
		c.maxTotal = body.Total
	}
	if u.members == nil {
		u.live += body.Length
	} else if _, dup := u.members[body.Offset]; !dup {
		u.members[body.Offset] = struct{}{}
		u.live += body.Length
	}

	if u.partial {
		return
	}
	e := blobEntry{routingKey: routingKey, fileID: fileID, body: body}
	u.refs = append(u.refs, e)
	u.held += e.approxSize()
	c.held += e.approxSize()
	if c.held >= c.limit {
		c.evict()
	}
}

// evict frees candidate lists until the accumulator is back under its cap.
//
// Largest first, because a blob holding many live entries is the least likely
// to be sparse and is therefore the cheapest coverage to give up. Evicting
// down to three quarters rather than to exactly the cap is what keeps this off
// the per-entry path: the next eviction is a quarter of the budget away.
func (c *blobConsolidator) evict() {
	tracked := make([]string, 0, len(c.blobs))
	for _, ref := range c.order {
		if len(c.blobs[ref].refs) > 0 {
			tracked = append(tracked, ref)
		}
	}
	slices.SortFunc(tracked, func(a, b string) int {
		if d := cmp.Compare(c.blobs[b].held, c.blobs[a].held); d != 0 {
			return d
		}
		return cmp.Compare(a, b)
	})
	for _, ref := range tracked {
		if c.held <= c.limit*3/4 {
			return
		}
		u := c.blobs[ref]
		c.held -= u.held
		u.refs, u.held, u.partial = nil, 0, true
		// live stops being deduplicated once the member set goes; see the
		// field comment for why over-counting is the safe direction.
		u.members = nil
	}
}

// blobRewritePlan is the work one backup's consolidation will do: which blobs
// are being retired, and every entry that has to be repointed for them to be.
type blobRewritePlan struct {
	blobs   []string
	entries []blobEntry
	// bytes is the stored blob content the plan will read and rewrite, which
	// is what the budget bounds.
	bytes int64
}

// plan decides which blobs this backup consolidates.
//
// The trigger is how many distinct blobs the new snapshot must read against
// how few it could: a repository whose blobs are as packed as its data allows
// is left alone however its entries are distributed. Utilization enters second,
// as the filter that picks which blobs to retire, and the budget third, as the
// bound on how many of them one backup takes on.
func (c *blobConsolidator) plan() blobRewritePlan {
	var empty blobRewritePlan
	if c == nil || len(c.blobs) < 2 {
		return empty
	}
	full := c.full()
	if full <= 0 {
		return empty
	}

	var live int64
	for _, u := range c.blobs {
		live += u.live
	}
	// ideal is the blob count this snapshot's live bytes would occupy if every
	// blob were full. Nothing to gain when the repository is already there.
	ideal := (live + full - 1) / full
	if int64(len(c.blobs)) <= ideal {
		return empty
	}

	mark := full * consolidateFill() / 100
	candidates := make([]string, 0, len(c.blobs))
	for _, ref := range c.order {
		u := c.blobs[ref]
		if u.partial || len(u.refs) == 0 || u.live >= mark {
			continue
		}
		candidates = append(candidates, ref)
	}
	// Emptiest first: that is the most requests retired per byte rewritten,
	// and it is what makes a budget too small for the whole set still spend
	// itself well.
	slices.SortStableFunc(candidates, func(a, b string) int {
		return cmp.Compare(c.blobs[a].live, c.blobs[b].live)
	})

	budget := consolidateBudget()
	plan := blobRewritePlan{}
	for _, ref := range candidates {
		u := c.blobs[ref]
		if plan.bytes+u.live > budget && len(plan.blobs) > 0 {
			break
		}
		plan.blobs = append(plan.blobs, ref)
		plan.entries = append(plan.entries, u.refs...)
		plan.bytes += u.live
	}

	// Fewer than two blobs is not a consolidation. Rewriting a single blob's
	// bodies into a single new blob writes bytes and leaves the snapshot
	// reading exactly as many objects as before; only a merge retires one.
	if len(plan.blobs) < 2 {
		return empty
	}

	// Entries in routing-key order, so the inserts that follow walk the leaves
	// once rather than descending to a different one every time — the same
	// reason upload sorts its insert batches.
	slices.SortFunc(plan.entries, func(a, b blobEntry) int {
		if d := cmp.Compare(a.routingKey, b.routingKey); d != 0 {
			return d
		}
		return cmp.Compare(a.fileID, b.fileID)
	})
	return plan
}

// blobRewrite is one entry waiting to be repointed at the body's new home.
type blobRewrite struct {
	entry   blobEntry
	value   string
	payload *hamt.Payload
	promise *bodyPromise
}

// consolidate rewrites the live bodies of this backup's sparsest blobs into
// the blobs it is writing, and repoints their entries.
//
// It runs after upload, so a consolidated body joins whatever blob the writer
// opens next rather than displacing the backup's own content, and before the
// tree is committed, so the repointed entries are part of the snapshot being
// written.
//
// A blob that cannot be read does not fail the backup. The data is already
// safely stored — that is what makes a body cheap to move — so such a blob is
// left exactly where it is, with its entries still pointing at it, and the
// failure is reported by check rather than by a housekeeping pass inside a
// backup that has otherwise succeeded. What is returned is the other kind:
// a store that will not take a write, or a tree that will not read, neither of
// which this run could have survived anyway.
func (bm *BackupManager) consolidate(ctx context.Context) error {
	if bm.consolidation == nil || bm.blobs == nil || bm.bodies == nil {
		return nil
	}
	plan := bm.consolidation.plan()
	if len(plan.entries) == 0 {
		return nil
	}

	phase := bm.reporter.StartPhase("Consolidating", int64(len(plan.entries)), false)
	rewrites, err := bm.rewriteBodies(ctx, plan, phase)
	if err != nil {
		phase.Error()
		return err
	}
	// Seals whatever the rewrites are still sitting in: an entry whose promise
	// is unresolved has no reference to encode.
	if err := bm.blobs.Flush(ctx); err != nil {
		phase.Error()
		return err
	}
	moved, err := bm.applyRewrites(ctx, rewrites)
	if err != nil {
		phase.Error()
		return err
	}
	phase.Done()
	bm.log.Debugf("consolidated %d blobs: %d of %d entries moved, %d bytes rewritten",
		len(plan.blobs), moved, len(plan.entries), plan.bytes)
	return nil
}

// rewriteBodies reads every body the plan names and hands it to the blob
// writer, returning the entries that now need repointing.
func (bm *BackupManager) rewriteBodies(ctx context.Context, plan blobRewritePlan, phase ui.Phase) ([]blobRewrite, error) {
	// Resolve the entries against the working tree first. The plan holds the
	// reference each entry carried when it was walked; what it must be
	// rewritten from is what it carries now, and an entry re-uploaded since
	// then is no longer this pass's business.
	targets := make([]blobRewrite, 0, len(plan.entries))
	reads := make([]blobRead, 0, len(plan.entries))
	hashes := make([]string, 0, len(plan.entries))
	for _, e := range plan.entries {
		value, p, err := bm.txn.LookupEntry(ctx, e.routingKey, e.fileID)
		if err != nil {
			return nil, err
		}
		if value == "" || p == nil || p.Body == nil || *p.Body != *e.body {
			continue
		}
		meta, err := bm.metas.loadMeta(ctx, value, p)
		if err != nil {
			return nil, err
		}
		if meta.ContentHash == "" {
			// The content hash is the member's key material, so a body cannot
			// be moved without it. Nothing writes such an entry with a body
			// today; left alone rather than guessed at.
			continue
		}
		reads = append(reads, blobRead{index: len(targets), ref: p.Body})
		hashes = append(hashes, meta.ContentHash)
		targets = append(targets, blobRewrite{entry: e, value: value, payload: p})
	}

	// A blob is retired only if every one of its bodies moves, so once one of
	// its spans cannot be read there is nothing further to gain from the rest:
	// the entries that stayed keep the blob alive, and moving its neighbours
	// would write bytes that buy nothing.
	failed := map[string]bool{}

	out := make([]blobRewrite, 0, len(targets))
	for _, s := range planBlobSpans(reads, restoreIOGap, restoreSpanBytes()) {
		if failed[s.blob] {
			continue
		}
		data, err := bm.bodies.span(ctx, s)
		if err != nil {
			// A blob that cannot be read is not consolidated. Its entries keep
			// naming it, so the snapshot stays correct and prune keeps the
			// blob; the failure belongs to check, not to a housekeeping pass
			// inside a backup that has otherwise succeeded.
			bm.log.Debugf("consolidation skipped %s: %v", s.blob, err)
			failed[s.blob] = true
			continue
		}
		for _, idx := range s.members {
			t := targets[idx]
			sealed, err := s.slice(data, t.payload.Body)
			if err != nil {
				bm.log.Debugf("consolidation skipped a member of %s: %v", s.blob, err)
				failed[s.blob] = true
				break
			}
			body, err := bm.bodies.member(sealed, t.payload, hashes[idx])
			if err != nil {
				bm.log.Debugf("consolidation skipped %s: %v", hashes[idx], err)
				failed[s.blob] = true
				break
			}
			promise, err := bm.blobs.Add(ctx, hashes[idx], body)
			if err != nil {
				// Adding is where a blob may seal and be written, so this is a
				// store failure on the write path, not a read that can be
				// skipped.
				return nil, err
			}
			t.promise = promise
			out = append(out, t)
			phase.Increment(1)
		}
	}
	return out, nil
}

// applyRewrites repoints each moved entry at its body's new home, returning
// how many entries were rewritten.
//
// The entry's value does not change — it is the content address of the
// metadata, and the metadata is untouched — so change detection sees nothing
// on the next run and the file is not re-read. Only the body reference inside
// the leaf moves.
func (bm *BackupManager) applyRewrites(ctx context.Context, rewrites []blobRewrite) (int, error) {
	moved := 0
	for _, r := range rewrites {
		placed := r.promise.placed()
		if placed == nil {
			// Unreachable: the writer was flushed above, which resolves every
			// promise it handed out. Checked because the alternative is an
			// entry inserted with no body at all.
			continue
		}
		// A new payload rather than a mutated one. Payloads are immutable once
		// attached and shared by every copy of the entry, including the
		// previous snapshot's nodes still in the node cache — writing through
		// this pointer would repoint an old snapshot at a blob it never named.
		p := &hamt.Payload{
			Meta:   r.payload.Meta,
			Size:   r.payload.Size,
			Body:   placed,
			Chunks: r.payload.Chunks,
		}
		if err := bm.txn.InsertWithPayload(ctx, r.entry.routingKey, r.entry.fileID, r.value, p); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}
