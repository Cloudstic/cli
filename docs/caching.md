# Caching

Cloudstic caches in several places, at different layers, with different
lifetimes. This document is the inventory: what each cache holds, how long it
lives, what bounds it, and — the question that keeps coming up — why none of
them is redundant with any other.

None of these caches is persistent. Every one lives in process memory and dies
with the `Client` or the operation that created it. Nothing here is part of the
repository format, so changing or removing a cache is never a compatibility
event (`docs/compatibility.md`).

## The inventory

| Cache | Where | Key → value | Lifetime | Bound |
|---|---|---|---|---|
| `KeyCacheStore.knownKeys` | `internal/storelayer/keycache.go` | object key → exists | one `backup` run | every key under the preloaded prefixes |
| `PackStore.catalog` / `packKeys` | `internal/storelayer/pack.go` | object key → pack ref + offset | `Client` | one entry per packed object |
| `PackStore.packCache` | `internal/storelayer/pack.go` | pack ref → raw packfile bytes | `Client` | LRU, 4 packs (~32 MB at 8 MB/pack) |
| `NodeStore.cache` | `internal/hamt/nodestore.go` | node ref → decoded `*node` | `hamt.Tree` | LRU, 4096 nodes |
| `metaLoader.cache` | `internal/engine/metaloader.go` | filemeta ref → decoded `core.FileMeta` | manager | unbounded; opt-in per constructor |
| `findScanner.evaluated` | `internal/engine/find_scan.go` | 16-byte ref digest → match verdict | one `find` run | one small entry per distinct filemeta ref |
| `Resolver.cache` | `pkg/secretref/secretref.go` | `scheme://path` → secret | `Resolver` | only `keychain`, `wincred`, `secret-service` |
| `Client.repoIDCache`, `openCfg` | `client.go` | — → repository marker fields | `Client` | one value each |

Three things in the engine look like caches in a listing but are not, and are
covered below so nobody unifies them with one: `BackupManager.newMetas`,
`BackupManager.pendingMetas`, and `CopyManager`'s remap tables.

## Where they sit

The store chain assembled by `NewClient` is:

```text
CompressedStore → EncryptedStore → MeteredStore → [PackStore] → <backend>
                                                       │
                                          catalog + packCache
```

`KeyCacheStore` is **not** in that chain. `NewClient` never builds one.
`BackupManager` wraps the whole assembled chain in one from above, for the
duration of a single run:

```text
KeyCacheStore → CompressedStore → EncryptedStore → MeteredStore → [PackStore] → <backend>
```

That position is the point. A `Put` of a content-addressed key the cache already
knows returns without touching anything below it, so the object is never
compressed, never encrypted, and never packed. Sitting below any of those layers
would mean paying for the work before discovering it was unnecessary.

The engine's own caches sit above the chain entirely, holding decoded objects:
`NodeStore` for `node/`, `metaLoader` for `filemeta/`.

## Why none of them is redundant

The pairs that look like they overlap, and why each is a distinct job.

### `KeyCacheStore` vs `PackStore`'s catalog

Both can answer "does key X exist" without a network call, so this is the pair
worth being careful about. They are not interchangeable:

- The pack catalog only knows **packed** objects, and only gives **positive**
  answers. A key it has never heard of falls through to a backend `Exists`.
- `KeyCacheStore` also gives **negative** answers: it records which prefixes it
  has listed, so a miss under a listed prefix is a definitive "absent" with no
  downstream call. That is precisely the question backup's dedup check asks
  millions of times, and the reason the round trip disappears.
- `KeyCacheStore.Put` **elides the write entirely** for a known
  content-addressed key, and uses `singleflight` so concurrent writes of the
  same key collapse into one. The pack catalog has no equivalent; it is an
  index, not a write gate.

### `NodeStore.cache` vs `PackStore.packCache`

Different granularity at different layers. A `NodeStore` hit returns an
already-decoded node and skips the entire store chain. A miss that lands in
`packCache` still avoids the network but pays decompression, decryption and
JSON decoding. Nodes are small and heavily re-visited during a walk; packs are
8 MB blobs holding thousands of unrelated objects. Neither subsumes the other:
4 cached packs cannot keep a hot node resident, and 4096 cached nodes do not
help the next `content/` read that happens to share a pack.

The same reasoning applies to `metaLoader.cache` against `packCache`.

### `metaLoader.cache` vs `NodeStore.cache`

No overlap at all — different object types. `NodeStore` holds `node/` objects,
`metaLoader` holds `filemeta/` objects. A HAMT walk touches both, which is why
both exist.

### `findScanner.evaluated` vs `metaLoader.cache`

Deliberately inverted, and the one place the trade-off is explicit. `find`
constructs an **uncached** `metaLoader`, because a full scan crosses every
snapshot in the repository and holding each filemeta it decodes would grow
without bound. It memoizes the *verdict* instead — a few bytes keyed by a
truncated ref digest — keeping the metadata object only when the entry matched
or is a folder whose name is needed for path resolution. Caching both would
defeat the reason the loader is uncached.

## The three that are not caches

Listing them here because each has been mistaken for one.

**`BackupManager.newMetas`** holds filemetas produced during the current scan.
It is a staging area, not a cache: the bytes for these refs are still sitting in
`pendingMetas` and have not been written, so `metaLoader.load` would fail
outright for them. `lookupMetaByFileID` consults it first for exactly that
reason. Note also that the two hold *different values* for the same ref —
`newMetas` keeps the in-memory `FileMeta` including `Paths`, while the persisted
form written to the store has `Paths` stripped (`persistedFileMeta`).

**`BackupManager.pendingMetas`** is a write buffer: filemeta JSON awaiting a
batched, concurrent flush. It is drained and reset by `flushPendingMetas`.

**`CopyManager.chunkRefs` / `contentRefs` / `metaRefs`** are remap tables, not
caches of store content. A copy rebuilds the object graph under the
destination's key, so every reference changes; these record source ref →
destination ref so an object already rebuilt is not rebuilt again. `lastCopied`
is the same idea one level up, remembering per lineage which source tree
produced which destination tree so the next snapshot applies as a diff.

Likewise `hamt.Txn` holds dirty nodes in memory until `Commit`, which is a
write-deferral mechanism rather than a read cache: nodes superseded during the
transaction are never serialized at all.

## Memory characteristics worth knowing

Most of these are explicitly bounded. Two are not, and both are bounded by the
work rather than by a constant:

- `KeyCacheStore.knownKeys` holds every key under `chunk/`, `content/` and
  `node/` after `PreloadKeys`. On a large repository that is a set of millions
  of short strings, live for the length of one backup. This is the deliberate
  trade — one `List` per prefix instead of an `Exists` per object.
- `metaLoader.cache`, where enabled, grows with the number of distinct
  filemetas an operation touches. `backup`, `diff` and `prune` enable it
  because they cross several snapshots, where an unchanged file keeps its
  filemeta from one snapshot to the next and the same ref recurs. `ls`,
  `restore` and `find` do not.

`ls` is worth a note: it takes an uncached loader not to save memory but because
a cache there could never hit. A HAMT key derives from `meta.FileID`, which is
itself a `FileMeta` field, so no two keys share a filemeta ref and a single-root
walk reaches every ref exactly once.

## Invalidation

Almost none is needed, and that is a property of the format rather than luck.
Everything under `chunk/`, `content/`, `filemeta/`, `node/` and `snapshot/` is
content-addressed: the key is a hash of the bytes, so a ref's value can never
change and a cached entry can never go stale.

The exceptions are the mutable keys, and they are handled explicitly:

- `KeyCacheStore.Put` only elides writes for keys under a listed
  content-addressed prefix (`isContentAddressed`). Mutable keys such as
  `index/latest` are always written through.
- `KeyCacheStore.Delete` and `PackStore`'s delete and repack paths evict.
- `secretref.Resolver` caches only the interactive native backends, where the
  cost is an OS re-prompt. `env://`, file-backed and `config-token` refs are
  re-read every time, since they are cheap and may legitimately change within a
  process.
