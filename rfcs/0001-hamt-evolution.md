# RFC 0001: HAMT Evolution & Optimizations

*   **Status:** Proposed
*   **Date:** 2026-03-06

## Abstract
This document tracks potential architectural changes to the Hash Array Mapped Trie (HAMT) structure to reduce metadata bloat, improve locality, and optimize mutation performance.

## 1. Affinity Model (Locality-Preserving Keys)

**Status:** Proposed / Exploratory

### Context
Currently, HAMT keys are based solely on `FileID` (e.g., `Hash(FileID)`). This results in files from the same directory being scattered randomly across the trie, which causes "path amplification" during backups: updating 1,000 files in one folder might require rewriting hundreds of unrelated internal nodes.

### Proposal
Bias the HAMT key to group siblings together by incorporating the parent's identity.
* **Key Format:** `Hash(ParentID)[:4] + Hash(FileID)[:28]`
* **Impact:** 
    * Files with the same parent share the same 4-byte prefix.
    * They map to the same continuous subtree in the HAMT.
    * Backing up a directory modified a single branch, minimizing internal node churn.
* **Trade-off:** If a file moves to a new parent, its HAMT key changes. This requires an explicit `Delete(OldKey)` and `Insert(NewKey)`.
* **Backward Compatibility:** **Breaking.** This is a structural change to how the trie is indexed. Existing trees cannot be migrated in-place without rehashing every key. New snapshots using this model should be tagged with a format version (e.g., `HAMTv2`) to ensure older clients don't attempt to traverse or modify them using the old keying assumptions.

---

## 2. Path Normalization in `FileMeta`

**Status:** Proposed / High-Impact

### Context
`core.FileMeta` currently stores full strings for `Paths` and `Parents`. This data is highly redundant for deeply nested files.

### Proposal
Strip the `Paths` field and only store the `Filename` and list of `ParentID`s.
* **Recovery:** Reconstruct paths dynamically by walking the `Parents` chain.
* **Compatibility:** 
    * **Backup:** Filtering happens source-side using ephemeral paths; `FileMeta.Paths` is not used.
    * **Restore:** `internal/engine/restore.go` already has a fallback to walk the parent chain for path reconstruction if `Paths` is missing.
* **Impact:** Drastic reduction in the size of `FileMeta` objects and the resulting JSON structural overhead.
* **Backward Compatibility:** **Fully Compatible.** Older snapshots will still contain `Paths` strings, which the engine will use as a fast-path. Newer snapshots will omit them, and the engine will automatically use its existing parent-chain reconstruction logic. No format versioning is required.

---

## 3. In-Memory Mutable TreeBuilder

**Status:** Proposed / Performance Optimization

### Context
The current `t.Insert` implementation treats the tree as immutable. Although the `TransactionalStore` caches writes, every insert still involves cloning structs, JSON serialization, and SHA-256 hashing for intermediate nodes that are ultimately discarded.

### Proposal
Introduce a `TreeBuilder` that holds a mutable representation of the trie in-memory.
* **Process:** Apply all backup mutations to the builder using standard Go pointers/maps.
* **Flush:** Perform a single depth-first traversal at the end of the backup session (or chunk) to compute hashes and serialize nodes.
* **Impact:** Significant reduction in CPU usage and GC pressure during high-concurrency scans.
* **Backward Compatibility:** **Transparent.** This is a purely internal implementation change. The resulting `HAMTNode` objects stored in the backend are identical to those produced by the current immutable `Insert` logic.

---

## 4. Operational Optimizations

### Flattening the Tree
* **Proposal:** Increase `maxLeafSize` from `32` to `128` or `256`.
* **Impact:** Shallower trees, fewer internal nodes, and faster traversal.
* **Backward Compatibility:** **Soft Breaking.** Older clients can *read* large leaves without issues (it's just a JSON array), but if they try to *write* to the tree, they might split a leaf that was intentionally kept large by a newer client, causing "structural oscillation."

### Subtree Inlining
* **Proposal:** Inline very small subtrees directly into the parent's `Children` array as raw data rather than separate content-addressed objects.
* **Impact:** Fewer discrete objects in the backing store, yielding faster garbage collection and syncs.
* **Backward Compatibility:** **Breaking.** Requires a schema update to `core.HAMTNode`. Older clients will fail to parse the new inlined structure.
