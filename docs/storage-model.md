# Storage Model & Crash Safety

## Object Types

Every piece of data in a Cloudstic repository is a content-addressed object
stored under a key derived from its hash. Objects are immutable once written.

| Prefix       | Description                                           |
|--------------|-------------------------------------------------------|
| `chunk/`     | Compressed file data segments (zstd, FastCDC boundaries) |
| `content/`   | Manifest listing the chunk refs that make up a file   |
| `filemeta/`  | File metadata (name, size, mod time, content hash)    |
| `node/`      | HAMT tree nodes (directory structure)                 |
| `snapshot/`  | Root object tying a tree to a point in time           |
| `index/latest`     | Mutable pointer to the most recent snapshot            |
| `index/snapshots`  | Snapshot catalog (lightweight summaries, self-healing) |
| `index/packs`      | Pack catalog — offset map for objects inside packfiles  |

## Write Order During Backup

A backup writes objects bottom-up, from raw data to the root pointer:

```
1. chunk/*        – file content segments (parallel, during upload phase)
2. content/*      – per-file chunk manifests
3. filemeta/*     – file metadata referencing its content hash
4. node/*         – HAMT tree nodes (buffered in memory, flushed at the end)
5. snapshot/*     – snapshot object referencing the HAMT root
6. index/latest   – mutable pointer updated to the new snapshot
7. index/packs    – pack catalog updated (if packfiles are enabled)
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
| `index/packs` catalog          | Catalog stale or lost; rebuilt from packfile footers | None (see below) |

In every case the previous `index/latest` still points at a fully valid
snapshot with a complete, consistent tree.

`index/packs` needs a note. It records the offset and length of every packed
object, and a packed object has no object of its own at its logical key — so
losing the catalog would strand data that is otherwise intact. It is therefore
the one index that must be recoverable by another route.

Since RFC 0018 it is. Every packfile ends with a self-describing footer listing
its contents, which makes the catalog a *cache*: if it is missing, it is rebuilt
by listing `packs/` and reading footers, automatically and before any read is
served. That is the same relationship `index/snapshots` has with
`LIST snapshot/`.

Two things still hold:

- Packfiles written before RFC 0018 have no footer and cannot be recovered this
  way. A rebuild reports how many such packs it found rather than returning a
  silently partial catalog; `prune` rewrites them with footers over time.
- An unreadable catalog is not treated as a missing one. A read failure fails
  the operation rather than degrading to an empty catalog, because an empty
  catalog is indistinguishable from "nothing is packed" and would make `prune`
  treat every packed object as unreachable.

### Individual object atomicity

- **B2 (Backblaze):** Incomplete uploads are not visible. An object is only
  readable after the upload completes successfully.
- **S3 / S3-compatible:** Same as B2 — objects become visible only after the
  upload completes.
- **SFTP:** `Put` writes to a `.tmp` file and renames via `PosixRename`,
  which is atomic on most SFTP server implementations.
- **Local filesystem:** `Put` writes to a `.tmp` file and renames atomically
  (`os.Rename`), which is atomic on POSIX systems.

## Garbage Collection (Prune)

Prune performs a mark-and-sweep to reclaim space from orphaned objects:

1. **Mark** — walk every `snapshot/*` key, then follow the chain
   snapshot → HAMT nodes → filemeta → content → chunks, collecting all
   reachable keys.
2. **Sweep** — list all keys under each object prefix and delete any key
   not in the reachable set.
3. **Repack** — when packfiles are enabled, fragmented packs (more than 30%
   wasted space from deleted objects) are repacked: live objects are extracted,
   re-bundled into new packs, and the old packs are deleted.

Running prune after an interrupted backup will delete all orphaned objects and
restore the repository to a clean state. No data from completed snapshots is
affected.

### Batched deletion

The sweep hands its unreachable keys to the store in batches rather than one
at a time, through the optional `store.BatchDeleter` capability. Delete is the
one direction object stores batch — S3's `DeleteObjects` takes up to 1,000 keys
per request, and neither S3 nor any other backend offers a multi-object GET or
PUT — so on an S3-family backend the sweep costs one request per thousand
objects instead of one per object. A backend without the capability keeps
working unchanged: `store.DeleteAll` loops for it.

Batching does not loosen what prune may claim. `DeleteObjects` reports success
and failure per key in one response, and only the keys a store confirms gone
are counted as deleted or credited as space reclaimed. A key the backend
refused, and a key its response did not mention at all, both count as still
there. A sweep that could not delete every object it classified as garbage
deletes as many as it can and then **fails**, rather than reporting a success
whose object count and reclaimed total describe a repository that still holds
the garbage. The next run re-marks and re-sweeps, so failing costs nothing but
the exit code.

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

## In-process caching

Several layers cache in memory: the pack layer holds a catalog and an LRU of
recently read packfiles, the HAMT holds decoded nodes, and the engine holds
decoded filemetas. A `KeyCacheStore` wraps the whole chain from above during a
backup — it is not part of the chain itself.

See `docs/caching.md` for the full inventory, the lifetime and bound of each
cache, and why none is redundant with another.
