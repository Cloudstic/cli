package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/objkey"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"
)

// CheckOption configures a check operation.
type CheckOption func(*checkConfig)

type checkConfig struct {
	readData    bool
	snapshotRef string
}

// WithReadData enables full byte-level verification: re-hash every chunk
// against its key, and reconstruct each file from its content manifest to
// confirm it still hashes to what its filemeta records.
//
// Without it, check verifies that every referenced object is readable and that
// the self-addressed ones (snapshot, node, filemeta) are the bytes their keys
// name. That is enough to detect a broken or substituted tree, but not enough
// to detect corruption inside the file data itself.
func WithReadData() CheckOption {
	return func(cfg *checkConfig) { cfg.readData = true }
}

// WithSnapshotRef limits the check to a single snapshot instead of all.
func WithSnapshotRef(ref string) CheckOption {
	return func(cfg *checkConfig) { cfg.snapshotRef = ref }
}

// CheckError describes a single integrity error found during a check.
type CheckError struct {
	Key     string // Object key (e.g. "chunk/abc123")
	Type    string // Error category: "missing", "read_error", "corrupt", "parse_error"
	Message string
}

func (e CheckError) String() string {
	return fmt.Sprintf("%s: %s: %s", e.Type, e.Key, e.Message)
}

// CheckResult holds the outcome of a check operation.
type CheckResult struct {
	SnapshotsChecked int
	ObjectsVerified  int
	Errors           []CheckError
}

// CheckManager verifies the integrity of a repository by walking the full
// reference chain and checking that every referenced object can be read.
type CheckManager struct {
	store    store.ObjectStore
	tree     *hamt.Tree
	reporter ui.Reporter
	// verified holds one entry per object the walk has already looked at,
	// so a shared filemeta, content object or chunk is read once rather than
	// once per referencing snapshot. It is sized by the repository, which is
	// why it is an objkey.Set rather than the map[string]bool it reads as.
	verified *objkey.Set
	hmacKey  []byte
	// v3 is the repository's recorded format (Deps.FormatV3): entries carry
	// their metadata and content in leaf payloads, so the per-entry chain is
	// verified from the leaf rather than fetched per object.
	v3 bool
}

// NewCheckManager creates a CheckManager.
func NewCheckManager(d Deps) *CheckManager {
	return &CheckManager{
		store:    d.Store,
		tree:     hamt.NewTree(d.Store, d.treeOptions()...),
		reporter: d.Reporter,
		hmacKey:  d.HMACKey,
		v3:       d.FormatV3,
	}
}

// Run verifies the repository integrity.
func (cm *CheckManager) Run(ctx context.Context, opts ...CheckOption) (*CheckResult, error) {
	var cfg checkConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	cm.verified = objkey.NewSet()
	result := &CheckResult{}

	// Resolve which snapshots to check.
	snapRefs, err := cm.resolveSnapshots(ctx, cfg.snapshotRef)
	if err != nil {
		return nil, err
	}

	phase := cm.reporter.StartPhase("Checking repository integrity", int64(len(snapRefs)), false)

	for _, ref := range snapRefs {
		if err := cm.checkSnapshot(ctx, ref, result, &cfg, phase); err != nil {
			phase.Error()
			return nil, fmt.Errorf("check snapshot %s: %w", ref, err)
		}
		result.SnapshotsChecked++
		phase.Increment(1)
	}

	phase.Done()
	return result, nil
}

// resolveSnapshots returns the list of snapshot refs to check.
func (cm *CheckManager) resolveSnapshots(ctx context.Context, snapshotRef string) ([]string, error) {
	if snapshotRef != "" {
		ref := snapshotRef
		if ref == "latest" {
			data, err := cm.store.Get(ctx, "index/latest")
			if err != nil {
				return nil, fmt.Errorf("read index/latest: %w", err)
			}
			var idx core.Index
			if err := json.Unmarshal(data, &idx); err != nil {
				return nil, fmt.Errorf("parse index/latest: %w", err)
			}
			ref = idx.LatestSnapshot
		} else if !strings.HasPrefix(ref, "snapshot/") {
			ref = "snapshot/" + ref
		}
		return []string{ref}, nil
	}

	keys, err := cm.store.List(ctx, "snapshot/")
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	return keys, nil
}

// checkSnapshot verifies a single snapshot and its entire reference chain.
func (cm *CheckManager) checkSnapshot(ctx context.Context, ref string, result *CheckResult, cfg *checkConfig, phase ui.Phase) error {
	if cm.verified.Has(ref) {
		return nil
	}

	// 1. Read and parse the snapshot.
	data, err := cm.store.Get(ctx, ref)
	if err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "read_error", Message: fmt.Sprintf("cannot read snapshot: %v", err),
		})
		return nil // continue checking other snapshots
	}
	cm.verified.Add(ref)
	if err := core.VerifyRef(ref, data); err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "corrupt", Message: err.Error(),
		})
		return nil
	}
	result.ObjectsVerified++
	phase.Logf(ui.DetailVerbose, "OK: %s", ref)

	var snap core.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "parse_error", Message: fmt.Sprintf("cannot parse snapshot: %v", err),
		})
		return nil
	}

	// 2 and 3 together in v3: verify each node as it is reached and check the
	// entries its leaves hold, in one traversal. Doing the node pass and the
	// entry pass separately reads every leaf twice, and a v3 leaf is the data
	// — see hamt.WalkTree.
	if cm.v3 {
		if err := cm.tree.WalkTree(ctx, snap.Root,
			func(nodeRef string) error {
				// A node already verified by this run was reached through a
				// full walk of its subtree, and a ref names that whole
				// subtree — so everything beneath it is verified too, and
				// descending again would re-read it for nothing. Consecutive
				// snapshots differ in a narrow spine and share the rest, so
				// this is most of the tree on every snapshot after the first.
				if cm.verified.Has(nodeRef) {
					return hamt.ErrSkipSubtree
				}

				// Counted as verified without reading it here, because the
				// walk is about to read it and NodeStore.load checks a node
				// against its own ref exactly as verifyObject would — a node
				// ref *is* the SHA-256 of its bytes. Fetching it separately
				// first was doubling every node read in the traversal: on a
				// five-snapshot repository holding 251 nodes, check performed
				// 342 node reads for a single snapshot.
				//
				// A node that cannot be read or does not match its ref is
				// reported by the handler below, which is what keeps this
				// from trading the finding for the saving.
				cm.verified.Add(nodeRef)
				result.ObjectsVerified++
				phase.Logf(ui.DetailVerbose, "OK: %s", nodeRef)
				return nil
			},
			func(_, ref string, p *hamt.Payload) error {
				return cm.checkLeafEntry(ctx, ref, p, result, cfg, phase)
			},
			hamt.WithNodeErrorHandler(func(nodeRef string, err error) error {
				// Record and carry on: an integrity check that stopped at the
				// first unreadable node would report one fault and leave the
				// rest of the repository unexamined. The subtree beneath it is
				// unreachable either way.
				result.Errors = append(result.Errors, CheckError{
					Key: nodeRef, Type: "missing",
					Message: fmt.Sprintf("node not found, unreadable, or does not match its ref: %v", err),
				})
				phase.Log(fmt.Sprintf("UNREADABLE: %s", nodeRef))
				return nil
			}),
		); err != nil {
			result.Errors = append(result.Errors, CheckError{
				Key: snap.Root, Type: "read_error", Message: fmt.Sprintf("cannot walk HAMT tree: %v", err),
			})
		}
		return nil
	}

	// 2. Walk HAMT nodes — verify each node is readable.
	if err := cm.tree.NodeRefs(ctx, snap.Root, func(nodeRef string) error {
		return cm.verifyObject(ctx, nodeRef, result, cfg, phase)
	}); err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: snap.Root, Type: "read_error", Message: fmt.Sprintf("cannot walk HAMT tree: %v", err),
		})
		return nil
	}

	// 3. Walk leaf entries — verify filemeta → content → chunks.

	if err := walkEntriesBatched(ctx, cm.tree, snap.Root, func(entries []treeEntry) error {
		refs := make([]string, len(entries))
		for i, e := range entries {
			refs[i] = e.ref
		}
		return readGrouped(ctx, cm.store, refs, func(ref string) error {
			return cm.checkFileMeta(ctx, ref, result, cfg, phase)
		})
	}); err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: snap.Root, Type: "read_error", Message: fmt.Sprintf("cannot walk HAMT entries: %v", err),
		})
	}

	return nil
}

// checkLeafEntry verifies one v3 entry: the metadata bytes its leaf carries
// against the content address the entry records, the agreement between the
// meta's two content fields, and the chunk chain when the content is chunked.
// The leaf's own integrity was already checked when the node was loaded — a
// node ref names its bytes — so what is verified here are the claims the
// payload makes, not its transport.
func (cm *CheckManager) checkLeafEntry(ctx context.Context, ref string, p *hamt.Payload, result *CheckResult, cfg *checkConfig, phase ui.Phase) error {
	if cm.verified.Has(ref) {
		return nil
	}
	cm.verified.Add(ref)

	if p == nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "missing", Message: "v3 leaf entry carries no payload",
		})
		return nil
	}

	if err := core.VerifyRef(ref, p.Meta); err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "corrupt", Message: err.Error(),
		})
		return nil
	}
	result.ObjectsVerified++
	phase.Logf(ui.DetailVerbose, "OK: %s", ref)

	meta, err := decodePayloadMeta(ref, p)
	if err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "parse_error", Message: fmt.Sprintf("cannot parse filemeta: %v", err),
		})
		return nil
	}

	if meta.ContentHash == "" {
		return nil // folder or file with no content
	}

	if len(cm.hmacKey) > 0 && meta.ContentRef != "" {
		if want := crypto.ComputeHMAC(cm.hmacKey, []byte(meta.ContentHash)); want != meta.ContentRef {
			result.Errors = append(result.Errors, CheckError{
				Key:  ref,
				Type: "corrupt",
				Message: fmt.Sprintf("content_ref %s does not derive from content_hash %s",
					meta.ContentRef, meta.ContentHash),
			})
			return nil
		}
	}

	for _, chunkRef := range p.Chunks {
		if err := cm.checkChunk(ctx, chunkRef, result, cfg, phase); err != nil {
			return err
		}
	}

	if cfg.readData {
		cm.checkLeafContent(ctx, ref, p, meta, result, phase)
	}
	return nil
}

// checkLeafContent is checkManifest for a v3 entry: reconstruct the content
// from the payload — inline bytes, or the ordered chunk list — and compare it
// with the hash and size the metadata records.
func (cm *CheckManager) checkLeafContent(
	ctx context.Context,
	ref string,
	p *hamt.Payload,
	meta *core.FileMeta,
	result *CheckResult,
	phase ui.Phase,
) {
	hasher := sha256.New()
	if len(p.Inline) > 0 {
		_, _ = hasher.Write(p.Inline)
	}
	for _, chunkRef := range p.Chunks {
		data, err := cm.store.Get(ctx, chunkRef)
		if err != nil {
			// The chunk pass above already recorded this as missing; adding a
			// second finding for the same object would only inflate the count.
			return
		}
		_, _ = hasher.Write(data)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != meta.ContentHash {
		result.Errors = append(result.Errors, CheckError{
			Key:  ref,
			Type: "corrupt",
			Message: fmt.Sprintf("reconstructed content hashes to %s, filemeta records %s",
				got, meta.ContentHash),
		})
		phase.Log(fmt.Sprintf("CORRUPT: %s", ref))
		return
	}

	if p.Size != 0 && p.Size != meta.Size {
		result.Errors = append(result.Errors, CheckError{
			Key:  ref,
			Type: "corrupt",
			Message: fmt.Sprintf("leaf entry records size %d, filemeta records %d",
				p.Size, meta.Size),
		})
	}
}

// verifyObject checks that an object can be read from the store.
func (cm *CheckManager) verifyObject(ctx context.Context, key string, result *CheckResult, cfg *checkConfig, phase ui.Phase) error {
	if cm.verified.Has(key) {
		return nil
	}

	data, err := cm.store.Get(ctx, key)
	if err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: key, Type: "missing", Message: fmt.Sprintf("object not found or unreadable: %v", err),
		})
		cm.verified.Add(key)
		return nil
	}

	cm.verified.Add(key)
	// A node key is the SHA-256 of its bytes. The HAMT walk that produced this
	// ref checks that too, but reporting it here names the offending object as
	// a finding rather than aborting the walk with an opaque error.
	if core.IsSelfAddressed(key) {
		if err := core.VerifyRef(key, data); err != nil {
			result.Errors = append(result.Errors, CheckError{
				Key: key, Type: "corrupt", Message: err.Error(),
			})
			return nil
		}
	}
	result.ObjectsVerified++
	phase.Logf(ui.DetailVerbose, "OK: %s", key)
	return nil
}

// checkFileMeta verifies a filemeta object and its content/chunk chain.
func (cm *CheckManager) checkFileMeta(ctx context.Context, ref string, result *CheckResult, cfg *checkConfig, phase ui.Phase) error {
	if cm.verified.Has(ref) {
		return nil
	}

	data, err := cm.store.Get(ctx, ref)
	if err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "missing", Message: fmt.Sprintf("filemeta not found or unreadable: %v", err),
		})
		cm.verified.Add(ref)
		return nil
	}
	cm.verified.Add(ref)
	if err := core.VerifyRef(ref, data); err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "corrupt", Message: err.Error(),
		})
		return nil
	}
	result.ObjectsVerified++
	phase.Logf(ui.DetailVerbose, "OK: %s", ref)

	var meta core.FileMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "parse_error", Message: fmt.Sprintf("cannot parse filemeta: %v", err),
		})
		return nil
	}

	if meta.ContentHash == "" {
		return nil // folder or file with no content
	}

	contentKey := meta.ContentRef
	if contentKey == "" {
		contentKey = meta.ContentHash
	}

	// A content object is named by an HMAC of the file's content hash, not of
	// its own bytes, so it cannot be checked against its key the way a filemeta
	// can. What can be checked, for free, is that the filemeta's two content
	// fields agree — a filemeta redirected at some other file's content is
	// otherwise invisible until a restore fails the hash check.
	if len(cm.hmacKey) > 0 && meta.ContentRef != "" {
		if want := crypto.ComputeHMAC(cm.hmacKey, []byte(meta.ContentHash)); want != meta.ContentRef {
			result.Errors = append(result.Errors, CheckError{
				Key:  ref,
				Type: "corrupt",
				Message: fmt.Sprintf("content_ref %s does not derive from content_hash %s",
					meta.ContentRef, meta.ContentHash),
			})
			return nil
		}
	}

	return cm.checkContent(ctx, "content/"+contentKey, &meta, result, cfg, phase)
}

// checkContent verifies a content object and its referenced chunks.
//
// meta is the filemeta that pointed here. It carries the only value that can
// prove a manifest is intact: the SHA-256 of the whole file. Neither the
// content object's key nor its bytes encode the order of the chunk list, so a
// store that reorders, drops, or substitutes entries produces a manifest that
// is individually valid at every chunk and reconstructs the wrong file. Only
// re-running the concatenation catches that, which is why it belongs to
// -read-data rather than the cheap pass.
func (cm *CheckManager) checkContent(ctx context.Context, ref string, meta *core.FileMeta, result *CheckResult, cfg *checkConfig, phase ui.Phase) error {
	if cm.verified.Has(ref) {
		return nil
	}

	data, err := cm.store.Get(ctx, ref)
	if err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "missing", Message: fmt.Sprintf("content object not found or unreadable: %v", err),
		})
		cm.verified.Add(ref)
		return nil
	}
	cm.verified.Add(ref)
	result.ObjectsVerified++
	phase.Logf(ui.DetailVerbose, "OK: %s", ref)

	var content core.Content
	if err := json.Unmarshal(data, &content); err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "parse_error", Message: fmt.Sprintf("cannot parse content: %v", err),
		})
		return nil
	}

	for _, chunkRef := range content.Chunks {
		if err := cm.checkChunk(ctx, chunkRef, result, cfg, phase); err != nil {
			return err
		}
	}

	if cfg.readData {
		cm.checkManifest(ctx, ref, &content, meta, result, phase)
	}
	return nil
}

// checkManifest reconstructs the file this content object describes and compares
// it with the hash the filemeta recorded, covering both storage layouts: the
// inline bytes of a small file and the ordered chunk list of a large one.
//
// Inline content costs nothing extra — the bytes are already in hand. The
// chunked path re-reads the chunks, which for a repository with heavy
// cross-file deduplication means reading a shared chunk once per file that
// references it rather than once overall. That is the price of checking the
// one claim nothing else checks, and -read-data is the mode whose whole purpose
// is to pay for certainty.
func (cm *CheckManager) checkManifest(
	ctx context.Context,
	ref string,
	content *core.Content,
	meta *core.FileMeta,
	result *CheckResult,
	phase ui.Phase,
) {
	if meta == nil || meta.ContentHash == "" {
		return
	}

	hasher := sha256.New()
	if len(content.DataInlineB64) > 0 {
		_, _ = hasher.Write(content.DataInlineB64)
	}
	for _, chunkRef := range content.Chunks {
		data, err := cm.store.Get(ctx, chunkRef)
		if err != nil {
			// The chunk pass above already recorded this as missing; adding a
			// second finding for the same object would only inflate the count.
			return
		}
		_, _ = hasher.Write(data)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != meta.ContentHash {
		result.Errors = append(result.Errors, CheckError{
			Key:  ref,
			Type: "corrupt",
			Message: fmt.Sprintf("reconstructed content hashes to %s, filemeta records %s",
				got, meta.ContentHash),
		})
		phase.Log(fmt.Sprintf("CORRUPT: %s", ref))
		return
	}

	if content.Size != 0 && content.Size != meta.Size {
		result.Errors = append(result.Errors, CheckError{
			Key:  ref,
			Type: "corrupt",
			Message: fmt.Sprintf("content records size %d, filemeta records %d",
				content.Size, meta.Size),
		})
	}
}

// checkChunk verifies a chunk object. With --read-data, it also verifies the
// hash of the chunk data matches the key.
func (cm *CheckManager) checkChunk(ctx context.Context, ref string, result *CheckResult, cfg *checkConfig, phase ui.Phase) error {
	if cm.verified.Has(ref) {
		return nil
	}

	data, err := cm.store.Get(ctx, ref)
	if err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "missing", Message: fmt.Sprintf("chunk not found or unreadable: %v", err),
		})
		cm.verified.Add(ref)
		return nil
	}
	cm.verified.Add(ref)
	result.ObjectsVerified++

	if cfg.readData {
		// The key is "chunk/<hash>". Verify the data hashes to the expected value.
		parts := strings.SplitN(ref, "/", 2)
		if len(parts) == 2 {
			var actual string
			if len(cm.hmacKey) > 0 {
				actual = crypto.ComputeHMAC(cm.hmacKey, data)
			} else {
				actual = core.ComputeHash(data)
			}
			if actual != parts[1] {
				result.Errors = append(result.Errors, CheckError{
					Key:     ref,
					Type:    "corrupt",
					Message: fmt.Sprintf("hash mismatch: expected %s, got %s", parts[1], actual),
				})
				phase.Logf(ui.DetailVerbose, "CORRUPT: %s", ref)
				return nil
			}
		}
	}

	phase.Logf(ui.DetailVerbose, "OK: %s", ref)
	return nil
}
