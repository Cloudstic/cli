# RFC 0025: Traversal Order and Demand-Counted Reads

- **Status:** Draft (exploratory)
- **Date:** 2026-08-08
- **Affects:** `internal/engine`, `internal/storelayer`, snapshot format (additive)
- **See also:** [RFC 0024](0024-metadata-in-the-tree.md), which reduces object
  count. This RFC was split out of it: the two are independent, and this half
  needs no change to how objects are encoded.

## Abstract

`restore`, `check` and `prune`'s mark phase all traverse a whole snapshot. All
three are slower and larger than they need to be, for two reasons that have
nothing to do with how many objects there are:

1. **Nothing reads in a useful order.** The HAMT walk is a pass over hash
   buckets — deterministic, but not topological, and contiguous on disk only by
   accident. So `restore` rebuilds the topology at runtime and holds six
   O(files) structures before writing a byte.
2. **The pack cache guesses.** For a traversal that reads each metadata object
   exactly once, recency is *anti-correlated* with future need, so LRU retains
   precisely the packs that are finished. Two separate heuristics
   (`packPromoteAfter`, `packPenalized`) exist to paper over information the
   snapshot could simply state. A pack mixes object kinds with different demand
   semantics, which is why the answer is a per-kind count rather than one number
   per pack.

This RFC proposes deriving the traversal order from the routing key that already
exists, and having a snapshot record how many of its objects of each kind live in
each pack.

The two halves address **different** costs, which is worth stating up front
because it was measured the hard way (§1). Derived order removes the O(files)
plan and keeps a pack's residency span short — it decides whether a pack is
*re*-fetched. Demand counting removes the probe sequence — it decides what a
pack's *first* contact costs. Neither substitutes for the other.

## Context

`hamt.walk` is a depth-first pass over hash buckets. It is deterministic, but it
is **not topological** — routing by `AffinityKey` says nothing about whether a
parent sorts before its child — and it is only contiguous on disk because backup
happened to write objects during that same walk.

The consequences are visible in `internal/engine/restore.go`:

- `collectMetadata` loads **every** entry in the snapshot before anything is
  written: `refs[]`, then `byID{}` and `walkOrder[]`.
- `restoreOrder` then rebuilds the topology at runtime into `interior{}`,
  `emitted{}` and `out[]` — with cycle handling and three fallback passes,
  because it is reconstructing information from data rather than reading it.
- Only then does the first file get written.

That is six O(files) structures live simultaneously before any output. `FileMeta`
is a twenty-field struct with two maps and a slice, retaining roughly 400 bytes
per entry, so a million-file snapshot needs ~400 MB of plan before it writes a
byte — and cannot start early, because the order is not known until the whole
thing is built.

Meanwhile **the routing key already encodes the order nobody reads.**
`AffinityKey` puts a directory's children under a shared prefix, so a
parent-before-child walk is recoverable from the tree rather than needing to be
recorded. #455 got at this by hand for restore's write phase, sorting leaves back
into walk order to take pack-cache misses from 55.5% to 0.74%.

### What the pack cache has cost so far

The pack body cache was tuned three times, and each attempt traded one cost for
another rather than removing it:

| attempt | result |
|---|---|
| shrink it (RFC 0023 §1–2 prototype) | worse — a miss re-reads ~9 MB to return ~200 bytes |
| size it to the working set (#460) | worked for one repository shape; broke when the benchmark grew to build 13/21/37 packs at 5k/20k/50k files |
| size it to the *new* working set (#474, reverted) | fixed transfers by holding every pack resident — +347 MB peak RSS on `check`, +580 MB on `prune`. O(repository) residency, which RFC 0023 exists to remove |
| bound the cost of overrunning it (#474, shipped) | 15x less transferred, 2.7x faster, peak RSS flat — but requests rise 1.4–2.7x, because a penalised pack is read one object at a time |
| stop overrunning it in the first place (#481, shipped) | −47%/−61% requests when the working set exceeds the budget; **exactly zero** when it fits (§1) |

What every one of those has in common is that it tuned a *policy* while leaving
the *information* alone. The cache is asked to predict which packs will be wanted
again from nothing but which ones were wanted recently — and for a traversal, that
signal is worse than useless (§2).

## Proposal

### 1. The read order is derived, not stored

A traversal needs parents before children, and wants to touch each pack once.
Both fall out of the routing key that already exists, so nothing has to be
recorded and nothing has to be rewritten per backup.

```go
func AffinityKey(parentID, fileID string) string {
    parentHash := core.ComputeHash([]byte(parentID))
    fileHash := core.ComputeHash([]byte(fileID))
    return parentHash[:4] + fileHash[4:]
}
```

The leading 4 hex characters come from the *parent*. Every child of a directory
therefore shares a 16-bit routing prefix and lands in the same HAMT subtree.
Listing a directory is a descent to that prefix and a scan, discarding entries
that do not name it as their primary parent:

```text
children(D) = scan(subtree under prefix hash(D)[:4]) where Parents[0] == D
```

A traversal is then the obvious depth-first walk: take the snapshot's root ID,
list its children, recurse into the folders. **Parent-before-child is guaranteed
by construction** — a directory has to be read before its children can be
located, since locating them requires its `FileID` — so `restoreOrder` and
everything it builds disappears without anything replacing it. Note that this
does not depend on ordering *within* a leaf: entries are sorted by `routing`, not
topologically, and it does not matter, because the descent supplies the ordering.

#### What this enumerates, and what it does not

`AffinityKey` routes by **primary** parent, so this walk visits every entry
exactly once, under `Parents[0]`. That is the right set for `restore`, `check`
and `prune`: each writes, verifies or marks an entry once.

It is **not** a complete directory listing. A file whose `Parents[1]` is `D` —
the multi-parent case the identity model exists to support — will not appear
under `D` in this walk. Anything that must show every path to an entry (`ls` of a
secondary parent, `fileMetaPaths`) needs a secondary-parent index, which this RFC
does not propose. Traversal and listing are different operations and only the
first is claimed here.

#### Locality, and what bounds it

Backup writes a directory's entries together and affinity routing groups them, so
a directory's metadata tends to be contiguous. When that directory is next
touched, its entries are rewritten together into the same new pack, so **each
backup re-establishes locality for whatever it touched.**

Two things bound how strong that is, and both should be stated rather than
assumed:

- `maxLeafSize` is 32, so a directory with more than 32 children spans several
  leaves. "A directory is one leaf" is a convenient approximation, not an
  invariant.
- The affinity prefix is 16 bits, so directories collide once their count
  approaches 65,536: a listing scans ~1.08 directories' worth of entries at 5,000
  directories, ~15 at 1,000,000. That makes the prefix width a parameter worth
  choosing rather than inheriting (open question 1).

**A stored order list was considered and rejected.** It costs ~291 KB per
50,000-file snapshot, and an incremental that changed three files still rewrites
all of it, so every backup pays in proportion to repository size rather than to
churn. Chunking and content-addressing the list recovers that, at which point it
is more machinery than derivation for a weaker locality guarantee.

#### What ordering buys, measured

An ordering-only prototype shipped ahead of this RFC to size the effect: #481
added a `store.LocalityGrouper` capability and had `restore`'s metadata fetch
issue its reads grouped by `(packRef, offset)` instead of in walk order. It
changes only the order of reads, not which reads happen.

| repository | packs | cache budget | requests before | after |
|---|---|---|---|---|
| benchmark pipeline | 13 | 8 | 10,650 | 5,666 (−47%) |
| the same, `-no-verify` | 13 | 8 | 10,593 | 4,133 (−61%) |
| incremental series (§4) | 9 | 8 | 112 | **112 (unchanged)** |

The third row is the important one, and it refuted the hypothesis the prototype
was built on. It is the very repository whose +9-requests-per-backup slope
motivates this RFC, and grouping did **nothing** to it — while instrumentation
confirmed 6,375 of 6,376 refs were genuinely reordered and the restored output
was byte-identical.

The explanation is that ordering prevents a pack from being fetched *again*, and
when the working set fits in the cache there is no second fetch to prevent. The
~9 requests are therefore not a re-contact cost that order can remove; they are
**first-contact** cost — `packPromoteAfter` probing a pack to decide whether it
is worth fetching whole. No permutation of the read sequence touches that.

Three consequences carry into the rest of this RFC:

- §1 and §2 do not overlap. §1's benefit appears only above the cache budget;
  §2's appears regardless of it.
- The heuristic cluster (`packPromoteAfter`, `packPenalized`) **cannot** be
  retired by ordering. It was hoped it could be, which would have simplified
  `PackStore` without any format change. Only §2 retires it.
- A benchmark that sizes the cache generously will show §1 doing nothing and
  conclude ordering is worthless; one that sizes it tightly will show §1 doing
  everything and conclude demand counting is unnecessary. Both conclusions are
  artefacts of the budget. Measurements here should state the pack count and the
  budget together, as the table above does.

### 2. The pack cache is demand-counted, not LRU

During a traversal **every metadata object is read exactly once** — `check` keeps
a `verified` set specifically to guarantee it. That single fact makes LRU the
wrong policy, and not merely a suboptimal one:

> The moment a pack's last needed object is consumed, that pack becomes the
> *most recently used* entry in the cache, and simultaneously the one entry
> guaranteed never to be needed again. LRU preferentially retains finished packs
> and evicts packs with outstanding demand that happen not to have been touched
> lately.

The measured cost of guessing instead of knowing is ~9 requests per pack visit
(§3), of which `packPromoteAfter - 1` are ranged reads issued purely to discover
whether the pack is worth fetching whole. `packPenalized` (#474) is the same
guess from the other side — inferring that a pack is not worth caching from the
fact that it was evicted.

That those ~9 are *irreducible by ordering* is not an assumption: #481 reordered
the entire read sequence of exactly this workload and left the count at 112
(§1). Whatever else is true of the read order, the probe sequence survives it,
and only stating the demand removes it.

None of that is necessary, because **the demand is knowable**. A snapshot carries
a manifest of `(packRef, kind) → count of this snapshot's objects of that kind in
that pack`. It is O(packs × kinds), not O(objects) — hundreds of entries, a few
KB — and it is maintained incrementally: snapshot *N*'s manifest is snapshot
*N−1*'s, less the counts for packs holding superseded entries, plus the counts for
what this backup wrote. That is O(churn), which is what keeps it from becoming the
per-backup cost that ruled out the stored order list in §1.

#### Why the count is per kind, not per pack

A pack is not a bag of one thing. `packablePrefixes` admits five namespaces —
`node/`, `filemeta/`, `snapshot/`, `content/`, `chunk/` — and `isSmallObject`
takes anything under 512 KB, measured *after* compression and encryption, since
`PackStore` sits below both layers. So a single pack routinely mixes tree nodes,
per-file metadata, chunk manifests, inlined small-file bodies, and compressed
chunks that happened to shrink below the threshold.

Those kinds do not share demand semantics, and a single count per pack would be
wrong in three separate ways:

- **They are read in different phases.** A restore reads `node/` and `filemeta/`
  while traversing, and `content/` and `chunk/` while writing file bodies. One
  combined count either pins a pack from the traversal that first touched it
  until the last body is written, or releases it at zero and re-fetches it.
- **They have different multiplicities.** A metadata object is read exactly once.
  A `content/` or `chunk/` object shared by several files through deduplication
  is read once *per referencing file*, so its demand is a reference count.
- **Different operations want different subsets.** `check` verifies everything;
  `prune`'s mark phase needs metadata but not file bodies; a `-path` restore
  needs a subtree of both. A count that cannot be sliced serves one of them.

Per-kind counts let each caller sum the kinds it will actually read. A streaming
restore sums metadata and content together — and because §1 makes it read a
directory's metadata and then immediately write that directory's files, the two
phases interleave per directory rather than running end to end, so a pack holding
both is used contiguously and released once.

This is also where the two RFCs stop being merely independent and start
composing. In the benchmark repository — 5,000 files, 3.6 KB median, below
`cdcMinSize` so nothing is chunked — roughly 6,500 packed objects are metadata
and roughly 5,000 are `content/` objects holding inlined bodies. Demand counting
over the traversal set alone would therefore cover a little over half of what a
restore reads. [RFC 0024](0024-metadata-in-the-tree.md) moves small-file content
into the leaf, which collapses those two classes into one: the traversal set
becomes the whole read set, and the manifest covers all of it. Neither RFC needs
the other, but this is the seam where each makes the other worth more.

With exact counts a reader gets three things it cannot have today:

- **Optimal admission.** Fetch a pack whole if this traversal needs enough of it,
  ranged-read it otherwise — decided once, before the first read, instead of
  after seven probes.
- **Immediate release.** Free a pack when its count reaches zero rather than
  waiting for eviction pressure to notice. The cache holds only packs with
  outstanding demand.
- **Belady-optimal eviction** when memory is short: evict by furthest next use,
  which the manifest supplies, rather than by least recent use, which is
  anti-correlated with it here.

Two limits worth stating rather than discovering later:

- **"Exactly once" is a property of metadata, not of file data.** A `content/` or
  `chunk/` object shared by several files through deduplication is fetched once
  per referencing file. The per-kind manifest expresses that naturally — it is a
  count either way — but "read once" must not be assumed uniformly, and the
  counting pass has to follow references rather than count distinct objects.
- **Demand counting alone does not bound residency.** A pack stays resident from
  the first to the last access of its objects; if traversal order were
  uncorrelated with pack layout, that span would be the whole traversal and every
  pack would be live at once. §1 is what keeps the spans short. The two compose:
  derived order makes residency brief, demand counting makes admission and
  eviction exact.

This supersedes `packPromoteAfter` and `packPenalized` **for the kinds the
manifest covers**. Both remain the right heuristics everywhere else: `cat` of one
file, a dedup probe during backup, and any read of a kind a given operation did
not count — where nothing can say what comes next.

### 3. Operations become streaming

Given §1 and §2, the O(files) plan disappears:

- **`restore`** walks directories in derived order and writes each entry as it is
  decoded. No `byID`, no `walkOrder`, no `interior`/`emitted`/`out`, no
  `restoreOrder` at all. Memory becomes O(tree depth + in-flight writes) rather
  than O(files), and the first file is written after the first directory rather
  than after the last.
- **`check`** verifies directory by directory. Its `verified` set is then needed
  only for objects reachable more than once — deduplicated `content/` and
  `chunk/` objects, and metadata reachable through a secondary parent — rather
  than one entry per object verified.
- **`prune`'s mark phase** marks reachable refs as it streams, instead of
  building a set over every key first. The *sweep* still materialises every key,
  because `ObjectStore.List` returns a slice; that is unchanged and remains a
  non-goal (RFC 0023).

**How large a cache this needs is conditional, and the condition should be
stated.** With a freshly written or compacted repository, a directory's entries
are contiguous and a very small cache suffices. After churn, a snapshot's
entries span one pack per contributing backup (§4), and the working set is the
number of packs live across the current directory — still small, but not one.
Demand counting is what makes that degrade gracefully instead of thrashing: packs
are admitted on known need and released at zero, so exceeding the budget costs
ranged reads rather than repeated whole-pack transfers.

## What it costs to restore, N backups later

Each backup flushes its own packs, so a later snapshot's tree is assembled from
all of them. Measured against MinIO — 5,000 files, then six incrementals of 200
changed files each, restoring the latest snapshot after every one:

| | packs | requests | transferred |
|---|---|---|---|
| after 1 backup | 3 | 58 | 177.8 MB |
| after 2 backups | 4 | 67 | 179.1 MB |
| after 4 backups | 6 | 85 | 181.6 MB |
| after 7 backups | 9 | 112 | 184.5 MB |

Exactly linear: **+1 pack, +9 requests and +1.1 MB per backup**, and the churn
volume barely enters into it — 200 changed files out of 5,000 costs the same as
any other small change, because what is added is a pack visit, not the data. The
+9 is `packPromoteAfter` showing through: a pack contributing a handful of
objects is read ~7 times by ranged read, then fetched whole once.

The mechanism is confirmed by restoring an *old* snapshot from the same
repository:

| | requests | transferred |
|---|---|---|
| latest (7th) snapshot | 112 | 184.5 MB |
| 1st snapshot, six backups later | 63 | 178.3 MB |

The first snapshot still restores at its original cost. Nothing moved it: its
entries were all written by one backup and still sit in that backup's three
packs. So the cost is not "the repository has many packs" — it is **how many
distinct backups contributed entries to the snapshot being read**. A stable tree
with occasional churn keeps most of its entries in the original epoch and stays
cheap; a heavily churned one accumulates.

The linear term has two factors — how many packs a snapshot spans, and what each
visit costs — and this RFC addresses only the second:

- **Ordering does not change the pack count, and here does not change the cost
  either.** §1 decides whether a pack is revisited, not how many packs a
  snapshot spans — and at 9 packs against an 8-pack budget there is barely a
  revisit to prevent, which is why #481 measured this exact series at 112
  requests before and after (§1).
- **Demand counting attacks the per-visit cost.** Of those ~9 requests, 8 are
  probing. §2 knows the answer before the first read, so a visit costs one
  request instead of nine: the slope falls roughly 9x and the term stays linear.
- **Only compaction removes the linear term**, by rewriting scattered entries
  back into contiguous packs. `prune`/`Repack` is where that belongs, and today
  it does not happen on its own: measured on a repository with every snapshot
  still reachable, `prune` left the pack count unchanged, because nothing was
  garbage. Compaction has to be driven by *layout*, not only by reachability.

Two further options were considered for removing the linear term outright and are
recorded rather than proposed, because each is a larger change than this RFC asks
for:

- **Carry the partial pack forward** instead of sealing one per backup, so pack
  count grows with data rather than with backup count. The obstacle is that a
  pack is named by its contents (`packRef = packPrefix + packHash`), so a growing
  pack changes identity; it would need sequence-named packs with integrity from
  the footer, and it reopens the concurrent-writer question that the append-only
  shard design in `packshard.go` exists to answer.
- **Serialise metadata as a content-defined-chunked stream** in traversal order,
  the way file content already is. Unchanged regions dedupe, chunks land above
  `maxObjectSize` and bypass packing entirely, and a restore reads a number of
  objects set by metadata size rather than by backup count. The cost is write
  amplification: a one-byte change rewrites a whole chunk, trading incremental
  cost against restore cost through the chunk-size parameter.

## What this does not solve

- **Restore cost still grows with backup count.** §2 takes a visit from ~9
  requests to 1 and the slope falls accordingly, but the term stays linear:
  nothing here reduces how many packs a snapshot spans. Removing it needs
  layout-driven compaction, which does not exist yet (open question 3).
- **`prune`'s sweep still materialises every key.** `ObjectStore.List` returns a
  slice; a streaming enumeration is a public-API change and its own RFC.
- **Streaming is not primarily a wall-clock win on a local store.** Measured on a
  20,000-file restore to a local backend, metadata collection is 189 ms of
  1.53 s — about 12%. The win is that memory stops scaling with file count, that
  the ordering logic disappears, and that a remote backend reads a directory at a
  time. Anyone expecting restore to get 2x faster on a local disk will be
  disappointed.
- **Demand counting covers only the kinds an operation counts.** A pack mixes
  five namespaces, and anything a caller does not count still falls back to
  `packPromoteAfter`. On today's format that leaves a real fraction uncovered for
  a restore of small files — measured on the benchmark repository, `content/`
  objects holding inlined bodies are roughly 5,000 of the ~11,500 packed objects
  a restore reads. RFC 0024 is what collapses that class into the traversal set.
- **Ordering does not retire the heuristics.** `packPromoteAfter` and
  `packPenalized` answer a question — *is this pack worth fetching whole?* —
  that no read order asks. Measured in #481 (§1). Only §2 removes them, and only
  for the kinds it counts.
- **Only `restore` reads in pack order today.** #481 wired
  `store.GroupByLocality` into `collectMetadata` and nowhere else, so `check`
  and `prune`'s mark phase still read in walk order and pay the revisit cost in
  full whenever they overrun the budget. Extending it is mechanical, but it is
  not done, and neither operation is covered by the numbers above.
- **Secondary parents are not enumerated.** See §1; `ls` of a secondary parent
  needs an index this RFC does not propose.

## Open questions

1. **How wide should the parent prefix be?** §1 derives directory listings from
   `hash(parentID)[:4]` — 16 bits, so junk grows once directory count approaches
   65,536. Widening to 6 hex characters gives 16M buckets and keeps junk
   negligible at any plausible scale; the fileID half retains ample entropy
   either way. It is currently 4 because that is what `AffinityKey` was written
   with, which is not a reason.
1. **How is the pack manifest kept correct?** §2 maintains it incrementally, so
   it is derived state that can drift from the tree it describes. A count too low
   frees a pack early and costs a re-fetch; too high wastes residency. Neither is
   a correctness failure, which is the right property, but the manifest should be
   cheaply verifiable — `check` is the natural place — and `RebuildCatalog` is
   precedent for repairing derived state from the objects themselves.
1. **What is the right granularity of "kind"?** §2 counts per `(packRef, kind)`
   because the five packable namespaces differ in read phase and multiplicity.
   Whether `node/` and `filemeta/` need separating from each other — they are read
   in the same phase, exactly once each — or whether the useful split is simply
   *metadata* versus *file data*, decides how large the manifest is and how much a
   caller has to know to slice it. Under RFC 0024 the distinction largely
   dissolves, which is an argument for not over-specifying it now.
1. **What does the reference-counting pass cost?** Metadata demand is one per
   object, but `content/` and `chunk/` demand is one per *reference*, so building
   those counts means walking references rather than distinct objects. For a
   heavily deduplicated repository that is more work than counting, and it happens
   at backup time on the incremental path. Whether it can be maintained as a delta
   like the rest of the manifest, or has to be recomputed, is unresolved.
1. **What triggers compaction?** The largest unresolved item, and the one this RFC
   flattens rather than solves. `prune`/`Repack` compacts on *reachability* and
   leaves layout alone, so a repository whose snapshots are all still live keeps
   every pack. A layout-driven trigger needs designing, and its cost measured
   against the backup it would compete with for I/O.
1. **Does derivation hold up under a partial restore?** `-path` filtering selects
   a subtree. Derivation should be *better* here than a global order — it can
   descend straight to the directories it wants — and the manifest's counts then
   become upper bounds rather than exact, since some counted objects fall outside
   the filter. Both need measuring.
1. **What does a secondary-parent index cost?** Out of scope here, but the
   multi-parent case is real and `ls` needs it. Whether it is a second routing of
   the same entry, or a side index, changes the write cost of a multi-parent file.

## Why this over the alternatives

**A better eviction policy alone** is the obvious thing to reach for, and it does
not work. The problem is not that LRU picks badly among equals; it is that for a
traversal reading each metadata object once, recency is anti-correlated with
future need — a pack is most recently used at precisely the moment it becomes
useless. No policy over that signal recovers what the snapshot could state.

**A bigger cache** was measured three times (see Context) and the last attempt
reintroduced O(repository) residency, which is what RFC 0023 exists to remove. A
cache budget is a tuning knob, not a bound: some repository always exceeds it,
and what matters is the cost when it does.

**Recording the traversal order** works but is paid for on every backup in
proportion to repository size rather than churn (§1), and derivation gets the
same property for nothing.

**Reducing object count** ([RFC 0024](0024-metadata-in-the-tree.md)) is
complementary and independent. It makes each backup's packs smaller and the
catalog cheaper; it does not change how many packs a snapshot spans or what the
cache knows about them. Either RFC is useful without the other, which is why they
are separate documents.
