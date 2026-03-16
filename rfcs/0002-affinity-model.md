# RFC 0002: Affinity Model (Locality-Preserving Keys)

* **Status:** Implemented
* **Date:** 2026-03-07
* **Related:** [RFC 0001](./0001-hamt-evolution.md)

## Abstract

This document specifies a locality-preserving keying scheme for the Hash Array Mapped Trie (HAMT) to reduce metadata bloat and path amplification during incremental backups.

## 1. Context

Currently, `hamt.computePathKey(id string)` produces a HAMT routing key by hashing the raw `FileID` alone:

```go
// internal/hamt/hamt.go
func computePathKey(id string) string {
    return core.ComputeHash([]byte(id)) // SHA-256 hex string
}
```

SHA-256 produces uniformly distributed output, which is ideal for collision resistance but **catastrophic for locality**. Files sharing the same parent directory end up in entirely unrelated subtrees. Consider a directory with `N` modified files: every file's routing key starts with a statistically independent 5-bit prefix, so updates fan out across all 32 top-level buckets of the trie. This forces `O(N · depth)` intermediate node rewrites on every incremental backup.

### Routing Mechanics

The current trie has these constants:

| Constant        | Value | Effect                                      |
| :-------------- | ----: | :------------------------------------------ |
| `bitsPerLevel`  |     5 | 32 children per internal node               |
| `branching`     |    32 | —                                           |
| `maxDepth`      |     6 | Maximum trie depth                          |
| `maxLeafSize`   |    32 | Entries per leaf before forced split        |

`indexForLevel` extracts routing from the **first 8 hex characters (32 bits)** of the key at each level, consuming 5 bits per level. With 6 levels, only 30 bits of the 256-bit hash are used for internal routing; the remaining bits serve as collision disambiguation at the leaf level.

## 2. Proposed Key Format

Bias the HAMT key so that files sharing a parent directory group into a common trie subtree:

```
AffinityKey(parentID, fileID) = SHA256(parentID)[:4] + SHA256(fileID)[4:]
```

Where:

* `[:N]` denotes the first `N` hex characters of the SHA-256 hex string.
* `SHA256(parentID)[:4]` = **16 bits** (2 bytes) of parent-derived entropy.
* `SHA256(fileID)[4:]` = **240 bits** (30 bytes) of file-local entropy.

Full key length remains 64 hex characters, identical to the current `computePathKey` output length, so the rest of the routing machinery (`indexForLevel`, `insertAt`, `lookupAt`, etc.) is unchanged.

### Locality Guarantee

Because routing consumes the first 32 bits (8 hex chars) of the key, and the parent prefix occupies the first 16 bits (4 hex chars):

* **The top 3 trie levels** (consuming bits [31..17]) are determined entirely by the parent's hash prefix. All siblings share an identical path through these 3 levels.
* **Levels 4–6** are determined by the file's own hash, uniquely distributing siblings within their shared subtree.

In concrete terms: **a backup of a directory with `N` files now writes to a single O(1) subtree root instead of up to `N` distinct subtree paths.** The number of rewritten internal nodes during an incremental backup of a flat directory collapses from `O(N · maxDepth)` to `O(maxDepth)`.

### What "ParentID" Means

In `core.FileMeta`, `Parents` is `[]string` of raw source identifiers (for example, Google Drive folder IDs or normalized local parent paths), not `filemeta/<sha256>` object references. For the affinity key, `parentID` is this raw source-level parent identifier so keys remain stable across snapshots.

For sources (like local filesystems) where files can have multiple parents, use the **primary parent** (index 0 of the parent list, or the closest filesystem ancestor) for the key construction.

## 3. Required Changes

### 1. `computePathKey` in `internal/hamt/hamt.go`

Introduce a new key constructor and update the call sites:

```go
// AffinityKey produces a locality-preserving HAMT routing key.
// parentID is the raw source-level parent identifier (e.g. GDrive folder ID).
// fileID is the raw source-level file identifier.
func AffinityKey(parentID, fileID string) string {
    parentHash := core.ComputeHash([]byte(parentID))
    fileHash   := core.ComputeHash([]byte(fileID))
    return parentHash[:4] + fileHash[4:]  // 32-char total; same as current key length
}
```

All call sites (`Insert`, `Lookup`, `Delete`) must receive the parent context alongside the key. The `Tree` API needs a corresponding update:

```go
// Before:
func (t *Tree) Insert(root, key, value string) (string, error)

// After (HAMTv2):
func (t *Tree) Insert(root, parentID, fileID, value string) (string, error) {
    pathKey := AffinityKey(parentID, fileID)
    return t.insertAt(root, pathKey, fileID, value, 0)
    //                                   ^ LeafEntry.Key remains the raw fileID
}
```

Note that `LeafEntry.Key` continues to store the **raw `fileID`** — it is the logical key used for exact-match lookups. The path key is only the routing index into the trie, not the stored identity.

### 2. Snapshot Format Version

No separate snapshot version field was required for this rollout. Compatibility is handled at the HAMT leaf-entry level: `LeafEntry.PathKey` stores the routing key used by newer writers, while legacy entries without `PathKey` are still handled by fallback logic during reads/diff/walk.

## 4. Trade-offs and Constraints

### File Moves

If a file moves to a new parent directory, its affinity key changes:
`AffinityKey(oldParent, fileID) ≠ AffinityKey(newParent, fileID)`

The engine must perform an **explicit `Delete(oldKey)` + `Insert(newKey)`** pair. Backup sources that emit delta events (e.g., Google Drive's change tokens) already surface moves as distinct events, so this pairs naturally with the existing incremental backup loop.

For sources without explicit move detection, a fallback scan-and-reconcile remains available: if `Lookup(AffinityKey(currentParent, fileID))` fails but `Lookup(legacyKey(fileID))` succeeds (or a full-tree walk finds the entry), a re-key migration can be triggered.

### Hash Collisions in the Parent Prefix

With a 16-bit parent prefix, the probability of two distinct directories sharing the same 4-char prefix is `N²/2¹⁶` (birthday bound). For a repository with up to 65,536 distinct directories, collision probability remains below 50%. For very large repositories, the parent prefix length can be increased to 6 hex chars (24 bits) at the cost of allocating fewer bits to file-local entropy, if desired.

### Lookup Without Context

Current `Lookup(root, key)` only needs `fileID`. With the affinity model, a lookup requires `parentID` too. This is always available to the engine (which holds the full `FileMeta` context) but must be factored into any public-facing API.

## 5. Backward Compatibility

Implemented as a rolling-compatible change:

1. New writes use affinity routing (`AffinityKey(parentID, fileID)`) and store `PathKey` in leaf entries.
2. Older trees remain readable because legacy entries without `PathKey` fall back to `computePathKey(fileID)` in read/diff paths.

This avoids a repository-wide migration step and does not require a dedicated `Snapshot` format field.
