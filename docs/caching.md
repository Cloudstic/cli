# Caching

Cloudstic caches in several places, at different layers, with different
lifetimes. This document is the inventory: what each cache holds, how long it
lives, what bounds it, and — the question that keeps coming up — which of them
overlap.

The first version of this document claimed none was redundant. That was wrong
about one: `prune` memoized filemetas behind a `reachable` set that already
guaranteed one load per ref, so its cache could never return a hit. It has been
removed. The lesson is in how it was found — by counting hits rather than
reading the code, which is why the benchmarks in
`internal/engine/metaloader_bench_test.go` now exist.

None of these caches is persistent. Every one lives in process memory and dies
with the `Client` or the operation that created it. Nothing here is part of the
repository format, so changing or removing a cache is never a compatibility
event (`docs/compatibility.md`).

## The inventory

| Cache | Where | Key → value | Lifetime | Bound |
|---|---|---|---|---|
| `KeyCacheStore.knownDigests` / `knownKeys` | `internal/storelayer/keycache.go` | raw digest (fallback: object key) → exists | one `backup` run | every key under the preloaded prefixes |
| `PackStore.catalog` / `packKeys` | `internal/storelayer/pack.go` | object key → pack ref + offset | `Client` | one entry per packed object |
| `PackStore.packCache` | `internal/storelayer/pack.go` | pack ref → raw packfile bytes | `Client` | LRU, 4 packs (~32 MB at 8 MB/pack) |
| `NodeStore.cache` | `internal/hamt/nodestore.go` | node ref → decoded `*node` | `hamt.Tree` | LRU, 4096 nodes |
| `metaLoader.cache` | `internal/engine/metaloader.go` | filemeta ref → decoded `core.FileMeta` | manager | unbounded; enabled only for `backup` and `diff` |
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

## Which ones overlap

The pairs that look like they duplicate each other, and why each surviving one
is a distinct job. (The one that genuinely was redundant, `prune`'s, is gone —
see the note at the top.)

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

- `KeyCacheStore.knownDigests` holds every key under `chunk/`, `content/` and
  `node/` after `PreloadKeys` — millions of entries on a large repository, live
  for the length of one backup. This is the deliberate trade — one `List` per
  prefix instead of an `Exists` per object. The set is keyed by the raw
  32-byte digest decoded from each key's hex suffix, one map per preloaded
  prefix, rather than by the hex string itself (issue #430): a
  `map[[32]byte]struct{}` has no pointer in its key, so the GC does not scan
  it, and it does not need a separately retained string backing array the way
  a `map[string]struct{}` does. Measured via
  `BenchmarkKeySetShapeRetained` (`internal/storelayer/keycache_bench_test.go`)
  at 500k entries: 136 B/entry retained for the string-keyed set versus 84
  B/entry for the digest-keyed one. A key whose suffix isn't a well-formed
  64-hex-char digest falls back to `knownKeys`, keyed by the full string, so
  it is still tracked correctly rather than mis-filed or dropped.
- `metaLoader.cache`, where enabled, grows with the number of distinct
  filemetas an operation touches. Only `backup` and `diff` enable it, because
  they are the two that read the same ref more than once — `diff` walks both
  roots, and an unchanged file keeps its filemeta from one snapshot to the
  next. `ls`, `restore`, `find` and `prune` read through.

Two operations take an uncached loader for reasons worth stating, since both
look at first glance like they should memoize:

- `ls` walks a single root. A HAMT key derives from `meta.FileID`, which is
  itself a `FileMeta` field, so no two keys share a filemeta ref and the walk
  reaches every ref exactly once. A cache could not hit.
- `prune` guards every load behind its `reachable` set — `markFileMeta` returns
  early for a ref it has already marked — so it too reads each ref at most once
  per run, no matter how many snapshots share it. This was measured, not
  assumed: over four snapshots of one 2000-file tree the loader ended with 2000
  entries and zero hits.

### Why an LRU is the wrong bound here

The obvious fix for an unbounded cache is to cap it the way `NodeStore` caps
its node cache at 4096. Measurement says otherwise, and the reason generalises.

`NodeStore` works under a bound because a HAMT descent re-touches the root and
the upper levels on every single lookup — real temporal locality, and a small
cache captures nearly all of it. A filemeta traversal has none: it sweeps each
ref exactly once per snapshot, uniformly. Under a cyclic sweep, LRU evicts
precisely the entry the next sweep is about to ask for, so the hit rate does
not degrade gracefully — it collapses.

Hit rate over eight sweeps, from `BenchmarkMetaLoaderDiffPattern`:

| working set | cache 1024 | cache 4096 | cache 16384 |
|---|---:|---:|---:|
| 1,000 files | 87.5% | 87.5% | 87.5% |
| 10,000 files | 0.0% | 0.0% | 87.5% |

At 10,000 files a 4096-entry cache turns 10,000 store reads into 80,000. Any
fixed bound has this cliff; it only moves with the number. So the cache stays
unbounded, and the memory was taken out of the traversals instead — see below.

### Measured footprints

From `BenchmarkMetaLoaderRetained` and the tests in
`internal/engine/metaloader_memory_test.go`. "Before" is the state prior to the
two changes described above: `prune` memoizing, and `diff` retaining every entry
in its parent lookup rather than only folders.

`prune`, heap retained after a run:

| files | before | after |
|---|---:|---:|
| 5,000 | 3.3 MB | 1.3 MB |
| 20,000 | 12.7 MB | 2.9 MB |
| 50,000 | 28.1 MB | 4.0 MB |
| 100,000 | 54.6 MB | 6.7 MB |

`diff`, peak heap while both parent lookups are live, on a tree with one folder
per twenty files:

| files | parent entries before | after | peak before | peak after |
|---|---:|---:|---:|---:|
| 5,000 | 10,504 | 504 | 11.7 MB | 8.6 MB |
| 20,000 | 42,004 | 2,004 | 67.8 MB | 55.5 MB |
| 50,000 | 105,004 | 5,004 | 148.2 MB | 119.3 MB |
| 100,000 | 210,004 | 10,004 | 275.5 MB | 217.6 MB |

Neither change costs a single extra store read, which is what distinguishes
them from bounding the cache.

`diff`'s remaining peak is dominated by the loader cache, which is `O(tree)` by
design for the reason given above. Reducing it further means not walking both
roots in full, which is a change to the algorithm rather than to a cache.

## Invalidation

Almost none is needed, and that is a property of the format rather than luck.
Everything under `chunk/`, `content/`, `filemeta/`, `node/` and `snapshot/` is
content-addressed: the key is a hash of the bytes, so a ref's value can never
change and a cached entry can never go stale.

The exceptions are the mutable keys, and they are handled explicitly:

- `KeyCacheStore.Put` only elides writes for keys under a listed
  content-addressed prefix (`contentAddressedPrefix`). Mutable keys such as
  `index/latest` are always written through.
- `KeyCacheStore.Delete` and `PackStore`'s delete and repack paths evict.
- `secretref.Resolver` caches only the interactive native backends, where the
  cost is an OS re-prompt. `env://`, file-backed and `config-token` refs are
  re-read every time, since they are cheap and may legitimately change within a
  process.
