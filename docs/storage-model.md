# Storage Model & Crash Safety

## Object Types

Every piece of data in a Cloudstic repository is a content-addressed object
stored under a key derived from its hash. Objects are immutable once written.

| Prefix       | Description                                           |
|--------------|-------------------------------------------------------|
| `chunk/`     | Compressed file data segments (zstd, FastCDC boundaries) |
| `content/`   | Manifest listing the chunk refs that make up a file   |
| `filemeta/`  | File metadata (name, size, mod time, content hash)    |
| `blob/`      | Packed run of file bodies, members sealed individually (format v3) |
| `node/`      | HAMT tree nodes (directory structure)                 |
| `snapshot/`  | Root object tying a tree to a point in time           |
| `index/latest`     | Mutable pointer to the most recent snapshot            |
| `index/snapshots`  | Snapshot catalog (lightweight summaries, self-healing) |
| `index/packs`      | Pack catalog — offset map for objects inside packfiles  |

In a format-v3 repository the shape differs: `filemeta/` and `content/` do not
exist, because an entry's metadata rides in its HAMT leaf and its body lives in
a `blob/` object the entry points at. There is no packfile layer, so no
`packs/` or `index/packs` either.

`blob/` objects are written **below** the compression and encryption layers,
the position `PackStore`'s catalog and footers occupy, because each of a blob's
members carries its own compression and its own seal. That is what lets a
reader fetch one body's byte range and decrypt exactly it; sending a blob
through the chain would compress and encrypt the whole object a second time and
make a ranged read return bytes that cannot be decoded. See RFC 0026.

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

A format-v3 backup writes a different set, because metadata and small bodies
are no longer standalone objects:

```
1. chunk/*        – segments of files above the inline threshold
2. blob/*         – packed runs of file bodies, sealed when a budget fills
3. node/*         – HAMT nodes, whose leaves carry metadata and body references
4. snapshot/*     – snapshot object referencing the HAMT root
5. index/latest   – mutable pointer updated to the new snapshot
```

The ordering constraint is the same one step 6 expresses above, applied a level
down: **a blob is stored before any entry naming it is written**. An entry
referencing a blob that was never stored is a dangling reference, and a
snapshot carrying one is worse than a failed backup — so a body is not
considered placed until its blob has been put.

### Blob consolidation (format v3)

A blob stays live while any one of its bodies is still referenced, so without
a counter-measure a repository accumulates blobs that are mostly garbage. The
counter-measure is **not** repacking during prune: rewriting a blob an old
snapshot references either breaks that snapshot or needs an indirection layer
the format exists to avoid. Instead the *next backup* consolidates forward
(History-Aware Rewriting, Fu et al., USENIX ATC '14).

A full-scan v3 backup accumulates, per blob it inherits, the bytes the
snapshot it is writing still needs — the denominator comes free from
`BodyRef.Total`, which every referencing entry repeats. After the upload it
reads the live bodies of its sparsest blobs and hands them back to the same
blob writer, which packs them into new blobs, and repoints those entries. At
least two blobs are always merged: rewriting one blob's bodies into one new
blob would leave the snapshot reading as many objects as before. Three
properties bound it:

- **A blob is worth rewriting when its live bytes are below half of what a
  full blob delivers in this repository.** That covers both a blob whose
  members have mostly been superseded and one that was sealed small — the tail
  blob every incremental backup writes for its own churn, which is why blob
  count otherwise grows with the number of backups rather than with the data.
- **The work is bounded by a byte budget per backup**, not by a clock. What
  one backup does not reach, the next one does.
- **Old snapshots are untouched.** An entry's value is the content address of
  its metadata and does not change; only the body reference inside the leaf
  moves, in the new snapshot's own leaves. Older snapshots keep naming the
  blobs they always did and keep restoring byte for byte. The retired blobs
  become collectable once no retained snapshot references them, which is where
  the waste is actually reclaimed — see Garbage Collection below.

Consolidation is skipped for change-feed sources, whose scan visits only what
changed and so cannot say what a blob still holds; for a dry run, which writes
nothing; and for a backup asked to produce no snapshot when nothing changed
(`WithIgnoreEmptySnapshot`), whose contract it would otherwise break.

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
   reachable keys. In format v3 the chain is
   snapshot → HAMT nodes → the leaf entry's own references, which are its
   chunk refs and its `blob/` reference; there are no `filemeta/` or
   `content/` objects to follow.

   Two properties of the v3 mark are load-bearing. An entry whose references
   cannot be read fails the prune rather than counting as reaching nothing,
   since `docs/compatibility.md` forbids collecting garbage over data that
   could not be fully read. And an entry's references are marked *every* time
   it is walked, never skipped because its metadata ref has been seen before:
   identical metadata can be reached again in a later snapshot while pointing
   at a different blob, and skipping the second would sweep data a retained
   snapshot still needs.
2. **Sweep** — list all keys under each object prefix and delete any key
   not in the reachable set.
3. **Repack** — when packfiles are enabled, fragmented packs (more than 30%
   wasted space from deleted objects) are repacked: live objects are extracted,
   re-bundled into new packs, and the old packs are deleted.

   Format-v3 blobs are **never** repacked, here or anywhere else. A blob an
   old snapshot references cannot be rewritten without either breaking that
   snapshot or introducing an indirection layer, so a sparse blob is retired
   forward by the next backup instead (see Blob consolidation above) and
   collected here, unchanged, once no retained snapshot names it.

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
