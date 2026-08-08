# RFC 0023: Bounding the Pack Catalog

- **Status:** §5–§6 implemented. §1–§2 withdrawn on measurement — see Outcome.
  #474 raised #460's cache budget (8 → 48 packs' worth) and, more durably,
  bounded the cost of outgrowing it at any size — see the 2026-08-08 addendum.
- **Date:** 2026-08-04
- **Affects:** `internal/storelayer`, `internal/engine`, `pkg/store`, docs

## Abstract

`PackStore.catalog` is an in-memory map holding one entry per packed object in
the entire repository. Every operation loads all of it before serving a single
read, so opening a repository costs memory proportional to its object count
rather than to what the operation touches.

This RFC proposes bounding that memory. It also argues that bounding the cache
alone is not sufficient, because `ObjectStore.List` returns a slice and `prune`
is built on it — so the catalog is only half of the O(repository) footprint.

## Outcome

This RFC's central proposal was **withdrawn on measurement**. What shipped from
it is the two sections that were added late, after tracing; what it was written
to argue for turned out to be the wrong trade. Recording that here rather than
leaving the document reading as a plan.

### What shipped

| | |
|---|---|
| §5 restore writes in pack order | #455 — write-phase misses 55.5% → 0.74% |
| §6 ranged reads for packed objects | #451, #452 |

Both came out of the "Measured access locality" section, which was added to this
document after the first draft. Neither was in the original proposal.

Also shipped, from the *rejected* alternatives below: a compact catalog encoding
(#457), 158 → 73 B/entry, `check` live heap 74.1 → 57.8 MB.

### What was withdrawn, and why

**§1 and §2 — footer-granular bounded residency.** The proposal was to hold a
bounded number of pack footers and resolve a miss by reading one. Prototyping the
same trade one level down, on the pack *body* cache, measured it as strictly
worse:

| body cache budget | packs held (of 4 × 9 MB) | `check` peak RSS |
|---|---|---|
| 16 MB | 1 | 249 MB |
| 32 MB | 3 | 251 MB |
| 48 MB | 4 | 163 MB |
| unchanged (4 by count) | 4 | 167 MB |

Peak RSS is driven by the transient cost of a re-transfer, not by the resident
size of what was evicted. Trading residency for re-reads is only a good trade
when the re-read is cheap, and a pack miss re-reads ~9 MB to return a few hundred
bytes. There is no reason to expect the footer variant to behave differently.

**The direction was inverted, not merely wrong.** The body cache turned out to be
*undersized*: at 7 packs against a 4-pack cache, `check` re-read whole packfiles
404 times and allocated 4.8 GB (#458). Sizing it to the working set took that to
550 MB and peak RSS from 266 MB to 204 MB (#460). This RFC argued for holding
less; the measurement said hold more.

**§3 — streaming enumeration** was not attempted. It remains the right shape for
the `prune` non-goal below.

### Where the problem actually went

Every structure this RFC and its issues tried to bound is O(objects): the
catalog, the body cache, `CheckManager.verified`, `prune`'s reachable set,
`ObjectStore.List`'s result. Bounding them individually produced diminishing and
finally negative returns.

[RFC 0024](0024-metadata-in-the-tree.md) takes the other route — reduce the
object count itself, from 109,601 to about 9,100 for a 50,000-file snapshot — on
the grounds that it makes every one of those structures small enough not to need
bounding, and cuts round trips in the same motion.

### Net effect of the work this RFC tracked

Peak RSS growth from 5,000 to 50,000 files, across seven merged changes:

| operation | before | after |
|---|---|---|
| check | +140 MB | +20 MB |
| restore | +117 MB | +72 MB |
| prune | +179 MB | +123 MB |
| diff | +91 MB | +69 MB |
| backup-incremental | +122 MB | +99 MB |
| backup-initial | +200 MB | +179 MB |
| **total** | **+849 MB** | **+562 MB** |

### Recalibrated, 2026-08-08

Issue #460 sized `packBodyCacheBudget` to 8 packs' worth against a 6-pack
repository. The benchmark grew four days later (#467 added incrementals, a 200
MB growth step and a deduplicated backup to every size), and the same
50,000-file point now builds 37 packs, not 6. A live run against MinIO showed
the cost: `check` and `restore` re-reading whole packfiles again, #458's
pathology recurring at a scale #460 never measured.

The conclusion above still holds — hold more, not less — so the budget was
raised to **48 packs' worth**, the same ~1.3x headroom over the new high point
that 8 was over the old one. But a bigger constant only moves the day a bigger
repository hits the same wall; it was the third time (four packs → #460's
eight → this), and would not have been the last.

So #474 also closes the part that kept recurring: `PackStore` now tracks
whether a promoted pack was evicted before it served enough hits to earn back
its transfer, and stops promoting a pack that loses that bet
(`onPackEvicted`, `packPenalized` in `pack.go`). A pack the cache cannot hold
falls back to ranged reads, bounded by object size, instead of repeating a
whole-pack transfer, bounded by pack size, every time it is touched again.
That bounds the cost of a repository outgrowing the budget at any size, not
just the sizes measured so far — the budget is still worth raising for
performance (residency beats ranged reads when it fits), but no longer stands
between a working repository and one that reads 280x its own size.

Verified on the same MinIO run: `check` 4.0 GB → 98 MB transferred, `restore`
28 GB → 550 MB. `TestPackStore_ThrashingPackStopsBeingRepromoted` pins the
mechanism directly: a working set the cache cannot hold at any budget still
promotes each pack at most once, not once per pass.

## Context

### The measurement

`scripts/benchmark/memory.sh` was added to answer "does this grow with the
repository". Its first run said yes, for every operation. Attributing that with
heap profiles and a control run with `CLOUDSTIC_DISABLE_PACKFILE=1` put roughly
90% of the growth in the pack catalog.

On a 50,000-file tree (109,601 objects: 50,500 filemeta, 50,000 content, 9,096
node, 1 snapshot):

| | |
|---|---|
| catalog shard on disk | 18.2 MB — a third of the 54 MB repository, 174 B/entry |
| catalog resident, decoded | ~25 MB, about 230 B/entry |
| packfiles | 4: 1.63, 10.66, 11.83, 11.84 MB |
| footers | 0.23, 2.66, 3.83, 3.84 MB — 10.6 MB total, 29.4% of pack bytes |
| entries per pack | ~27,400 |

Two things worth noting from that. The index is already stored twice — 18.2 MB of
shard and 10.6 MB of footers — so footers are not an additional cost this RFC
introduces; they are already written and already paid for.

And the footer encoding is the more compact of the two: **101 B/entry against the
shard's 174**, because a footer does not repeat the pack ref on every entry — the
pack it terminates *is* the ref. Interning recovered some of that in memory
(#436), but the on-disk difference is structural.

`internal/storelayer` issues #436, #438 and the restore work in #439 reduced the
constants around this. None of them changed the fact that the map holds
everything.

Extrapolating: a one-million-file repository is roughly 2.2M entries, ~500 MB
resident, and more than that transiently while decoding — before the operation
does any work of its own.

### Why a bounded cache is tractable here

RFC 0018 made every packfile self-describing: a footer lists the contents of the
pack it terminates. `index/packs` and the `index/packmap/` shards are therefore
already a *rebuildable cache* rather than the source of truth. A missing catalog
is healed from footers today, and `RebuildCatalog` exposes the same repair
explicitly.

That is what makes bounding possible at all. A cache may evict, because anything
evicted can be recovered.

### The scaling property that matters

The natural unit of recovery is a **footer**, not an entry: a footer read yields
every entry in its pack at once. That gives the right shape for a bound.

Entries per pack is bounded by `maxPackSize / average packed object size` and
does **not** grow with the repository. Pack count does. So caching *footers*
makes resident memory `O(cached packs × entries per pack)` — constant in
repository size — where caching entries would leave it proportional to total
entries.

Concretely, at the measured ~27,400 entries per pack and ~230 bytes per entry,
one decoded footer is ~6 MB resident. The worst case is a pack full of the
smallest packable objects (a content object for a tiny file is ~150 bytes),
giving ~55,000 entries and ~12 MB for one footer. Both are constants, not
functions of repository size.

### The part that is not the catalog

`ObjectStore.List` returns `[]string`:

```go
List(ctx context.Context, prefix string) ([]string, error)
```

`PackStore.List` must include every packed key under the prefix, because a
packed object exists nowhere else a caller can see. `prune`'s sweep calls it once
per prefix to size its progress bar and again to iterate, and holds a `reachable`
set alongside.

So even with a perfectly bounded catalog, `prune` still materializes every key in
the repository — twice. **Bounding the catalog does not bound `prune`.** Fixing
that means a streaming enumeration on the `ObjectStore` contract, which is
public API and therefore governed by RFC 0022.

This RFC treats the two as separable, and proposes doing the first without
pretending it finishes the job.

### What needs full enumeration today

| Site | Purpose |
|---|---|
| `List` | every packed key under a prefix; `prune` depends on completeness |
| `Repack` | active bytes per pack, and pack → keys |
| `packIsReferenced` | does any entry still point at this pack |
| `discardPack` | forget entries for a pack whose upload failed |
| `CompactCatalog` | rewrite the whole index as one shard |

Point lookups — `Get`, `Exists`, `Size` — are the only sites that a bounded
cache serves directly. Every other site wants a full pass.

## Goals

- Peak memory for `check`, `restore`, `diff`, `ls` and `cat` is bounded by their
  working set rather than by the repository's object count.
- A miss is correct and recoverable: resolved from a footer, not reported as a
  missing object.
- The compatibility rules in `docs/compatibility.md` continue to hold — in
  particular that an index which cannot be read fails the operation instead of
  degrading to an empty one, and that `prune` never proceeds on data it could not
  fully read.
- The bound is explicit, documented, and pinned by a test.

## Non-goals

- Bounding `prune`. It needs a streaming `List`, which is a public API change and
  belongs in its own RFC. This RFC must not make `prune` *worse*, and must say
  plainly that it does not make it better.
- Changing the on-disk format. Footers, shards and the legacy catalog stay
  exactly as they are.
- Removing `index/packmap/` shards. They remain the fast path; footers are the
  fallback.

## Proposal

### 1. Footer-granular resident state

**Withdrawn — see Outcome.**

Replace the unbounded `catalog map[string]PackEntry` with:

- a bounded LRU keyed by **pack ref**, whose value is that pack's decoded footer
  (its `map[string]PackEntry`), and
- a small unbounded map of **locally-written entries** — what `prepareFlushLocked`
  produces this run — which are authoritative, not recoverable from a footer
  until the pack is uploaded, and bounded by the run rather than the repository.

A lookup checks the active buffer, then local entries, then the LRU, then
resolves through the shards/footers.

### 2. A key → pack index is still needed

**Withdrawn with §1 — see Outcome.**

A lookup by object key cannot find a footer without knowing which pack to read.
Something must map key → pack ref, and that something is the thing this RFC is
trying not to hold.

Two candidate resolutions, and this is the central open question:

**(a) Keep a key → pack map, drop the offsets.** `PackEntry` is
`{PackRef string, Offset, Length int64}`. Interning pack refs (done in #436)
means the marginal cost of an entry is the key string plus a pointer plus 16
bytes of offsets. Dropping to key → pack-ref-only removes the offsets and lets
the footer supply them on demand. Cheaper, still `O(entries)`.

**(b) Probe footers.** Hold no key index at all; on a miss, consult footers.
Without an index this is a scan over packs, which is `O(packs)` reads per miss —
acceptable only if misses are rare, which is exactly what is not known yet.

(a) is a constant-factor improvement on the status quo; (b) is a genuine bound
with an unquantified latency cost. **Neither should be chosen without measuring
the miss rate for real operations**, which is the prototype work below.

### 3. Enumeration becomes a streaming pass

**Not attempted.** Still the right shape for the `prune` non-goal.

The five enumeration sites above stop ranging over a resident map and instead
iterate the index: every shard, then any footer for a pack the shards do not
cover. The iteration must not accumulate — each site reduces as it goes
(`Repack` sums per pack, `packIsReferenced` short-circuits) — except `List`,
which is obliged by its signature to accumulate and is the subject of the
non-goal above.

### 4. Failure and compatibility

- A miss that cannot be resolved from a footer is an **error**, never "absent".
  This is the rule that keeps `prune` from deleting a live repository.
- A pack predating footers cannot be resolved that way. Today a *missing catalog*
  with footerless packs fails loudly with a count of what could not be recovered.
  A *cache miss* on a footerless pack is a new situation and needs the same
  treatment: fail, naming the pack.
- Eviction must never lose a locally-written entry that has not yet been
  persisted to a shard, which is why §1 keeps those separate from the LRU.

## Alternatives considered

**Leave it, and cap repository size in documentation.** Rejected: the failure is
silent until an operation cannot open a repository at all, and the limit would
land on exactly the users with the most data.

**Compress the resident catalog.** A more compact encoding (sorted arrays, prefix
compression on keys) is a constant-factor win and much less invasive. It does not
change the growth, so it postpones the problem rather than removing it — but it
may be the right first step if the miss cost in §2 turns out to be prohibitive.

> This is what was implemented (#457), and the hedge at the end of that paragraph
> is the part that held. Measured at 158 → 73 B/entry rather than the 5x
> estimated here, because the estimate assumed a sorted array and the shipped
> change kept a map with inline keys. It postpones rather than removes the
> growth, exactly as written — which is why RFC 0024 goes after the object count
> instead.

**Memory-map the index.** Pushes residency onto the OS page cache and would work
for `local`, but the store contract is remote-first; there is no file to map on
S3 or B2.

## Open questions

1. **What is the miss rate in practice?** For `restore` of a whole snapshot the
   working set is everything, so a bounded cache is pure overhead unless access
   order has pack locality. Backup writes objects in walk order, so a
   subsequent restore in the same order may have excellent locality — this is
   measurable and nobody has measured it.
2. **§2(a) or §2(b)?** Depends entirely on 1.
3. **What is the bound, and is it configurable?** A fixed pack count is simplest.
   A byte budget is more honest given entries-per-pack varies with object size.
4. **Does `prune` regress?** It streams instead of reading one resident map. On a
   remote backend that could be many more requests. It must be measured, not
   assumed, before this ships.

## Testing strategy

- A test that pins the bound: a repository far larger than the cache is opened
  and read, and resident entry count stays under the limit.
- Miss resolution: an entry evicted from the cache is still served, from its
  footer, byte-identical.
- Failure: a footerless pack whose entry is not cached fails the operation and
  names the pack, rather than reporting the object absent.
- `prune` correctness under eviction: mark-and-sweep on a repository whose
  catalog never fits in the cache deletes exactly what it deletes today.
- The existing `TestPackStore_*` suite passes unchanged — this is an internal
  representation change, not a behavioural one.

## Rollout plan

1. Prototype §2(b) behind a build tag or an unexported option, purely to measure
   the miss rate and the resulting request count for `restore`, `check` and
   `prune` on a repository large enough to matter. No API changes.
2. Decide §2 on those numbers. Record the decision in this RFC.
3. Convert the enumeration sites to a streaming pass. This is independently
   useful and can merge before the cache changes.
4. Introduce the bound.
5. Separate RFC for a streaming `List` and the `prune` half.

Steps 3 and 4 are separable and should be separate pull requests; step 3 touches
five call sites and no representation, step 4 touches the representation and no
call sites.
