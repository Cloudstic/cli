# RFC 0025: Traversal Order and Pack-Contiguous Reads

- **Status:** Draft (exploratory)
- **Date:** 2026-08-08 (revised 2026-08-09)
- **Affects:** `internal/engine`, `internal/storelayer`, `pkg/store` (interface)
- **See also:** [RFC 0024](0024-metadata-in-the-tree.md), which reduces object
  count. This RFC was split out of it: the two are independent, and this half
  needs no change to how objects are encoded.

> **Revision note (2026-08-09).** This RFC previously proposed a
> snapshot-carried per-kind demand manifest, and titled itself
> "Demand-Counted Reads". That design is **withdrawn**. Measuring compaction
> and cache budget together (§5) showed the decisive variable is neither
> per-pack demand nor per-kind granularity but *residency* — whether a pack
> body survives from the first of its objects to the last. Residency is set by
> execution order, which the store does not control, and no amount of
> information handed to the store fixes an order it cannot change. The proposal
> is now to make the reader's execution order pack-contiguous, at which point
> the manifest, the demand-declaration API, and the whole heuristic cluster
> become unnecessary rather than better-informed. The measurement record that
> produced the old design is retained in full, because the sequence of wrong
> turns is the evidence for the right one.

## Abstract

`restore`, `check` and `prune`'s mark phase all traverse a whole snapshot. All
three are slower and larger than they need to be, for two reasons that have
nothing to do with how many objects there are:

1. **Nothing reads in a useful order.** The HAMT walk is a pass over hash
   buckets — deterministic, but not topological, and contiguous on disk only by
   accident. So `restore` rebuilds the topology at runtime and holds six
   O(files) structures before writing a byte.
2. **Nothing bounds how long a pack body must stay resident.** A reader hands
   the store a large, unordered set of keys and the store is left to infer, one
   miss at a time, which packs are worth holding. Every mechanism in
   `PackStore` — `packPromoteAfter`, `packPenalized`, `packAdmission`, the LRU
   body cache and its fixed byte budget — is an attempt to guess an execution
   order the store is never told and cannot change.

This RFC proposes deriving the traversal order from the routing key that already
exists, and reading **one pack at a time** so that a pack body is live for a
contiguous span rather than an unbounded one.

The two halves are the same idea at two scales, and the second is what the
earlier drafts of this RFC missed. Derived order supplies parent-before-child
without an O(files) plan. Pack-contiguous execution makes residency O(1) in the
size of the snapshot — so the cache stops being a cache with a policy and
becomes a small fetch buffer with none.

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
| tell it the demand instead of guessing (#487, open) | −9% requests at 42 packs, −39% at 82; +15% bytes in the aged case, and release-at-zero turned out not to be reachable this way at all (§2) |
| compact the layout so the set is smaller (spike, §5) | −74% requests at 22 packs; **+603% bytes at 82 packs** — a severe regression, because compaction cannot reduce live bytes |
| raise the budget so the set fits (spike, §5) | 54x fewer bytes on a compacted repository, unchanged residency cost — an O(repository) buffer by another name |

Measured over repository age rather than repository size (§4), those attempts
share a shape: each is a policy applied to a working set the store cannot
describe, and each holds only while that set fits the budget. At 82 packs the
penalty path converts 741 reads into one request per object — the shipped
behaviour working as designed, and the constant's own comment says so: *a floor,
not a cure*.

Seven attempts is enough to stop treating this as a tuning problem. What every
one of them has in common is that it changed *what the store knows* or *how much
it can hold*, and left *when the reader asks for things* alone. The last two
rows are the clearest statement of the trap: compaction and a bigger budget pull
in opposite directions on the same repository, each is a large regression on its
own, and the pair only works in a narrow band where the compacted result happens
to fit the raised budget. That is not a design; it is two constants tuned against
each other.

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

#### Deriving an order and sorting into one are not interchangeable

The shipped prototype sorted, and sorting needs the whole set in hand before the
first read (#481). That set is available in `restore` only because
`collectMetadata` already materialises every ref — the very O(files) plan §3
exists to remove. `check` cannot be given the same treatment: it streams,
handing each ref to a callback as `hamt.Walk` reaches it, and never holds a
batch to sort.

So the shipped prototype and the streaming goal pull against each other, and
that tension resolves only one way. **A derived order is available a directory at
a time; a sorted order is not available until the walk is over.** Derivation is
therefore not merely a cheaper way to obtain the same ordering than recording one
(the argument made above against a stored order list) — it is the only way to
have locality and streaming at once. That makes §1 load-bearing for §3 rather
than an independent optimisation, which is not how either was originally framed.

A bounded-window compromise exists — buffer *N* refs, sort within the window,
fetch, repeat — trading O(window) memory for approximate locality without a
format change. It is worth measuring as a cheap probe of how much of §1's
benefit survives approximation, but it is a fallback, not the design.

### 2. Reads are pack-contiguous, so residency needs no policy

During a traversal **every metadata object is read exactly once** — `check` keeps
a `verified` set specifically to guarantee it. That single fact makes LRU the
wrong policy, and not merely a suboptimal one:

> The moment a pack's last needed object is consumed, that pack becomes the
> *most recently used* entry in the cache, and simultaneously the one entry
> guaranteed never to be needed again. LRU preferentially retains finished packs
> and evicts packs with outstanding demand that happen not to have been touched
> lately.

Earlier drafts concluded from this that the store should be *told* the demand,
and proposed a snapshot-carried manifest to tell it. That conclusion does not
follow, and §5 is the measurement that broke it. The problem is not that the
store picks badly among the packs it holds. It is that **whether a whole-pack
fetch pays for itself is not a property of the pack at all** — it is a property
of whether the body survives from the first of its objects to the last.

#### The cost model, stated once

Reading *K* objects totalling *B* bytes from a pack of size *S* has two
realisations:

| | requests | bytes | requires |
|---|---|---|---|
| ranged | *K* | *B* | nothing |
| whole | 1 | *S* | the body stays resident across all *K* reads |

The second row is the one every mechanism in `PackStore` is chasing, and the
"requires" column is the one none of them can guarantee. What actually happens
is *F* × (1 request + *S* bytes), where *F* is how many times the body was
fetched and then evicted before finishing. §5 measured *F* ≈ 59 on a repository
where every whole-fetch decision was individually correct.

**Residency span** is the distance in the read sequence between a pack's first
and last read. The working set at any instant is the number of spans overlapping
there. Nothing in the current design bounds either, because both are determined
by the order the reader issues its reads — and the reader issues them in HAMT
walk order, which is uncorrelated with pack layout.

#### The proposal

Group the reads by pack and process one group at a time. Then every span is
contiguous by construction, exactly one span is live at a time, and *F* = 1 with
a **one-pack buffer** — regardless of whether the snapshot spans 12 packs or
12,000.

This is not a better policy. It removes the question the policy existed to
answer:

- **No LRU, and no eviction policy at all.** For a pack-contiguous sequence
  every reasonable replacement policy is optimal, because a body is never needed
  after the group that owns it. FIFO over a small ring is Belady-optimal here.
- **No `packPromoteAfter`.** *K* and *B* are known exactly at group time, from
  the catalog entries the caller already resolved. Admission becomes arithmetic:
  fetch whole iff `(K−1)·requestCost > (S−B)·byteCost`.

  As implemented this holds **only for reads the caller declared**. *K* and *B*
  are exact for the groups `PlanReads` formed, and a read of a key in one of
  those packs that was not declared — or one arriving after the group's
  declared keys are spent — gets the estimate instead, which is what
  `TestPackStore_UngroupedReadStillProbes` pins. That is not a gap to be closed
  later: it is why the estimator survives, and the counts below show it carries
  the majority of misses.
- **No `packPenalized`, no `packMissWindow`, no `packAdmission`.** These exist
  to detect and bound repeated-eviction failure. Contiguity makes that failure
  unreachable.

  **Measured false** — see "The estimator is load-bearing" below. The estimator
  serves the majority of misses in every command, and this bullet is retained
  only because it is the claim that section refutes.
- **No `packBodyCacheBudget` as a tuning constant.** The buffer is
  `W × maxPackSize` where *W* is fetch concurrency — a number chosen for
  throughput, not for repository shape.
- **No manifest, and no format change.** The catalog already maps key → pack,
  which is all grouping needs. This is what withdraws stage C.
- **No demand declaration.** The group boundary *is* the release signal, and it
  is exact and complete at the moment the group is formed. This is what
  withdraws stage B, and with it `DemandDeclarer`, `DemandScope` and
  `packDemand` (#487, +863 lines).

#### The interface

`store.LocalityGrouper` shipped first (#481). It returned a flat permutation,
which hands the caller a better order but tells it nothing about where one pack
ends and the next begins — so the store still could not know when a body is
finished. Returning the grouping instead of flattening it was the entire change,
and `LocalityGrouper` was replaced by the interface below rather than joined by
it:

```go
type ReadPlanner interface {
    // PlanReads declares the keys a caller is about to read and returns what
    // the store knows about them: a partition into groups that share a bundle,
    // and how many groups may be read at once without defeating it.
    PlanReads(ctx context.Context, keys []string) ReadPlan
}

type ReadPlan struct {
    Groups      [][]string
    Concurrency int
}
```

`Concurrency` is part of the answer rather than the caller's business because a
worker reading a group holds that group's body for its duration — so
concurrency and residency are the same number, and only the store knows its own
buffer. Stating it caller-side means hand-computing a storelayer constant in a
package that cannot see it, which is what the first implementation did.

Layering is preserved: the engine never learns what a packfile is. It asks for
groups and consumes them in order, and pack membership stays below the
encryption layer where it belongs.

#### Why per-kind counting stops being a question

Earlier drafts devoted a section to why demand had to be counted per
`(packRef, kind)` — packs mix five namespaces, the kinds are read in different
phases and have different multiplicities, and a single per-pack count released
bodies the next phase immediately re-fetched. All of that was correct, and it was
correct about a design that no longer exists. Under grouping there is no count to
get wrong: a group is defined by the keys the caller actually handed over, so it
is exact, it is complete before the first read, and it needs no distinction
between partial and final. Open question 3 is answered by deletion.

The two-phase problem survives in a smaller form and is stated under "What this does not solve": a pack
holding both metadata and content is grouped once per phase and therefore
fetched twice. §1's directory-at-a-time order is the mitigation, and the residual
cost is bounded by one extra fetch per mixed pack rather than by *F*.

#### What the withdrawn manifest design was, and what it measured

Retained because the measurements are what invalidated it, and a later reader
will otherwise re-propose it.

The design was: a snapshot carries a manifest of
`(packRef, kind) → count of this snapshot's objects of that kind in that pack`,
O(packs × kinds) rather than O(objects), maintained incrementally so each backup
pays in churn rather than repository size. The per-kind split was load-bearing
because `packablePrefixes` admits five namespaces and a pack routinely mixes tree
nodes, per-file metadata, chunk manifests and inlined small-file bodies — kinds
that are read in different phases, and whose multiplicities differ (a metadata
object is read once; a deduplicated `content/` object is read once *per
referencing file*, so its demand is a reference count).

Two results came out of prototyping it against the catalog first (#487), and both
survive the withdrawal as facts:

- **Admission from catalog-derived counts works.** Restore fell from 507 requests
  to 460 at 42 packs, and from 2,118 to 1,290 at 82, with no format change.
- **Release-at-zero does not work this way.** 507 requests and 320.8 MB became
  937 and 647.0 MB — bytes almost exactly doubling — because the metadata phase
  released packs the write phase immediately re-fetched. Declaring content
  objects too narrows the gap but cannot close it: a content hash is a field of a
  `FileMeta`, so the write phase's keys are unknowable until the metadata phase
  has read, and therefore exhausted, its own.

The old §2 concluded from the second result that release requires counts
available *before the first read*, which only a carried manifest supplies. That
is a valid inference from a false premise. It assumes the reader's phase
structure is fixed and the store must be equipped to cope with it. Grouping
changes the phase structure instead, and the manifest's one remaining advantage
evaporates: a group is complete before its first read by construction, at no
format cost.

Three claims from that section do carry forward, and are absorbed into §2's
proposal above:

- **Admission should be decided on bytes, not object count.** A pack 79 KB is
  wanted from is not worth 8 MB whatever the object count. This is what #487's
  +15% bytes in the aged case was. The grouped design gets both *K* and *B*
  exactly, so the arithmetic rule replaces `packPromoteAfter` outright.
- **"Read exactly once" is a property of metadata, not of file data.** A
  deduplicated object is read once per reference. A group is a list of keys, so
  repeats are naturally expressible; this must not be de-duplicated, which is a
  mistake #487 made and measured (620 requests, 496.2 MB).
- **Bounding residency is what the whole thing turns on.** The old section said
  this explicitly — *"demand counting alone does not bound residency"* — and then
  treated §1 as the thing that would keep spans short. §5 shows spans were never
  bounded at all, which is the finding that promotes contiguity from a supporting
  optimisation to the proposal itself.

[RFC 0024](0024-metadata-in-the-tree.md) remains complementary but for a smaller
reason than the old text claimed: folding small-file content into the leaf
collapses metadata and `content/` into one class, which removes the two-phase
double-fetch described under "What this does not solve" rather than making a
manifest cover more.

### 3. Operations become streaming

Given §1 and §2, the O(files) plan disappears:

- **`restore`** walks directories in derived order and writes each entry as it is
  decoded. No `byID`, no `walkOrder`, no `interior`/`emitted`/`out`, no
  `restoreOrder` at all. Memory becomes O(tree depth + in-flight writes) rather
  than O(files), and the first file is written after the first directory rather
  than after the last.
- **`check`** already streams its traversal — `CheckManager.Run` hands each ref
  to a callback as `hamt.Walk` reaches it and never holds a batch — so §3
  changes nothing about its walk. What it gains is a smaller `verified` set,
  needed then only for objects reachable more than once: deduplicated `content/`
  and `chunk/` objects, and metadata reachable through a secondary parent, rather
  than one entry per object verified. The streaming property `check` has is also
  exactly why it cannot use #481's ordering (§1).
- **`prune`'s mark phase** marks reachable refs as it streams, instead of
  building a set over every key first. The *sweep* still materialises every key,
  because `ObjectStore.List` returns a slice; that is unchanged and remains a
  non-goal (RFC 0023).

#### Streaming and grouping want the same window, not opposite ones

An earlier draft held that streaming *replaces* ordering and demand counting,
because both work by handing the store the whole batch and streaming is the
removal of that batch. That framing put streaming last and made a carried
manifest its precondition.

Grouping dissolves the conflict, because a group is a batch of exactly one pack.
Streaming a traversal a directory at a time (§1) yields a modest set of keys per
directory; grouping that set by pack yields a handful of groups; residency is one
group. Neither needs the *whole* plan — they need overlapping windows of it, and
§1's window is already the smaller of the two.

What remains true is that the batch sizes must be compatible. A directory whose
entries scatter across many packs produces many one-key groups, and grouping buys
nothing there; that is the aged, uncompacted case, and it is where the arithmetic
admission rule correctly chooses ranged reads. Compaction (stage D) is what
restores the correlation between "one directory" and "one pack" — which is its
real justification, and a much narrower one than the old §4 claimed.

#### What the O(files) plan actually costs

The plan's memory is the argument for §3, and it had not been measured. Holding
`byID` plus one ordering, with representative names and hashes, retains **555
bytes per entry** — `core.FileMeta` is 216 bytes and the strings it points at
are the remainder. `restoreOrder` then builds `interior`, `emitted` and `out` on
top of that, so this is a floor rather than the total.

Against the fixed buffers a restore already holds — `restoreMemoryBudget` is
128 MB of in-flight chunk data and `packBodyCacheBudget` another 64 MB — that
puts the crossover a long way out:

| files | plan (floor) | share of a ~192 MB fixed baseline |
|---|---|---|
| 5,000 | 2.8 MB | 1.4% |
| 50,000 | 27.8 MB | 14% |
| 200,000 | 105.8 MB | 55% |
| 1,000,000 | ~529 MB | 275% |

**So §3's benefit is real and entirely outside the range anything here has been
measured at.** The benchmark harness tops out at 50,000 files, where the plan is
27.8 MB against a ±60 MB run-to-run spread — smaller than the noise. Every
measurement in this RFC was taken at 5,000 files, where the plan is 1.4% of peak
RSS and streaming would be invisible.

That does not make §3 wrong; it makes it unevidenced, and specifically scoped:
it is a change for repositories of hundreds of thousands of files, and it should
be justified and measured there rather than in general. The wall-clock note
below already says streaming is not primarily a speed win, and at 5,000 files it
is not a memory win either.

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

Linear over this range: **+1 pack, +9 requests and +1.1 MB per backup**, and the
churn volume barely enters into it — 200 changed files out of 5,000 costs the
same as any other small change, because what is added is a pack visit, not the
data. The +9 is `packPromoteAfter` showing through: a pack contributing a handful
of objects is read ~7 times by ranged read, then fetched whole once.

Seven backups is a week of daily use, and the linearity does **not** survive to
the interesting range. See "Where linearity stops" below, which supersedes any
reading of this table as the whole curve.

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

### Where linearity stops

Extending the same series to 80 backups (`scripts/benchmark/aging.sh`, which
sweeps backup count with the tree held fixed) shows the linear regime ending:

| backups | packs | `check` req | req/pack | `restore` req | req/pack | `check` peak |
|---|---|---|---|---|---|---|
| 10 | 12 | 114 | 9.5 | 139 | 11.6 | 157 MB |
| 40 | 42 | 385 | 9.2 | 507 | 12.1 | 179 MB |
| 50 | 52 | 947 | 18.2 | 940 | 18.1 | 185 MB |
| 60 | 62 | 1,344 | 21.7 | 1,734 | 28.0 | 189 MB |
| 80 | 82 | 1,867 | **22.8** | 2,118 | **25.8** | 199 MB |

Requests per pack hold flat out to 42 packs, then step ~2.5x and **plateau**. So
the cost is linear in backup count in both regimes, with a step between them —
not super-linear, and not the single slope the table above suggests. Wall time
follows (`check` 0.58 s → 2.87 s), and peak RSS grows modestly but really
(`check` 156 → 199 MB, `restore` 292 → 384 MB), which is the repository-size axis
RFC 0023 exists to keep clear.

**The step is `packPenalized`, not `packPromoteAfter`.** Counting reads inside
`resolveFromPack` at both ends:

| | 42 packs | 82 packs |
|---|---|---|
| whole-pack fetches | 42 (exactly 1 per pack) | 120 (re-fetches) |
| ranged reads | 294 (7 per pack) | 1,595 |
| **of those, penalized** | **0** | **741** |

At 42 packs every pack is probed 7 times, fetched once, and then serves the rest
of its objects from cache — the designed behaviour, exactly. At 82 packs
promotions are evicted before paying for themselves, `onPackEvicted` penalizes
those packs, and each subsequent object costs its own request.

Two hypotheses were tested and killed before this one: that `packMissWindow`'s
bounded structures were aging out (raising it 64 → 512 moved nothing), and that
packs were falling below `packPromoteAfter` (the miss distribution shows they
are not). Recorded because both are the natural first guesses.

The trigger is a **byte** threshold, not a pack count: `packBodyCacheBudget` is
64 MB, and incremental packs are small, so 42 packs still fit where 82 do not.
Re-running the 82-pack repository with the budget raised 8x confirms it — 741
penalized reads become 0, whole fetches fall 120 → 82, and misses per pack
collapse to exactly 8 for *every* pack, matching the healthy 42-pack profile.
That run is a diagnostic bound and not a proposal: it is the #474 experiment
already reverted for O(repository) residency, and peak RSS rose with it.

#### What this prices §2 at

The raised-budget run separates two costs that were confounded. It is what
perfect *caching* achieves with no *knowledge*; §2 adds the knowledge:

| `check`, 82 packs | requests |
|---|---|
| today | 1,867 |
| perfect caching only (budget 8x, diagnostic) | 744 |
| one transfer per pack plus overhead | ~130 |

Roughly **14x**, and the middle row is exactly the 8-misses-per-pack floor: 7
probes that exact knowledge of *K* makes unnecessary, plus the one fetch that is
real work. §5 later reached 113 requests on a restore by the middle row's method
— a budget large enough to hold everything — which confirms the floor and prices
what it costs in residency. §2 reaches the same floor with a buffer that does not
scale with the repository.

This is a stronger case than the ~9→1 argument made elsewhere in this RFC, which
prices §2 only in the regime where the cache still fits. The regime that hurts is
the one where it does not.

### The two factors

The linear term has two factors — how many packs a snapshot spans, and what each
visit costs — and this RFC addresses only the second:

- **Ordering does not change the pack count, and here does not change the cost
  either.** §1 decides whether a pack is revisited, not how many packs a
  snapshot spans — and at 9 packs against an 8-pack budget there is barely a
  revisit to prevent, which is why #481 measured this exact series at 112
  requests before and after (§1).
- **Contiguity attacks the per-visit cost, in both regimes.** Of the ~9 requests
  below the budget, 8 are probing; above it a visit costs ~23. A group states
  *K* and *B* before the first read, so a visit costs one transfer either way —
  roughly 9x in the first regime and 14x in the second.
- **Only compaction removes the linear term**, by rewriting scattered entries
  back into contiguous packs. `prune`/`Repack` is where that belongs, and today
  it does not happen on its own: measured on a repository with every snapshot
  still reachable, `prune` left the pack count unchanged, because nothing was
  garbage. Compaction has to be driven by *layout*, not only by reachability.

  This section previously added that compaction is "the only thing that keeps a
  repository *out* of the penalized regime", and promoted it to the load-bearing
  question on that basis. **That is wrong, and §5 measured why.** The budget is
  in bytes and compaction preserves live bytes, so it cannot move a working set
  under a budget the live data exceeds; applied on its own it made restore 6x
  more expensive in bytes. Compaction reduces the linear term and nothing else.

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

## 5. Compaction and budget, measured together

§4 named `packBodyCacheBudget` as the step trigger and left compaction as the
obvious remedy. Both halves were tested directly, on the aged repository from
`aging.sh` with `Repack` patched to compact regardless of waste and `prune`
calling it. This is the measurement that withdrew the manifest.

At **20 backups**, compaction looks like a clean win:

| | packs | requests | transferred |
|---|---|---|---|
| restore before | 22 | 220 | 202.6 MB |
| restore after | 5 | 57 (−74%) | 44.0 MB (−78%) |

At **80 backups** it inverts, and the same run varies the cache budget to
separate the two effects:

| | packs | requests | transferred | wall |
|---|---|---|---|---|
| restore, 64 MB budget (before) | 82 | 1,408 | 917.7 MB | 4s |
| restore, 192 MB budget (before) | 82 | 972 | 555.0 MB | 4s |
| prune + compact | → 12 | 4,593 | 302.7 MB | 9s |
| restore, 64 MB budget (after) | 12 | 948 | **5,574.1 MB** | 21s |
| restore, 192 MB budget (after) | 12 | **113** | **103.8 MB** | 2s |
| `check`, 64 MB budget (after) | 12 | 5,554 | 140.2 MB | 6s |

Restores were byte-compared against each other and are identical, so this is a
performance result throughout. The 64 MB post-compaction row replicates in an
independent run (958 requests, 5,674.5 MB).

Three findings, in increasing order of consequence.

**Compaction alone is a severe regression.** 806.8→5,574 MB and 4s→21s on
restore; `check` went from 1,867 requests (§4's table, 82 packs) to 5,554. The
20-backup result does not generalise, and a layout-driven trigger shipped on its
own would regress exactly the aged repositories it targets.

**Compaction cannot bring a repository under a byte budget.** The budget is
denominated in bytes; compaction reduces *pack count* while preserving *live
bytes*. A snapshot whose working set is 96 MB of live objects still needs 96 MB
however few packs hold it. §4's claim that compaction is "the only thing that
keeps a repository *out* of the penalized regime" is therefore structurally
wrong, and open question 5 inherited the error.

What compaction actually removed was accidental protection. Sparse packs each
contribute few objects, fall below `packPromoteAfter`, and are ranged-read
cheaply — which is why the uncompacted case transferred 917 MB rather than
5,574. Dense packs clear the threshold, get whole-fetched, and thrash. **The
penalized path is not only cost; it is also the mechanism that was keeping the
aged case survivable.**

**The whole-fetch decisions were each individually correct.** 5,574 MB ÷ 8 MB
≈ 700 whole-pack transfers for 12 distinct packs — about 59 fetches per pack.
Nothing chose the wrong pack; the same body was fetched, evicted before its
objects were consumed, and fetched again. `resolveFromPack` decides one pack at a
time and never forms the sum, so it commits to holding 96 MB in a 64 MB buffer
and is individually right about every one of the twelve. That is the observation
§2 is built on.

The 192 MB rows price the ceiling. On the compacted layout a budget the working
set fits reaches 113 requests and 103.8 MB — one transfer per pack, live bytes
once. That is the floor any design can reach, and §2's claim is that
pack-contiguous execution reaches the same floor with an 8 MB buffer instead of
192 MB, because it bounds residency by construction rather than by capacity.

**Caveat.** The compaction step ran full `prune`, so retention may have collected
old snapshots; the 82→12 pack reduction conflates garbage collection with
compaction and should not be read as a compaction ratio. It does not affect the
comparison the section turns on: the 64 MB and 192 MB rows are the same
repository in the same state with one constant changed.

## 6. Replayed against real traces

Stage 0 built first, and it is why this section exists at all. Pack policy is a
pure function of (read trace, policy) -> (requests, bytes), so a recorder in
`PackStore.Get` dumps every catalog-resolved read of a real operation, and a
replayer scores candidate policies offline. Traces were taken at 10, 40 and 80
backups; each replay is milliseconds against the ~30 minutes and ~7%
non-determinism of an end-to-end run.

**Calibration first.** At 42 packs the replayer reproduces the instrumented
breakdown in §4 exactly — 42 whole fetches, 294 ranged reads, 0 penalized — and
independently reproduces the null result recorded there for `packMissWindow`
64 -> 512. At 82 packs it gets whole fetches right (119 against 120) and
undercounts ranged reads (1,082 against 1,595), most likely because it models
sequential execution where restore is concurrent and duplicate misses are real.
That error understates the shipped policy's cost, so it is conservative with
respect to every comparison below; absolute figures in the penalized regime
should not be quoted as predictions.

### Restore

| restore, 82 packs | requests | bytes | resident bodies |
|---|---|---|---|
| shipped (64 MB LRU) | 1,207 | 126.7 MB | 64.0 MB |
| windowed, N = 4,096 | 270 | 267.7 MB | 11.6 MB |
| windowed, N = 8,192 | **176** | 189.3 MB | 11.3 MB |
| whole plan grouped (N = infinity) | 170 | 177.2 MB | 11.1 MB |

Two results, and the second was the surprise.

**Grouping is worth ~7x on requests and ~6x on residency, at 1.5x the bytes.**
There is no setting of the admission exchange rate that wins on both axes: at
64 KB per request the same trace is 497 requests and 139.4 MB. This is a real
trade, not a free win, and the rate is the knob that positions it.

**A bounded window is worth as much as the whole plan.** 8,192 refs reaches 176
requests where the entire plan reaches 170 — six requests, on a curve that is
flat from there. At roughly 170 bytes per buffered ref that is ~1.4 MB of refs
plus ~11 MB of bodies, against today's 64 MB budget *plus* an O(files) plan at
555 B per entry. Sorting the whole plan is not what makes grouping work.

### What the implementation measured, and where it stopped

Stages 1-2 are built (`feat/pack-contiguous-reads`). Measured end to end against
MinIO with `aging.sh`, at 82 packs:

| | requests | transferred | peak RSS |
|---|---|---|---|
| main | 2,118 | 917.7 MB | 384 MB |
| grouped reads | 933 | 453.5 MB | 250 MB |
| + bounded fetch concurrency | 900 | 393.8 MB | 227 MB |
| + spent plans retired | 1,178 | 245.1 MB | 249 MB |
| + write phase declared | 936 | 433.7 MB | 221 MB |

**Grouping is worth roughly 56% of requests and 55% of bytes. Everything after
it moves along a requests-versus-bytes frontier without advancing it** — the
last three rows differ by less than this benchmark's ~7% spread on requests, and
trade bytes against them in both directions. That is worth stating plainly
because each of those changes was made for a reason that sounded like a win.

Applied to the other traversals, at 82 packs, counted at the backend by key
prefix:

| command | before | after | |
|---|---|---|---|
| `restore` | 2,118 | 936 | −56% |
| `check` | 1,813 | 782 | −57% |
| `ls` | 1,381 | 619 | −55% |
| `find` | 743 | 743 | untouched, control |
| `backup` | 1,001 | 985 | untouched, control |

The two untouched commands are what makes the rest attributable: they were
measured in the same runs and did not move.

#### Backup, which was a control until it was not

`backup` stayed flat above because nothing had been applied to it, not because
it had nothing to gain. Its breakdown at 82 packs was 791 ranged reads out of
985 requests — the same probe sequence, from the same cause, arriving through a
different traversal. Change detection reads the previous filemeta of every entry
the source walks, and a source walk is in no relation to storage layout: a
file's filemeta sits in whichever pack was open the last time that file changed,
so after eighty backups an unchanged tree's metadata is spread across every pack
the repository has.

Buffering the walk and declaring each batch's refs before reading any of them:

| `backup`, 82 packs | requests | ranged | whole-pack |
|---|---|---|---|
| before | 985 | 791 | 92 |
| index consolidation | 896 | 765 | 92 |
| + declared scan reads | 575 | 446 | 90 |

**−42% of requests with whole-pack transfers unchanged**, which is the shape
worth having: the ranged reads became cache hits rather than being converted
into transfers, so the bytes did not move. `find` measured 679 in both runs,
identical to the digit, which is what makes the rest attributable.

Two things distinguish this from `check` and `find`, where declaring bought
nothing. The declaration is *exact* — every ref handed over is read moments
later, because a filemeta names its own `FileID` and no two entries can share
one, so nothing is declared that a dedupe set then skips. And the processing
order is left alone: only the reads are grouped. The order a scan queues entries
in becomes the upload order, which is what gives newly written objects their own
locality, and reordering that to match where the *previous* snapshot's metadata
happens to live would trade a one-time read win for a permanent write
regression — the same mistake `orderLeavesByContentLocality` made on restore.

#### The index cost nobody was counting

Instrumenting by key prefix surfaced a term that has nothing to do with
traversal order and was larger than several things this RFC does address. Every
command spent one request per pack-index object before doing any work at all:
81 for `check`, `ls` and `find`, 86 for `backup`, 84 for `prune`.

The pack index is append-only by design (RFC 0018) — one shard per flush, so
that concurrent writers cannot erase each other. The consequence is that opening
a repository costs a request per flush ever made, growing with the number of
backups taken and with nothing else. Only `prune` bounded it, by compacting, and
a repository that is only ever backed up never runs one.

Backup now consolidates the index once it exceeds a threshold, after releasing
its shared lock and under the exclusive lock compaction needs. Concurrent
backups fail to take it and skip, so whichever finishes alone does the work:

| command | before | after | index reads |
|---|---|---|---|
| `check` | 753 | 703 | 81 → 17 |
| `ls` | 627 | 556 | 81 → 17 |
| `find` | 743 | 679 | 81 → 17 |
| `backup` | 985 | 896 | 86 → 22 |

The percentages are small at 80 backups and that is the least interesting thing
about them: the term was linear in the repository's history and is now a
constant. It is recorded here because it was invisible to every measurement this
RFC took before requests were broken down by key prefix — aggregate request
counts had been carrying it the whole time.

**`prune` is absent, and why is a finding of its own.** Its request count came
out 2,210 / 2,148 / 1,725 / 2,842 / 1,974 across five runs. The last three are
*identical code*, spanning 1,725–2,842 — a 65% spread — so its run-to-run
variance exceeds any effect this work could have there, and the −22% an earlier
revision of this section reported was two noisy points compared against each
other.

The noise is specific to `prune`, not to the harness. In the same runs:

| command | across runs | spread |
|---|---|---|
| `find` | 743, 743, 743 | 0% |
| `ls` | 619, 627, 628 | 1.5% |
| `check` | 782, 753, 752 | 4% |
| `prune` | 1,725, 2,842, 1,974 | **65%** |

The cost is dominated by `Repack`, whose work depends on per-pack waste — and
pack composition depends on which objects happen to land together during
concurrent upload, which is not deterministic. So `prune` is structurally noisy
in a way the read-only traversals are not.

**A single `prune` measurement means nothing.** It needs repeated runs and a
stated variance, which is the protocol `bench.sh` already applies to timings and
this workstream never applied to request counts — a gap that went unnoticed only
because `restore`, `check` and `ls` happen to be stable enough to survive it.

#### That stability was a property of the old regime, and grouping ended it

The table above is **stale for `check`, and quoting it produced a false
regression report.** Reading the CI benchmark after the backup work, `check`
appeared to go 186 -> 457 requests — a 146% regression on a command the change
did not touch. The 4% figure above made that look like a real signal.

Re-running the same benchmark on one machine, against the *unchanged* baseline
commit, twice:

| `check`, `bench.sh` MinIO cell | requests |
|---|---|
| CI, baseline | 186 |
| local run 1, baseline | 254 |
| local run 2, baseline | 316 |
| local, index consolidation only | 475 |
| CI, both commits | 457 |

**The baseline alone spans 186–316, a 70% spread on identical code.** The
apparent regression was two draws from a distribution nobody had sampled.

The mechanism is the one already stated for `prune`, and it reached `check`
*because of the work in this RFC*. `PackStore.Put` uploads a filled pack outside
the lock while backup uploads concurrently, so which objects share a pack is not
deterministic. When `check` cost 10,721 requests it was in the penalty regime —
roughly one request per object, a cost indifferent to which pack an object sits
in, hence 4%. Grouping took it to a few hundred, where cost is dominated by
per-pack whole-versus-ranged admission decisions, which are highly sensitive to
exactly that layout.

So this work moved `check` from a layout-insensitive regime to a
layout-sensitive one, and its variance moved with it. **A measured improvement
can invalidate the variance estimate that justified measuring it that way** —
which is not a special fact about `check`, and is the reason to re-derive spread
after any change that alters what dominates a cost, rather than carrying an
older figure forward.

Two further notes for anyone reading the CI benchmark against this branch. It
runs six backups against a fresh repository, so `backup`'s index consolidation
(threshold 16) never fires there and is invisible by construction — that term is
linear in repository *age*, which is `aging.sh`'s axis and one `bench.sh`
structurally cannot see. And `SAMPLES` is not the way to get repeats from that
cell: the source tree is regenerated once per size and mutated in place by the
churn steps, so a second sample measures a larger, different repository. A valid
repeat is a fresh invocation of the whole script, which is how the three
baselines above were taken.

**`diff` had never been measured at all.** Four rounds of this harness invoked
it without the two snapshot IDs it requires, so it failed instantly and reported
zero. Fixed, it costs 39 requests — 16 of them the index reads above — which is
why nothing here targets it.

Three findings came out of building it, none of which the replay predicted.

**The replay is trustworthy for ranking policies and not for magnitude.** It
predicted 176 requests where the implementation measures 936. It calibrates
exactly on the shipped policy's *request* breakdown at 42 packs and is wrong by
7x on that same policy's *bytes* — an error visible in data already collected
and not checked. Rank with it; do not quote its absolute figures.

**Contiguity and declared demand are separate properties.** Instrumenting the
backend by key prefix decomposed a 1,258-request restore as 986 ranged reads,
179 whole-pack transfers, 84 index reads, 9 everything else. The dominant cost
was the probe sequence, still running on the write phase — which already had
contiguity, because `restoreOrder` emits leaves in walk order and walk order is
the order backup wrote them into packs (#455). It lacked only a statement of
demand. §2 above treats grouping as supplying both at once; where an order is
already right, only the declaration is missing, and the permutation must be
discarded rather than applied. Applying it regressed every axis: requests +25%,
bytes +87%, peak RSS +38%.

**179 whole-pack transfers for 82 packs is the two-phase double fetch, and it is
the floor.** Metadata and content share packs; the metadata phase transfers one
and releases it; the write phase asks again. No admission or ordering policy
removes that. [RFC 0024](0024-metadata-in-the-tree.md) does, by folding
small-file content into the leaf so the traversal set is the read set — which
makes it the next lever rather than a parallel idea.

### The estimator is load-bearing, and this RFC assumed it was not

§2 says the heuristic cluster becomes "unnecessary rather than better-informed"
once reads are planned, and lists `packAdmission`, `packPromoteAfter`,
`packPenalized` and `packMissWindow` as deletions the design earns. **That is
wrong**, and it was measured by counting, per command, how many pack misses
arrive with a plan against how many do not:

| command | planned | unplanned |
|---|---|---|
| `restore` | 420 | 480 |
| `check` | 134 | 596 |
| `ls` | 59 | 480 |
| `find` | 2 | 640 |
| `backup` | 0 | 848 |
| `prune` | 46 | 1,296 |

The estimator handles the majority of misses in every command, restore
included. Two things put it there, and both are consequences of decisions this
RFC records approvingly.

**Retiring a spent plan manufactures unplanned reads.** `consume` exists so a
finished pass cannot decide for a later one, which is right — but the moment a
pack's declared objects are read, every further read of that pack is undeclared
and falls to the estimator. The two mechanisms are not layered, they are
interleaved, and the estimator is the one carrying the load.

**A plan is only worth having when the caller reads what it declares.** Restore
does. `check`, `prune` and `find` all skip a large fraction of a batch through a
dedupe set — `verified`, `reachable`, a cache — so their declarations overstate
demand. Overstated demand is not harmless: it pushes admission toward whole-pack
transfers for objects nobody reads, which is why adding a second declared pass
to `check` cost 752 -> 819 requests with whole transfers going 139 -> 202, and
why planning `find` moved it 743 -> 729 on 2 planned reads out of 642.

So the simplification this RFC promised is not available on the evidence. What
grouping buys is real and measured (§6), but it buys it *alongside* the
heuristics rather than in place of them, and any future attempt to delete them
should start by re-running this count rather than by re-reading §2.

### Check, which does not work

| check, 82 packs | requests | bytes | resident bodies |
|---|---|---|---|
| shipped (64 MB LRU) | 1,659 | 110.0 MB | 64.0 MB |
| windowed, N = 16,384 | 9,230 | 228.5 MB | 11.5 MB |

A 5.6x regression. `check`'s trace is 8,881 alternating runs of length one — a
`filemeta` and then immediately its content — so any window that respects the
metadata-to-content dependency contains one or two keys and nothing groups.

An earlier reading of this measurement showed `check` at 85 requests. That
figure came from a window spanning the whole trace, which reorders content reads
ahead of the metadata that discovers their keys, and is not a permutation any
implementation can perform. It is recorded here because it is exactly the shape
of error this replay harness exists to make cheap to catch, and it was caught
only by checking the window size against the trace length.

The consequence is that `check` needs restructuring rather than a buffer, and
that its benefit is unmeasurable until that is written: a trace records the order
the current code produces.

## 7. The write phase declares nothing, and repository age was the wrong axis

`declareContentReads` (#496) handed restore's whole content read set to
`PlanReads` before the write phase started. It shipped on the reasoning in its
own doc comment: a declaration is a statement about *what* will be read, so it
stays true whatever order the reads arrive in.

The statement is true. The use admission makes of it is not, and the difference
cost 18 GB of transfer on an 11-pack repository.

### What the declaration actually tells admission

`PlanReads` records *K*, how many objects each pack owes this caller, and *B*,
what they come to in bytes. `fetchWhole` promotes a pack once
`(K-1) x packRequestBytes > S - B` — the requests saved outweighing the bytes
added. Over a 50,000-key plan, *K* per pack is in the hundreds, so the left side
is hundreds of megabytes and every pack the plan names is marked worth
transferring whole.

That arithmetic is sound only if the caller reads a pack's declared objects
while the body is resident. **The declaration says nothing about that**, and the
planned path is the one place admission has no feedback to fall back on:
`resolveFromPack` consults `groupPlan().fetchWhole` and, when the plan knows the
pack, never calls `recordMiss` — so `packAdmission`'s eviction penalty, the
mechanism that exists precisely to stop a working set larger than the cache
paying for the same transfer forever, is skipped. Declaring does not merely fail
to help; it disables the thing that was containing the damage.

The two restore phases sit on opposite sides of this, and the contrast is the
whole finding:

| | declares | consumes | bounded residency |
|---|---|---|---|
| metadata (`collectMetadata`) | whole ref set | group by group, `plan.Concurrency` workers | yes, by construction |
| write phase (removed) | whole content set | walk order, `restoreFileConcurrency` workers | no |

`plan.Concurrency` is `packBodyCacheBudget / maxPackSize`. The metadata phase's
residency is bounded by the cache's own capacity because the plan's concurrency
*is* that capacity. The write phase runs 16 workers over walk order and touches
every pack before returning to any of them.

Isolated in a unit test — same declared set, same store, only the consumption
order differing (`packdeclaredresidency_test.go`): in plan order, 12 transfers
for 12 packs. Out of plan order, **192 transfers for 192 reads**, 12.9 MB moved
to deliver 768 KB.

### Measured across repository age, which turned out not to be the variable

`aging.sh` grew a policy axis for this. Comparing builds by running it twice does
not work — two runs age into two different repositories, which is the same
non-determinism that makes `check` and `prune` unusable as cross-run controls —
so `POLICIES` now reads *one* repository several ways at the same checkpoint.
`check` is identical across policies at every size below, which is what makes the
restore column attributable.

5,000 files, `source` profile, MinIO, `SAMPLES=1`, restore requests and MB sent:

| packs | baseline | declare nothing | window 2,048 | plan respects penalty |
|---|---|---|---|---|
| 3 | 26 / 24.6 | 26 / 24.6 | 26 / 24.6 | 26 / 24.6 |
| 12 | 107 / 34.5 | 107 / 34.5 | 107 / 34.5 | 107 / 34.5 |
| 22 | 180 / 46.1 | 180 / 46.1 | 182 / 47.1 | 180 / 46.1 |
| 42 | 329 / 69.5 | 331 / 69.5 | 331 / 70.5 | 329 / 69.5 |
| 82 | 871 / 433.3 | 1,087 / 240.7 | 871 / 413.5 | 1,048 / 310.8 |

**Below 82 packs every policy is identical**, and the reason is the one thing
the aging axis was not built to vary: `packBodyCache` bounds *bytes*, not packs,
so a repository of small incremental packs keeps every body resident however
many of them there are. Forty backups of 200-file churn reach 42 packs and
69.5 MB — a working set that fits — and admission's decisions cannot matter
because nothing is ever evicted. The 82-pack row is the first that overruns the
64 MB budget, and it reproduces §6's stage table closely: declaring buys −20%
requests for +80% bytes, which is the frontier §6 already described rather than
progress along it.

**The storm lives on the other axis.** 20,000 files, where packs are full rather
than incremental:

| | requests | transferred | wall | peak RSS |
|---|---|---|---|---|
| 11 packs, baseline | 2,274 | 18,416 MB | 93.5 s | 439 MB |
| 11 packs, declare nothing | 4,726 | **160 MB** | **3.1 s** | **272 MB** |
| 11 packs, window 2,048 | 2,378 | 19,236 MB | 70.8 s | 430 MB |
| 11 packs, plan respects penalty | 4,620 | 628 MB | 4.8 s | 438 MB |
| 15 packs, baseline | 3,836 | 28,182 MB | 103.8 s | 428 MB |
| 15 packs, declare nothing | 7,524 | **771 MB** | **5.6 s** | 395 MB |
| 15 packs, window 2,048 | 3,874 | 28,429 MB | 103.7 s | 438 MB |
| 15 packs, plan respects penalty | 5,894 | 805 MB | 5.5 s | 424 MB |

**Eleven packs and one backup.** Repository age is not what produces this, and
the §5 *F* ≈ 59 framing — a cost that arrives after eighty backups — pointed the
investigation at the wrong axis. What produces it is a working set of full packs
larger than the body cache, which a *single* backup of a large tree already has
and which forty backups of small churn never reach.

### What this decides

- **The write-phase declaration is removed**, not windowed. 115x the bytes and
  30x the wall time, for 2x the requests, is not a trade that needs positioning.
- **A window is not a mitigation.** 2,048 measured *worse than baseline* — a
  window that size still spans all eleven packs, so it still marks all of them
  whole. §8's earlier suggestion that the derived walk's 30x transfer reduction
  was "a property of the window" is wrong as stated: 2,048 refs of *walk order*
  buys nothing, so whatever the derived walk gained came from its batches being
  pack-local by construction of the descent, not from their size.
- **Making admission residency-aware is the right general fix and is not this
  change.** Having the planned path respect the eviction penalty recovers most of
  it (18,416 -> 628 MB) and, in the unit test, fixes the out-of-order case
  without touching the in-order one — and by 15 packs it has converged with
  declaring nothing on bytes (805 against 771 MB) while spending 22% fewer
  requests to get there, which is the better answer if it holds up. What stops it
  shipping here is blast radius: it applies to *every* planned caller, and
  `check` — which declares and also reads in walk order — went 705 -> 1,292
  requests for −20 MB under it. That is a second frontier trade dressed as a bug
  fix, and it wants its own measurement rather than a ride on this one. Removing
  the declaration is strictly narrower: it touches the one caller whose order was
  never compatible with the arithmetic, and `check` is bit-identical across it at
  every size measured.
- **§6's −56% is untouched.** That figure is the metadata phase's grouping, the
  `grouped reads` row of §6's stage table, not this declaration — whose own row
  moved 1,178 -> 936 requests while moving 245 -> 434 MB. The two results were
  never in tension, and the premise that they were the same arithmetic in two
  regimes was a misreading of which row earned what.

`packdeclaredresidency_test.go` keeps both halves: the in-order case as a guard
on the property the metadata phase depends on, and the out-of-order case as a
tripwire that fails if admission is ever made residency-aware — at which point
this deletion is worth revisiting.

## 8. Stage 3 built: derived order, and what it turned out to measure

Stage 3 is built. `restore` no longer materialises a plan: it descends
`hash(parentID)[:4]`, lists a directory, writes what it finds and recurses, with
`hamt.ScanPrefix` supplying the descent. §1's construction holds — every entry a
backup wrote is reached — and the ordering machinery it replaces
(`collectMetadata` + `restoreOrder`, six O(files) structures) now runs only for a
snapshot the descent cannot enumerate.

**Derivation is not trusted, it is checked.** The walk counts what it claimed and
the caller compares that against the tree's own entry count, which is a node-only
traversal and costs no filemeta. Anything short falls back to the materialised
plan for the entries the descent missed, and `derivedReachable` decides which
those are by *replaying* the descent over the materialised tree rather than by an
equivalent rule. That distinction is load-bearing and was nearly got wrong: the
obvious analytic test — "the primary-parent chain reaches the root through
folders" — is true of every entry in a pre-affinity repository, whose entries the
descent cannot find at all, so believing it would have restored nothing while
reporting success. Comparing each entry's stored routing prefix against the one
its primary parent implies is what the descent actually does, so that is what the
fallback compares.

### What the traversal is worth, measured as retention

`B/op` answers this backwards, and running it first was the useful mistake: the
derived walk allocates ~11% *more* than the plan, because it reads a batch, uses
it and drops it where the plan reads once and keeps everything. What separates
them is what survives. `BenchmarkRestoreTraversal` reports heap still in use once
the traversal has finished, with what it produced still reachable:

| retained | 5,000 files | 50,000 files |
|---|---|---|
| materialised plan | 751 B/entry | 583 B/entry |
| derived walk | 287 B/entry | 128 B/entry |

The plan's figure is flat per entry and lands on the 555 B/entry floor §6
estimated. The derived walk's *falls* as the tree grows, which is what a bounded
structure looks like divided by a growing denominator: 29.4 MB against 6.5 MB at
50,000 files, and the 6.5 MB is almost entirely the HAMT node cache, which both
traversals fill and neither scales.

**So §3's own prediction was right, and it is the honest headline.** This is a
change for repositories of hundreds of thousands of files. At the sizes the
harness runs it is worth tens of megabytes against a ~192 MB fixed baseline, and
peak RSS cannot see it.

### The end-to-end numbers are much larger than that, and they are not the traversal

Against MinIO, `SAMPLES=1` (the churn steps mutate the tree in place, so later
samples measure a different repository and a median across them means nothing):

| restore | requests | transferred | wall | peak RSS |
|---|---|---|---|---|
| 5,000, main | 2,749 | 21,286 MB | 78.8 s | 500 MB |
| 5,000, derived | 1,753 (−36%) | 4,216 MB (−80%) | 17.3 s | 420 MB |
| 20,000, main | 11,552 | 95,480 MB | 348.7 s | 532 MB |
| 20,000, derived | 7,025 (−39%) | 16,609 MB (−83%) | 64.0 s | 448 MB |

Both axes move the same way, which is not the requests-versus-bytes frontier §6
found everywhere else, and that alone should have been the signal that something
other than ordering was being measured. **`main` transfers 95 GB to restore a
180 MB repository.** That is §5's *F* ≈ 59 refetch storm, reproduced on the
ordinary benchmark rather than on an 80-backup aged one.

The cause is `declareContentReads`, and the control isolates it. Restoring the
same repository three ways — main, main with its content declaration switched
off, and the derived build — on a local backend, where allocation counts every
pack body that was transferred and dropped:

| | wall | peak RSS | allocation |
|---|---|---|---|
| main | 3.04 s | 558 MB | 22,227 MB |
| main, content declaration removed | 3.00 s | 313 MB | 1,763 MB |
| derived walk (declared per batch; since removed) | 3.62 s | 418 MB | 3,255 MB |

**Declaring the write phase's reads all at once is what causes the storm, and
declaring none at all is better than either.** The mechanism is the one §5 states
and §2 was built to remove: exact *K* and *B* over a 50,000-key plan tell
`PackStore` that almost every pack is worth transferring whole, it commits to
holding far more than `packBodyCacheBudget`, and each body is fetched, evicted
before its objects are consumed, and fetched again. A declaration is a statement
about *what*, and admission needs *when*; handing over more of the what makes the
gap worse, not better.

This section first attributed the derived walk's advantage in those rows to
streaming having forced the declaration to be *windowed* — 2,048 refs rather
than the whole snapshot — and concluded that the 30x transfer reduction was a
property of the window rather than of the descent. **That attribution is wrong,
and §7 is the measurement that broke it.** A 2,048-entry window over the
materialised plan costs 19,236 MB, slightly *worse* than declaring the whole
snapshot, because a window that size still spans every pack. Size was never the
variable. What the derived walk had instead was batches that are pack-local by
construction — a descent lists one directory, and a directory's entries were
written together — so its declaration named few packs rather than merely fewer
keys.

Three consequences, the first two settled by measuring the declaration directly:

- **A window is not a mitigation, so there is nothing left to tune.** The
  derived walk declares nothing on its write phase, matching what §7 does to the
  materialised one; `derivedScanBatch` sizes a read batch and no longer sizes a
  declaration. The end-to-end rows above were taken before that removal and
  understate the walk.
- **This was a live regression on `main`, not a historical note.** It shipped
  with #496 and was worst on exactly the repositories restore matters for. §7 is
  the change that removes it. The apparent tension with §6's −56% resolved the
  other way from the guess recorded here: that figure is the *metadata* phase's
  grouping, not this declaration, so the two results were never in conflict and
  repository age was never the axis that separated them.
- **`check` and `prune` in the same runs are unusable as controls.** `check` went
  434 → 158 requests at 5,000 files and 1,294 → 2,611 at 20,000, on code this
  change does not touch, and `prune` 2,705 → 11,830. That is the layout
  non-determinism §6 records, and it is a reminder that a single MinIO cell
  prices only the effects large enough to clear it. Restore's is; theirs is not.

### What derivation does not buy, and one thing it costs

- **A path filter still reads the whole snapshot.** Pruning the descent to the
  selected subtree looks free and is not: a pre-RFC 0015 snapshot persists a path
  that need not agree with where the entry sits in the tree, so a subtree cannot
  be excluded on its directory's path alone. Selection and cost are therefore
  both unchanged, and open question 7 is answered only in the weaker sense that
  derivation does not *break* partial restore. What it does gain is that
  ancestors of a match are created by deferring rather than by a second pass.
- **Listing a directory sees its prefix neighbours.** 16 bits is 65,536 buckets,
  so directories collide, and a colliding directory's entries are read, discarded
  and read again when their own parent is listed. Open question 1 — widening the
  prefix — is the fix, and it is now a measurable cost rather than a
  hypothetical one.
- **One extra node-level traversal**, for the entry count the completeness check
  needs. It reads no filemeta and warms the node cache the descents then use, and
  it is what lets the fallback exist at all.

## What this does not solve

- **Restore cost still grows with backup count.** §2 takes a pack visit to one
  transfer, but the term stays linear: nothing here reduces how many packs a
  snapshot spans. Removing it needs layout-driven compaction — which §5 shows is
  safe only once admission is contiguity-bounded (open question 5).
- **A two-phase reader still fetches mixed packs twice.** A pack holding both
  metadata and inlined content is grouped once per phase. The cost is bounded —
  one extra transfer per mixed pack, rather than the unbounded refetching §5
  measured — and §1's directory-at-a-time interleaving is what shrinks it.
  RFC 0024 removes the class distinction entirely.
- **`prune`'s sweep still materialises every key.** `ObjectStore.List` returns a
  slice; a streaming enumeration is a public-API change and its own RFC.
- **An undeclared read depletes a plan it was never part of.** `packGroupPlan`
  holds a *count* per pack, and `consume` runs on every serve from a packed
  location — so a read nobody declared decrements a declared pack's budget. Every
  traversal mixes the two: a batch of declared filemeta reads interleaved with
  undeclared content reads, node reads and path resolution. Declared reads later
  in the batch then find the budget spent and fall back to the estimate.

  This matters for the count in "The estimator is load-bearing": an unknown share
  of the unplanned misses recorded there are declared reads whose slot was taken,
  not reads nobody planned — so that table understates how far declaration
  reaches, and it is the wrong basis for concluding the estimator cannot be
  removed. The fix is contained (hold the declared key set rather than a bare
  count; one batch of string headers, not catalog-sized), and **it has to land
  before the count is re-run**, which is itself the precondition that section
  sets for deleting anything.
- **A `PackStore` holds one plan, not one per caller.** `PlanReads` records into
  a single field, so two operations sharing a `Client` — and therefore a store —
  overwrite each other's declarations. The consequence is bounded: a read
  matched against the wrong plan gets a wrong *admission decision*, whole where
  ranged was cheaper or the reverse, and returns identical bytes either way. It
  degrades toward the estimate, which the counts below show is carrying most of
  the load regardless. Scoping a plan to the execution that declared it needs a
  plan token threaded through `ReadPlanner`, which is a public-interface change
  and is not worth making before there is a measurement showing the collision
  costs anything.
- **Streaming is not primarily a wall-clock win on a local store.** Measured on a
  20,000-file restore to a local backend, metadata collection is 189 ms of
  1.53 s — about 12%. The win is that memory stops scaling with file count, that
  the ordering logic disappears, and that a remote backend reads a directory at a
  time. Anyone expecting restore to get 2x faster on a local disk will be
  disappointed.
- **Grouping covers only the keys a caller hands over.** A read outside any
  group — `cat` of one file, a dedup probe during backup, a lookup the caller did
  not plan — still arrives one key at a time. Those should take the arithmetic
  rule with *K*=1, which is always a ranged read, and need no heuristic at all.
- **Sorted ordering alone does not retire the heuristics.** `packPromoteAfter`
  and `packPenalized` answer a question — *is this pack worth fetching whole?* —
  that a flat permutation does not ask. Measured in #481 (§1). It is the group
  *boundary*, not the order, that retires them.
- **Declaring is not free for every traversal, so not every one does it.**
  `restore`, `check`, `ls`, `diff` and `prune`'s mark phase read through
  `walkEntriesBatched` + `readGrouped`, and `backup`'s full scan declares its
  change-detection reads. `find` does not: it was wired and reverted, because 2
  of its 642 pack misses arrived planned and the request count did not move. The
  discriminator is whether the caller reads what it declares — see "The
  estimator is load-bearing" above — and it has to be measured per traversal
  rather than assumed from the shape of the walk.
- **Secondary parents are not enumerated.** See §1; `ls` of a secondary parent
  needs an index this RFC does not propose.

## Sequencing

Two stages are withdrawn and the order of the rest has changed, so the previous
table is given first as a record rather than a plan.

**Withdrawn.** Stage B (declare catalog-derived demand to the store, #487) and
stage C (snapshot-carried per-kind manifest) are both superseded by grouping,
which supplies what each was for at lower cost: B's admission from the group's
exact *K* and *B*, C's release from the group boundary. B has an open branch
carrying +863 lines in `internal/storelayer`; it should be closed rather than
merged, and `DemandDeclarer`/`DemandScope`/`packDemand` deleted with it. C never
started and needs no format change to replace.

| stage | change | format | status | evidence |
|---|---|---|---|---|
| 0 | trace-replay harness for pack policy | none | **done** (§6) | reproduces the shipped policy's instrumented breakdown exactly at 42 packs |
| 1 | grouped reads + arithmetic admission in `restore` | none | not started | **1,207 → 176 requests, 64 MB → ~13 MB** on a real 82-pack trace (§6) |
| 2 | bound the read window; delete `packAdmission`, `packPenalized`, `packMissWindow` | none | not started | an 8,192-ref window is within 3% of holding the whole plan (§6) |
| 3 | derived traversal order (§1), for **write** ordering | none | **done** (§8) | retention 583 → 128 B/entry at 50,000 files; the end-to-end numbers were the write-phase declaration (§7), not the order |
| 4 | batch entry refs in streaming traversals | none | **done** for `check`, `ls`, `prune` | `check` −57%, `ls` −55% at 82 packs; `prune` too noisy to measure (§6) |
| 5 | layout-driven compaction (#486) | none | blocked on 1–2 | −74% requests at 22 packs; **+603% bytes at 82** standalone (§5) |

**Stage 0 comes first, and it is the process change this workstream needs.**
Pack policy is a pure function of (read trace, policy) → (requests, bytes), and
it has been tested seven times with a 30-minute end-to-end MinIO run carrying
±7% non-determinism — twice with an instrument that was itself broken. Recording
one trace per repository age and replaying it in a table test makes each of the
questions above cost seconds and removes the temptation to generalise from a
single scale, which is how §5's 20-backup result was briefly mistaken for a
result. End-to-end runs then confirm decisions rather than discover them.

**Stages 1 and 2 are one change split for reviewability.** The interface returns
groups; the store stops guessing. Together they should be a net deletion in
`internal/storelayer`, and that — not a request count — is the acceptance
criterion. A version of this that adds machinery has misunderstood the finding.

  **The acceptance criterion was not met, and the finding it rests on was
  wrong.** The store did not stop guessing; see "The estimator is load-bearing"
  below. This is retained as written because it is the prediction that section
  refutes.

**Stage 3 is not a prerequisite for 1–2, which is how earlier drafts had it.**
Restore already separates the two orders: `collectMetadata` reads through
`store.PlanReads` — in pack order — while `restoreOrder` computes the
parent-before-child *write* sequence from the materialised `byID` map. Read
order is therefore free to be anything, and grouping it changes no ordering
guarantee. Derived order is what supplies the write sequence once the plan is
deleted; that is its whole job, and it is needed for streaming rather than for
locality.

**Streaming is no longer a separate stage.** It was stage 6, justified as a
memory win that trades away the whole-plan sorting stages 1–2 depend on. §6
measures that trade at 6 requests out of 176: an 8,192-ref window reaches 176
where the entire plan reaches 170. So bounding the window *is* the streaming
design for reads, and it is stage 2 rather than an eventual rewrite. What
remains of the old stage 6 is the write-ordering half, which is stage 3.

**Stage 4 turned out to be small, and the claim that it was large was wrong.**
An earlier revision of this section recorded that `check` needed restructuring
into an explicit batch rather than "a buffer bolted onto the existing walk",
because replaying a window over its trace regressed it to 9,230 requests. That
replay windowed the *whole* read sequence, including the per-entry content reads
`check` issues inside its callback, into singleton groups. Buffering only the
walk's entry refs is a different change, and it is exactly the buffer the
section said would not work: `check` went 1,813 -> 758 requests at 82 packs,
with ranged reads 1,624 -> 531.

The general lesson is the one §6 already states about magnitude, sharpened: the
replay ranks policies faithfully only when the policy it scores is the policy
that will be built. A near-miss in what gets grouped inverted the answer.

`prune` needed one thing more, and the reason generalises even though the
measurement did not survive scrutiny (see above). Marking an entry reads its
filemeta and then the content object that filemeta names, inline. That
alternates between two namespaces living in different packs, so each grouped
filemeta read is punctuated by a content read that evicts the body the grouping
just arranged. Collecting the content keys during the walk and reading them as
their own grouped batch fixes the mechanism; what it is worth on `prune`
specifically is unmeasured.

**A batch must cover one namespace at a time.** That is the same constraint the
withdrawn manifest design expressed as per-kind counts, arrived at from the
opposite direction: not because kinds have different demand semantics, but
because interleaving them destroys contiguity. `restore` gets this for free by
having two phases already; a traversal that reads both per entry has to be split
deliberately.

`find` is untouched because its callback needs the HAMT key as well as the ref
and appends to an ordered result, so reordering would change output order.

**Stage 5 is no longer independent, and no longer first.** §5 shows compaction
standalone regressing restore bytes 6x on the repositories it targets, because it
converts cheap ranged reads into whole-pack transfers the buffer cannot hold.
Under contiguous consumption that failure mode is unreachable, and compaction
becomes an ordinary optimisation: it reduces the number of packs a snapshot
spans, therefore the number of transfers, and it cannot backfire. It stays
valuable — it is still the only thing that reduces the linear term — but it must
land after 1–2, not before.

[RFC 0024](0024-metadata-in-the-tree.md) remains orthogonal, and helps stage 1
specifically: collapsing metadata and inlined content into one class removes the
two-phase double-fetch that grouping otherwise leaves behind.

**The honest summary is that the cheap half of this RFC shipped and was worth
having, the expensive half was withdrawn before it was built, and the finding
that withdrew it — that residency, not information, is what the pack cache
lacks — was available from the start and cost seven measurement cycles to
reach.** The sequencing above puts a cheap feedback loop first for that reason.

## Open questions

1. **How wide should the parent prefix be?** §1 derives directory listings from
   `hash(parentID)[:4]` — 16 bits, so junk grows once directory count approaches
   65,536. Widening to 6 hex characters gives 16M buckets and keeps junk
   negligible at any plausible scale; the fileID half retains ample entropy
   either way. It is currently 4 because that is what `AffinityKey` was written
   with, which is not a reason. §8 promotes this from hypothetical to real: a
   colliding directory's entries are now read, discarded and read again when
   their own parent is listed, so the prefix width is a read-amplification
   parameter and not only a layout one. Changing it is a format change —
   `derivedPrefixLen` and `AffinityKey` have to agree, and old entries keep the
   old width, which the completeness check turns into a fallback rather than a
   loss.
1. **What is the exchange rate between a request and a byte?** §2's admission
   rule compares `(K-1)` requests against `(S-B)` bytes, and the constant
   relating them is a property of the backend — latency, per-request price,
   bandwidth — not of the repository. #487 measured what getting it wrong costs
   in the other direction: −39% requests and +15% bytes, a threshold calibrated
   for a different regime. It is probably a per-backend value, possibly a knob,
   and the trace-replay harness (stage 0) is how it should be chosen.
1. **How wide should a fetch window be?** Contiguity needs only one group live,
   but a remote backend wants several transfers in flight. *W* groups resident
   costs `W × maxPackSize` and buys concurrency; *W* = 1 is provably minimal
   memory and probably poor throughput against S3. Where the curve flattens is a
   measurement, and it is the only tuning constant the design retains.
1. **Does grouping survive restore's write concurrency?** The write phase uses an
   errgroup for throughput. Consuming groups in order constrains that: files
   within a group can be written concurrently, but a group boundary is a partial
   barrier. Whether per-group parallelism is enough to keep a remote backend
   saturated, or whether *W* > 1 is doing that work, needs measuring rather than
   assuming — this is the most likely place the design meets trouble.
1. **What triggers compaction?** `prune`/`Repack` compacts on *reachability* and
   leaves layout alone, so a repository whose snapshots are all still live keeps
   every pack. A layout-driven trigger needs designing, and its cost measured
   against the backup it competes with for I/O. Tracked as #486. §5 demotes this
   from "the largest of the four" to an optimisation gated on stages 1–2 — and
   corrects the reasoning that promoted it: compaction preserves live bytes, so
   it can never bring a working set under a byte budget, and the previous claim
   that it is "the only thing that keeps a repository out of the penalized
   regime" was wrong.
1. **At what repository size does streaming start to pay?** §8 measures the two
   traversals' retention directly and confirms the estimate this question was
   built on: 583 B/entry for the plan against 128 for the descent at 50,000
   files, so 29.4 MB against 6.5 MB — real, and still inside the ±60 MB spread
   peak RSS carries. The crossover is therefore where the plan clears that
   spread, around 100,000 files, and it is a change for the hundreds of thousands.
   What is still unmeasured is the far end: nothing here has run at 200,000 or
   1,000,000 files, where the node cache (4,096 entries) also stops holding the
   tree and the descent starts paying for re-descents.
1. **Does derivation hold up under a partial restore?** Yes, and for a weaker
   reason than expected: §8 keeps `-path` selecting exactly what it selected
   before, at exactly the cost it cost before, because descending straight to the
   selected subtree is not sound. A snapshot predating RFC 0015 persists its own
   paths, and a persisted path need not agree with where the entry sits in the
   tree, so a subtree cannot be excluded on its directory's path alone. Pruning
   the descent needs that case decided first — either by declaring persisted
   paths non-authoritative for selection, or by pruning only when a snapshot
   carries none.
1. **What does a secondary-parent index cost?** Out of scope here, but the
   multi-parent case is real and `ls` needs it. Whether it is a second routing of
   the same entry, or a side index, changes the write cost of a multi-parent file.

## Why this over the alternatives

**A better eviction policy alone** is the obvious thing to reach for, and it does
not work. The problem is not that LRU picks badly among equals; it is that for a
traversal reading each metadata object once, recency is anti-correlated with
future need — a pack is most recently used at precisely the moment it becomes
useless. No policy over that signal recovers what the read order destroys.

**A bigger cache** was measured four times (see Context) and remains the wrong
shape of answer even though §5 shows it working spectacularly in one
configuration — 5,574 MB to 103.8 MB on a compacted repository. It works by
holding the whole working set, which is O(repository) residency, which is what
RFC 0023 exists to remove. A budget is a tuning knob, not a bound: some
repository always exceeds it, and what matters is the cost when it does.
Contiguity reaches the same floor with a buffer that does not scale.

**Telling the store the demand** (#487) was built, measured, and is withdrawn.
It improves admission — 507 requests to 460 at 42 packs — and cannot deliver
release, because a two-phase reader's later keys are fields of its earlier ones.
More fundamentally it is information handed to a component that cannot act on it:
the store learns what is wanted but not when, and residency is a function of
when. §5's 59-fetches-per-pack is that gap measured.

**A snapshot-carried manifest** was the proposed fix for the above and is
withdrawn with it. It would supply complete counts before the first read, at the
cost of a format change, incremental maintenance, a reference-counting pass at
backup time, and a drift-repair story. Grouping supplies the same completeness
from the catalog, per group, for free.

**Compaction alone** is what §5 tested directly. It reduces pack count but not
live bytes, so it cannot bring a working set under a byte budget; and by
concentrating each pack's contribution it converts cheap ranged reads into
whole-pack transfers, which is a 6x bytes regression when those transfers cannot
be held. It is a good optimisation underneath a design that bounds residency and
a hazard on top of one that does not.

**Recording the traversal order** works but is paid for on every backup in
proportion to repository size rather than churn (§1), and derivation gets the
same property for nothing.

**Reducing object count** ([RFC 0024](0024-metadata-in-the-tree.md)) is
complementary and independent. It makes each backup's packs smaller and the
catalog cheaper; it does not change how many packs a snapshot spans or what the
cache knows about them. Either RFC is useful without the other, which is why they
are separate documents.
