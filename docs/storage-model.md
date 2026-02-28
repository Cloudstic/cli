# Storage Model & Crash Safety

## Object Types

Every piece of data in a Cloudstic repository is a content-addressed object
stored under a key derived from its hash. Objects are immutable once written.

| Prefix       | Description                                           |
|--------------|-------------------------------------------------------|
| `chunk/`     | Compressed file data segments (gzip, FastCDC boundaries) |
| `content/`   | Manifest listing the chunk refs that make up a file   |
| `filemeta/`  | File metadata (name, size, mod time, content hash)    |
| `node/`      | HAMT tree nodes (directory structure)                 |
| `snapshot/`  | Root object tying a tree to a point in time           |
| `index/`     | Mutable pointers (`latest`, `snapshots` catalog)       |

## Write Order During Backup

A backup writes objects bottom-up, from raw data to the root pointer:

```
1. chunk/*        – file content segments (parallel, during upload phase)
2. content/*      – per-file chunk manifests
3. filemeta/*     – file metadata referencing its content hash
4. node/*         – HAMT tree nodes (buffered in memory, flushed at the end)
5. snapshot/*     – snapshot object referencing the HAMT root
6. index/latest   – mutable pointer updated to the new snapshot
7. index/snapshots – catalog updated with the new snapshot summary
```

The commit point is step 6: until `index/latest` is updated, the previous
backup state is fully intact.

## Crash Safety

Because all data objects are content-addressed and append-only, an interrupted
backup **cannot corrupt existing data**. A partial write can never overwrite
or modify an object that was already stored.

### Interruption scenarios

| Interrupted during             | Effect                                | Risk        |
|--------------------------------|---------------------------------------|-------------|
| Chunk / Content / FileMeta     | Orphaned blobs in store               | None        |
| HAMT Flush                     | Orphaned node + blob objects          | None        |
| Snapshot write                 | Orphaned snapshot + all its objects    | None        |
| `index/latest` update          | New snapshot exists but isn't "latest" | None        |
| `index/snapshots` catalog      | Catalog stale; self-heals on next read | None        |

In every case the previous `index/latest` still points at a fully valid
snapshot with a complete, consistent tree.

### Individual object atomicity

- **B2 (Backblaze):** Incomplete uploads are not visible. An object is only
  readable after the upload completes successfully.
- **S3 / S3-compatible:** Same as B2 — objects become visible only after the
  upload completes.
- **SFTP:** `Put` writes to a `.tmp` file and renames via `PosixRename`,
  which is atomic on most SFTP server implementations.
- **Local filesystem:** `Put` writes to a `.tmp` file and renames atomically
  (`os.Rename`), which is atomic on POSIX systems.

## Snapshot Catalog (`index/snapshots`)

The `index/snapshots` object is a **best-effort cache** that stores lightweight
summaries of all snapshots. It exists purely to speed up operations like `list`
and `forget` — avoiding the need to fetch and deserialize every individual
`snapshot/*` object.

This catalog is **self-healing**. Every time it is read, the engine reconciles
it against the live `snapshot/` key listing:

- **Missing entries** (snapshot exists on disk but not in the catalog) are
  fetched and added automatically.
- **Stale entries** (entry in catalog but snapshot was deleted) are removed.

If the catalog is lost, corrupted, or out of date — for example because a
backup was interrupted after writing the snapshot but before updating the
catalog — it is transparently rebuilt on the next read. No manual intervention
is required.

The source of truth is always the set of `snapshot/*` keys in the store.
The catalog is a disposable acceleration layer on top.

## File-Meta Catalog (`index/filemeta`)

The `index/filemeta` object is a **best-effort cache** that maps every
`filemeta/<hash>` ref to its full `FileMeta` value. It is stored as a JSON
object (key → value) so it doubles as an in-memory lookup table once loaded.

This catalog is **self-healing**. Every time `LoadFileMetaCache` is called it:

1. **Lists** all live `filemeta/` keys (cheap — key names only).
2. **Loads** the cached catalog (one `Get`).
3. **Reconciles:**
   - key exists + in cache → trust the cached value (zero cost).
   - key exists + not cached → fetch from store, add to cache.
   - key gone + in cache → drop from cache.
4. **Flushes** the updated catalog back to the store if anything changed.

On a steady-state repo with 10 000 files and 10 changes since the last run
the cost is: 1 `List` + 1 `Get` (catalog) + 10 `Get` (new entries) + 1 `Put`
= 13 store operations — instead of 10 000 individual fetches.

During backup, newly created file-meta objects are also eagerly merged into
the catalog via `AddFileMetasToIndex` so the cache is already warm for the
next command.

The source of truth is always the set of `filemeta/*` keys in the store.
The catalog is a disposable acceleration layer on top.

## Garbage Collection (Prune)

Prune performs a mark-and-sweep to reclaim space from orphaned objects:

1. **Mark** — walk every `snapshot/*` key, then follow the chain
   snapshot → HAMT nodes → filemeta → content → chunks, collecting all
   reachable keys.
2. **Sweep** — list all keys under each object prefix and delete any key
   not in the reachable set.

Running prune after an interrupted backup will delete all orphaned objects and
restore the repository to a clean state. No data from completed snapshots is
affected.

### Edge case: snapshot written, index not updated

If the interruption occurs between writing the snapshot and updating
`index/latest`, the snapshot object exists under `snapshot/` and is therefore
**reachable** during prune's mark phase. It will survive garbage collection as
a valid, complete snapshot.

## Deduplication

Dedup is content-addressed at two levels:

- **Chunk level:** Before writing a chunk, `Exists("chunk/<hash>")` is checked.
  If the chunk is already stored, the write is skipped. When encryption is
  enabled, the chunk hash is an **HMAC-SHA256** keyed by a dedup key derived
  from the encryption key. This prevents the storage provider from confirming
  file contents by hashing known plaintext. When encryption is disabled, plain
  SHA-256 is used.
- **Content level:** Before streaming a file, `Exists("content/<hash>")` is
  checked using the source-provided content hash (e.g. Drive MD5). If the
  content object exists, the entire file upload is skipped.

This means a "new" file with identical content to a previously backed-up file
produces zero additional chunk/content bytes — only a new filemeta and
possibly new HAMT nodes are written.
