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
| `index/`     | Mutable pointers (`latest`, `snapshots` catalog)      |

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
| `index/snapshots` catalog      | Catalog stale, snapshot still valid    | None        |

In every case the previous `index/latest` still points at a fully valid
snapshot with a complete, consistent tree.

### Individual object atomicity

- **B2 (Backblaze):** Incomplete uploads are not visible. An object is only
  readable after the upload completes successfully.
- **Local filesystem:** `Put` writes to a `.tmp` file and renames atomically
  (`os.Rename`), which is atomic on POSIX systems.

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
  If the chunk is already stored, the write is skipped.
- **Content level:** Before streaming a file, `Exists("content/<hash>")` is
  checked using the source-provided content hash (e.g. Drive MD5). If the
  content object exists, the entire file upload is skipped.

This means a "new" file with identical content to a previously backed-up file
produces zero additional chunk/content bytes — only a new filemeta and
possibly new HAMT nodes are written.
