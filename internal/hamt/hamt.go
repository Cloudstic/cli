package hamt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"slices"
	"sort"
	"strconv"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/store"
)

const (
	bitsPerLevel = 5
	branching    = 32 // 2^bitsPerLevel
	maxDepth     = 6
	maxLeafSize  = 32

	// Format v3 routes 2 bits per level instead of 5, and this is the
	// difference between the format working and not.
	//
	// Splitting is what sets leaf fill, because a leaf that overflows is
	// partitioned by the next routing bits and its children never merge back.
	// At 5 bits each child receives about a thirty-second of the entries, so
	// leaves settle a factor of ~32 below the budget that triggered the split:
	// measured at 13 KB average against a 512 KB budget, which put ~25x more
	// objects in a repository than the format intends and left v3 needing
	// thousands of requests where v2 needed dozens.
	//
	// At 2 bits a split quarters instead, so a leaf holds between a quarter of
	// the budget and all of it. The tree is deeper in levels but its interior
	// nodes are tiny and hot in cache, while the objects that cost a request —
	// the leaves — land near their intended size.
	//
	// 2 is the measured optimum, not a midpoint. Fill is bounded by the split
	// geometry: a full leaf partitions into quarters and each refills to the
	// budget, so the steady state runs between 25% and 100% and averages
	// around half. On a 2,000-file `source` tree that is 883 KB average and
	// 1.07 MB median against a 2 MB budget — 44% and 53%, the geometric floor
	// rather than a defect, and the reason there is no fill left to recover by
	// packing leaves harder. Narrowing to 1 bit raises the floor to 50% but
	// costs a level of interior nodes for every bit it stops routing, and
	// measured *worse* overall on the same tree: 761 stored objects against
	// 700, with check and restore unmoved. Widening to 3 bits drops fill back
	// toward an eighth: 928 objects, and check from 904 requests to 1,268.
	//
	// So arity is settled and fill is at its ceiling. The only remaining dial
	// on how many objects a repository holds is the byte budget below.
	bitsPerLevelV3 = 2
	branchingV3    = 4  // 2^bitsPerLevelV3
	maxDepthV3     = 15 // 30 of the 32 routing bits, as in v2

	// The format-v3 leaf split rule (RFC 0026 §2): a leaf splits when its
	// encoded size passes a byte budget, not when it holds a fixed number of
	// entries. Every stored object being large is the property the whole
	// format rests on, so the budget is the primary bound; the entry cap only
	// keeps the linear scans inside one leaf bounded when entries are tiny.
	//
	// Both are write-path tuning knobs, not compatibility surface: a reader
	// accepts any leaf size, so revising them changes new nodes only. Routing
	// arity is not revisable that way — a routed lookup must use the shape
	// that wrote the tree — but the budget is.
	//
	// The budget counts *encoded* bytes, while the 8 MB packfiles this format
	// replaces are 8 MB *stored*. Nothing reconciles the two: a leaf passes
	// through CompressedStore on its way out, and zstd takes a leaf of real
	// file content to roughly a fifth of its encoded size. A 2 MB budget was
	// therefore producing ~190 KB objects — a repository of 667 of them where
	// the packfile format held 21 — which is where v3's request counts on a
	// fresh repository came from, not from leaves failing to fill.
	//
	// 8 MB is the size the packfile layer used for the same purpose, and
	// measured on the benchmark pipeline over a 2,000-file `source` tree it is
	// where the read side stops improving cheaply. Against the 2 MB it
	// replaces: check 904 → 206 requests, restore 915 → 128, prune 1,023 →
	// 200, backup-dedup 2,067 → 178, and the repository holds 213 objects
	// instead of 667.
	//
	// What pays for it is write amplification, because a changed entry
	// rewrites its whole leaf including the untouched content of its
	// neighbours. A single-file incremental uploads 1.6 MB instead of 0.4 MB.
	// That is the honest cost and it is bounded by the budget itself; it does
	// not compound, because it is per changed leaf rather than per snapshot —
	// a repository kept at one snapshot stores 52 MB at either budget, and the
	// 79 MB → 104 MB spread seen with six snapshots retained is the retention,
	// not the format.
	leafSplitBytesV3 = 8 * 1024 * 1024
	maxLeafEntriesV3 = 2048

	nodePrefix = "node/"

	// maxTreeDepth bounds every recursive descent. Routing consumes 5 bits per
	// level out of a 32-bit prefix, so a well-formed tree cannot nest deeper
	// than maxDepth internal levels plus its leaves. Going past it means the
	// structure is not a tree: a child ref reaching back into its own ancestry
	// would otherwise recurse until the stack runs out. Hash verification in
	// NodeStore.load makes such a cycle hard to build, but this guard costs one
	// comparison and turns a hang into an error.
	maxTreeDepth = maxDepth + 2
)

// errTooDeep reports a descent past maxTreeDepth.
func errTooDeep(limit, depth int) error {
	return fmt.Errorf("hamt node nesting exceeds %d levels at depth %d: tree is cyclic or corrupt", limit, depth)
}

// DiffEntry represents a single change between two trees.
// OldValue is empty for additions; NewValue is empty for deletions.
//
// The payloads are the entries' format-v3 bodies, nil on v2 entries. They ride
// along so a v3 caller can read what a removed or replaced entry carried
// without a store round trip — in v3 there is no standalone object behind the
// value to fetch.
type DiffEntry struct {
	Key        string
	OldValue   string
	NewValue   string
	OldPayload *Payload
	NewPayload *Payload
}

// Tree reads persistent Hash Array Mapped Tries.
//
// A HAMT here is a map from (routing key, key) to an opaque string value. The
// routing key is a hex string chosen by the caller; its first 32 bits decide
// the path through the trie, so callers wanting locality between related keys
// give them a shared prefix. The key identifies the entry within its leaf. The
// value is opaque — callers store object refs there, but the tree never
// interprets them.
//
// Tree itself holds no tree state: every read names the root it applies to, so
// one Tree serves any number of snapshots and shares a single node cache
// between them. Mutation happens in a Txn, obtained from Edit.
type Tree struct {
	nodes *NodeStore
}

// NewTree creates a Tree backed by the given object store.
func NewTree(s store.ObjectStore, opts ...TreeOption) *Tree {
	ns := NewNodeStore(s)
	for _, opt := range opts {
		opt(ns)
	}
	return &Tree{nodes: ns}
}

// TreeOption configures the node store behind a Tree.
//
// It is variadic so that the trees which never log — diff, restore, find, ls,
// prune, check — keep their existing call sites unchanged.
type TreeOption func(*NodeStore)

// WithLogger sends the node store's debug output to w.
func WithLogger(w io.Writer) TreeOption {
	return func(ns *NodeStore) { ns.log = ns.log.To(w) }
}

// WithFormatV3 puts the tree in repository-format-v3 mode (RFC 0026): nodes
// seal in the binary encoding, leaves split on the byte budget, entries may
// carry payloads, and routing consumes bitsPerLevelV3 bits per level.
//
// Decoding is unaffected — load sniffs each node, so either format's nodes
// are readable and a walk crosses formats freely. Routing is not: an entry's
// position depends on the arity the tree was built with, so a routed lookup
// must use the shape that wrote the tree. Both follow from the repository's
// recorded format, which is why this is a property of the tree rather than of
// any one operation.
func WithFormatV3() TreeOption {
	return func(ns *NodeStore) {
		ns.v3 = true
		ns.cache.Configure(nodeCacheSizeV3, v3NodeCacheBytes())
	}
}

// NewTreeWithNodes creates a Tree over an existing NodeStore, so several trees
// can share one read cache.
func NewTreeWithNodes(ns *NodeStore) *Tree {
	return &Tree{nodes: ns}
}

// errFoundSentinel is used to short-circuit a Walk in LookupByKey.
var errFoundSentinel = fmt.Errorf("found")

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// Lookup returns the value associated with key in the tree rooted at root, or
// ("", nil) if not found. routingKey must be the key the entry was inserted
// under.
func (t *Tree) Lookup(ctx context.Context, root, routingKey, key string) (string, error) {
	value, _, err := t.LookupEntry(ctx, root, routingKey, key)
	return value, err
}

// LookupEntry is Lookup returning the entry's payload as well. The payload is
// nil for an absent entry and for any entry written without one (every v2
// entry); it is shared with the node cache and must not be mutated.
func (t *Tree) LookupEntry(ctx context.Context, root, routingKey, key string) (string, *Payload, error) {
	if root == "" {
		return "", nil, nil
	}
	e, ok, err := lookupEntry(ctx, t.nodes, child{ref: root}, routingKey, key)
	if err != nil || !ok {
		return "", nil, err
	}
	return e.Value, e.payload, nil
}

// LookupByKey finds a value by walking the entire tree and matching on the raw
// key. This is O(N) and slower than Lookup, but does not require the entry's
// routing key. Use only when the routing key cannot be reconstructed.
func (t *Tree) LookupByKey(ctx context.Context, root, key string) (string, error) {
	if root == "" {
		return "", nil
	}
	e, _, err := lookupEntryByKey(ctx, t.nodes, child{ref: root}, key)
	return e.Value, err
}

// Walk visits every (key, value) pair stored in the tree rooted at root.
func (t *Tree) Walk(ctx context.Context, root string, fn func(key, value string) error) error {
	return t.WalkEntries(ctx, root, func(key, value string, _ *Payload) error {
		return fn(key, value)
	})
}

// WalkEntries is Walk delivering each entry's payload as well — nil for
// entries written without one. Payloads are shared with the node cache and
// must not be mutated or retained past the callback unless copied.
func (t *Tree) WalkEntries(ctx context.Context, root string, fn func(key, value string, p *Payload) error) error {
	if root == "" {
		return nil
	}
	return walk(ctx, t.nodes, child{ref: root}, 0, func(e leafEntry) error {
		return fn(e.Key, e.Value, e.payload)
	})
}

// Diff structurally compares two persisted trees and calls fn for every entry
// added, removed, or modified between root1 and root2.
func (t *Tree) Diff(ctx context.Context, root1, root2 string, fn func(DiffEntry) error) error {
	if root1 == root2 {
		return nil
	}
	return diff(ctx, t.nodes, child{ref: root1}, child{ref: root2}, fn)
}

// NodeRefs visits every HAMT node ref reachable from root (including root
// itself). This is useful for garbage-collection marking, and applies only to
// persisted trees: an uncommitted node has no ref.
func (t *Tree) NodeRefs(ctx context.Context, root string, fn func(ref string) error) error {
	if root == "" {
		return nil
	}
	return nodeRefs(ctx, t.nodes, root, 0, fn)
}

// WalkTree visits every node and every entry in one traversal: onNode for each
// node ref as it is reached, then onEntry for each entry a leaf holds.
//
// It exists because check and prune want both, and asking for them separately
// — NodeRefs followed by WalkEntries — reads the whole tree twice. In format
// v2 the second pass was nearly free, since packfiles bundle the nodes and the
// pack body cache still held them. In v3 the leaves *are* the data: a snapshot
// whose leaves exceed the node cache pays a second full round of reads, which
// measured as roughly double the requests for both operations. One traversal
// makes that structural rather than dependent on what the cache happened to
// keep.
//
// A node is loaded once and reported before its children, so a caller can
// treat onNode as the point where a node's bytes were read and verified.
func (t *Tree) WalkTree(ctx context.Context, root string, onNode func(ref string) error, onEntry func(key, value string, p *Payload) error) error {
	if root == "" {
		return nil
	}
	return walkTree(ctx, t.nodes, root, 0, onNode, onEntry)
}

func walkTree(ctx context.Context, ns *NodeStore, ref string, depth int, onNode func(string) error, onEntry func(string, string, *Payload) error) error {
	if depth > ns.maxTreeDepth() {
		return errTooDeep(ns.maxTreeDepth(), depth)
	}
	if onNode != nil {
		if err := onNode(ref); err != nil {
			return err
		}
	}
	n, err := ns.load(ctx, ref)
	if err != nil {
		return err
	}
	if n.leaf {
		if onEntry == nil {
			return nil
		}
		for _, e := range n.entries {
			if err := onEntry(e.Key, e.Value, e.payload); err != nil {
				return err
			}
		}
		return nil
	}
	for _, cc := range n.children {
		if err := walkTree(ctx, ns, cc.ref, depth+1, onNode, onEntry); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Mutation
// ---------------------------------------------------------------------------

// Txn is a mutable working copy of a tree.
//
// Insert and Delete rewrite nodes in memory and write nothing; the resulting
// tree is a mixture of clean children, still shared with the tree it was opened
// from, and dirty ones that exist only here. Commit is the only write path: it
// serializes the dirty spine bottom-up and returns the new root ref. Nodes that
// were superseded during the transaction were never serialized in the first
// place, so there is nothing to garbage-collect afterwards.
//
// A Txn is not safe for concurrent use.
type Txn struct {
	nodes *NodeStore
	root  child
}

// Edit opens a transaction over the tree rooted at root. Pass an empty root to
// build a new tree.
func (t *Tree) Edit(root string) *Txn {
	tx := &Txn{nodes: t.nodes}
	if root != "" {
		tx.root = child{ref: root}
	}
	return tx
}

// Insert adds or updates the entry for key. routingKey decides the entry's path
// through the trie; key is stored as the leaf key.
func (tx *Txn) Insert(ctx context.Context, routingKey, key, value string) error {
	return tx.InsertWithPayload(ctx, routingKey, key, value, nil)
}

// InsertWithPayload is Insert carrying the entry's format-v3 body. The payload
// is stored inside the leaf and returned by LookupEntry and WalkEntries; it
// must not be mutated after this call. A nil payload is exactly Insert.
func (tx *Txn) InsertWithPayload(ctx context.Context, routingKey, key, value string, p *Payload) error {
	r, err := newRouting(routingKey)
	if err != nil {
		return err
	}
	entry := leafEntry{Key: key, PathKey: r.hex, Value: value, payload: p}
	c, _, err := tx.insert(ctx, tx.root, r, entry, 0)
	if err != nil {
		return err
	}
	tx.root = c
	return nil
}

// Delete removes the entry for key. Deleting a key that is not present, or
// deleting from an empty tree, is a no-op.
func (tx *Txn) Delete(ctx context.Context, routingKey, key string) error {
	if tx.root.isZero() {
		return nil
	}
	r, err := newRouting(routingKey)
	if err != nil {
		return err
	}
	c, _, err := tx.delete(ctx, tx.root, r, key, 0)
	if err != nil {
		return err
	}
	tx.root = c
	return nil
}

// Lookup returns the value for key in the working tree, including changes made
// in this transaction that have not been committed.
func (tx *Txn) Lookup(ctx context.Context, routingKey, key string) (string, error) {
	value, _, err := tx.LookupEntry(ctx, routingKey, key)
	return value, err
}

// LookupEntry is Lookup returning the entry's payload as well, under the same
// sharing rules as Tree.LookupEntry.
func (tx *Txn) LookupEntry(ctx context.Context, routingKey, key string) (string, *Payload, error) {
	if tx.root.isZero() {
		return "", nil, nil
	}
	e, ok, err := lookupEntry(ctx, tx.nodes, tx.root, routingKey, key)
	if err != nil || !ok {
		return "", nil, err
	}
	return e.Value, e.payload, nil
}

// LookupByKey is Tree.LookupByKey over the working tree.
func (tx *Txn) LookupByKey(ctx context.Context, key string) (string, error) {
	value, _, err := tx.LookupEntryByKey(ctx, key)
	return value, err
}

// LookupEntryByKey is LookupByKey returning the entry's payload as well.
func (tx *Txn) LookupEntryByKey(ctx context.Context, key string) (string, *Payload, error) {
	if tx.root.isZero() {
		return "", nil, nil
	}
	e, ok, err := lookupEntryByKey(ctx, tx.nodes, tx.root, key)
	if err != nil || !ok {
		return "", nil, err
	}
	return e.Value, e.payload, nil
}

// Walk visits every (key, value) pair in the working tree.
func (tx *Txn) Walk(ctx context.Context, fn func(key, value string) error) error {
	if tx.root.isZero() {
		return nil
	}
	return walk(ctx, tx.nodes, tx.root, 0, func(e leafEntry) error {
		return fn(e.Key, e.Value)
	})
}

// DiffFrom compares a persisted tree against the working tree, reporting what
// this transaction would change. It writes nothing, so callers can report a
// would-be result without committing.
func (tx *Txn) DiffFrom(ctx context.Context, oldRoot string, fn func(DiffEntry) error) error {
	return diff(ctx, tx.nodes, child{ref: oldRoot}, tx.root, fn)
}

// Root computes the ref this tree would have if committed, without writing
// anything. Callers that only need to know whether a transaction changed
// anything — or that are reporting a dry run — use this instead of Commit.
func (tx *Txn) Root(ctx context.Context) (string, error) {
	return tx.seal()
}

// sealFlushBytes is how many bytes of sealed-but-unwritten nodes a commit
// holds before flushing them.
//
// The seal used to build the whole batch first and write it at the end, which
// on a v2 tree was a map of small JSON documents and on a v3 tree is every
// dirty leaf's encoded bytes — inline file content included — held alongside
// the payloads the tree still points at. That is what took an initial v3
// backup's peak RSS to 561 MB against v2's 183 MB, and 1.2 GB on a 200 MB
// addition. Flushing as the seal goes bounds it by this budget instead of by
// how much the backup changed.
//
// The value trades that residency against write parallelism: a flush is where
// concurrency happens (putAll fans out across the batch), so too small a
// budget serialises the uploads. 32 MB keeps several hundred small nodes, or
// a few dozen full leaves, in flight per flush.
const sealFlushBytes = 32 * 1024 * 1024

// Commit writes every node that is actually part of the final tree and returns
// the new root ref. Superseded intermediate nodes were never serialized, so
// "dirty" and "reachable" are the same set and no reachability pass is needed.
//
// Nodes are written as the seal produces them rather than all at the end, so
// what the commit holds is bounded by sealFlushBytes and not by the size of
// the change. Children are sealed before their parents, so a partial write
// interrupted midway leaves unreferenced nodes — garbage the next prune
// collects — never a node naming a child that was never written.
//
// After Commit the transaction is clean and can be committed again cheaply; a
// second Commit writes nothing and returns the same ref.
func (tx *Txn) Commit(ctx context.Context) (string, error) {
	pending := make(map[string][]byte)
	pendingBytes := 0

	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		if err := tx.nodes.putAll(ctx, pending); err != nil {
			return err
		}
		pending = make(map[string][]byte)
		pendingBytes = 0
		return nil
	}

	ref, err := tx.sealChild(tx.root, func(ref string, data []byte) error {
		pending[ref] = data
		pendingBytes += len(data)
		if pendingBytes >= sealFlushBytes {
			return flush()
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := flush(); err != nil {
		return "", err
	}

	// Every node is persisted now, so the whole tree is clean again.
	if ref == "" {
		tx.root = child{}
	} else {
		tx.root = child{ref: ref}
	}
	return ref, nil
}

// seal computes the root ref without writing or retaining anything: each
// node's bytes are hashed and dropped. Root uses it, which is what makes a
// dry run both read-only and bounded.
func (tx *Txn) seal() (string, error) {
	return tx.sealChild(tx.root, func(string, []byte) error { return nil })
}

// sealChild serializes the dirty spine bottom-up, handing every node it seals
// to sink. Children are sealed — and so handed over — before their parents.
func (tx *Txn) sealChild(c child, sink func(ref string, data []byte) error) (string, error) {
	if c.node == nil {
		return c.ref, nil // clean (or absent): already persisted
	}

	var childRefs []string
	if !c.node.leaf {
		childRefs = make([]string, len(c.node.children))
		for i, cc := range c.node.children {
			ref, err := tx.sealChild(cc, sink)
			if err != nil {
				return "", err
			}
			childRefs[i] = ref
		}
	}

	ref, data, err := tx.nodes.seal(encodeNode(c.node, childRefs))
	if err != nil {
		return "", err
	}
	if err := sink(ref, data); err != nil {
		return "", err
	}
	return ref, nil
}

// own returns a node for c that this transaction may mutate in place. A dirty
// child is already ours; a clean one is shared with the node cache and possibly
// with older snapshots, so it is copied first. This copy is the entire cost of
// a mutation — no marshalling, no hashing, no I/O.
func (tx *Txn) own(ctx context.Context, c child) (*node, error) {
	if c.node != nil {
		return c.node, nil
	}
	n, err := tx.nodes.load(ctx, c.ref)
	if err != nil {
		return nil, fmt.Errorf("load node %s: %w", c.ref, err)
	}
	return n.clone(), nil
}

// ---------------------------------------------------------------------------
// Internal: insert
// ---------------------------------------------------------------------------

// insert places entry in the subtree rooted at c, reporting whether it replaced
// an entry already under the same key rather than adding a new one.
//
// The caller needs that distinction to keep a tree's shape a pure function of
// its contents, which is what node-level deduplication between snapshots rests
// on. An addition can only make a leaf bigger, and the split rule covers that.
// A replacement can move it either way, and in v3 by a lot: an entry's payload
// carries the file's own bytes, so a small file growing to half a megabyte
// overfills a leaf that never reconsiders splitting, and the reverse leaves
// behind a subtree that split under content it no longer holds. The first is
// handled by putting the replacement through the same split rule as an append
// (see insertIntoLeaf); the second by collapsing on the way back up, which is
// exactly the invariant delete already maintains.
//
// Only the replacement path pays for it, so the ordinary append — every entry
// of a full scan, which rebuilds its tree from scratch — is untouched. The
// path that reaches this is the change-feed scan (gdrive-changes,
// onedrive-changes), which edits the previous snapshot's tree in place, so
// every changed file arrives as a replacement.
func (tx *Txn) insert(ctx context.Context, c child, r routing, entry leafEntry, level int) (child, bool, error) {
	var n *node
	if c.isZero() {
		n = &node{leaf: true}
	} else {
		var err error
		if n, err = tx.own(ctx, c); err != nil {
			return child{}, false, err
		}
	}

	if n.leaf {
		return tx.insertIntoLeaf(n, entry, level)
	}

	idx, err := r.indexAt(level, tx.nodes.bits())
	if err != nil {
		return child{}, false, err
	}
	pos, exists := childPos(n.bitmap, idx)

	var cur child
	if exists {
		cur = n.children[pos]
	}
	newChild, replaced, err := tx.insert(ctx, cur, r, entry, level+1)
	if err != nil {
		return child{}, false, err
	}

	if exists {
		n.children[pos] = newChild
	} else {
		n.bitmap |= uint32(1) << idx
		n.children = slices.Insert(n.children, pos, newChild)
	}
	if !replaced {
		return child{node: n}, false, nil
	}
	collapsed, _, err := tx.collapse(ctx, n, level)
	if err != nil {
		return child{}, false, err
	}
	return collapsed, true, nil
}

func (tx *Txn) insertIntoLeaf(n *node, entry leafEntry, level int) (child, bool, error) {
	if i := n.indexOfKey(entry.Key); i >= 0 {
		n.entries[i] = entry
		// A replacement can push a leaf over the budget as easily as an
		// append can — in v3 the new payload may be far larger than the one
		// it displaces — so it faces the same split rule.
		if level < tx.nodes.maxDepth() && tx.nodes.leafOverfull(n.entries) {
			c, err := tx.buildNode(n.entries, level)
			return c, true, err
		}
		return child{node: n}, true, nil
	}

	n.entries = append(n.entries, entry)
	sortEntries(n.entries)

	// Split once the leaf is over its budget — entry count in v2, bytes in v3
	// (see leafOverfull) — unless routing bits have run out, in which case the
	// leaf may grow past it.
	if level < tx.nodes.maxDepth() && tx.nodes.leafOverfull(n.entries) {
		c, err := tx.buildNode(n.entries, level)
		return c, false, err
	}
	return child{node: n}, false, nil
}

// buildNode recursively partitions entries into a subtree starting at level.
func (tx *Txn) buildNode(entries []leafEntry, level int) (child, error) {
	if !tx.nodes.leafOverfull(entries) || level >= tx.nodes.maxDepth() {
		sortEntries(entries)
		return child{node: &node{leaf: true, entries: entries}}, nil
	}

	buckets := make(map[int][]leafEntry)
	for _, e := range entries {
		r, err := routingForEntry(e)
		if err != nil {
			return child{}, err
		}
		idx, err := r.indexAt(level, tx.nodes.bits())
		if err != nil {
			return child{}, err
		}
		buckets[idx] = append(buckets[idx], e)
	}

	n := &node{}
	for i := range tx.nodes.branching() {
		bucket, ok := buckets[i]
		if !ok {
			continue
		}
		c, err := tx.buildNode(bucket, level+1)
		if err != nil {
			return child{}, err
		}
		n.bitmap |= 1 << i
		n.children = append(n.children, c)
	}
	return child{node: n}, nil
}

// ---------------------------------------------------------------------------
// Internal: delete
// ---------------------------------------------------------------------------

// delete returns the child that should replace c, and whether anything changed.
// A zero child means the subtree became empty. When nothing changed, c is
// returned untouched — in particular a clean child stays clean, so a delete
// that misses costs no copy and no write.
func (tx *Txn) delete(ctx context.Context, c child, r routing, key string, level int) (child, bool, error) {
	n, err := tx.resolve(ctx, c)
	if err != nil {
		return c, false, err
	}

	if n.leaf {
		i := n.indexOfKey(key)
		if i < 0 {
			return c, false, nil
		}
		if len(n.entries) == 1 {
			return child{}, true, nil
		}
		owned, err := tx.own(ctx, c)
		if err != nil {
			return c, false, err
		}
		owned.entries = slices.Delete(owned.entries, i, i+1)
		return child{node: owned}, true, nil
	}

	idx, err := r.indexAt(level, tx.nodes.bits())
	if err != nil {
		return c, false, err
	}
	pos, exists := childPos(n.bitmap, idx)
	if !exists {
		return c, false, nil
	}

	newChild, changed, err := tx.delete(ctx, n.children[pos], r, key, level+1)
	if err != nil {
		return c, false, err
	}
	if !changed {
		return c, false, nil
	}

	owned, err := tx.own(ctx, c)
	if err != nil {
		return c, false, err
	}

	if !newChild.isZero() {
		owned.children[pos] = newChild
		return tx.collapse(ctx, owned, level)
	}

	// Child became empty; drop the slot.
	owned.bitmap &^= uint32(1) << idx
	if owned.bitmap == 0 {
		return child{}, true, nil
	}
	owned.children = slices.Delete(owned.children, pos, pos+1)

	// Collapse: if a single leaf remains, promote it in place of this node.
	// This subsumes the general rule below for a leaf that has outgrown
	// maxLeafSize, which only happens at maxDepth where a leaf may not split.
	if len(owned.children) == 1 {
		if only, err := tx.resolve(ctx, owned.children[0]); err == nil && only.leaf {
			return owned.children[0], true, nil
		}
	}
	return tx.collapse(ctx, owned, level)
}

// collapse turns an internal node back into a leaf once its subtree holds few
// enough entries to be one, so that a tree's shape depends on its contents and
// not on the history that produced them.
//
// Insertion is already history-independent: buildNode is a pure function of the
// entry set, and a leaf splits at exactly the size buildNode would stop at.
// Deletion was not. It dropped empty slots and promoted a lone remaining leaf,
// but never re-merged several under-full leaves, so a subtree that split under
// load and then shrank stayed split. Equal content reached by different
// histories then produced different roots, which costs node-level deduplication
// between repositories and leaves delete-heavy trees carrying nodes they no
// longer need.
//
// The invariant restored here is buildNode's own: an internal node exists only
// where the entries beneath it exceed maxLeafSize. Applying it as the recursion
// unwinds lets a collapse cascade — a parent reconsiders itself once a child has
// collapsed — without a second pass.
//
// Cost is bounded by the collapse actually being possible: subtreeEntries stops
// as soon as it has seen more entries than a leaf could hold, so a large subtree
// is rejected after a handful of node loads rather than a full traversal.
func (tx *Txn) collapse(ctx context.Context, n *node, level int) (child, bool, error) {
	entries, ok, err := tx.subtreeEntries(ctx, child{node: n}, level)
	if err != nil {
		return child{}, false, err
	}
	if !ok {
		return child{node: n}, true, nil
	}
	sortEntries(entries)
	return child{node: &node{leaf: true, entries: entries}}, true, nil
}

// subtreeEntries returns every entry beneath c when there are few enough to
// fit in one leaf, reporting false as soon as that stops being possible.
//
// The budget is what keeps this affordable on a hot path: a subtree too large
// to collapse is abandoned as soon as the accumulated entries pass the split
// budget, which for a wide tree means reading a couple of nodes rather than
// all of them.
func (tx *Txn) subtreeEntries(ctx context.Context, c child, depth int) ([]leafEntry, bool, error) {
	if depth > tx.nodes.maxTreeDepth() {
		return nil, false, errTooDeep(tx.nodes.maxTreeDepth(), depth)
	}
	n, err := tx.resolve(ctx, c)
	if err != nil {
		return nil, false, err
	}
	if n.leaf {
		if tx.nodes.leafOverfull(n.entries) {
			return nil, false, nil
		}
		return slices.Clone(n.entries), true, nil
	}

	var out []leafEntry
	for _, sub := range n.children {
		entries, ok, err := tx.subtreeEntries(ctx, sub, depth+1)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, nil
		}
		out = append(out, entries...)
		if tx.nodes.leafOverfull(out) {
			return nil, false, nil
		}
	}
	return out, true, nil
}

// ---------------------------------------------------------------------------
// Internal: traversal
//
// These operate on a child rather than a ref, so the same code serves a
// persisted tree and a transaction's uncommitted working tree.
// ---------------------------------------------------------------------------

// resolve returns the node c points at, loading it if c is clean. The result
// must be treated as read-only; use Txn.own to obtain a mutable node.
func (tx *Txn) resolve(ctx context.Context, c child) (*node, error) {
	return resolve(ctx, tx.nodes, c)
}

func resolve(ctx context.Context, ns *NodeStore, c child) (*node, error) {
	if c.node != nil {
		return c.node, nil
	}
	return ns.load(ctx, c.ref)
}

func lookupEntry(ctx context.Context, ns *NodeStore, c child, routingKey, key string) (leafEntry, bool, error) {
	r, err := newRouting(routingKey)
	if err != nil {
		return leafEntry{}, false, err
	}
	for level := 0; level <= ns.maxTreeDepth(); level++ {
		n, err := resolve(ctx, ns, c)
		if err != nil {
			return leafEntry{}, false, err
		}
		if n.leaf {
			if i := n.indexOfKey(key); i >= 0 {
				return n.entries[i], true, nil
			}
			return leafEntry{}, false, nil
		}

		idx, err := r.indexAt(level, ns.bits())
		if err != nil {
			return leafEntry{}, false, err
		}
		pos, exists := childPos(n.bitmap, idx)
		if !exists {
			return leafEntry{}, false, nil
		}
		if pos >= len(n.children) {
			return leafEntry{}, false, fmt.Errorf("corrupt node: bitmap indicates child but array too short")
		}
		c = n.children[pos]
	}
	return leafEntry{}, false, errTooDeep(ns.maxTreeDepth(), ns.maxTreeDepth())
}

func lookupEntryByKey(ctx context.Context, ns *NodeStore, root child, key string) (leafEntry, bool, error) {
	var found leafEntry
	err := walk(ctx, ns, root, 0, func(e leafEntry) error {
		if e.Key == key {
			found = e
			return errFoundSentinel
		}
		return nil
	})
	if errors.Is(err, errFoundSentinel) {
		return found, true, nil
	}
	return leafEntry{}, false, err
}

func walk(ctx context.Context, ns *NodeStore, c child, depth int, fn func(e leafEntry) error) error {
	if depth > ns.maxTreeDepth() {
		return errTooDeep(ns.maxTreeDepth(), depth)
	}
	n, err := resolve(ctx, ns, c)
	if err != nil {
		return err
	}
	if n.leaf {
		for _, e := range n.entries {
			if err := fn(e); err != nil {
				return err
			}
		}
		return nil
	}
	for _, cc := range n.children {
		if err := walk(ctx, ns, cc, depth+1, fn); err != nil {
			return err
		}
	}
	return nil
}

func nodeRefs(ctx context.Context, ns *NodeStore, ref string, depth int, fn func(string) error) error {
	if depth > ns.maxTreeDepth() {
		return errTooDeep(ns.maxTreeDepth(), depth)
	}
	if err := fn(ref); err != nil {
		return err
	}
	n, err := ns.load(ctx, ref)
	if err != nil {
		return err
	}
	if n.leaf {
		return nil
	}
	for _, cc := range n.children {
		if err := nodeRefs(ctx, ns, cc.ref, depth+1, fn); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal: diff
// ---------------------------------------------------------------------------

func diff(ctx context.Context, ns *NodeStore, c1, c2 child, fn func(DiffEntry) error) error {
	// Identical clean subtrees have identical refs, which is what makes an
	// unchanged subtree free to skip.
	if c1.node == nil && c2.node == nil && c1.ref == c2.ref {
		return nil
	}
	n1, err := resolve(ctx, ns, c1)
	if err != nil {
		return err
	}
	n2, err := resolve(ctx, ns, c2)
	if err != nil {
		return err
	}
	return diffNodes(ctx, ns, n1, n2, 0, fn)
}

func diffNodes(ctx context.Context, ns *NodeStore, n1, n2 *node, level int, fn func(DiffEntry) error) error {
	if level > ns.maxTreeDepth() {
		return errTooDeep(ns.maxTreeDepth(), level)
	}
	if n1.leaf && n2.leaf {
		return diffLeaves(n1, n2, fn)
	}

	for i := 0; i < ns.branching(); i++ {
		c1, err := childForBucket(ctx, ns, n1, i, level)
		if err != nil {
			return err
		}
		c2, err := childForBucket(ctx, ns, n2, i, level)
		if err != nil {
			return err
		}

		switch {
		case c1 == nil && c2 == nil:
			continue
		case c1 == nil:
			if err := collectAll(ctx, ns, c2, level, func(e leafEntry) error {
				return fn(DiffEntry{Key: e.Key, NewValue: e.Value, NewPayload: e.payload})
			}); err != nil {
				return err
			}
		case c2 == nil:
			if err := collectAll(ctx, ns, c1, level, func(e leafEntry) error {
				return fn(DiffEntry{Key: e.Key, OldValue: e.Value, OldPayload: e.payload})
			}); err != nil {
				return err
			}
		default:
			if err := diffNodes(ctx, ns, c1, c2, level+1, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// childForBucket returns the child node for bucket idx.
// When the node is a leaf acting as a virtual internal (mixed-type comparison),
// it returns a synthetic leaf containing only entries that route to this bucket.
func childForBucket(ctx context.Context, ns *NodeStore, n *node, idx, level int) (*node, error) {
	if !n.leaf {
		pos, exists := childPos(n.bitmap, idx)
		if !exists {
			return nil, nil
		}
		return resolve(ctx, ns, n.children[pos])
	}

	var filtered []leafEntry
	for _, e := range n.entries {
		r, err := routingForEntry(e)
		if err != nil {
			continue
		}
		i, err := r.indexAt(level, ns.bits())
		if err != nil {
			continue
		}
		if i == idx {
			filtered = append(filtered, e)
		}
	}
	if len(filtered) == 0 {
		return nil, nil
	}
	return &node{leaf: true, entries: filtered}, nil
}

func diffLeaves(n1, n2 *node, fn func(DiffEntry) error) error {
	left := make(map[string]leafEntry, len(n1.entries))
	for _, e := range n1.entries {
		left[e.Key] = e
	}

	for _, e := range n2.entries {
		old, ok := left[e.Key]
		if !ok {
			if err := fn(DiffEntry{Key: e.Key, NewValue: e.Value, NewPayload: e.payload}); err != nil {
				return err
			}
			continue
		}
		if old.Value != e.Value {
			if err := fn(DiffEntry{
				Key:      e.Key,
				OldValue: old.Value, OldPayload: old.payload,
				NewValue: e.Value, NewPayload: e.payload,
			}); err != nil {
				return err
			}
		}
		delete(left, e.Key)
	}

	for k, e := range left {
		if err := fn(DiffEntry{Key: k, OldValue: e.Value, OldPayload: e.payload}); err != nil {
			return err
		}
	}
	return nil
}

func collectAll(ctx context.Context, ns *NodeStore, n *node, depth int, fn func(e leafEntry) error) error {
	if depth > ns.maxTreeDepth() {
		return errTooDeep(ns.maxTreeDepth(), depth)
	}
	if n.leaf {
		for _, e := range n.entries {
			if err := fn(e); err != nil {
				return err
			}
		}
		return nil
	}
	for _, cc := range n.children {
		child, err := resolve(ctx, ns, cc)
		if err != nil {
			return err
		}
		if err := collectAll(ctx, ns, child, depth+1, fn); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal: routing keys
// ---------------------------------------------------------------------------

// routing is a parsed routing key: the hex form, as stored on leaf entries, plus
// its leading 32 bits. Parsing once per operation keeps the per-level index a
// shift and a mask instead of re-parsing the same 8 hex characters at every
// level of every descent.
type routing struct {
	hex    string
	prefix uint32
}

func newRouting(keyHex string) (routing, error) {
	if len(keyHex) < 8 {
		return routing{}, fmt.Errorf("key too short")
	}
	val, err := strconv.ParseUint(keyHex[:8], 16, 32)
	if err != nil {
		return routing{}, err
	}
	return routing{hex: keyHex, prefix: uint32(val)}, nil
}

// routingForEntry returns the routing key of a stored leaf entry, falling back
// to SHA256(Key) for legacy entries written before PathKey existed.
func routingForEntry(e leafEntry) (routing, error) {
	pk := e.PathKey
	if pk == "" {
		pk = core.ComputeHash([]byte(e.Key))
	}
	return newRouting(pk)
}

func (r routing) indexAt(level, bits int) (int, error) {
	shift := 32 - (level+1)*bits
	if shift < 0 {
		return 0, fmt.Errorf("level too deep for 32-bit key prefix")
	}
	return int((r.prefix >> shift) & ((1 << bits) - 1)), nil
}

func sortEntries(entries []leafEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})
}

// childPos returns the index of bucket idx within a node's packed children
// array, and whether the node has a child in that bucket at all.
func childPos(bitmap uint32, idx int) (int, bool) {
	bit := uint32(1) << idx
	return bits.OnesCount32(bitmap & (bit - 1)), bitmap&bit != 0
}
