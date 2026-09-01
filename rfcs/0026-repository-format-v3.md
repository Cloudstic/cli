# RFC 0026: Repository Format v3 (Blob Bodies, No Packfiles)

- **Status:** Proposed, and measured. Stages 1–3 were implemented behind
  `init -format 3` and then profiled, which changed the design: the
  aggregation was right and putting metadata and content in the *same*
  object was not. What this document describes is the format as it now
  stands — metadata in the HAMT leaves, file bodies in `blob/` objects the
  leaves reference, no packfile layer. "Appendix: how the design changed"
  records what was first built, what measuring it showed, and what replaced
  it. Nothing released has ever written format v3, which is what made
  changing it free.
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

- File metadata moves **into the HAMT leaves**, in a binary encoding, with
  chunk manifests inlined into the leaf entry. File bodies live in `blob/`
  objects — packed runs of many entries' bodies — that a leaf entry points at
  by `(blob ref, offset, length)`, so the operations reading only metadata
  never fetch content at all.
- The packfile layer is **removed entirely**. Nothing in v3 is small enough to
  need it, so the catalog, shards, footers, admission heuristics, group plans,
  heal and repack machinery are deleted rather than improved.
- The version gate is raised: v3 binaries refuse v2 repositories with an error
  naming the migration tool. Migration is a separate deliverable (see
  *Compatibility and migration*).

The claim to hold this RFC to: read cost proportional to bytes read and
independent of backup count, peak memory independent of repository size, and a
net deletion of several thousand lines. The unified benchmark harness (aging
folded into `bench.sh`, part of this RFC) is what verifies each claim against
the recorded v2 baselines.

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
manifest gives up, and the reason v3 keeps a tree rather than "chunking the
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
is exactly the shape of `prune`'s sweep, and v3 should use it — see
*Proposal → 6. Batched deletes*.

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

- **The migration tool.** Its requirements are stated in *Compatibility and
  migration*; its design is its own RFC.
- **Streaming `ObjectStore.List`.** `prune`'s sweep still materialises every
  key; v3 shrinks that list ~12x but it stays O(objects). Public-interface
  change, separate RFC (carried over from RFC 0023's non-goals).
- **Changing the identity model.** `FileID` identity, multi-parent files,
  `AffinityKey` routing, and content-addressing by `ComputeJSONHash` /
  `ComputeHash` are untouched (RFC 0024's constraint section, which remains
  binding: path is layout, never identity).
- **Changing chunking of large-file content.** FastCDC chunks were never the
  problem; they are large and random-access by nature.

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

## Proposal

Six pieces that only make sense together: what objects a v3 repository holds,
what a metadata leaf carries, where file bodies live, what the store chain
looks like with the pack layer gone, what the read path then costs, and the
one direction object stores still batch.

### 1. The object model

| key | content | notes |
|---|---|---|
| `chunk/<hash>` | raw chunk data | unchanged |
| `blob/<hash>` | a packed run of file bodies | new in v3; see *The encoding* |
| `node/<hash>` | HAMT spine node or metadata leaf, binary | leaves carry metadata and a body reference |
| `content/<hash>` | chunk-ref spill list | rare; only for very large files |
| `snapshot/<hash>` | snapshot | unchanged shape, v3-stamped |
| `index/latest`, `index/snapshots` | mutable pointers/catalog | unchanged |
| `keys/<slot>`, `config` | key slots, repo marker | unchanged |

Gone relative to v2: `filemeta/*` (absorbed into leaves), `packs/*`,
`index/packs`, `index/packmap/*`, and `content/*` in its role as a per-file
manifest (it survives only as the spill form).

### 2. Metadata leaves

The design is RFC 0024's, promoted from opportunistic layout change to the
only form:

- **Leaf entries carry the metadata itself** in a fixed-offset binary record
  (identity, parents, size, mtime, mode, content identity), with names in a
  per-leaf arena. `Lookup` returns the decoded entry; change detection
  compares the fields that define a change rather than a `filemeta/` ref.
- **A file's body is named, not carried.** The entry holds
  `(blob ref, offset, length, blobTotal)` for a body packed into a `blob/`
  object — see *3. Blob objects* and *The encoding*. The entry's value, the
  content address of its metadata, is unchanged by that, so change
  detection, `diff` and dedup semantics are untouched.
- **Chunked files inline their chunk refs** in the entry, up to
  `inlineRefsMax` (~16 KB, ~512 refs, ~2 GB of file at 4 MB average chunks).
  Beyond that the ref list spills to a `content/` object — which is then at
  least 16 KB itself, so the spill can never reintroduce a tiny-object
  population.
- **The split rule is an entry cap, not a byte budget.** With bodies gone a
  leaf of metadata cannot reach the byte budget, so `leafSplitBytesV3` never
  fires and `maxLeafEntriesV3` becomes the whole rule — which is why it has
  to be chosen deliberately rather than inherited (see *What would break
  silently*). Fifty thousand `source`-profile files serialise to a few dozen
  leaves instead of 109,600 objects.
- The leaf is content-addressed as a whole; per-entry addresses are not
  reintroduced. `check` verifies leaves, and every entry in one, by the one
  hash.

Deduplication granularity moves from per-file to per-leaf. What that costs
on an incremental is bounded by the split rule — a changed entry rewrites
its leaf and the dirty spine above it — and is one of the numbers the
harness gates (see *Performance targets*).

### 3. Blob objects

File bodies live in `blob/` objects rather than inside the leaf that describes
them, and the reason is what the leaves themselves measured.

A leaf is mostly file content. Measured on the 20,000-file `source` tree — and
*measured*, not extrapolated: setting the inline threshold to 1 byte makes
every body chunked, which produces a real tree whose leaves carry metadata and
refs and no bodies, the closest thing the format as built can express to the
one proposed here.

| | as built | metadata only |
|---|---:|---:|
| plaintext | 311.1 MB | **14.9 MB** |
| stored | 69.4 MB | **3.1 MB** |
| leaves | 219 | 25 |
| nodes including interior | 302 | 33 |

Change detection, `prune`, `ls`, `find` and `diff` read the metadata and never
touch the content, so they pay for a structure **21x larger and 9x more
numerous** than the one they use. A no-change backup allocates 3,125 MB,
1.23 GB of it decompressing and decoding leaves; `prune` allocates 9,360 MB,
essentially all of it decompressing leaves to read chunk refs. Issue #539 could
stop prune *decoding* the payloads it does not want but not stop it *fetching*
them, because they share the object it must read.

An earlier draft of this section put the metadata at 8.9 MB and the tree at ~5
leaves, and both were wrong. The byte figure came from `leafstat`, which sums
payload fields and omits each entry's key, path key and value — about 180 bytes
an entry, or 4.6 MB here. The leaf count came from extrapolating the 4 MB byte
budget, but with bodies gone a leaf cannot reach 4 MB of metadata, so
`leafSplitBytesV3` never fires and `maxLeafEntriesV3` (2048) becomes the sole
split rule. The measured tree has 25 leaves, against a floor of 13 at perfect
fill. **The metadata-leaf dial is therefore the entry cap, not the byte budget**
— which the two-dial framing in *Alternatives considered → Why the format had
only one dial* has to mean literally.

**So a leaf holds metadata and a reference to where the content lives.** File
bodies move into content-addressed `blob/` objects, each a packed run of many
entries' bodies; an entry names `(blob ref, offset, length)`. The entry's value
— the content address of its metadata — does not change, so change detection,
`diff` and dedup semantics are untouched.

A blob stays live while any one of its bodies is referenced, which is a
reclamation problem aggregating metadata does not have. What bounds it is
*Consolidating sparse blobs forward*.

### 4. The store chain without packs

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

### 5. The read path

RFC 0025 §1 — deriving the traversal order from the routing key instead of
holding O(files) plans — survives and matters more, not less: leaf routing by
`AffinityKey(primaryParentID, FileID)` keeps a directory's entries in few
leaves, so a directory-order traversal touches each leaf once. What dies with
the packs is everything that existed to *guess* what to keep resident:
`packPromoteAfter`, `packPenalized`, `packAdmission`, the LRU body cache and
its budget, `PlanReads` and the single shared plan. A leaf is fetched once,
decoded, consumed, and released; there is nothing to admit, promote, or
penalize, because there is no second address space to manage.

`restore` reads: one snapshot object, the spine, the leaves, the `blob/`
objects holding the bodies those leaves name, and chunks for large files —
every one a single request for a large object, and none of it dependent on
which backup wrote what. The two-phase metadata/content separation that made streaming restore
regress (RFC 0025 §8) does not disappear, though — bodies in blobs are what
the second phase reads, and restore's worker pool is bounded on the premise
that writing a body is a memory copy. That is item 4 of *What would break
silently*.

`prune` marks over ~12x fewer objects and sweeps a ~12x shorter list. Waste
from superseded leaves is ordinary garbage — unreferenced objects deleted by
the existing mark-and-sweep — not fragmentation inside a shared container, so
`Repack` and its durability ordering go away entirely.

### 6. Batched deletes

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

## The encoding

Concrete enough to implement against and to argue with. It was written before
any of it existed; *Appendix: how the design changed* records what building it
settled.

### Object kinds

`node/`, `chunk/`, `snapshot/`, `config`, `keys/` are unchanged. One kind is
added:

- **`blob/<hash>`** — a packed run of file bodies, written in walk order, never
  appended to after it is sealed, and addressed by the hash of its members'
  **digests in order**.

  Not by the hash of the concatenated bodies, which does not determine where
  one member ends and the next begins: `["a", "bc"]` and `["ab", "c"]` would
  name one object with two different layouts, and an empty file reaches that
  without contrivance since `["", "abc"]` and `["abc"]` concatenate alike. A
  content-addressed store would keep whichever arrived first while the other's
  entries carried offsets into it. Fixed-width digests remove the ambiguity,
  commit to the bodies exactly as strongly — a member's digest is its identity
  everywhere else — and are already in hand, so naming a blob costs no second
  pass over its content.

### The leaf entry

The record gains a body reference and loses inline bytes. Flags stay a byte;
`entryFlagInline` (2) is replaced by `entryFlagBody` (8) rather than reused, so
a decoder meeting an old bit knows it is old rather than misreading it.

```text
key      uvarint len + bytes
pathKey  uvarint len + bytes
value    uvarint len + bytes
flags    1 byte      1 = payload, 4 = chunks, 8 = body-in-blob
payload (flag 1):
  size      uvarint
  meta      uvarint len + bytes
  body      (flag 8):  blobRef, uvarint offset, uvarint length, uvarint blobTotal
  chunks    (flag 4):  uvarint count, count x (uvarint len + bytes)
```

`blobTotal` is the blob object's **stored** size, repeated in every entry that
references it. It is three or four bytes and it closes the gap the review of
this RFC found: deciding "this blob is below the threshold" needs the blob's
total size, and an entry otherwise knows only its own slice. With it, a backup
accumulates live bytes per blob as it walks and already holds the denominator —
no lookup, no second index, no read of the blob itself.

Stored rather than plaintext, and the distinction is not pedantic. `offset` and
`length` address the stored object, because they are what a ranged GET is
given, and members are compressed. Dividing summed stored lengths by a
plaintext total would report a perfectly full blob as roughly half empty and
consolidate it — turning compression into apparent waste and driving exactly
the rewrite loop consolidation exists to bound.

**Nothing carries a body hash.** The body's content address is already in
`meta.ContentHash`, and any reader holding the metadata can verify a ranged
read against it. Adding a second copy would cost 32 bytes an entry — 5% of the
metadata — to duplicate a fact the leaf already contains.

### The blob

```text
member_1 || member_2 || ... || member_n || index || uint32 index_length
```

Each member is compressed and sealed **independently** — the aggregate is a
container, not a cryptographic unit — so one member can be fetched by range and
decrypted alone. The trailing index lists each member's offset, length and
plaintext hash, is itself sealed, and makes the blob self-describing: an index
can be rebuilt from the store without any catalog, exactly as `PackStore`'s
footers already allow (RFC 0018). Index at the end rather than the start so a
backup can stream members out as it packs them, which is restic's reason too.

Two details that only surfaced on writing it, both load-bearing:

- **A member's compression codec travels inside its sealed bytes**, not in the
  index. Putting it in the index would make decoding a member require the
  index, costing the second request the ranged read exists to avoid. One byte
  per member buys a range that is self-describing.
- **The index cannot be keyed on its own hash.** A reader must derive the key
  before it can decrypt the index, and it does not know the plaintext until it
  has. So the key material is a fixed domain string and the blob's ref in the
  AAD is what makes it blob-specific — sound because a blob has exactly one
  index whose contents are a function of the ref, so two distinct indexes under
  one key would mean two member lists hashing alike.

**Sizing is derived rather than swept** (see *Alternatives considered → What
object storage actually charges, and who else has built this*). A blob should
be about the bandwidth-delay product, `time-to-first-byte x bandwidth` — 4.5–9 MB on a fast
link, ~1 MB on a domestic uplink — because below that a second request costs
more than fetching and discarding the gap. The 4 MB reached by sweeping sits
inside that band, which is reassuring but was luck.

### What packs a blob: walk order

Bodies should be packed in **walk order** — the order the source produced them,
which is also the status quo that preserves RFC 0025's upload locality. The
reasoning that led to the question was wrong, though, and the correction is
worth keeping.

**Routing-key order does not do what it was supposed to.** The hypothesis was
that it groups a directory's bodies for a path-scoped restore. It groups a
directory's *own* entries — that is what the `parentHash[:4]` prefix buys — but
it places the directories themselves in hash order, so a subtree's child
directories scatter. Walk order makes the whole subtree contiguous, which is
what restoring a path actually needs.

Blobs fetched for a path-scoped restore at a 4 MB budget, summed over all 1,594
distinct subtrees, against an oracle that packs each subtree perfectly:

| | oracle | routing | walk | random |
|---|---:|---:|---:|---:|
| recursive subtree | 1,659 | 5,827 | **5,430** | 13,340 |
| with shared-body ownership removed | 1,659 | 2,241 | **2,026** | 9,650 |

Walk wins by about 7%, and for the 96% of subtrees holding fewer than 50 files
the two are *identical*. They diverge only on the handful of top-level
directories, where walk is near-oracle. Both beat a random order by 2.3x — so
**a** locality-preserving order matters a great deal and **which** one barely
does. The decision is therefore to keep the order backup already produces.

**Fragmentation is indifferent to order and sensitive to budget.** At 4 MB and
8 MB the blob count, fill and waste are identical to three significant figures
across routing, walk and random. What does move is the budget: at 8 MB, 14 of
38 blobs are under half full, and they are exactly the 13 churn backups plus a
tail. **A budget above a backup's new-byte volume leaves a half-empty blob per
backup**, since each backup's churn is packed and then closed. On this workload
(~3 MB of new bodies per backup) 4 MB leaves none under half full, and that
coincidence is the reason to pick it — not a general constant.

Three limits on this, all worth carrying forward:

- The simulation packs **sequentially**. A real backup uploads concurrently, so
  bodies reach the packer interleaved by worker and walk order's locality
  degrades unless the packer is explicitly order-preserving. Every walk number
  above is an upper bound on what a parallel implementation delivers, and
  "preserve walk order into the packer" is a requirement the implementation
  inherits rather than a free property.
- The metric counts **distinct blobs fetched, not prefetch**. Routing-key order
  is the order a leaf-order restore traverses, so it could win on sequential
  prefetch while losing on request count. That is unmeasured and is the
  strongest remaining argument for the other choice.
- **The first backup dominates**: 297.5 MB of 350 MB of new bodies land in it,
  so packing order is mostly a property of the initial ingest. A repository
  grown from empty would show a weaker ordering effect either way.

### Encryption

Per member, with the key derived from the member's own plaintext hash and a
repository secret, and the containing blob's ref bound into the AAD.

**Two bindings, doing different jobs.** The *key* is derived from the member's
own plaintext hash, so it binds the member; the *AAD* is the blob's ref, so it
binds the container. Both matter, and conflating them invites a simplification
that removes one.

The key binding is what defeats the cheapest leaf-level mutation: repointing an
entry's byte range at another member of the same blob. Every member of a blob
shares the AAD, so the AAD cannot catch that — but the reader derives its key
from the entry's own content hash, so it derives the wrong key and the bytes do
not authenticate. This is the mutation `restore -no-verify` would otherwise
write to disk, since that flag skips the `meta.ContentHash` comparison by
construction.

It also means **nonce reuse cannot arise**. Key and nonce are both functions of
(plaintext hash, blob ref), so two members sharing them are the same plaintext
in the same blob — identical ciphertext, which leaks nothing — unless SHA-256
collides. A blob stores a repeated body once in any case, so the question does
not come up within one, and across blobs the differing ref separates them.

**The AAD binding is load-bearing, not belt-and-braces.** A blob's ref is the
hash of its *plaintext*, which is the discipline every self-addressed namespace
here already follows — `core.VerifyRef` hashes decrypted bytes, and `chunk/`
refs are HMACs rather than hashes for the same reason. But a blob is the first
object whose plaintext **no reader ever assembles**: readers want one member,
so nothing recomputes the concatenation and `VerifyRef` can never be applied.
`blob/` therefore stays out of `core.SelfAddressedPrefixes`, and the binding
that whole-object addressing would have supplied has to come from somewhere
else. It comes from the AAD: a member lifted from another blob, or moved within
one, fails to authenticate.

`meta.ContentHash` is a second, independent check on the same substitution —
but it is not a *replacement*, because `restore -no-verify` skips it by
construction. The AAD binding holds on that path too.

Three further consequences, all wanted:

- A ranged read decrypts exactly its member.
- Sealing is **deterministic given the blob**, so a retried upload is
  byte-identical — which a random nonce cannot offer a content-addressed
  store. It is deliberately not deterministic *across* blobs; that is the
  same AAD binding seen from the other side, and it costs nothing, since
  bodies deduplicate on `ContentHash` and never on sealed bytes.
- Nonce reuse across distinct plaintexts would require a hash collision, since
  two members with the same key and AAD are the same member of the same blob.

A ranged read authenticates only what it fetches; `restore` and
`check -read-data` read wholes and keep the strong guarantee.

Members are **not** padded, and their sizes are not uniform: each is compressed
on its own, so its stored length is a function of its content. What that
discloses, and to whom, is worth stating rather than waving at. The index is
sealed, so offsets and lengths are not visible without the key; an observer with
store access alone learns a blob's total size and how many blobs there are,
which is what any layout grouping bodies discloses. A holder of the key sees
per-member compressed sizes, and is entitled to the bodies themselves anyway.
Padding to fixed frames would close the first gap at a cost in stored bytes
nothing here has shown is worth paying — but that is the claim to make, not
that framing leaks nothing.

**This needs a primitive `pkg/crypto` does not have.** `Encrypt` takes no AAD
and draws a random nonce; both are wrong here. The member seal is a third
sibling to `HKDFInfoBackupV1` and `HKDFInfoPackIndexV1`, and it is the one
piece of this design that touches the security boundary — it should land as its
own reviewed change, before anything that calls it.

### What each operation does

- **backup** — packs new bodies into blobs in walk order; writes the metadata
  leaf with `(blobRef, offset, length, blobTotal)`; accumulates live bytes per
  referenced blob and consolidates forward past the threshold, under a bounded
  per-backup rewrite budget (see *Consolidating sparse blobs forward*).
- **restore** — reads metadata leaves, then fetches each referenced blob whole
  (it wants all of it), or ranges into it for a path-scoped restore.
- **check** — a default run confirms each referenced blob exists and that its
  trailing index authenticates and covers the offsets entries name. It cannot
  do more: verifying a blob against its ref means decrypting every member and
  hashing the concatenation, which *is* the `-read-data` path, where each
  member is checked against `meta.ContentHash`. Saying a default run
  "verifies blobs" would be the same overclaim as reading "cannot decode" as
  "empty".
- **prune** — marks `blob/` refs alongside `chunk/`, and deletes blobs no
  retained snapshot references. **It never repacks.**

### The four things that would break silently

The long form, with the code each one passes through, is *What would break
silently*. Each needs handling in the same commit that introduces the body
reference, not after:

1. **`prune` would delete every blob.** `WalkChunkRefs` has no slot for a body
   reference and `payloadChunksOnly` skips the region one would occupy, so an
   entry reports reaching nothing while `hasPayload` stays true and the safety
   valve does not fire. The reduced decoder and the callback signature must
   learn about bodies in the same change that adds `blob/` to
   `objectPrefixesV3` — adding the prefix alone *activates* the deletion.
2. **`check` would stop verifying bodies exist.** The per-entry chunk loop is
   the only existence check a default run makes, and a body-referencing entry
   has no chunks. It needs an equivalent for `blob/`.
3. **`restore`'s directory heuristic** tests `Inline` and `Chunks`; it must test
   the body reference too, or it decodes `Meta` for every entry instead of a few
   per cent.
4. **`restore`'s worker pool** is capped on the premise that writing an inline
   body is a memory copy. It becomes a fetch and needs resizing against the
   bandwidth-delay product (*Alternatives considered → What object storage
   actually charges, and who else has built this*).

And three that are visible but easy to get wrong. `blob/` must **not** be added
to `core.SelfAddressedPrefixes`: every other member of that list names an
object whose plaintext a reader reconstructs, and a blob's never is, so
`VerifyRef` would fail on correct data. `copy`'s payload elision should
be **deleted**, not translated — a body reference is small enough to cache,
and rewriting the condition would re-read the body on every repeated visit; and
`objkey` needs `blob/` appended to its namespaces, or the reachable set falls
back to a string map for the repository's largest namespace.

### What this does not change

Routing, `AffinityKey`, the split rule's *shape*, `diff`'s ref short-circuit,
chunking above the inline threshold, and the no-index bet. `leafSplitBytesV3`
stops binding — a leaf of metadata cannot reach 4 MB — so `maxLeafEntriesV3`
becomes the split rule in fact and should be chosen deliberately rather than
inherited, along with the node cache and `sealFlushBytes`, which are both
expressed in terms of a leaf that will no longer exist.

## Consolidating sparse blobs forward

Aggregating bodies into blobs creates a reclamation problem that aggregating
metadata into leaves does not. What follows is the argument, the published
precedent it rests on, and what measuring an implementation settled.

### The risk, and why it is answerable

**A blob stays live while any one of its bodies is referenced**, so a
repository accumulates blobs that are mostly garbage — exactly what `PackStore`
did and what this format exists to escape. This is the question that decides
whether separating bodies from metadata is sound, so it is worked here rather
than deferred.

**The obvious answer does not work, and that is architectural rather than a
failure of imagination.** Across thirteen systems surveyed the rule is without
exception: *every system that repacks has an indirection between the logical
name and the physical location, and every system that lacks one does not
repack.* ZFS is the pure case of our constraint — an immutable Merkle tree
whose parents embed both a physical address and the child's checksum — and
block pointer rewrite has gone unimplemented for two decades, with ZFS's own
architect on record that it would be the last feature ever added; the accepted
remedy is `send | recv` into a fresh pool, which is consolidate-forward at
whole-pool granularity. git enforces the same discipline deliberately:
`OFS_DELTA` is the only physical offset it uses and it is valid only inside its
own pack, never escaping into a commit or tree, precisely so that `repack` stays
free.

v2 repacked fragmented packs during `prune` (`internal/engine/prune.go`), and it
could only do so because the pack catalog was an indirection: an object's key resolved to a pack through
`index/packs`, so repacking rewrote the catalog and left every snapshot alone.
That catalog is precisely what this format removed, and RFC 0023 is the record
of failing to bound it.

Without an indirection an entry names its blob directly, so moving a body to a
new blob changes the entry, which changes its leaf, which changes every node up
to the root, which changes the snapshot. Repacking at `prune` would mean
**rewriting the tree of every retained snapshot** — mutating objects that are
content-addressed and immutable, and changing snapshot identities that users
and `copy` rely on. That is not available at any price.

**Consolidating forward is, and it is a published technique rather than an
invention.** It is *History-Aware Rewriting* (Fu et al., USENIX ATC 2014): a
container whose utilization falls below a threshold is a **sparse container**,
and the *next* backup rewrites into fresh containers the members it would
otherwise have deduplicated against a sparse one. Old snapshots are never
touched. HAR's central empirical finding is exactly the one this design needs —
**a sparse container stays sparse in the next backup** — which makes the
prediction cheap and accurate. The industry term is *copy forward* (Data
Domain); SlimStore (ICDE 2021) does the same thing on cloud object storage,
though with a fingerprint index that lets it update old recipes, which is why
HAR and not SlimStore is the precedent here. Searching for "rewriting sparse
containers" finds the literature; "consolidate forward" finds nothing.

A backup already writes a new snapshot with a new root. It can therefore write
*fresh* blobs for bodies it finds in fragmented ones, and reference those
instead:

- No existing tree is rewritten. Older snapshots keep pointing at the old
  blobs, which stay live and correct for exactly as long as those snapshots do.
- A blob dies **whole**, when the last snapshot referencing it is forgotten.
  `prune` therefore needs no repacking at all: fully-dead blobs are set
  membership, the same test chunks already get.
- The bound lives on the **write path**, which is where this repository has
  already learned it must live. `packIndexCompactThreshold` exists because
  shard count "grows with the number of backups a repository has ever taken,
  and only `prune` ever bounded it", and Kopia #5057 is that same failure left
  running for a week. A consolidation triggered by a measured live fraction is
  an absorption bound, not a clock.

So the space a repository wastes is bounded by how fragmented a blob is allowed
to get before the next backup migrates out of it, times how many snapshots are
retained — rather than growing without limit in time.

**What it costs is write amplification, and that is the honest trade.**
Consolidation rewrites live bodies out of sparse blobs, so the threshold is a
dial between wasted space and bytes written per backup. It is the same trade
this format has fought throughout — but now it applies to content bytes at a
tunable rate, rather than being forced on every touched leaf at a rate the leaf
budget fixes.

**Measured, on a repository that exists.** `internal/cmd/leafstat -entries`
emits each snapshot's bodies, so blob packing can be simulated over real churn
without writing an encoding. On a 20,000-file `source` tree aged with 14
backups of 300 churned files each, packing each backup's new bodies into 4 MB
blobs in routing-key order, then forgetting all but the newest snapshot:

| policy | blobs | fully dead | waste | extra bytes written |
|---|---:|---:|---:|---:|
| none | 62 | **0** | 35.4 MB (15%) | — |
| consolidate below 50% live | 69 | 11 | 16.8 MB (8%) | +13.4 MB (**+5.7%**) |
| consolidate below 80% live | 82 | 28 | 10.4 MB (5%) | +63.9 MB (+27%) |

(Packing order turns out not to matter here — see *The encoding → What packs a
blob: walk order*. In walk order the same
three rows are 62 / 70 / — blobs and 15.1% / 6.0% waste for +7.3%: the same
curve, reached at a slightly different point.)

The important row is the first one, and it is worse than "15% waste" makes it
sound: **not one of 62 blobs is collectable.** Where v2 would reclaim a whole
pack, a blob repository reclaims essentially nothing, because each blob retains
a few bodies of files that were touched once and never again. Consolidation is
what buys the reclamation back, and it is cheap exactly where it is worthwhile
— a sparse blob is cheap to migrate out of precisely because little of it is
live.

Three things about this table are worth stating so it is not read for more than
it says.

**The 15% is not the alarming number, and it was nearly read as one.** Every
shipped threshold in the field sits at or above it: restic's `--max-unused`
defaults to 5%, borg's `compact --threshold` and `bup gc --threshold` to 10%,
HAR's sparse-container line to ~50%, SlimStore's to ~30%. By prevailing practice
a repository with 15% dead bytes is barely worth touching for space.

**The alarming number is the other one: 0 of 62 containers collectable.** That
is a container-count and request-count problem, not a byte problem, and
utilization is the wrong metric for it. The metric that matches is Lillibridge's
*capping* criterion — how many distinct containers must be read to reconstruct
the newest snapshot — which is also what actually drives request counts. **The
consolidation trigger should be that, with utilization as a secondary filter,
rather than a dead-byte percentage.**

**The 15% is churn, not fragmentation.** Sweeping the blob budget from 1 MB to
16 MB moves the waste by less than 0.1 MB — it is 35.4 MB at every size, and
15.1% again under position-addressed bodies. It is simply the fraction of body
bytes that `keep-last-1` supersedes, and one object per body would show the
same number. So this table does not measure how *fragmented* blobs get; what it
establishes is the sentence above, that nothing becomes collectable, which is
the real cost of aggregating bodies at all.

**Consolidation is measured at its most favourable retention.** `live` here is
the newest snapshot alone. Under retain-all the store has no waste to recover
and consolidation is strictly negative — it adds 13.4 MB of duplication for
nothing. The policy has to be driven by what `forget` will actually remove, not
run unconditionally.

**And it needs a fact the entries would not otherwise carry.** Deciding "this
blob is below the threshold" requires the blob's total size; an entry knows
only its own offset and length. That was a real gap rather than a detail when
this was first argued, and it is why the leaf entry carries `blobTotal` — the
blob's whole stored size, repeated in every entry referencing it, so a backup
accumulates live bytes as it walks and already holds the denominator. See
*The encoding → The leaf entry*, which specifies it and says why it is the
stored size rather than the plaintext one.

**The LFS cleaning formula half applies, and the half that does not is
inverted.** Rosenblum and Ousterhout select a segment by
`(1 − u) × age / (1 + u)`. The `(1 − u)/(1 + u)` term transfers directly — `u`
is the blob's live fraction and `1 + u` is genuinely the cost, one read plus a
rewrite of the live members. In consolidate-forward the read is partly free
because the backup was reading those leaves anyway, which lowers the denominator
and argues for a *more* aggressive threshold than a repacking system would use.

The `age` term does not transfer. LFS uses age as a proxy for how long reclaimed
space will stay free, assuming old data stays put. A retention-driven backup
does not need a proxy — **the retention policy says exactly when each snapshot
dies** — and the correlation runs backwards: an old blob that has survived
several `forget` cycles is one pinned by the *oldest retained* snapshot, which
is the next to expire, so consolidating it is close to pure waste. The
substitution is to weigh the expected remaining lifetime of the *youngest*
snapshot referencing the blob, computed from the retention policy. That is a
strictly better estimator than LFS's, because we have ground truth where it had
a heuristic.

**And the cost needs a ceiling.** Rewriting forgoes deduplication for the
migrated members — a second copy exists until the old blob dies — and the cost
scales with how much of the live working set currently sits in sparse blobs,
which can spike after an unusual `forget`. HAR and the context-based-rewriting
line both bound this with an explicit per-backup rewrite budget, and any
implementation here should carry one.

**The strongest justification for the design is not the one this RFC gave.**
A blob written by snapshot N holds exactly the bodies snapshot N found new, so
if nothing ever appends to an existing blob, its liveness is a step function of
retention and it dies whole. The reason all 62 blobs are partially live is
*deduplication*: snapshot N references bodies inside blobs written by 1..N−1.
Consolidating forward is precisely the operation that converts a shared blob
back into an unshared one and restores the die-whole property.

**The cautionary tale is bup.** git packfiles plus backup semantics produced
exactly this failure mode — backups entangled, no pack safely deletable — and
bup escaped only because git packs carry an `.idx`, so `bup gc` can rewrite
them. Even then the shipped implementation is probabilistic and retains some
unreachable data by design. **A format with no index has no such escape hatch**,
and that is the thing to be certain about before committing: we are giving up
the option bup needed.

**And the comparison that matters is not close.** The same 14 snapshots, in
encoded bytes:

| | stored, 14 snapshots |
|---|---:|
| v3 today | **392.0 MB** (measured on disk, 1,666 distinct nodes) |
| bodies in blobs, position-addressed | 355.5 MB plaintext → ~78 MB |
| metadata, 14.9 MB x 14 snapshots | 208.6 MB plaintext → ~43 MB |
| **blob design** | **~121 MB** |

A leaf carries content, so every snapshot that touches a leaf stores another
copy of its neighbours' bodies. Bodies in blobs are written once and shared by
every snapshot that references them, and only the metadata is rewritten. That
is **about 3.2x less stored** for the same history.

An earlier draft claimed 4.7x, from two mistakes that happened to compound.
The 1,695.7 MB figure was *plaintext* — `leafstat -refs` reports encoded bytes
before compression — where the repository on disk holds 392.0 MB for exactly
those refs. And the two sides of the comparison used different dedup rules: the
blob column came from a simulation that keys bodies by content hash, granting
whole-body dedup across files, while the design as written says bodies are
addressed by position and never deduplicated. Under the design's own rule the
bodies are 355.5 MB rather than 233.8 MB. Both figures above are now in stored
bytes, with metadata compressing at the 4.81x measured on the metadata-only
tree and content at the 4.56x measured on the repository.

These are simulations over a real repository's bodies, churn and snapshot
sequence — not a running implementation — and they are the numbers to re-check
against one.

### What consolidation measured

Consolidating forward was the last piece, and measuring it settled one thing
and opened another. Against MinIO on a 5,000-file tree, at 1/10/25 retained
backups:

| | v2 | v3 before | v3 after |
|---|---:|---:|---:|
| restore@25 | 210 req | 101 req | **53 req** |
| check@25 | 218 req | 58 req | **37 req** |
| restore slope | 7.62/backup | 2.25/backup | **0.21/backup** |
| check slope | 8.25/backup | 0.96/backup | **0.04/backup** |

**The flat curve is real.** It is the property this format was argued for and
the one it did not have until consolidation existed: a read costs what the data
costs, not what the history costs. At 25 retained backups v3 is 4.0x cheaper
than v2 on restore and 5.9x on check.

**It is bought with retained bytes.** Over the same checkpoints the repository
grows 18.6 → 36.1 → 71.0 MB, against 18.4 → 29.2 → 47.6 MB without
consolidation and 24.7 → 29.6 → 39.4 MB for v2. Consolidation rewrites bodies
forward while older snapshots still reference the originals, so a repository
that never forgets holds both — about 0.97 MB per retained backup here, 1.8x
v2's total at 25.

Pruning reverses it, because the superseded blobs become collectable the moment
the snapshots holding them are forgotten. So the trade is not "space for
requests" flatly; it is **space while everything is retained, for requests
always**. A repository with a retention policy pays little and gains the flat
curve; one that keeps every snapshot forever pays 1.8x v2's storage to serve
restores 4x cheaper.

That is a real choice rather than a defect, but it is the format's sharpest
remaining edge, and where `consolidateFillPercent` should sit on it is not yet
established — 50 was chosen on a convergence argument (no two blobs under half
a blob's worth can both survive a merge) rather than against this curve.

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

## What the measurements showed

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

### What v3 stores, and when

"Format v3 stores more" is the wrong summary and worth correcting here,
because the pipeline number invites it. Measured on a 2,000-file `source` tree
with six backups of 200 files of churn between each:

| | 6 snapshots retained | after `forget --keep-last 1 --prune` |
|---|---|---|
| packfile format | 12.2 MB | 10.8 MB |
| v3 | 32.0 MB | **6.3 MB** |

v3 releases 80% of its stored bytes on prune where the packfile format
releases 11%, and at a single snapshot it stores **42% less** — the object
count and whole-leaf compression showing through once the garbage is gone.

So the cost is not in the data but in the history: a changed entry rewrites its
whole leaf, so each retained snapshot keeps a superseded copy of every leaf it
touched. v3 trades history size for live-data size, and which way that falls
depends on how a repository is used — pruning regularly is cheaper than on the
packfile format, keeping every snapshot forever is dearer.

That cost is not proportional to churn, which is the part worth being precise
about (issue #525). The affinity key routes a directory's entries together, so
a backup rewrites roughly one leaf per *directory* it touched, and what a
retained snapshot keeps is about `directories touched x mean leaf size` — a
figure independent of how large the repository is. On a `source` tree, 200
churned files land in ~47 directories:

| | 2,000 files (23 MB) | 20,000 files (357 MB) |
|---|---|---|
| leaves in the tree | 19 | 219 |
| nodes rewritten per backup | 20 of 21 | ~91 of 296 |
| of the tree, by encoded bytes | 100% | 31% |
| stored per retained snapshot | 5.5 MB | 23 MB |

The small tree **saturates**: 47 touched directories cannot fit into 19 leaves,
so every leaf holds a change and each snapshot costs a full copy of the
repository. That is arithmetic, not a defect in the routing — no locality
scheme places 47 directories into 19 leaves without collision — and the only
cure is more leaves, which means a smaller budget, which is the read/storage
trade already settled at 4 MB. Once leaves outnumber the touched directories
the absolute cost settles at the product above and the *fraction* decays with
every file added.

At the larger size the comparison against the packfile format is:

| | packfile | v3 |
|---|---|---|
| 1 snapshot | 98 MB | 83 MB |
| 6 snapshots retained | 105 MB | 198 MB |
| after `forget --keep-last 1 --prune` | 103 MB | **82 MB** |
| growth per retained snapshot | ~1.4 MB | ~23 MB |

A pruned v3 repository is 20% smaller; a fully retained one is larger from the
second snapshot on. The gap is an additive ~23 MB per snapshot set by the leaf
budget and the churn's directory spread, not a multiple of the repository.

Two things this rules out, both measured rather than argued. Giving
metadata-only leaves a smaller budget has nothing to act on: leaves are 97%
inline file content and 3% metadata, and a `source` tree has essentially no
metadata-only leaves at all (0 of 19 at 2,000 files, 13 of 219 holding 7.5 KB
at 20,000), because the 512 KiB inline threshold catches all but the tail of a
document tree. Moving that content out to `chunk/` objects instead is the
design [#514](https://github.com/Cloudstic/cli/issues/514) already rejected,
for −2% stored bytes against +46% restore requests. `internal/cmd/leafstat`
is the instrument these come from.

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

One gap remains open, and it is not a defect in the format:

1. **`check` and `prune` request counts** stay above v2's, because v2 answers
   a full traversal from ~50 packs where v3 reads ~900 leaves. Both are
   nonetheless faster in wall time and move half the bytes.

**Whole-file dedup is reinstated, as placement reuse** (issue #514). The gap
recorded here — a duplicate small file re-read and re-stored, because v3 has
no `content/<hash>` to probe — turned out to cost far more than "the
redundancy compresses" allowed for once bodies moved out of leaves and into
`blob/`. Touching 300 files whose bytes did not change grew a repository by
1,568 KB against format 2's 216 KB, none of it compressible away, and the same
mechanism was 83% of what renaming a large directory cost (issue #543 §4).

What is reused is a *placement*, not an object: the entry points at the blob
member that already holds those bytes, so nothing is stored, no object is
added, and restore issues no request it would not have issued anyway. That is
the difference from the chunk promotion measured and rejected above, which
added an object per duplicated file and a request per referencing file. The
index behind it is populated by the change-detection sweep that already reads
every leaf, bounded in bytes the way `consolidateTrackBytes` is, and released
when the upload ends — it is written nowhere and read back nowhere, so it is
not the repository-wide content index this format exists to avoid. Measured on
a 3,000-file `source` tree: the touch above falls to 428 KB, all of it `node/`
and all of it reclaimed by `prune`, with zero new `blob/` bytes; `backup-dedup`
falls from 3,432 KB to 652 KB against v2's 620 KB; and an initial backup of the
same tree drops 9.5%, because duplicates that landed either side of a blob seal
were previously stored twice. That last figure is a function of the blob
budget, and shrinks as the budget grows: at the 8 MB budget this was first
measured against it was 15%, since a larger blob catches more duplicates on its
own.

### What retention costs, measured

A snapshot keeps a superseded copy of every leaf a backup touched. What that
costs is an occupancy curve rather than a linear law: with `L` leaves and `D`
directories touched, the leaves rewritten are `L(1 − e^{−D/L})` — linear while
`D ≪ L`, saturating at a full repository copy once `D` approaches `L`.

Measured with `gentree -churn-dirs` on the 20,000-file tree, 219 leaves:

| churn | directories touched | stored per snapshot |
|---|---:|---:|
| 200 files, capped at 24 dirs | 11–20 | 12–16 MB |
| 200 files, natural | 41–52 | 20–23 MB |
| 1000 files, natural | 190–197 | 46–49 MB |

Five times the volume gives four times the breadth and only twice the retained
bytes, because touched directories increasingly land in leaves already being
rewritten. Breadth is also not free to choose: it is coupled to volume by
fan-out, so 200 files cannot change in 4 directories of median fan-out 6.

In money this is small — daily backups kept a year cost about $2 to $8 more
than the packfile format at S3 Standard, and the ratio that looks alarming on a
357 MB repository is three per cent on a 1 TB one. **The case for separating
bodies from metadata is not the storage.** It is the 21x on every
metadata-only read, and the fact that write amplification then acts on 14.9 MB
instead of 311 MB.

### Side by side with the packfile format, under a retention policy

The section above measures what a *retained* snapshot costs while snapshots
accumulate. That is the right measurement for the write amplification the
format causes, and the wrong one for what a repository weighs, because nobody
keeps every backup forever. Expiry is the normal case and it changes the sign
of the result.

Both formats were aged in lockstep on one 20,000-file tree — same churn
sequence from `gentree -churn`, renames suppressed so the comparison is about
content change rather than about path identity — with `forget -keep-last 5
-prune` on every round past the fifth, which is what a scheduled backup with a
retention policy actually does.

| round | 5 | 10 | 15 | 20 | 25 | 30 |
|---|---:|---:|---:|---:|---:|---:|
| packfile format | 102 MB | 110 | 115 | 116 | 116 | **119** |
| format v3 | 94 MB | 99 | 100 | 105 | 101 | **102** |

v3 reaches a flat steady state around 101 MB; the packfile format drifts upward
by 17% over the same 30 rounds. Unpruned over 25 backups the ordering is the
opposite and dramatic — 223 MB against 148 MB — which is the figure the section
above predicts, and it is measuring a repository nobody operates. **v3's
retention cost is high and reclaimable; the packfile format's is lower and
partly is not.**

The rest of the comparison, at that steady state, with no local object cache so
that the numbers are properties of the formats:

| operation | packfile time | v3 time | packfile requests | v3 requests |
|---|---:|---:|---:|---:|
| `list` | 0.15 s | **0.07 s** | 8 | 5 |
| `ls` | 0.60 s | **0.22 s** | 189 | **34** |
| `find` | 0.97 s | **0.28 s** | 9,050 | **124** |
| `diff` | 0.64 s | **0.28 s** | 167 | **61** |
| `check` | 2.11 s | **0.36 s** | 7 | 4 |
| `restore` | 2.13 s | 2.18 s | 6,746 | **2,125** |
| `forget --prune` | 3.22 s | **0.62 s** | 1,143 | 1,380 |

Peak resident memory runs 136–325 MB for the packfile format against 89–174 MB
for v3, and the packfile figures grow with snapshot count where v3's do not.

Two results in that table are worth more than the ratios. `find` at 9,050
requests against 124 is the metadata-read case the format was built for,
arriving at 73x rather than the 21x the targets asked for. And a *partial*
restore — one subtree, three files, 20 KB — moves **115 MB in the packfile
format against 4.2 MB in v3**, because a packed repository has no locality
between a subtree and the packs its metadata landed in, so a scattered read
touches most of them. Restoring one folder is a more common operation than
restoring everything, and it is the widest gap measured anywhere in this work.

### What the local object cache changes about all of this

The disk cache (`internal/storelayer/diskcache.go`) was added after these
measurements and it moves both formats, unequally, so the comparison above is
the uncached one on purpose: with a cache warm enough, the winner is whichever
format has fewer distinct objects to fetch, which is a different question.

It also corrects a claim this RFC's read path is built on. Ranged reads were
taken to be strictly cheaper than whole-object fetches, and for a full restore
they are not: coalescing merges spans within one batch but never across
batches, so each of a repository's blobs is re-read about ninety times, and the
restore moves **854 MB to read 74 MB of blobs — 8.4x**. The packfile format is
not exempt, at 3.9x. A cache that holds whole objects brings both to about
1.0x. The `restoreIOGap` reasoning still holds for a partial restore, which was
measured to be byte-identical with the cache and without it, because an object
read once never reaches the threshold that promotes it.

So the honest summary of the read path is narrower than §5 states it: ranged,
coalesced reads are right for reading *part* of a repository and wrong for
reading all of it, and nothing in the format tells the reader which it is
doing. Stating demand — the mechanism RFC 0025 §2 describes — is what would
close that, and it is not implemented for blobs.

### What is not measured here

- Everything above is a local store. Request counts and bytes transferred are
  properties of the work and carry over; wall time does not, and no figure here
  is evidence about latency against object storage.
- One churn pattern on one tree, at 20,000 files. The retention steady state in
  particular is a property of the churn's breadth, which §"What retention
  costs" shows is the variable v3 is most sensitive to.
- Peak resident memory is transient-dominated: under an aggressive collector
  the same operations retain 24–32% less, so the figures above overstate what
  is held rather than allocated.

## Alternatives considered, and what they rule out

Each of these was reached for and put down again, and the reasons are
load-bearing: taken together they are what says the remaining trade is
intrinsic, and what the format would have to give up to escape it.

### Why the format had only one dial

Sweeping v3's knobs — leaf budget at 2/4/8 MB, routing arity at 1/2/3 bits, the
inline threshold, chunk promotion — always produced the same shape: requests
against stored bytes against write amplification, with no setting winning
everything. That is not a missing idea.

**The read/update trade is provably intrinsic.** Brodal and Fagerberg (SODA
2003) showed that in the external-memory model a dictionary whose insertions
cost `O(λ log_λ N / B)` must admit a query costing `Ω(log_λ N)`, and the
Bε-tree meets that bound. Choosing the leaf budget *is* choosing λ, so every
sweep was moving along a proven-optimal curve. Looking for a cleverer
arrangement of leaves was the wrong search.

**But the retention cost is not on that curve.** v3 makes its tree persistent
by *path copying*, the least efficient of the standard techniques since
Driscoll, Sarnak, Sleator and Tarjan (1986). The multiversion B-tree (Becker et
al., VLDB J. 1996) retains every version in space **linear in the number of
updates**, and the buffered version (ESA 2025) achieves that together with
Bε-tree update bounds.

**And full scans do not pay for buffering.** In those bounds the buffering
factor multiplies the `log_B N` term and not the `K/B` output term — and
`restore`, `check`, `prune` and `find` are all `K/B`. That is what makes this a
backup-shaped problem rather than a database-shaped one.

What none of that explains is why v3 has only *one* knob. It has one because
metadata and content share an object, so a single size serves two opposed
purposes. Separated, there are two:

| dial | trades | acts on |
|---|---|---|
| metadata leaf size (`maxLeafEntriesV3`) | traversal requests **vs** metadata rewritten per backup | 14.9 MB |
| content blob size | restore requests **vs** content rewritten per backup | 297.5 MB |

Write amplification keeps its shape but multiplies 14.9 MB rather than 311 MB.
A content blob is also not routed, so it escapes the ~50% fill that split
geometry imposes on a leaf (see `bitsPerLevelV3`): 297.5 MB packs into ~74
blobs at 4 MB where it occupies 219 leaves today — 3x fewer objects for the
same bytes before anything else changes.

### Why "inline" and "chunked" do not collapse

The tempting simplification is that a chunk is just a blob holding one body, so
the inline threshold disappears and v3 has one content concept instead of two.
It does not, and the reason is worth stating because it explains the threshold
rather than merely preserving it.

**Chunks are shared between different files almost never.** Measured across
snapshot sequences, distinct chunks appearing under more than one path within a
single snapshot are 21 of 2,578 on `media` and 5 of 68 on `source` — **under
1%**. Every one of those was verified to span genuinely different files rather
than repeated positions in one.

That is the only thing content addressing earns here, and it is worth being
precise about why. An earlier draft of this section reported a "4.51x / 13.51x
dedup factor" and read it as content addressing paying off on the temporal
axis. Both halves were wrong. The factor is just the snapshot count times the
survival rate — divide it by the number of snapshots and a retention rate is
what remains — and the temporal axis is not bought by content addressing at
all: change detection re-inserts an unchanged entry with its previous payload
verbatim (`internal/engine/backup_scan.go`), so the file is never re-read,
never re-chunked, and no `Exists("chunk/<hash>")` probe is ever issued.

**But that is not what settles it.** A chunk is already at least
`inlineThreshold` bytes, because that is the CDC minimum chunk size — the two
constants are the same number for that reason. Chunks are therefore *already*
objects of the size blobs exist to create. Packing them into blobs would buy a
smaller factor than it costs, and it would require either a hash-to-location
index — the catalog this format removed — or dropping content addressing for
chunk bodies and comparing against the previous version's chunk list instead,
which recovers the temporal dedup but loses the spatial and adds a hash per
reference to every entry.

So the threshold survives, and its meaning is precise — and it does not rest on
the dedup argument at all: **it is the size at which a body is already large
enough to be its own object.** Below it, bodies
are too small to be objects and are packed into blobs, addressed by position,
never deduplicated (which issue #514 measured and accepted). Above it, chunking
already produces blob-sized objects, addressed by content, deduplicated for
free.

Blobs and chunks are the same idea — aggregate until an object is worth a
request — applied at the two sizes where the answer differs.

### The aggregate is a container, not a cryptographic unit

This is the finding that most changes the design, and it contradicts something
an earlier draft treated as a constraint.

Cloudstic seals each stored object as one AES-256-GCM box with a random nonce
(`crypto.Encrypt`). A ranged read is therefore useless, and the RFC recorded
that as a property of encrypted backup. It is a property of *this* repository.
Of eight comparable tools surveyed, seven apply the AEAD to the individual
deduplicated unit and not to the aggregate:

| tool | aggregates? | AEAD applied to | can range-read one member? |
|---|---|---|---|
| restic, rustic | packs | each blob | **yes** |
| Kopia | packs, 20–40 MB | each content | **yes** |
| Borg | ~500 MB segment logs | each chunk | **yes** |
| Duplicacy, bupstash, Tarsnap | one object per unit | each chunk/block | n/a |
| **Duplicati** | 50 MB zip volumes | **the whole volume** | **no** |

Duplicati is the only tool that shares our constraint, and it is not the one
anyone holds up as the reference design.

For the aggregating tools this is deliberate, not incidental. restic's design
document gives the reason in its own words: blobs are "authenticated and
encrypted independently", which "enables repository reorganisation without
having to touch the encrypted Blobs" and lets a reader authenticate a pack's
header without reading the pack. Kopia's read path issues a ranged fetch of
exactly `(PackOffset, PackedLength)` and decrypts only that slice. All of them
compress the *member* rather than the aggregate, for the same reason.

**What this changes here.** An earlier justification for keeping blobs near
today's leaf budget was "blob size bounds the waste on a targeted read".
With per-member encryption that justification disappears: a targeted read
fetches and decrypts exactly its member. Blobs can then be sized for whatever
restore and reclamation want — larger, and fewer — without penalising `cat` or
a path-scoped restore. It removes one of the two dials' worst constraint.

**A second, free win.** Our per-object key is fixed and the nonce is random, so
the same plaintext encrypts differently every time. Deriving the key or nonce
from the object's own plaintext hash — Duplicacy does exactly this, and
Kopia derives its IV from the content hash — makes encryption *deterministic*.
For a content-addressed store that makes a re-upload byte-identical, so writes
become idempotent and retries free. Nonce reuse across different plaintexts
would require a hash collision, because two objects with the same key are the
same object.

**What stays true, and belongs in `docs/compatibility.md`:** a ranged read can
never authenticate more than it fetches. Whole-object AEAD gives whole-object
authenticity by making the reader pay for the whole object; anything cheaper
gives correspondingly less. Segment-level schemes (the STREAM construction,
Tink's `AES-GCM-HKDF-STREAMING`) recover *positional* and *object* binding by
putting the segment index and a final-segment flag in the nonce and the object's
name in the AAD — but a reader of one segment still learns nothing about the
segments it did not fetch. `check` and `restore` read wholes and keep the strong
guarantee; targeted readers get the weaker one knowingly.

**One caveat worth carrying.** Kopia packs partly "to obscure individual content
sizes", and per-member framing partially undoes that by making boundaries
visible in the object's layout. Fixed-size segments leak nothing beyond total
length, which whole-object AEAD leaks anyway. This is not hypothetical: the 2025
result on chunking attacks against content-defined chunking (Alexeev, Percival
and Zhang) recovers chunker parameters and then fingerprints files by their
compressed chunk sizes, on Tarsnap among others. Whatever framing is chosen
should be decided with that paper in hand.

### What this rules out, and why

- **Ranged reads inside an object are unavailable *as this repository is
  built*, and that is a choice rather than a law.** The chain is
  `CompressedStore → EncryptedStore → MeteredStore → <backend>`: a node is one
  zstd stream inside one AES-256-GCM box with a random nonce, so byte *k* of the
  stored object is unrelated to byte *k* of the node and the tag authenticates
  all or nothing. An earlier draft recorded that as inherent to encrypted
  backup. It is not — see *The aggregate is a container, not a cryptographic
  unit*, which is the one finding that most changes this design.
- **Buffering updates in the tree is the same design Kopia ships, and its
  failures are public.** Kopia's epoch manager advances on >20 index blobs and
  >24 hours; issue #5057 is a repository at 28,467 index blobs where ten full
  maintenance runs *increased* the count, because a clock-based safety window
  held a backlog permanently, and #3224 is the epoch manager compacting on a
  read path and hanging against read-only storage. Any buffering here must
  bound on *absorption* rather than a clock — which is what this repository's
  own pack-catalog compaction already does.
- **Prolly trees offer nothing v3 lacks.** Hash-routed HAMT already has history
  independence, structural sharing and diff by content address, and they do not
  escape the trade either: Dolt targets 4 KB chunks where v3 targets 4 MB, a
  difference that is purely the cost model.
- **This is not issue #514.** That promoted bodies to *per-file* `chunk/`
  objects, where a body shared by nine files is fetched nine times. A blob is
  per run of entries, fetched once by whoever reads that run.

### Why the HAMT stays

The survey — *What a survey of the field says about the HAMT* — found that no
other backup tool hash-routes metadata, that directory-mirroring wins on
locality and on renames, and that the HAMT's case rests on bounded node size
and depth-independent write amplification. On that
evidence the trie looked like the weakest part of the format. It is not, and the
argument that saves it only becomes visible once content leaves the leaves.

**A directory-mirroring tree makes objects far too small to be worth a
request.** The 20,000-file tree has 1,548 directories and 14.9 MB of metadata:

| structure | objects | each | against the PUT break-even |
|---|---:|---:|---|
| directory-mirroring | 1,548 | 9.9 KB | **23x below** |
| hash-routed HAMT | 33 | 462 KB | 2.1x above |

One S3 PUT costs what storing 223 KB for a month costs, so a 9.9 KB object is
dominated by its own request fee. That is why every directory-mirroring system
in the survey packs — and packing is what needs an index mapping object to
`(pack, offset)`, which is the catalog RFC 0023 failed to bound and this format
was created to remove.

**So the trie is not an eccentricity; it is the aggregation.** restic and Kopia
aggregate metadata with packs plus an index. This format aggregates it with a
trie and needs no index, because a node *is* the aggregate and its content
address *is* its location. Same goal, different mechanism, and ours is the one
that does not accumulate a catalog.

That reframes the survey's finding rather than contradicting it. The HAMT's
advantages really are bounded node size and shape-independent write
amplification — and "bounded node size" turns out to mean *bounded below* as
well as above. A directory-mirroring tree has no lower bound at all: a
directory with three files in it is an object with three files in it.

**What it costs, stated honestly.** Exact path locality, which the affinity key
only approximates and which the survey measured as the metric mirroring wins
outright; and free subtree renames, which are worth about 9% of retention cost
here and are fixable independently by giving local and SFTP sources a
rename-stable identity (issue #543). Both are real. Neither is worth
reintroducing a catalog for.

**And the two are not mutually exclusive.** plakar keys a B+tree by path *and*
keeps a per-directory packed object, which is a directory-mirroring read path
over an aggregated storage layer. If path locality later proves to matter more
than these measurements suggest, that hybrid is the shape to reach for — but it
buys locality by adding the index this format exists without, so it is a
different bet rather than an improvement on this one.

### What object storage actually charges, and who else has built this

**Two of our four backends stopped charging for requests.** Backblaze made all
standard B2 API calls free on 1 May 2026, raising storage from $6.00 to
$6.95/TB-month to pay for it; Wasabi has never charged per request, subject to a
fair-use clause. So on B2 and Wasabi, **aggregation buys latency and nothing
else** — the money argument that motivates this whole format applies to S3 and
R2 only. That is a change to the premise, not to a constant, and it should be
stated wherever the format's rationale is.

Where requests are still billed the arithmetic is stark (checked 2026-08-29,
us-east-1). One S3 PUT costs the same as storing **223 KB for a month**. Writing
1 TB of new data costs **$83.89** in PUT fees at 64 KB objects and **$0.66** at
8 MB. GETs are 12.5x cheaper than PUTs, so on the read side money stops
mattering above a few kilobytes and **latency is the binding constraint**.

That last point has a formula, and it is the most directly useful number this
survey produced. Arrow's range coalescer merges two byte ranges whenever the gap
between them is smaller than the bandwidth-delay product:

```text
max_io_gap = time-to-first-byte x bandwidth
```

At 50–100 ms and ~90 MB/s that is **4.5–9 MB**; on a domestic uplink
(10 MB/s, 100 ms) it is **1 MB**. In other words it is cheaper to fetch and
discard several megabytes of unwanted bytes than to issue a second request.
**That is the number that should size a blob and drive a restore planner**, and
it is a better basis than the 4 MB we arrived at by sweeping.

**Someone has already shipped this design.** Arq 7 stores a `BlobLoc` inside the
tree node carrying `relativePath`, `offset`, `length` and `isPacked` — a file's
bytes are located by **one ranged GET with no index lookup, because the pointer
is the range**. That is precisely the blob reference proposed here, in a
shipping product, and it is the strongest evidence that the shape is viable.
Duplicati is the control: it aggregates into 50 MB volumes but cannot read
inside them, and its documentation states plainly that it "needs to download the
entire remote volume" — aggregation without ranged reads, which is what we have
today.

**And a mature tool has just gone the other way.** Borg 2 removed both its
segment files and its repository index: "Repository stores objects separately
now… no repository index is needed anymore because we can directly find the
objects by their ID." A well-engineered project looked at the same trade and
abandoned aggregation entirely, accepting more filesystem overhead to be rid of
compaction. On a backend with free requests that reasoning is stronger than
ours, and it deserves to be weighed rather than dismissed.

Two further ideas worth stealing:

- **rustic's hot/cold split.** `--hot-only` mirrors *all metadata* — under 1% of
  a repository's bytes — into a second always-warm bucket while packs go to
  Glacier, so `ls`, `diff` and `snapshots` never touch cold storage. That is a
  user-visible feature the metadata/content separation proposed here would make
  possible, and it is a better argument for the separation than any allocation
  number.
- **Parquet stratifies metadata by access frequency.** A footer at a fixed
  discoverable position holds what every reader needs; the page index sits
  *adjacent* to the footer so it coalesces into the same request when wanted and
  costs nothing when not; page headers are inline with the payload. Reading one
  column of one row group is **two requests**, independent of how many columns
  or row groups were skipped. Iceberg v4 applies the same instinct to the
  versioning problem under the name *commit amplification*, and its remedy —
  inlining small changes into the root rather than rewriting it — is a direct
  suggestion for any format that aggregates into leaves.

**No published work addresses this exact trade**: aggregating small objects to
cut object-store request counts, against the write amplification that
aggregation causes under versioning. The deduplication-fragmentation literature
has aggregation and versioning but prices reads in disk seeks; the table-format
literature has aggregation, versioning and request costs but no deduplication
and no tree-restore access pattern; Tarsnap and WarpStream have aggregation and
request costs but treat versioning as an append-only log. The gap is where this
RFC sits.

### What a survey of the field says about the HAMT

Thirteen systems were classified by how they represent directory metadata.
There are three families, not two:

- **Directory-mirroring** — one object per directory. restic, rustic, Kopia,
  bup, Perkeep, git, OSTree.
- **Chunked metadata stream** — items serialised into one stream and
  content-chunked, often with smaller chunker parameters than file data. Borg,
  Duplicacy, bupstash, Tarsnap.
- **Flat sorted index** — plakar, a path-keyed B+tree.

**No backup tool hash-routes file metadata into a global trie.** Nor is there a
design document or thread in any of these projects rejecting the idea; it
appears never to have been considered, which is weaker evidence than a
considered rejection and is recorded as such. Hash routing is routine one layer
down — btrfs keys directory entries by `crc32c(filename)` in a global B-tree —
but those structures update in place, so none of the content-addressed
rewrite cost applies.

The one real precedent is **Hyperdrive**, which in v10 adopted "hypertrie, an
append-only implementation of a hashed array-mapped trie" for a
content-addressed Merkle store, citing scaling to tens of millions of files at
`O(log₄ n)` requests — very close to this format's reasoning. Hyperdrive v11
replaced it with Hyperbee, an append-only *path-ordered* B-tree, and the whole
API became prefix-range shaped. The move is verifiable from primary sources;
**no published rationale for it could be found**, so it is suggestive rather
than dispositive.

**Where the HAMT genuinely wins, and it is not where this RFC assumed.**

| | HAMT | directory-mirroring |
|---|---|---|
| path-scoped read locality | clustered, never contiguous | **optimal — one object is one directory** |
| write amplification per changed file | `O(log₃₂ N)` bounded nodes, independent of tree shape | `Σ` ancestor fan-outs — unbounded if any ancestor is wide |
| a 100k-entry directory | **free** | needs an explicit splitter |
| subtree rename | free only for ID-stable sources | **free always** |

So the case for the HAMT rests on **bounded node size** and **depth-independent
write amplification** — not on locality, which mirroring wins outright, and not
on renames. The wide-directory failure is real and not hypothetical: restic
reports 2.8 GB of peak memory for a one-million-file directory, and Kopia's
issue #1542 proposes *mtime-bucketed* sharding for the same problem — reaching
for churn locality, which is the opposite of what hash spreading provides.

**And our rename story is worse than the table suggests.** `AffinityKey` is
built from `parentID` and `fileID`, and a local or SFTP source sets
`FileID` to the normalised path (`pkg/source/local/source.go:221`,
`pkg/source/sftp/source.go:219`). Renaming a directory therefore changes every
descendant's routing key, re-routing and rewriting the whole subtree — `O(subtree)`,
strictly worse than mirroring, on the most common source. Drive and OneDrive,
which carry stable provider IDs, are unaffected. This is a defect in the format
as it stands rather than in the separation of bodies from metadata, and it is
inside the retention measurements above, since `gentree` renames one directory
per churn round.

**A hybrid is where at least one system independently landed.** plakar keys a
B+tree by full path *and* keeps a second tree mapping a parent path to a single
packed object holding that directory's entries. Whoever built that started from
a global index and found they needed directory-granular objects for read
locality anyway — which is the same conclusion the affinity key gropes towards.

### Where v3 actually stands against v2, and what other tools do

Two things were checked before committing to the separation: what v3 costs
against v2 on identical history, and whether separating metadata from content
is a good idea or merely a local one.

**On identical history, v3 stores 3.4x more than v2.** Same 20,000-file tree,
same 14 backups, same churn seeds, same seeds for everything, local unencrypted
store:

| | v2 (packfile) | v3 |
|---|---:|---:|
| repository on disk | **123 MB** | **415 MB** |
| `check` | 1.78s, 1,221 MB alloc | 0.59s, 822 MB alloc |
| `restore` | 2.02s, 1,602 MB alloc | 2.42s, 1,739 MB alloc |

That is not a tuning gap. v3 bought its flat request curve — the aging tables
in *What the measurements showed*, where v2 grows about seven requests per backup and v3 does not — and paid
for it in stored bytes. On a local disk that is a bad trade; against object
storage with a year of history it is the trade the format was designed to make.
But 3.4x is large enough to be a user-visible regression, and it is the axis
this design closes: bodies written once put the repository at roughly 121 MB
stored, which is parity with v2.

**And the separation is what every comparable tool already does.**

- **restic** stores one tree object per directory and packs trees and data
  apart. Its format version 1 said data and tree blobs *should* be in separate
  pack files; **version 2 says they must be**. They hardened the rule when they
  revised the format.
- **Kopia** stores directory listings as objects with a `k` prefix and routes
  every prefixed object into `q` metadata packs, with file content in `p` data
  packs — the split is enforced by the storage layer rather than left to the
  caller.
- **Borg** serialises item metadata into its own stream and runs it through the
  chunker with *different parameters* from file data, deliberately producing
  smaller chunks for metadata.

Three tools, three mechanisms, one decision. **File bodies inside the metadata
structure is the outlier**, and v3 moves them back out towards what the field
settled on.

**The part that is genuinely unusual is the HAMT, and it deserves its own
question.** restic and kopia both use a directory-mirroring tree, where a
directory *is* an object. That gives exact directory locality for free, makes a
path-scoped read exactly a subtree read, and means a changed file rewrites its
ancestor chain of small objects rather than a 4 MB leaf. v3's `AffinityKey` —
`parentHash[:4] + fileHash[4:]`, with the 16 bits of directory locality noted
in *The encoding → What packs a blob: walk order* — is a workaround for
something a directory tree does not need to work around.

The reason v3 does not do that is the reason it exists: one object per directory
is a great many small objects, which needs packing, which needs an index, which
is what RFC 0023 failed to bound. So the real axis is **index or no index**:

- restic and kopia keep an index. Repacking is free, because the index absorbs
  the move. The index is what must be bounded, and Kopia #5057 is what happens
  when that bound fails.
- v3 has no index. There is nothing to bound, but repacking cannot rewrite
  history, so reclamation has to consolidate forward — and until it does, prune
  reclaims nothing.

That bet is coherent and this design keeps it: an entry names its blob
directly, so no catalog appears. Whether the *metadata* structure should stay a
HAMT is a separate question that this RFC does not answer and does not
depend on, and it is the strongest remaining reason to think v3 is not yet in
its final shape.

## What would break silently

A survey of every producer and consumer of `Payload.Inline` turned up four
places that keep compiling and stop being correct. They are listed first
because they are the ones a compiler will not find.

**1. `prune` would delete every blob.** This is data loss and it passes through
three layers unchanged. `prune`'s v3 marking uses `Tree.WalkChunkRefs`, whose
callback is `func(key, value string, chunks []string, hasPayload bool)` — there
is no slot for a blob ref. Underneath, `loadChunksOnly` decodes with
`payloadChunksOnly`, which `skipBytes()`es exactly the region where a blob ref
would sit. So the entry reports `chunks=[], hasPayload=true`, prune concludes it
reaches nothing, and the sweep collects the blob. `hasPayload` is *true*, so the
safety valve that refuses to prune an entry whose refs are unknowable never
fires.

What makes it worse: `objectPrefixesV3` does not list `blob/`, so today the
sweep would not delete them — **the two bugs cancel**. Adding `blob/` to the
prefix list, which any correct implementation must do or blobs leak forever, is
what *activates* the deletion. Either half alone is wrong in a different
direction.

This is precisely the hazard `WalkChunkRefs`'s own doc comment warns about in
prose. That warning was written about `Inline`; the change makes it true of the
body pointer instead, and the reduced decoder must learn about blob refs in the
same commit that adds them.

**2. `check` would stop verifying that bodies exist.** The per-entry chunk loop
in `checkLeafEntry` is the only existence check a default `check` makes. A
blob-referencing entry has an empty `Chunks`, so the loop runs zero times and
the entry passes. A repository whose every blob had been deleted — by bug 1 —
would report healthy. Only `-read-data`, which is opt-in, would catch it.

**3. `restore`'s directory detection degrades silently.** It uses
`len(p.Inline) > 0 || len(p.Chunks) > 0` as a cheap "this entry has content, so
it is not a directory" test, and a blob-bodied file matches neither. The answer
stays correct because a type check catches it, but the pass then decodes
`p.Meta` for every entry in the snapshot instead of a few per cent — an
order-of-magnitude regression in a pass that exists to be cheap.

**4. `restore`'s worker pool changes character.** Writing an inline body is a
memory copy, which is why the pool hands payloads to workers without copying and
is capped at 16. With a blob ref, each small file becomes a store fetch inside
that fan-out. The streaming-restore premise — "the leaf a file's metadata comes
from is the leaf its content comes from, so reading it once serves both" — is
exactly what the change removes, and the pool would need resizing against a
different cost.

Two more that are visible but easy to get wrong:

- **`copy`'s payload-elision must be deleted, not ported.** It caches an entry
  without its payload because inline bytes are unbounded in aggregate. With a
  blob ref a payload is a few hundred bytes and the cache can hold it — but
  rewriting the condition as `BlobRef != ""` would make copy re-read and
  re-write the body on every repeated visit to the same file, which is every
  snapshot after the first in a lineage. Deleting the mechanism is right;
  translating it is a performance trap.
- **The split rule has to be re-chosen, not inherited.** As in
  *Proposal → 2. Metadata leaves*,
  `leafSplitBytesV3` stops binding and `maxLeafEntriesV3` becomes the whole
  rule. Related constants inherit the same mistake: the node cache is sized as
  "16 leaf budgets", which would then bound a wildly different number of
  entries, and `sealFlushBytes` is justified in terms of "every dirty leaf's
  encoded bytes — inline file content included". All three are measuring a leaf
  that no longer exists.
- **`objkey` needs `blob/` appended to its namespaces.** Without it the
  reachable set falls back to a `map[string]struct{}` for what would be the
  repository's largest namespace — correct, silent, and it undoes the compact
  representation that exists so the set fits in memory.

## Constraints any implementation must respect

Verified against the code while working through this:

1. A node's ref is the SHA-256 of its plaintext bytes, checked in
   `NodeStore.load` for every consumer. Any split encoding must keep every
   fetched object independently verifiable, or the Merkle chain breaks on
   exactly the reads it was added for.
1. `diff` skips identical subtrees by ref (`internal/hamt/hamt.go`). A design
   where a subtree's identity stops being a content address of its contents
   loses that.
1. Reads must never require a write. `LoadRepoConfig` deliberately does not
   stamp a version on read paths, because restore runs under read-only
   credentials. Repacking can never be a precondition for reading.
1. "Cannot decode" is never "empty" (`docs/compatibility.md`). An unreadable
   blob must fail its operation, and `prune` must abort rather than treat an
   entry whose bodies it could not read as reaching nothing.
1. Anything bounded only by maintenance is unbounded.
   `packIndexCompactThreshold` exists because shard count "grows with the
   number of backups a repository has ever taken, and only `prune` ever bounded
   it"; Kopia #5057 is that failure left to run for a week.
1. Compaction must not delete what a concurrent reader listed — remove only
   what the store has itself absorbed.
1. WORM mode (RFC 0020, draft) cannot delete a superseded blob, so repacking
   there is pure growth and probably should not run.
1. `copy` between repositories (RFC 0017) must transfer a whole chain or
   repack in flight.
1. Whole-leaf compression is a measured win; splitting an object into
   independently compressed pieces spends stored size to buy read locality.
1. Decoded payloads must be copied out of the transport buffer, not aliased.
   Aliasing was measured and is worse on every axis, because a small retained
   slice pins the whole object's buffer (see `v3Decoder.bytes`).

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

Stages 1–3 of the original plan are built; *Appendix: how the design changed*
keeps them. What remains is the blob-body work, ordered by the blast-radius
survey in *What would break silently*, and then the format flip.

1. **The member seal** — an AAD-taking, deterministic-nonce primitive in
   `pkg/crypto` with its own HKDF info string. It is the only step that moves
   the security boundary, so it lands alone and reviewed, ahead of any caller.
1. **The leaf encoding and the blob writer**, together, since a blob reference
   is meaningless without something to point at.
1. **`prune` and `check` in the same change** — not after. Adding `blob/` to
   `objectPrefixesV3` without teaching the reduced decoder about body
   references activates a deletion bug that today's two halves happen to
   cancel, and `check` would report a repository healthy after it.
1. **`restore`**, including resizing its worker pool, which is currently
   bounded on the premise that writing a body is a memory copy.
1. **`copy`**, deleting the payload-elision mechanism rather than translating
   it.
1. **Consolidation last**, because a repository can be correct without it and
   cannot be correct without the four above. Trigger on how many distinct blobs
   the newest snapshot must read, with utilization as a secondary filter, under
   a bounded per-backup rewrite budget.

The consolidation threshold is a default to pick rather than a question to
answer: 50% is the obvious start, and the harness can sweep it once there is
something to sweep.

Then the format flip, unchanged from the original plan. Each stage is a PR or
small series; the format flips only once.

1. **Tune and gate.** Run the harness matrix plus aging on v3; choose
   `leafTargetSize` and `inlineContentMax` against targets 1 and 4; record
   the sweep in *What the measurements showed*.
1. **Flip the default.** `init` writes v3; v2 write paths freeze.
1. **Cut the transition release, then delete.** With the migration tool
   shipped in the transition release, main removes `internal/storelayer`'s
   pack files, the v2 leaf/filemeta decoders, and the public `ReadPlanner`
   surface (docs PR paired, per RFC 0022).

## Open questions

1. ~~**Leaf byte budget.**~~ **Answered, and then answered differently.** The
   original sweep chose 8 MB, on the reasoning that a changed entry rewrites
   its whole leaf *including its neighbours' untouched content* — which was a
   property of fat leaves and is gone. That sweep is kept in the Appendix as
   the record of a question asked about a format that no longer exists.

   For the format as it now stands the budget is 4 MB and **does not bind**:
   the largest leaf on a 20,000-file `source` tree is about 300 KB, so
   `maxLeafEntriesV3` (2048) is the split rule in practice. Sweeping the
   effective leaf size, 9 retained snapshots:

   | leaf size | nodes read per traversal | metadata per snapshot |
   |---:|---:|---:|
   | 128 KB | 530 | 1.41 MB |
   | 256 KB | 238 | 1.98 MB |
   | 512 KB | 105 | 2.77 MB |
   | ~196 KB median at the current caps | 29 | 3.51 MB |

   Smaller leaves buy retained bytes and cost requests at a bad rate: 128 KB
   leaves would multiply the nodes a traversal reads by 18 to save 2.5x on
   metadata — about 15 MB of a 107 MB repository, against a restore that
   issues ~214 requests in total. The budget not binding is the tree sitting
   where it should, and lowering it until it does would be a regression.

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
   measuring it — see *What the measurements showed*. The bytes are real
   (2–4.5% of stored size) and cost more in restore requests than they are
   worth, because a leaf's bytes are already being read while a
   chunk's are not. Note this stayed rejected while whole-file dedup was
   reinstated (issue #514): the two are different designs, and only this one
   adds objects and restore requests.
1. **Does `index/snapshots` want the same treatment later?** It is already a
   reconciling cache; nothing here changes it, but a v3 follow-up could fold
   snapshot summaries into a leaf-like object. Out of scope now.

Four more were opened by moving bodies out of the leaves. All four are
answered, and are kept struck through because what answers them is a
measurement rather than a decision:

1. ~~**How fragmented do blobs actually get?**~~ Answered in *Consolidating
   sparse blobs forward*: 15% waste, uncollectable without consolidation,
   halved for under 6% extra bytes written with it. What is left is choosing a threshold, which is tuning
   rather than soundness.
1. ~~**Does the inline threshold survive?**~~ Answered in *Alternatives
   considered → Why "inline" and "chunked" do not collapse*: yes, and it
   means something sharper than it did.
1. ~~**What packs a blob?**~~ Answered in *The encoding → What packs a blob:
   walk order*: walk order, which also wins the path-scoped-restore metric
   that routing order was supposed to win. The remaining requirement is that
   the packer preserve walk order under concurrency, which it does not get for
   free.
1. ~~**Does the metadata tree still want hash routing?**~~ Answered in
   *Alternatives considered → Why the HAMT stays*: yes, and for a reason
   neither this RFC nor the survey had assembled — the trie is what makes the
   metadata objects large enough to be worth a request *without* an index.

All four are answered and the encoding is written, so what remains is
building it — see *Sequencing*.

## Appendix: how the design changed

Format v3 was proposed with file bodies inside the HAMT leaves, built that
way behind `init -format 3`, and measured. The measurements moved the bodies
back out into `blob/` objects, which is the design the rest of this document
describes. Nothing in this appendix is current. It is kept because the
format is only legible against what it replaced, and because a design
changing under measurement is the part of the record worth re-reading.

### The thesis that changed

The fat-leaf thesis was that v2 minted too many small objects, and that
aggregating an entry's metadata *and* its content into one leaf fixes it. The
aggregation was right. Putting those two things in the *same* object was not.

The abstract said it this way while both designs were in the document:
small-file content moved into the leaf too in the version first built, and
moved back out into `blob/` objects an entry points at, because a leaf
turned out to be 3% metadata and 97% content and every operation but
restore reads only the 3%. The measurement behind that is in
*Proposal → 3. Blob objects*.

### Why changing it was free

This is a revision of v3 rather than a new format because **format v3 has never
been released**. The most recent release, v1.18.0 (2026-08-02), stamps
`MaxSupportedRepoFormat = 2` and cannot read a v3 repository at all; v3 landed
on `main` on 2026-08-28, twenty-six days later. So there is no repository
anywhere that a change here could strand, and no build in a user's hands that
could misread one.

That freedom is absolute today and ends the moment a release is cut from `main`
— not when #517 flips the default, which is the weaker claim an earlier draft
made. A release cut today would ship both the v3 writer (`init -format 3`) and
the v3 reader, and nobody would notice until someone created a repository with
it. Issue #545 tracks guarding against that.

### Fat leaves, as first proposed

This is §2 of the proposal as first written. Where it says a leaf carries an
entry's *content*, the current design has the leaf carry a reference to a
`blob/` object holding it — see *Proposal → 2. Metadata leaves* and
*3. Blob objects*, which take precedence throughout. The object model, the
removal of packs, the routing and the batched deletes all survive unchanged,
and knowing which parts did not is the point.

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

Two smaller claims went with it. The object-model table described
`node/<hash>` as a "HAMT spine node or fat leaf" whose leaves "carry
metadata + small content"; it now describes a metadata leaf carrying a body
reference, and gains a `blob/<hash>` row. And the read path claimed that
"the two-phase metadata/content separation that made streaming restore
regress (RFC 0025 §8) disappears with the class distinction, which is
exactly the seam RFC 0024 predicted". Separating bodies from metadata brings
that seam back; it is item 4 of *What would break silently*.

### What building it established

*The encoding* was written before any of it existed. Four things only
became visible once it did, and three of them are properties of the format
rather than of the code.

**A repository is no longer a function of its contents.** A leaf records
*where* a body landed, and placement depends on what else was packed alongside
it — which depends on the order upload workers finish. Two identical backups of
one tree therefore produce different root hashes. Measured, and isolated:
chunking every body instead restores determinism, so the body reference is
precisely the cause.

This was free under fat leaves, where a payload held content that is identical
whoever wrote it. Recovering it now would need one of: buffering every body
before packing, which is unbounded memory and the regression #526 fixed;
deriving blob membership from the content hash, which scatters a directory's
files and destroys the locality blobs exist for; or an index, which this format
exists to avoid. Determinism and locality are in direct conflict once a leaf
records physical placement.

It is given up deliberately. Restic, borg and kopia all produce different pack
layouts for identical input. The price is that copying a repository into one
that already holds the same snapshots no longer deduplicates the trees.

**The mark may not deduplicate on an entry's metadata ref.** Identical metadata
says nothing about where the body was packed — a re-upload puts the same bytes
into whatever blob is open — so an entry reached twice can name two different
blobs. Skipping the second marks the first blob only, and the sweep then
deletes data a retained snapshot needs. This is a garbage collector deleting a
live repository, reached by an optimisation that looks obviously safe, and it
is why `EntryRefs.Objects` exists rather than a field-by-field loop.

**`check` must verify a body reference outside `-read-data`.** Chunk refs are
verified unconditionally, and a body-referencing entry has no chunks, so a
default run otherwise checks nothing about an entry's content — a repository
missing every blob reports healthy. Confirming the blob exists and is long
enough for the range claimed costs no read, since its size is enough.
`-read-data` remains the reconstruction check. Nothing obliges a user to run it
before trusting a `check`.

**The blob budget is specified in the wrong units for its own derivation.** It
counts plaintext bytes, while the bandwidth-delay product it was sized from is
about transferred bytes, and members are compressed. An 8 MB budget yields a
median 2076 KB object on a `source` tree — a quarter of the size the reasoning
called for. The plaintext choice is defensible on its own terms, since a
stored-bytes budget makes blob size vary with how compressible a directory
happens to be, so this is a calibration to settle rather than a unit to switch.

### The original sequencing

Stages 1–3 as written before any of it was built. All three are complete;
stages 4–6 carry forward into *Sequencing*.

1. **Harness first.** Merge aging into `bench.sh`, delete `aging.sh`, capture
   and commit the v2 baselines. (Lands with this RFC.)
1. **v3 leaf encoding behind the gate.** Binary fat leaves, inline content,
   inline chunk refs + spill, byte-budget splitting — writable only when
   `init` is asked for format 3, which is not yet the default. The v3 chain
   is built without `PackStore` from the start.
1. **Engine on entries, not refs.** Change detection, `check`, `diff`, `find`
   read decoded entries; the derived traversal order (RFC 0025 §1) becomes
   the only read order.

### The leaf-budget sweep, as it was answered for fat leaves

Recorded because the reasoning is sound for the format it was about, and
because the current answer is only legible against it.

> **Leaf byte budget: 8 MB.** Swept at 2, 4, 8 and 16 MB on the `source`
> profile. Requests fall almost exactly as 1/budget — check
> 904 → 408 → 206 → 102, restore 915 → 229 → 128 → 76 — and what pays for it
> is write amplification, since a changed entry rewrites its whole leaf
> including its neighbours' untouched content: a single-file incremental
> uploads 0.4, 0.6, 1.6 and 1.6 MB across the same sweep. 8 MB is where the
> read side stops improving cheaply. The stored-size spread the sweep also
> shows (79 → 91 → 104 MB) is retention, not format: those figures hold six
> snapshots' trees, and a repository kept at one snapshot stores 54 MB at
> 2 MB and 52 MB at 8 MB, against the packfile format's 79 MB.

The load-bearing clause is *"a changed entry rewrites its whole leaf including
its neighbours' untouched content"*. That is what made the budget a real dial
and what made 8 MB the right point on it. With bodies in `blob/` objects a leaf
holds metadata, the budget stops binding at any plausible value, and the
question becomes the one answered in *Open questions*: not what the budget
should be, but that it should be left alone.

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
