# RFC 0026: Repository Format v3 (Fat Leaves, No Packfiles)

- **Status:** Proposed. Stages 1–3 implemented behind `init -format 3`; see
  "Implementation outcome" for what the first measurements changed about the
  design.
- **Date:** 2026-08-28
- **Affects:** repository format (major version), `internal/hamt`,
  `internal/storelayer`, `internal/engine`, `internal/core`, `pkg/store`,
  `docs/compatibility.md`, `scripts/benchmark`
- **Supersedes:** [RFC 0023](0023-bounding-the-pack-catalog.md) (closed),
  [RFC 0024](0024-metadata-in-the-tree.md) (absorbed),
  [RFC 0025](0025-traversal-order-and-pack-contiguous-reads.md) (partially —
  §1's derived traversal order survives; everything pack-specific does not).
  [RFC 0018](0018-self-describing-packfiles.md) becomes a v2-only document.

## Abstract

Format v2 mints roughly 2.2 objects per file — a `filemeta/`, a `content/`,
and an amortised share of `node/` — and then bolts a transparent aggregation
layer (`PackStore`) underneath to make that affordable against object storage.
Three RFCs of read-side machinery have been compensating for the consequences:
an O(repository) catalog that must be resident, durable, concurrent-writer-safe
and rebuildable; a probe-and-promote heuristic cluster guessing an execution
order the store is never told; and a read cost that grows with the number of
backups a repository has taken rather than with the data being read.

This RFC proposes **format v3**, a breaking change. The number is the
`config.version` integer, and 3 is the next free one: today's builds already
stamp **2** (`core.RepoFormatVersion`, raised when the pack index was sealed
and sharded) and read every version up to 2 as one continuously-compatible
family. Throughout this document "v2" means that current packfile-era family
as a whole — everything a released build reads today — and "v3" means the
format proposed here:

- File metadata and small-file content move **into the HAMT leaves**, in a
  binary encoding, with leaves sized by a byte budget so that every stored
  object is large. Chunk manifests inline into the leaf entry, with a spill
  object only for very large files.
- The packfile layer is **removed entirely**. Nothing in v3 is small enough to
  need it, so the catalog, shards, footers, admission heuristics, group plans,
  heal and repack machinery are deleted rather than improved.
- The version gate is raised: v3 binaries refuse v2 repositories with an error
  naming the migration tool. Migration is a separate deliverable (see
  "Migration" below).

The claim to hold this RFC to: read cost proportional to bytes read and
independent of backup count, peak memory independent of repository size, and a
net deletion of several thousand lines. The unified benchmark harness (aging
folded into `bench.sh`, part of this RFC) is what verifies each claim against
the recorded v2 baselines.

## Implementation outcome

Stages 1–3 are built. What the measurements changed about the design is
recorded here rather than folded silently into the proposal, because two of
the constants below were wrong by more than an order of magnitude and the
reasons generalise.

**Splitting decides leaf fill, and 32-way splitting decided it wrong.** A leaf
that overflows is partitioned by the next routing bits, and its children never
merge back, so leaves settle at roughly *budget ÷ arity*. At the HAMT's
inherited 5 bits that is a thirty-second: leaves measured 13 KB against a
512 KB budget, and a repository held ~25x more objects than the format
intends. v3 therefore routes **2 bits per level**.

**Fill then hit its ceiling, and the ceiling is the split geometry.** At 2
bits a full leaf partitions into quarters that refill to the budget, so the
steady state runs between 25% and 100% — measured at 883 KB average and
1.07 MB median against a 2 MB budget, 44% and 53%. That is the floor of the
geometry, not slack left on the table, and narrowing further does not help:
1-bit routing raises the fill floor to 50% but buys a level of interior nodes
for every bit it stops routing, and measured *worse* overall (761 stored
objects against 700, check and restore unmoved). 3 bits is worse again (928
objects, check 904 → 1,268 requests). Arity is settled and there is no fill
left to recover, which leaves the byte budget as the only dial on how many
objects a repository holds.

**The budget was counting the wrong bytes.** It counts a leaf's *encoded*
size, while the 8 MB packfiles this format replaces are 8 MB *stored*. A leaf
passes through `CompressedStore` on its way out, and zstd takes a leaf of real
file content to roughly a fifth of its encoded size, so a 2 MB budget was
producing ~190 KB objects — 667 of them where the packfile format held 21.
That, and not leaves failing to fill, is where v3's request counts on a fresh
repository came from. The budget is now **8 MB**, the size the packfile layer
used for the same purpose.

**A cache bounded by entry count means nothing when entries are sized in
bytes.** The node cache held 128 entries, which at the sawtooth fill above was
~6 MB of a ~60 MB leaf set, so every per-entry lookup missed. It is now
bounded by bytes, and sized as a multiple of the leaf budget (32 of them)
rather than as a flat figure, because what it has to hold is a working set
counted in leaves. A v3 backup reads every leaf — change detection looks each
scanned entry up in the previous snapshot, in batches sorted by routing key,
so each batch sweeps the whole key space once — which makes the cache the
difference between one sweep and one per batch. `backup-dedup` was falling off
exactly that cliff: a 221 MB tree against a 128 MB cache re-read 438 distinct
nodes 1,911 times.

**Whole-file dedup of inline content was measured and rejected.** v2
deduplicates a small file by probing `content/<ref>`; v3 has no such object,
so a second file with the same bytes inlines a second copy. The only globally
content-addressed namespace v3 still writes is `chunk/`, so recovering the
behaviour means promoting duplicated content into a chunk and referencing it
from both entries. Implemented and measured, that trades bytes for requests in
the wrong direction: content inside a leaf is read for free by any operation
that reads the leaf, while content in a chunk costs its own request *once per
referencing file*. On `source` it saved 2% of stored bytes and cost restore
46% more requests (229 → 335, a chunk shared by nine files fetched nine
times); on `mixed`, 4.5% of bytes for 32% more restore requests (421 → 555).
Even a perfect chunk cache leaves restore worse than not promoting, because 70
distinct objects replaced content the leaves were being read for anyway. The
decision is therefore option 4 of issue #514 — inline content stays inline —
and what actually made `backup-dedup` expensive was the node-cache cliff and
the object count above, not the duplicated bytes.

**Reading a leaf twice is the same mistake as fetching a pack twice.** Three
paths did it: restore looked each file up separately after collecting
metadata, and check and prune each enumerated nodes and then walked entries.
Restore now writes in leaf order from the payload in hand (creating
directories first, in topological order), and `hamt.WalkTree` gives check and
prune one traversal.

**Sealing held the whole commit.** `Txn.Commit` built the entire write batch
before writing any of it — every dirty leaf's encoded bytes, inline content
included — which took an initial v3 backup's peak RSS to 561 MB against v2's
183 MB. The seal now streams into bounded flushes.

Measured on a 2,000-file `source` tree against MinIO, v3 beats v2 on restore
requests (918 vs 992), on bytes moved (restore 100 vs 345 MB, check 100 vs
252 MB), on check and prune wall time, on incremental upload volume (5.4 vs
8.1 MB), on stored size (77.1 vs 81.2 MB), and on peak RSS for every read
operation.

### Aging is the axis this format was for

The single-pipeline numbers above measure a repository with one backup in it,
which is the packfile layer's best case and the least representative one. The
aging stage holds the tree fixed and varies how many backups contributed to
the snapshot being read (2,000 files, 100 files of churn per backup, MinIO):

| after | v2 restore | v3 restore | v2 check | v3 check |
|---|---|---|---|---|
| 1 backup | 23 req, 8.1 MB | 52 req, 5.7 MB | 16 req, 8.1 MB | 82 req, 11.1 MB |
| 10 backups | 103 req, 12.5 MB | 53 req, 6.4 MB | 97 req, 12.5 MB | 83 req, 12.5 MB |
| 25 backups | 199 req, 20.4 MB | 52 req, 6.8 MB | 210 req, 20.5 MB | 82 req, 13.3 MB |
| 40 backups | 275 req, 29.2 MB | **92 req, 8.5 MB** | 308 req, 29.2 MB | **160 req, 16.2 MB** |

v2 grows about seven requests per backup — the linear term RFC 0025 measured
and could not remove, because a snapshot's entries scatter across every pack
that ever contributed to it. v3 does not have that term: there is nothing to
scatter into, so the curve is close to flat and the two cross at around ten
backups, after which v3 wins by a widening margin. At forty backups v3 costs
a third of v2's requests for a restore and half for a check, moving a third
of the bytes; peak RSS is flat to within a megabyte for both formats and both
operations.

Against this RFC's target 1 (≤1.2x at 80 backups) the honest reading is
"much better, not yet met": restore is 1.8x and check 1.9x at forty backups,
against v2's 12x and 19x. The remaining growth is the leaf set of the latest
snapshot getting larger as churn rewrites it, not a per-backup visit cost, so
it is bounded by tree size rather than by history.

Two gaps remain open, and neither is a defect in the format:

1. **`check` and `prune` request counts** stay above v2's, because v2 answers
   a full traversal from ~50 packs where v3 reads ~900 leaves. Both are
   nonetheless faster in wall time and move half the bytes.
1. **Whole-file dedup of inline content is not reinstated.** v2 skips a
   duplicate file by probing `content/<hash>`; v3 has no such object, so
   duplicate small files are re-read and re-inlined. Stored size is
   unaffected — the redundancy compresses — but the work is wasted. The cheap
   recovery is to probe the *previous snapshot's* entries, which change
   detection already reads; a repository-wide content index is the thing this
   format exists to avoid.

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

The design space is well trodden, and v3 deliberately sits between two proven
points rather than inventing a third:

- **Kopia** is the v2 architecture done with more machinery: small contents
  packed into blobs (≤20 MB), index blobs mapping content ID → (blob, offset,
  length), a local index at the end of each pack for recovery — RFC 0018's
  footer, independently arrived at — and a persistent local cache of
  memory-mapped indexes to make the map cheap to hold. It works, and it is
  the road not taken here: it keeps the second address space and pays for it
  with an index-and-cache subsystem. RFC 0024 already rejected the sorted
  on-disk index for this codebase (hash-ordered lookups thrash a block
  cache), and v3 removes the mapping instead of engineering it.
- **Duplicacy** is the other endpoint: no packs, no central index at all.
  Everything is a CDC chunk — including the snapshot's file list, whose
  `files`, `chunks` and `lengths` sequences are chunked by the same
  variable-size algorithm as file content — and deletion is a two-step
  fossil-collection protocol instead of an indexed sweep. Its published
  rationale for dropping the index matches this RFC's: the index database is
  the complexity and the failure surface.

v3 takes duplicacy's conclusion (make every stored object a large,
self-sufficient, content-addressed unit; keep no location map) while keeping
the HAMT so that `FileID` identity, multi-parent files, and O(log n) sparse
updates from cloud change feeds survive — the properties a flat chunked
manifest gives up, and the reason v3 is "fat leaves" rather than "chunk the
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
has to happen in the data layout. v2 accepted that and packed below the store
interface; v3 accepts it and makes the objects themselves large.

The one direction batch APIs do exist is **delete**: S3 `DeleteObjects` takes
up to 1,000 keys per request (MinIO and B2's S3-compatible endpoint included),
Azure Blob Batch takes 256, GCS batches metadata and delete calls at 100. That
is exactly the shape of `prune`'s sweep, and v3 should use it — see §5.

## The decision this RFC records

v2's compatibility contract (`docs/compatibility.md`) makes backward
compatibility permanent and migration optional. RFC 0024 was written under
that constraint, which is why it proposed an opportunistic in-place layout
change and dual decoders held forever.

This RFC records the decision to break it once: **v3 is a new baseline, not a
new era in a mixed repository.** A v3 repository contains only v3 structures;
a v3 binary refuses a v2 repository outright rather than reading it; the
permanence rule re-applies from v3 onward. That decision is what turns RFC
0024's additive design into a subtractive one — no dual decoders, no
mixed-era leaves, no legacy catalog reader, and the pack layer deleted rather
than bypassed.

The costs are real and named: users must run a migration before a v3 binary
can read their data, and `docs/compatibility.md` must be amended to record the
one-time re-baselining. Both are accepted.

## Goals

- One design document for the v3 format, replacing the partial and partly
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
  key; v3 shrinks that list ~12x but it stays O(objects). Public-interface
  change, separate RFC (carried over from RFC 0023's non-goals).
- **Changing the identity model.** `FileID` identity, multi-parent files,
  `AffinityKey` routing, and content-addressing by `ComputeJSONHash` /
  `ComputeHash` are untouched (RFC 0024's constraint section, which remains
  binding: path is layout, never identity).
- **Changing chunking of large-file content.** FastCDC chunks were never the
  problem; they are large and random-access by nature.

## Proposal

### 1. The v3 object model

| key | content | notes |
|---|---|---|
| `chunk/<hash>` | raw chunk data | unchanged |
| `node/<hash>` | HAMT spine node or fat leaf, binary | leaves carry metadata + small content |
| `content/<hash>` | chunk-ref spill list | rare; only for very large files |
| `snapshot/<hash>` | snapshot | unchanged shape, v3-stamped |
| `index/latest`, `index/snapshots` | mutable pointers/catalog | unchanged |
| `keys/<slot>`, `config` | key slots, repo marker | unchanged |

Gone relative to v2: `filemeta/*` (absorbed into leaves), `packs/*`,
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

`PackStore` is not in the v3 chain and is deleted along with:

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
each — the only per-object request pattern v3 leaves in place. Object stores
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
are stated against recorded v2 numbers and verified by the unified harness
(next section). All at the `source` profile, 5,000 files, 200-file churn,
MinIO, matching the RFC 0025 aging series:

1. **Aging flatness.** `restore` and `check` of the latest snapshot after 80
   backups cost ≤ 1.2x their cost after 1 backup, in requests and in bytes
   transferred. v2 measured 139 → 2,118 restore requests over that range.
1. **Requests proportional to objects.** `check` at 80 backups within 2x of
   the object count a fresh v3 repository of the same tree holds. v2: 1,867
   requests against a ~130 floor.
1. **Memory off the repository axis.** Peak RSS for `backup`, `check`,
   `restore`, `prune` flat from 5,000 to 50,000 files within the measured
   noise band, `prune`'s `List` term excepted and reported. v2 grows on every
   operation (RFC 0023's context table).
1. **The accepted cost, bounded.** `backup-incremental-1` upload volume ≤ 2x
   v2's, and `backup-incremental-1000` ≤ 1.5x — the per-leaf dedup loss RFC
   0024 worked through, now gated rather than estimated. If tuning
   `leafTargetSize` cannot hold both this and target 1, that tension goes back
   to this RFC before the default is chosen.
1. **Fewer round trips before the first byte.** `backup-incremental-noop`
   request count strictly below v2's, which pays 1 + shard-count reads to load
   a catalog v3 does not have.

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
Baseline v2 sweeps — the full matrix plus aging to 80 — are captured and
committed to `benchmark-results/` **before the first v3 PR merges**; they are
the numbers the targets above are scored against.

## Compatibility and migration

This section deliberately inverts the usual one.

- **Version gate.** `core.RepoFormatVersion = 3` and
  `core.MaxSupportedRepoFormat = 3`. `LoadRepoConfig` in a v3 binary meeting
  `config.version: 2` or lower fails with an actionable error naming the
  migration path. It must never fall through to a partial read: v2 structures
  are not merely unsupported, they are absent from the binary.
- **Old binaries meet v3 safely today.** Every released build enforces
  `MaxSupportedRepoFormat = 2` at open, so a v2 binary refuses a v3 repository
  cleanly. Verified by running an old binary, per `docs/compatibility.md`,
  not by reasoning about it.
- **`docs/compatibility.md` is amended, not silently violated.** It gains a
  section recording the v3 re-baselining: the permanence rule holds within a
  major format, v2→v3 is the one crossing that requires migration, and the
  rule re-applies from v3 onward. The v2 fixture table moves with the v2 read
  paths (below).
- **Migration is a separate deliverable.** Requirements it must meet:
  streamed (never the whole repository in memory), resumable, verifying — a
  post-migration `check` over the v3 output before the v2 objects are
  touched — and shipped in a transition release that carries both stacks.
  Until it ships, v2 users stay on v2 releases; main drops the v2 read paths
  once the transition release is cut, and the v2 e2e fixtures move to
  asserting *refusal with the migration message* rather than readability.

## Testing strategy

- Golden fixtures for the v3 leaf and snapshot encodings from the first PR
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
   and commit the v2 baselines. (Lands with this RFC.)
1. **v3 leaf encoding behind the gate.** Binary fat leaves, inline content,
   inline chunk refs + spill, byte-budget splitting — writable only when
   `init` is asked for format 3, which is not yet the default. The v3 chain
   is built without `PackStore` from the start.
1. **Engine on entries, not refs.** Change detection, `check`, `diff`, `find`
   read decoded entries; the derived traversal order (RFC 0025 §1) becomes
   the only read order.
1. **Tune and gate.** Run the harness matrix plus aging on v3; choose
   `leafTargetSize` and `inlineContentMax` against targets 1 and 4; record
   the sweep in this RFC's outcome section.
1. **Flip the default.** `init` writes v3; v2 write paths freeze.
1. **Cut the transition release, then delete.** With the migration tool
   shipped in the transition release, main removes `internal/storelayer`'s
   pack files, the v2 leaf/filemeta decoders, and the public `ReadPlanner`
   surface (docs PR paired, per RFC 0022).

## Open questions

1. ~~**Leaf byte budget.**~~ **Answered: 8 MB.** Swept at 2, 4, 8 and 16 MB
   on the `source` profile. Requests fall almost exactly as 1/budget — check
   904 → 408 → 206 → 102, restore 915 → 229 → 128 → 76 — and what pays for it
   is write amplification, since a changed entry rewrites its whole leaf
   including its neighbours' untouched content: a single-file incremental
   uploads 0.4, 0.6, 1.6 and 1.6 MB across the same sweep. 8 MB is where the
   read side stops improving cheaply. The stored-size spread the sweep also
   shows (79 → 91 → 104 MB) is retention, not format: those figures hold six
   snapshots' trees, and a repository kept at one snapshot stores 54 MB at
   2 MB and 52 MB at 8 MB, against the packfile format's 79 MB.
1. **Inline content budget.** 64 KB default; the trade is leaf rewrite volume
   on churn against object count and restore requests. Measured on
   `backup-incremental-1` / `-1000` per RFC 0024's open question 1.
1. **Front-coding the name arena.** Sibling names share prefixes; zstd over
   the whole leaf may already capture most of it. Measure before adding a
   second encoding.
1. **Xattrs/ACLs.** Side table keyed by entry index, present when flagged
   (carried from RFC 0024).
1. ~~**Cross-directory metadata dedup.**~~ **Answered: leave it.** Quantified
   on `mixed` and `source` by building the chunk-promotion design and
   measuring it — see "Whole-file dedup of inline content" above. The bytes
   are real (2–4.5% of stored size) and cost more in restore requests than
   they are worth, because a leaf's bytes are already being read while a
   chunk's are not.
1. **Does `index/snapshots` want the same treatment later?** It is already a
   reconciling cache; nothing here changes it, but a v3 follow-up could fold
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
