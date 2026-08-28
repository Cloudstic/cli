# RFC 0026: Repository Format v2 (Fat Leaves, No Packfiles)

- **Status:** Proposed
- **Date:** 2026-08-28
- **Affects:** repository format (major version), `internal/hamt`,
  `internal/storelayer`, `internal/engine`, `internal/core`, `pkg/store`,
  `docs/compatibility.md`, `scripts/benchmark`
- **Supersedes:** [RFC 0023](0023-bounding-the-pack-catalog.md) (closed),
  [RFC 0024](0024-metadata-in-the-tree.md) (absorbed),
  [RFC 0025](0025-traversal-order-and-pack-contiguous-reads.md) (partially —
  §1's derived traversal order survives; everything pack-specific does not).
  [RFC 0018](0018-self-describing-packfiles.md) becomes a v1-only document.

## Abstract

Format v1 mints roughly 2.2 objects per file — a `filemeta/`, a `content/`,
and an amortised share of `node/` — and then bolts a transparent aggregation
layer (`PackStore`) underneath to make that affordable against object storage.
Three RFCs of read-side machinery have been compensating for the consequences:
an O(repository) catalog that must be resident, durable, concurrent-writer-safe
and rebuildable; a probe-and-promote heuristic cluster guessing an execution
order the store is never told; and a read cost that grows with the number of
backups a repository has taken rather than with the data being read.

This RFC proposes **format v2**, a breaking change:

- File metadata and small-file content move **into the HAMT leaves**, in a
  binary encoding, with leaves sized by a byte budget so that every stored
  object is large. Chunk manifests inline into the leaf entry, with a spill
  object only for very large files.
- The packfile layer is **removed entirely**. Nothing in v2 is small enough to
  need it, so the catalog, shards, footers, admission heuristics, group plans,
  heal and repack machinery are deleted rather than improved.
- The version gate is raised: v2 binaries refuse v1 repositories with an error
  naming the migration tool. Migration is a separate deliverable (see
  "Migration" below).

The claim to hold this RFC to: read cost proportional to bytes read and
independent of backup count, peak memory independent of repository size, and a
net deletion of several thousand lines. The unified benchmark harness (aging
folded into `bench.sh`, part of this RFC) is what verifies each claim against
the recorded v1 baselines.

## Context

The evidence is spread over three RFCs and is summarised here so this document
can stand alone; the originals keep the full measurement record.

**Object count is the root cause.** A snapshot of 50,000 files is ~109,600
objects, almost all tiny JSON documents (RFC 0024). Every structure the 2026
memory workstream spent months bounding — the pack catalog, the pack body
cache, `check`'s verified set, `prune`'s reachable set, `List`'s materialised
result — carries one entry per object. Each was attacked separately; each
attack measured as marginal because the cause was upstream of all of them.

**The pack layer is a filesystem grown inside the storage layer.** Objects are
files, `PackEntry` is an extent, the catalog is the allocation table, shards
are its write-ahead log, footers are superblock redundancy,
`healMissingCatalogLocked` is fsck, `Repack` is defrag. It carries the failure
modes of one, too: catalog loss must never read as an empty repository
(`docs/compatibility.md`), a failed pack upload must un-happen
(`discardPack`), shard merges must survive concurrent writers, and sitting
below `EncryptedStore` forces a separately derived index key whose omission is
a silent plaintext leak (`WithPackIndexKey`). That is ~2,500 lines of
non-test code and ~5,000 lines of tests in `internal/storelayer` alone.

**Read cost grows with backup count, not data.** Each backup seals its own
packs, so a snapshot's tree spans every backup that contributed to it.
Measured (RFC 0025): +1 pack and +9 requests per backup while the live pack
bytes fit `packBodyCacheBudget`, then a ~2.5x step into a penalized regime
once they do not — `check` at 80 backups of 200-file churn costs 1,867
requests against a ~130-request floor. The 9-request term is
`packPromoteAfter` probing: information the engine had and the store had to
rediscover one miss at a time, because the `ObjectStore` interface carries
keys but not intent.

**The compensation machinery hit its ceiling.** RFC 0025 built and measured
the read-side fixes available without a format change: declared read plans,
pack-contiguous grouping, admission from exact demand. Some shipped and paid;
streaming restore was built and abandoned (3x wall time, 6x transfers —
interleaving two namespaces evicts each phase's packs); the estimator the RFC
set out to delete turned out to be load-bearing. The conclusion of that
record, read together with RFC 0024's, is that the remaining wins are on the
write side: **produce few, large objects and the read side needs no policy at
all.**

### Prior art

The design space is well trodden, and v2 deliberately sits between two proven
points rather than inventing a third:

- **Kopia** is the v1 architecture done with more machinery: small contents
  packed into blobs (≤20 MB), index blobs mapping content ID → (blob, offset,
  length), a local index at the end of each pack for recovery — RFC 0018's
  footer, independently arrived at — and a persistent local cache of
  memory-mapped indexes to make the map cheap to hold. It works, and it is
  the road not taken here: it keeps the second address space and pays for it
  with an index-and-cache subsystem. RFC 0024 already rejected the sorted
  on-disk index for this codebase (hash-ordered lookups thrash a block
  cache), and v2 removes the mapping instead of engineering it.
- **Duplicacy** is the other endpoint: no packs, no central index at all.
  Everything is a CDC chunk — including the snapshot's file list, whose
  `files`, `chunks` and `lengths` sequences are chunked by the same
  variable-size algorithm as file content — and deletion is a two-step
  fossil-collection protocol instead of an indexed sweep. Its published
  rationale for dropping the index matches this RFC's: the index database is
  the complexity and the failure surface.

v2 takes duplicacy's conclusion (make every stored object a large,
self-sufficient, content-addressed unit; keep no location map) while keeping
the HAMT so that `FileID` identity, multi-parent files, and O(log n) sparse
updates from cloud change feeds survive — the properties a flat chunked
manifest gives up, and the reason v2 is "fat leaves" rather than "chunk the
file list".

### Why batch APIs cannot substitute for layout

Worth pinning, because "just batch the requests" is the natural objection to
treating object count as the root cause. No mainstream object store offers a
multi-object GET or PUT: S3, GCS, Azure and B2 all transfer exactly one
object's data per request (a ranged GET subsets one object; multipart upload
assembles one object). AWS "S3 Batch Operations" is an asynchronous job
service — a manifest plus a per-key COPY/DELETE/Lambda, minutes-to-hours of
latency, per-job pricing — built for storage administration, not for a read
path. Concurrency hides latency but every small object still bills and
round-trips as its own request. So the amplification of many small objects is
not fixable at the API layer, on any backend this tool targets: aggregation
has to happen in the data layout. v1 accepted that and packed below the store
interface; v2 accepts it and makes the objects themselves large.

The one direction batch APIs do exist is **delete**: S3 `DeleteObjects` takes
up to 1,000 keys per request (MinIO and B2's S3-compatible endpoint included),
Azure Blob Batch takes 256, GCS batches metadata and delete calls at 100. That
is exactly the shape of `prune`'s sweep, and v2 should use it — see §5.

## The decision this RFC records

v1's compatibility contract (`docs/compatibility.md`) makes backward
compatibility permanent and migration optional. RFC 0024 was written under
that constraint, which is why it proposed an opportunistic in-place layout
change and dual decoders held forever.

This RFC records the decision to break it once: **v2 is a new baseline, not a
new era in a mixed repository.** A v2 repository contains only v2 structures;
a v2 binary refuses a v1 repository outright rather than reading it; the
permanence rule re-applies from v2 onward. That decision is what turns RFC
0024's additive design into a subtractive one — no dual decoders, no
mixed-era leaves, no legacy catalog reader, and the pack layer deleted rather
than bypassed.

The costs are real and named: users must run a migration before a v2 binary
can read their data, and `docs/compatibility.md` must be amended to record the
one-time re-baselining. Both are accepted.

## Goals

- One design document for the v2 format, replacing the partial and partly
  withdrawn proposals in RFCs 0023–0025.
- Requests proportional to bytes: reading a snapshot costs one request per
  (large) object it references, independent of how many backups produced them.
- Peak memory independent of repository size for every operation, with the
  known `List` exception carried as an explicit non-goal.
- Delete the pack layer and its bookkeeping instead of improving it.
- A single benchmark harness whose axes cover both fresh-repository cost and
  aging, so the before/after is one report.

## Non-goals

- **The migration tool.** Its requirements are stated below; its design is its
  own RFC.
- **Streaming `ObjectStore.List`.** `prune`'s sweep still materialises every
  key; v2 shrinks that list ~12x but it stays O(objects). Public-interface
  change, separate RFC (carried over from RFC 0023's non-goals).
- **Changing the identity model.** `FileID` identity, multi-parent files,
  `AffinityKey` routing, and content-addressing by `ComputeJSONHash` /
  `ComputeHash` are untouched (RFC 0024's constraint section, which remains
  binding: path is layout, never identity).
- **Changing chunking of large-file content.** FastCDC chunks were never the
  problem; they are large and random-access by nature.

## Proposal

### 1. The v2 object model

| key | content | notes |
|---|---|---|
| `chunk/<hash>` | raw chunk data | unchanged |
| `node/<hash>` | HAMT spine node or fat leaf, binary | leaves carry metadata + small content |
| `content/<hash>` | chunk-ref spill list | rare; only for very large files |
| `snapshot/<hash>` | snapshot | unchanged shape, v2-stamped |
| `index/latest`, `index/snapshots` | mutable pointers/catalog | unchanged |
| `keys/<slot>`, `config` | key slots, repo marker | unchanged |

Gone relative to v1: `filemeta/*` (absorbed into leaves), `packs/*`,
`index/packs`, `index/packmap/*`, and `content/*` in its role as a per-file
manifest (it survives only as the spill form).

### 2. Fat leaves

The design is RFC 0024's, promoted from opportunistic layout change to the
only form:

- **Leaf entries carry the metadata itself** in a fixed-offset binary record
  (identity, parents, size, mtime, mode, content identity), with names in a
  per-leaf arena. `Lookup` returns the decoded entry; change detection
  compares the fields that define a change rather than a `filemeta/` ref.
- **Small-file content inlines into the entry**, up to `inlineContentMax`
  (default 64 KB, tunable — open question 2). For the `source` profile whose
  mean file is ~18 KB, this makes most files metadata-and-content in one
  object.
- **Chunked files inline their chunk refs** in the entry, up to
  `inlineRefsMax` (~16 KB, ~512 refs, ~2 GB of file at 4 MB average chunks).
  Beyond that the ref list spills to a `content/` object — which is then at
  least 16 KB itself, so the spill can never reintroduce a tiny-object
  population.
- **Leaves split on a byte budget, not an entry count.** Target
  `leafTargetSize` 256 KB, split at 512 KB (open question 1). Fifty thousand
  `source`-profile files serialise to a few thousand leaves instead of
  109,600 objects — and for metadata-only trees, to a few dozen.
- The leaf is content-addressed as a whole; per-entry addresses are not
  reintroduced. `check` verifies leaves, and every entry in one, by the one
  hash.

Deduplication granularity moves from per-file to per-leaf. What that costs on
an incremental is bounded by the byte budget — a changed file rewrites its
leaf and the dirty spine above it — and is one of the numbers the harness
gates (see "Performance targets").

### 3. The store chain without packs

```text
CompressedStore → EncryptedStore → MeteredStore → <backend>
```

`PackStore` is not in the v2 chain and is deleted along with:

- `packcatalog.go`, `packshard.go`, `packfooter.go`, `packadmission.go`,
  `packbodycache.go`, `packgroup.go`, `pack.go` and their tests;
- `WithPackIndexKey` and `crypto.HKDFInfoPackIndexV1` (the below-encryption
  index existed only because packs did);
- `internal/engine/packindex.go` and the post-lock consolidation dance;
- `CLOUDSTIC_DISABLE_PACKFILE`, `config.DisablePackfile`, and the pack-index
  compaction threshold.

What this buys beyond code: the concurrent-writer story collapses to what it
already was for everything else (immutable content-addressed objects plus the
existing `index/latest` / `index/snapshots` reconciliation); zstd now
compresses leaves as wholes, where sibling metadata records and name arenas
share long runs; and there is no rebuildable-cache tier whose loss must be
distinguished from emptiness.

`KeyCacheStore` survives unchanged — backup's "a key I already know needs no
write" short-circuit is independent of packing. `ReadPlanner`/`ReadPlan`
(`pkg/store`) exist only to feed pack grouping and are removed from the public
interface; RFC 0022's docs-pairing rule applies.

### 4. The read path

RFC 0025 §1 — deriving the traversal order from the routing key instead of
holding O(files) plans — survives and matters more, not less: leaf routing by
`AffinityKey(primaryParentID, FileID)` keeps a directory's entries in few
leaves, so a directory-order traversal touches each leaf once. What dies with
the packs is everything that existed to *guess* what to keep resident:
`packPromoteAfter`, `packPenalized`, `packAdmission`, the LRU body cache and
its budget, `PlanReads` and the single shared plan. A leaf is fetched once,
decoded, consumed, and released; there is nothing to admit, promote, or
penalize, because there is no second address space to manage.

`restore` reads: one snapshot object, the spine, the leaves (content for most
files arrives inside them), and chunks for large files — every one a single
request for a large object, and none of it dependent on which backup wrote
what. The two-phase metadata/content separation that made streaming restore
regress (RFC 0025 §8) disappears with the class distinction, which is exactly
the seam RFC 0024 predicted.

`prune` marks over ~12x fewer objects and sweeps a ~12x shorter list. Waste
from superseded leaves is ordinary garbage — unreferenced objects deleted by
the existing mark-and-sweep — not fragmentation inside a shared container, so
`Repack` and its durability ordering go away entirely.

### 5. Batched deletes

With packing gone, `prune` deletes standalone objects again, one request
each — the only per-object request pattern v2 leaves in place. Object stores
batch exactly this direction (see "Why batch APIs cannot substitute for
layout"): a new optional capability interface in `pkg/store`,

```go
// BatchDeleter deletes many keys in as few backend requests as the
// backend allows. Implementations report per-key failures; a caller
// must not treat a partial error as "all deleted".
type BatchDeleter interface {
    DeleteAll(ctx context.Context, keys []string) error
}
```

implemented by `s3.Store` over `DeleteObjects` (1,000 keys per request) and by
`local`/`sftp` as a loop, following the `RangeGetter` pattern: optional,
conformance-asserted in `storetest`, and forwarded by wrappers. `prune`'s
delete phase uses it when present. On an S3-family backend this takes the
sweep's delete cost down ~1,000x; it is deliberately a capability, not a
contract, so a custom backend without it keeps working unchanged.

## Performance targets

The point of this RFC is a breakthrough, not an improvement, so the targets
are stated against recorded v1 numbers and verified by the unified harness
(next section). All at the `source` profile, 5,000 files, 200-file churn,
MinIO, matching the RFC 0025 aging series:

1. **Aging flatness.** `restore` and `check` of the latest snapshot after 80
   backups cost ≤ 1.2x their cost after 1 backup, in requests and in bytes
   transferred. v1 measured 139 → 2,118 restore requests over that range.
1. **Requests proportional to objects.** `check` at 80 backups within 2x of
   the object count a fresh v2 repository of the same tree holds. v1: 1,867
   requests against a ~130 floor.
1. **Memory off the repository axis.** Peak RSS for `backup`, `check`,
   `restore`, `prune` flat from 5,000 to 50,000 files within the measured
   noise band, `prune`'s `List` term excepted and reported. v1 grows on every
   operation (RFC 0023's context table).
1. **The accepted cost, bounded.** `backup-incremental-1` upload volume ≤ 2x
   v1's, and `backup-incremental-1000` ≤ 1.5x — the per-leaf dedup loss RFC
   0024 worked through, now gated rather than estimated. If tuning
   `leafTargetSize` cannot hold both this and target 1, that tension goes back
   to this RFC before the default is chosen.
1. **Fewer round trips before the first byte.** `backup-incremental-noop`
   request count strictly below v1's, which pays 1 + shard-count reads to load
   a catalog v2 does not have.

A target missed is a design input, not a rounding error to explain away: the
harness rows go in the PR that misses them.

## Measurement: one harness, aging included

`bench.sh` and `aging.sh` measured the same pipeline with different axes —
fresh-store matrix versus backup count — and could not see each other's
regressions. They are merged (landing with this RFC, ahead of any format
work): `aging.sh` is deleted and `bench.sh` gains an optional aging stage.

- `AGE_CHECKPOINTS="1 10 40 80"` ages one repository per
  (profile, size, backend) cell with `AGE_CHURN` files of churn per backup,
  and measures `AGE_OPS` (default `restore check`) at each checkpoint.
- Rows land in the same CSV with `packs`, `backups` and `policy` columns;
  aging operations are labelled `restore@40` so `benchreport` renders each
  checkpoint as its own row instead of averaging the curve away.
- The `POLICIES` axis (several env-variable variants read the same aged
  repository, RFC 0025 §7) carries over — aging a repository twice ages two
  different repositories, so comparing builds any other way measures pack
  nondeterminism, not the change.
- `BENCH_CLOUDSTIC_BIN` is honoured without rebuilding, so a probe build is
  never silently replaced by the working tree.

Unset, `AGE_CHECKPOINTS` leaves `bench.sh` exactly as CI runs it today.
Baseline v1 sweeps — the full matrix plus aging to 80 — are captured and
committed to `benchmark-results/` **before the first v2 PR merges**; they are
the numbers the targets above are scored against.

## Compatibility and migration

This section deliberately inverts the usual one.

- **Version gate.** `core.RepoFormatVersion = 2` and
  `core.MaxSupportedRepoFormat = 2`. `LoadRepoConfig` in a v2 binary meeting
  `config.version: 1` fails with an actionable error naming the migration
  path. It must never fall through to a partial read: v1 structures are not
  merely unsupported, they are absent from the binary.
- **Old binaries meet v2 safely today.** Every released build enforces
  `MaxSupportedRepoFormat = 1` at open, so a v1 binary refuses a v2 repository
  cleanly. Verified by running an old binary, per `docs/compatibility.md`,
  not by reasoning about it.
- **`docs/compatibility.md` is amended, not silently violated.** It gains a
  section recording the v2 re-baselining: the permanence rule holds within a
  major format, v1→v2 is the one crossing that requires migration, and the
  rule re-applies from v2 onward. The v1 fixture table moves with the v1 read
  paths (below).
- **Migration is a separate deliverable.** Requirements it must meet:
  streamed (never the whole repository in memory), resumable, verifying — a
  post-migration `check` over the v2 output before the v1 objects are
  touched — and shipped in a transition release that carries both stacks.
  Until it ships, v1 users stay on v1 releases; main drops the v1 read paths
  once the transition release is cut, and the v1 e2e fixtures move to
  asserting *refusal with the migration message* rather than readability.

## Testing strategy

- Golden fixtures for the v2 leaf and snapshot encodings from the first PR
  that writes them; `TestRootHashGolden`'s role transfers to the binary
  encoding.
- `e2e/feature_legacy_repo_test.go` keeps every committed baseline, with the
  assertion flipped as described above;
  `TestCompatibilityDocListsEveryFixture` follows the amended doc.
- The layering invariant tests (`packlayering_test.go`,
  `packdeclaredresidency_test.go`, …) are deleted with the layer; the chain
  assembly test shrinks to the three surviving decorators.
- The unified harness is part of the definition of done for every stage below:
  a stage that moves a target's number attaches the rows.

## Sequencing

Each stage is a PR or small series; the format flips only once.

1. **Harness first.** Merge aging into `bench.sh`, delete `aging.sh`, capture
   and commit the v1 baselines. (Lands with this RFC.)
1. **v2 leaf encoding behind the gate.** Binary fat leaves, inline content,
   inline chunk refs + spill, byte-budget splitting — writable only when
   `init` is asked for format 2, which is not yet the default. The v2 chain
   is built without `PackStore` from the start.
1. **Engine on entries, not refs.** Change detection, `check`, `diff`, `find`
   read decoded entries; the derived traversal order (RFC 0025 §1) becomes
   the only read order.
1. **Tune and gate.** Run the harness matrix plus aging on v2; choose
   `leafTargetSize` and `inlineContentMax` against targets 1 and 4; record
   the sweep in this RFC's outcome section.
1. **Flip the default.** `init` writes v2; v1 write paths freeze.
1. **Cut the transition release, then delete.** With the migration tool
   shipped in the transition release, main removes `internal/storelayer`'s
   pack files, the v1 leaf/filemeta decoders, and the public `ReadPlanner`
   surface (docs PR paired, per RFC 0022).

## Open questions

1. **Leaf byte budget.** 256 KB target / 512 KB split is a guess with the
   right shape; targets 1 and 4 bound it from opposite sides and the harness
   decides. Bigger leaves also raise restore's decode granularity, which is
   free memory-wise only while a leaf is consumed then released.
1. **Inline content budget.** 64 KB default; the trade is leaf rewrite volume
   on churn against object count and restore requests. Measured on
   `backup-incremental-1` / `-1000` per RFC 0024's open question 1.
1. **Front-coding the name arena.** Sibling names share prefixes; zstd over
   the whole leaf may already capture most of it. Measure before adding a
   second encoding.
1. **Xattrs/ACLs.** Side table keyed by entry index, present when flagged
   (carried from RFC 0024).
1. **Cross-directory metadata dedup.** Two identical files in different
   directories shared a `filemeta/` object in v1 and do not share leaf bytes
   in v2. Quantify on the `mixed` profile; expected small against the object
   count win, but it should be a number.
1. **Does `index/snapshots` want the same treatment later?** It is already a
   reconciling cache; nothing here changes it, but a v2 follow-up could fold
   snapshot summaries into a leaf-like object. Out of scope now.

## References

External claims in this RFC, checked 2026-08-28:

- S3 `DeleteObjects` deletes up to 1,000 keys per request:
  [AWS API reference](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObjects.html).
- S3 Batch Operations is an asynchronous, manifest-driven job service
  (COPY/DELETE/Lambda per key, billions of objects per job), not a read-path
  API: [AWS user guide](https://docs.aws.amazon.com/AmazonS3/latest/userguide/batch-ops.html).
- Azure Blob Batch: ≤256 subrequests, and only Delete Blob / Set Blob Tier
  are supported:
  [Blob Batch REST API](https://learn.microsoft.com/en-us/rest/api/storageservices/blob-batch).
- Google Cloud Storage batch: ≤100 calls per batch, and explicitly no
  support for upload or download:
  [GCS batch documentation](https://docs.cloud.google.com/storage/docs/batch).
- Backblaze B2's S3-compatible endpoint implements `DeleteObjects`:
  [B2 S3-compatible API](https://www.backblaze.com/apidocs/s3-delete-objects).
- Duplicacy chunks the snapshot's `files`/`chunks`/`lengths` sequences with
  the same variable-size chunking as file content and keeps no central index:
  [DESIGN.md](https://github.com/gilbertchen/duplicacy/blob/master/DESIGN.md),
  [Lock-Free Deduplication](https://github.com/gilbertchen/duplicacy/wiki/Lock-Free-Deduplication).
- Kopia packs small contents into blobs with an ID→(blob, offset, length)
  index, a per-pack recovery index, and a memory-mapped local index cache:
  [architecture](https://kopia.io/docs/advanced/architecture/),
  [caching](https://kopia.io/docs/advanced/caching/).
