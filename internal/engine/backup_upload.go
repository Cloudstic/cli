package engine

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"sync"

	"golang.org/x/sync/semaphore"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"
)

const defaultUploadConcurrency = 10

// inlineThreshold is the maximum file size for which content is stored inline
// in the Content object rather than as separate chunk objects.
const inlineThreshold = 512 * 1024 // 512 KiB (matches CDC min chunk size)

// envInlineThreshold is a test-only override, in the spirit of the
// CLOUDSTIC_TEST_* knobs in internal/hamt/tuning.go: it exists so a leaf's
// composition can be varied without a rebuild. Setting it to 1 chunks every
// file, which produces a tree whose leaves carry metadata and chunk refs and
// no bodies — the closest thing to a metadata-only tree that the current
// format can express, and the way to measure one rather than estimate it.
const envInlineThreshold = "CLOUDSTIC_TEST_INLINE_BYTES"

// maxInlineBytes caps what the override may ask for.
//
// The value becomes an allocation — a read buffer sized to hold a whole body —
// so an unbounded one read from the environment is a way to make the process
// allocate arbitrarily, or to panic in make() on a platform where int is
// narrower than the int64 it was parsed as. 64 MB is far above any plausible
// setting: the inline path exists for bodies small enough not to be worth
// chunking, and the default is 512 KiB.
const maxInlineBytes = 64 << 20

func inlineLimit() int64 {
	if v, ok := os.LookupEnv(envInlineThreshold); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 && n <= maxInlineBytes {
			return n
		}
	}
	return inlineThreshold
}

// inlineBufferPool holds read buffers for bodies small enough to be stored
// whole. A buffer must be at least inlineLimit() long, not inlineThreshold:
// the routing decision uses the limit, so sizing the buffer from the constant
// silently truncated every body between the two whenever the two differed.
//
// They differ only under CLOUDSTIC_TEST_INLINE_BYTES, so production was never
// affected — but the knob is documented as sweepable, and sweeping it upward
// produced repositories that passed check and restored short files. A dial
// that corrupts when turned is worse than no dial.
var inlineBufferPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, inlineLimit())
		return &b
	},
}

// uploadResult is what one worker reports back about a single file.
//
// It carries no core.FileMeta. The upload phase has no reader for one — the
// paths built during the scan are already persisted, and newMetas is released
// before upload begins — so returning a 216-byte struct by value through the
// results channel would cost memory for every file to no end.
type uploadResult struct {
	fileID   string
	parentID string // primary parent's raw fileID (for the affinity routing key)
	ref      string
	// payload is the entry's leaf body in a v3 repository: the filemeta bytes
	// plus either the inline content or its chunk refs. Nil in v2, where both
	// were persisted as their own objects by the worker.
	payload *hamt.Payload
	// promise is where this entry's body landed, resolved once the blob
	// holding it is sealed. Nil for a folder, an empty file, or a chunked
	// body, none of which put anything in a blob.
	promise *bodyPromise
	err     error
}

// upload processes the pending file queue with concurrent workers and inserts
// each result into the working tree. Uploads run in parallel; the inserts do
// not — they all happen on this goroutine, which is what keeps the Txn's
// single-writer contract intact.
func (bm *BackupManager) upload(ctx context.Context, pending []core.FileMeta, totalBytes int64) error {
	if len(pending) == 0 {
		return nil
	}

	phase := bm.reporter.StartPhase("Uploading", totalBytes, true)

	concurrency := store.GetConcurrencyHint(bm.store, defaultUploadConcurrency)

	// Cap max memory in-flight to 150 MB to prevent OOMs on highly concurrent stores (S3)
	maxInFlight := int64(150 * 1024 * 1024)
	sem := semaphore.NewWeighted(maxInFlight)

	jobs := make(chan core.FileMeta, min(128, len(pending)))
	results := make(chan uploadResult, min(128, len(pending)))

	for range min(concurrency, len(pending)) {
		go func() {
			for meta := range jobs {
				weight := meta.Size
				// Large files are streamed in chunks, so they don't consume `meta.Size` RAM all at once.
				// Cap weight at 10MB to allow other files to process alongside large ones.
				if weight > 10*1024*1024 {
					weight = 10 * 1024 * 1024
				} else if weight <= 0 {
					weight = 1024 // min weight
				}

				_ = sem.Acquire(ctx, weight)
				res := bm.processFile(ctx, meta, phase)
				sem.Release(weight)

				results <- res
			}
		}()
	}

	// In v3 the pending list is dispatched in routing-key order rather than in
	// walk order, which is what lets the tree be committed mid-run (see
	// commitIfLarge). Uploads then complete in roughly leaf order, so a leaf
	// sealed by an intermediate commit is rarely touched again — and a leaf
	// touched after it was sealed has to be rewritten, its first copy becoming
	// garbage.
	//
	// Only v3 reorders. In v2 the upload order is the order newly written
	// objects land in a packfile, so it is the locality of the repository
	// being written and must stay the walk's (RFC 0025).
	if bm.v3 {
		sort.Slice(pending, func(i, j int) bool {
			return AffinityKey(primaryParentID(&pending[i]), pending[i].FileID) <
				AffinityKey(primaryParentID(&pending[j]), pending[j].FileID)
		})
	}

	go func() {
		for _, m := range pending {
			jobs <- m
		}
		close(jobs)
	}()

	// Results arrive in whichever order the workers finish, which is unrelated
	// to where their entries belong in the tree. Inserting them as they land
	// descends to a different leaf every time — the same scattered access that
	// made change detection expensive — and each descent loads the leaf it
	// lands in so the transaction can copy it. Buffering a run of results and
	// inserting them in routing-key order makes consecutive inserts share a
	// leaf, so it is loaded once instead of once per entry.
	//
	// It costs no memory: an insert hands the payload to the transaction,
	// which holds it until commit either way, so buffering only moves when
	// that happens rather than adding to it.
	awaiting := make([]uploadResult, 0, insertBatch)
	buf := make([]uploadResult, 0, insertBatch)
	ready := make([]uploadResult, 0, insertBatch)

	// An entry whose body is still in an unsealed blob has no reference to
	// encode, so it is held back rather than inserted. Holding back is the
	// alternative to sealing the blob early, which would let the insert batch
	// — an unrelated quantity — decide blob sizes, and produce a blob per
	// batch of small files far below the budget.
	//
	// What is held back is *not* bounded by one blob's worth of content, which
	// is the trap: a blob stores a repeated body once, so a run of identical
	// files adds nothing to its budget and it never fills. Backing up a tree
	// of duplicates would hold every entry to the end. So the entry count
	// forces a seal as well, which is what keeps this bounded by the batch
	// rather than by the run.
	flush := func(sealAll bool) error {
		if sealAll && bm.blobs != nil {
			if err := bm.blobs.Flush(ctx); err != nil {
				return err
			}
		}
		ready, buf = ready[:0], buf[:0]
		for _, res := range awaiting {
			if res.promise != nil {
				placed := res.promise.placed()
				if placed == nil {
					buf = append(buf, res)
					continue
				}
				res.payload.Body = placed
				// An entry that reused a placement a previous backup made
				// keeps that blob alive exactly as an unchanged entry does, so
				// consolidation has to count it. Without this the blob would
				// look emptier than it is — which both biases it towards being
				// selected and, once selected, leaves it alive after every
				// entry consolidation knew about has moved off it: bytes
				// rewritten for a blob that is not retired.
				if res.promise.inherited && bm.consolidation != nil {
					bm.consolidation.note(AffinityKey(res.parentID, res.fileID), res.fileID, placed)
				}
			}
			ready = append(ready, res)
		}
		awaiting = append(awaiting[:0], buf...)
		if len(ready) == 0 {
			return nil
		}
		sort.Slice(ready, func(i, j int) bool {
			return AffinityKey(ready[i].parentID, ready[i].fileID) < AffinityKey(ready[j].parentID, ready[j].fileID)
		})
		for _, res := range ready {
			if err := bm.txn.InsertWithPayload(ctx, AffinityKey(res.parentID, res.fileID), res.fileID, res.ref, res.payload); err != nil {
				return fmt.Errorf("hamt insert: %w", err)
			}
		}
		return nil
	}

	// A v3 entry carries its file's content, and the transaction holds every
	// entry it has been given until it is committed — so without this the
	// backup's memory is the total inlined bytes of the run rather than a
	// working set. Measured before it existed: 800 MB peak for a backup adding
	// 200 MB of small files, 1,675 MB for 600 MB, against 548 MB for the
	// packfile format doing the same work (#526).
	//
	// Committing releases them: Commit seals the dirty spine, writes it, and
	// leaves the transaction holding one clean root ref, so every node it was
	// carrying — payloads included — becomes unreachable. A tree committed in
	// several steps ends at the same root as one committed at the end, because
	// nodes are content-addressed and the shape is a pure function of the
	// contents; the superseded intermediate nodes are ordinary garbage that
	// prune collects.
	// Resolved once: the reader reaches os.Getenv and this loop runs once per
	// file backed up (#538).
	commitBytes := uploadCommitLimit()

	var retained int
	commitRetained := func() error {
		if _, err := bm.txn.Commit(ctx); err != nil {
			return fmt.Errorf("commit tree during upload: %w", err)
		}
		retained = 0
		return nil
	}

	for range pending {
		res := <-results
		if res.err != nil {
			phase.Error()
			return res.err
		}
		if res.payload != nil {
			retained += len(res.payload.Meta)
		}
		awaiting = append(awaiting, res)

		// Two independent triggers, because they bound different things. The
		// entry count keeps the insert batch large enough to be worth sorting;
		// the byte count is what bounds memory, and it has to be able to fire
		// on its own — a run of half-megabyte inlined files reaches the byte
		// budget in a couple of hundred entries and would otherwise wait for a
		// batch that is ten times further off.
		full := len(awaiting) >= insertBatch
		heavy := retained >= commitBytes
		if !full && !heavy {
			continue
		}
		// A full entry buffer seals the pending blob: see flush. For ordinary
		// content the blob has filled long before, so this changes nothing;
		// for content that deduplicates it is the only thing that bounds what
		// is held.
		if err := flush(full); err != nil {
			phase.Error()
			return err
		}
		if heavy {
			if err := commitRetained(); err != nil {
				phase.Error()
				return err
			}
		}
	}
	if err := flush(true); err != nil {
		phase.Error()
		return err
	}

	phase.Done()
	return nil
}

// uploadCommitBytes is how much inlined content a v3 backup accumulates in the
// working tree before committing it, which is what bounds the phase's memory.
//
// It is counted in bytes rather than entries because bytes are what is being
// bounded: a run of large inlined files and a run of tiny ones hold wildly
// different amounts for the same entry count. 64 MB is comfortably below the
// 150 MB the upload workers are already allowed in flight, so the tree stops
// being the dominant term without adding commits frequent enough for their
// rewrites to show.
const uploadCommitBytes = 64 * 1024 * 1024

// envUploadCommitBytes overrides uploadCommitBytes, for tests only.
//
// It exists because the quantity being bounded changed. While a payload
// carried the file's content, a few hundred entries crossed the threshold and
// a test could reach it with a realistic tree. A payload is now metadata and a
// body reference, so the same 64 MB is a few hundred thousand entries — past
// what a unit test should build to prove the mechanism works.
const envUploadCommitBytes = "CLOUDSTIC_TEST_UPLOAD_COMMIT_BYTES"

func uploadCommitLimit() int {
	if v, ok := os.LookupEnv(envUploadCommitBytes); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return uploadCommitBytes
}

// insertBatch is how many uploaded entries are buffered before being inserted
// into the working tree in routing-key order. Large enough that a batch spans
// many entries of the same leaf, small enough that the reordering never delays
// the tree far behind the uploads that feed it.
const insertBatch = 2048

// uploadedContent is what uploadContent established about one file's content:
// its identity, and where its bytes ended up — chunk objects, an inline copy
// (v3, where the caller places it into the leaf), or nowhere new (dedup'd).
type uploadedContent struct {
	hash   string
	size   int64
	ref    string   // HMAC(dedupKey, hash), or hash when unencrypted
	chunks []string // chunk refs; nil for a blob-bound or dedup'd body
	// place is where a v3 body lives, once the blob holding it is sealed. It
	// is resolved already when the placement was reused rather than written
	// (see bodyindex.go), and nil for a folder, an empty file or a chunked
	// body — none of which put anything in a blob.
	place *bodyPromise
}

// processFile uploads (or deduplicates) a single file's content and persists
// its FileMeta. It is safe to call from multiple goroutines.
func (bm *BackupManager) processFile(ctx context.Context, meta core.FileMeta, phase ui.Phase) uploadResult {
	phase.Logf(ui.DetailVerbose, "Processing: %s", meta.Name)

	content, err := bm.uploadContent(ctx, meta, phase)
	if err != nil {
		return uploadResult{err: err}
	}

	meta.ContentHash = content.hash
	meta.ContentRef = content.ref
	meta.Size = content.size

	persisted := persistedFileMeta(meta)
	metaRef, metaData, err := core.FileMetaRef(&persisted)
	if err != nil {
		return uploadResult{err: err}
	}

	if bm.v3 {
		// The filemeta rides in the leaf entry, and so does a reference to
		// where the body landed; the standalone objects a v3 file produces are
		// its chunks, or a share of one blob.
		res := uploadResult{
			fileID:   meta.FileID,
			parentID: primaryParentID(&meta),
			ref:      metaRef,
			payload: &hamt.Payload{
				Meta:   metaData,
				Size:   content.size,
				Chunks: content.chunks,
			},
		}
		res.promise = content.place
		return res
	}

	if err := bm.store.Put(ctx, metaRef, metaData); err != nil {
		return uploadResult{err: err}
	}
	return uploadResult{
		fileID:   meta.FileID,
		parentID: primaryParentID(&meta),
		ref:      metaRef,
	}
}

// contentRefFor derives the repository content identity for a plaintext hash.
func (bm *BackupManager) contentRefFor(hash string) string {
	if len(bm.hmacKey) > 0 {
		return crypto.ComputeHMAC(bm.hmacKey, []byte(hash))
	}
	return hash
}

// uploadContent streams, chunks, and stores file content. Skips upload on
// dedup, stores small files inline — as a content object in v2, packed into a
// blob (or pointed at one that already holds those bytes) in v3.
func (bm *BackupManager) uploadContent(ctx context.Context, meta core.FileMeta, phase ui.Phase) (uploadedContent, error) {
	// Read once and used by both size gates below. inlineLimit reaches
	// os.LookupEnv, and this runs per file backed up (#566).
	inline := inlineLimit()

	// Whole-file dedup before the file is opened, for a source that reports a
	// content hash of its own. v2 settles it by probing the content object; v3
	// has none to probe, and asks instead whether the repository already holds
	// a placement for those bytes (see bodyindex.go). Chunked files
	// deduplicate per chunk in storeChunk either way, which skips the upload
	// but not the local read.
	if meta.ContentHash != "" {
		contentRef := meta.ContentRef
		if contentRef == "" {
			contentRef = bm.contentRefFor(meta.ContentHash)
		}
		if bm.v3 {
			// Only over the size window whose bodies live in blobs. A body
			// outside it is chunked and has no placement to reuse, and the
			// window is a runtime value (CLOUDSTIC_TEST_INLINE_BYTES), so a
			// repository can hold placements this run would not have made.
			if meta.Size > 0 && meta.Size <= inline {
				if p := bm.reuse.lookup(meta.ContentHash, meta.Size); p != nil {
					phase.Logf(ui.DetailVerbose, "Deduplicated: %s", meta.Name)
					phase.Increment(meta.Size)
					return uploadedContent{hash: meta.ContentHash, size: meta.Size, ref: contentRef, place: p}, nil
				}
			}
		} else if exists, err := bm.store.Exists(ctx, "content/"+contentRef); err == nil && exists {
			phase.Logf(ui.DetailVerbose, "Deduplicated: %s", meta.Name)
			return uploadedContent{hash: meta.ContentHash, size: meta.Size, ref: contentRef}, nil
		}
	}

	rc, err := bm.source.GetFileStream(meta.FileID)
	if err != nil {
		return uploadedContent{}, fmt.Errorf("get stream for %s: %w", meta.FileID, err)
	}
	defer func() { _ = rc.Close() }()

	if meta.Size > 0 && meta.Size <= inline {
		return bm.uploadInline(ctx, rc, meta, phase)
	}

	chunkRefs, size, hash, err := bm.chunker.ProcessStream(ctx, rc, func(n int64) {
		phase.Increment(n)
	})
	if err != nil {
		return uploadedContent{}, fmt.Errorf("chunking %s: %w", meta.Name, err)
	}

	if bm.v3 {
		return uploadedContent{hash: hash, size: size, ref: bm.contentRefFor(hash), chunks: chunkRefs}, nil
	}

	contentRef, err := bm.chunker.CreateContentObject(ctx, chunkRefs, size, hash)
	if err != nil {
		return uploadedContent{}, fmt.Errorf("create content for %s: %w", meta.Name, err)
	}
	return uploadedContent{hash: hash, size: size, ref: contentRef, chunks: chunkRefs}, nil
}

// uploadInline reads the entire file into memory and stores it directly — in a
// Content object in v2, or in the returned buffer for the caller's leaf entry
// in v3. Uses a sync.Pool to minimize allocations.
func (bm *BackupManager) uploadInline(ctx context.Context, r io.Reader, meta core.FileMeta, phase ui.Phase) (uploadedContent, error) {
	bufPtr := inlineBufferPool.Get().(*[]byte)
	// A pooled buffer can predate a change of limit, so its length is checked
	// rather than assumed. io.ReadFull below stops at len(buf) and reports no
	// error when it fills it, so a short buffer is indistinguishable from a
	// body that ends exactly there.
	// int() is safe because inlineLimit is capped at maxInlineBytes, which fits
	// in an int on every platform this builds for.
	if limit := int(inlineLimit()); len(*bufPtr) < limit {
		b := make([]byte, limit)
		bufPtr = &b
	}
	buf := *bufPtr
	defer inlineBufferPool.Put(bufPtr)

	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return uploadedContent{}, fmt.Errorf("read %s: %w", meta.Name, err)
	}

	data := buf[:n]
	size := int64(n)
	phase.Increment(size)
	hash := core.ComputeHash(data)
	contentRef := bm.contentRefFor(hash)

	if bm.v3 {
		// Asked before the body is copied out of the pooled buffer, so a file
		// the repository already holds costs neither the copy nor the pack.
		// place re-checks under its own lock, so the miss below is a hint
		// rather than a decision.
		if p := bm.reuse.lookup(hash, size); p != nil {
			phase.Logf(ui.DetailVerbose, "Deduplicated: %s", meta.Name)
			return uploadedContent{hash: hash, size: size, ref: contentRef, place: p}, nil
		}
		// Copied out of the pooled buffer, which goes back to the pool on
		// return while the body lives until its blob is sealed.
		bodyCopy := make([]byte, n)
		copy(bodyCopy, data)
		p, err := bm.reuse.place(ctx, bm.blobs, hash, bodyCopy)
		if err != nil {
			return uploadedContent{}, err
		}
		return uploadedContent{hash: hash, size: size, ref: contentRef, place: p}, nil
	}

	contentKey := "content/" + contentRef
	if exists, _ := bm.store.Exists(ctx, contentKey); exists {
		phase.Logf(ui.DetailVerbose, "Deduplicated: %s", meta.Name)
		return uploadedContent{hash: hash, size: size, ref: contentRef}, nil
	}

	// Manually construct JSON to avoid json.Marshal allocating a huge string for the base64 data
	encodedLen := base64.StdEncoding.EncodedLen(len(data))
	prefix := fmt.Sprintf(`{"type":"content","size":%d,"data_inline_b64":"`, size)
	suffix := `"}`

	contentData := make([]byte, len(prefix)+encodedLen+len(suffix))
	copy(contentData, prefix)
	base64.StdEncoding.Encode(contentData[len(prefix):], data)
	copy(contentData[len(prefix)+encodedLen:], suffix)

	if err := bm.store.Put(ctx, contentKey, contentData); err != nil {
		return uploadedContent{}, err
	}
	return uploadedContent{hash: hash, size: size, ref: contentRef}, nil
}
