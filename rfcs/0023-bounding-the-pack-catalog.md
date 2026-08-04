# RFC 0023: Bounding the Pack Catalog

- **Status:** Draft
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

### Measured access locality

The first version of this document listed the miss rate as unmeasured and the
central open question. It has since been measured, and the answer changes the
proposal.

Method: a throwaway build records the pack each catalog resolution lands in, in
order, so an LRU of any size can be simulated offline from one run. `maxPackSize`
was lowered to 512 KB so a 50,000-file tree produces 51 packs rather than 4 —
with only 4 packs every cache holds everything and the curve is invisible. Cache
sizes are therefore best read as a fraction of the repository's packs.

Miss rate, which is exactly the rate of extra footer reads:

| operation | lookups | LRU 1 | 2 | 4 | 8 | 16 | 32 |
|---|---|---|---|---|---|---|---|
| check | 127,789 | 20.41% | 13.35% | 10.12% | 5.18% | 0.40% | 0.25% |
| ls | 59,597 | 29.47% | 16.16% | 12.09% | 7.02% | 0.84% | 0.52% |
| prune | 118,693 | 21.98% | 14.37% | 10.90% | 5.57% | 0.43% | 0.26% |
| restore | 109,597 | 57.21% | 50.86% | 46.91% | 39.39% | 28.04% | 9.98% |

`check`, `ls` and `prune` behave: caching under a third of the packs costs well
under 1% extra reads. `restore` does not. It still misses 28% of the time with 16
of 51 packs cached, and 10% with 32 — nearly two thirds of the repository
resident. At 109,597 lookups that is ~30,700 extra reads, which on a remote
backend is minutes of added latency to save tens of megabytes.

Two follow-ups locate the cause exactly.

**Reordering the same lookups by pack costs 0.047%** — 51 reads, each pack
exactly once, optimal. The lookups have perfect locality available; only the
order destroys it.

**Splitting the trace in half separates the phases**, and they could hardly be
more different:

| restore phase | miss rate at LRU 16 |
|---|---|
| metadata fetch | 0.62% |
| write | 55.47% |

So the concurrent metadata fetch is fine — refs arrive off the HAMT walk in the
order backup wrote them, which is pack order. It is `topoSort`'s parent-before-
child ordering in the write phase that scatters access across every pack.

Both numbers reproduce across runs (39.39/28.04/9.98 against 39.21/28.01/9.91 at
LRU 8/16/32), so this is structure, not noise.

**The lever is restore's write order, not the cache size.** And there is room to
move it: the topological constraint is that a folder precedes its contents, but a
regular file is a leaf — nothing is ever restored *into* a file. If that holds
across every source model, folders can be ordered topologically and files then
ordered by pack, satisfying both. That should be confirmed against `pkg/source`
before being relied on; it is stated here as the direction, not as a result.

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

Replace the unbounded `catalog map[string]PackEntry` with:

- a bounded LRU keyed by **pack ref**, whose value is that pack's decoded footer
  (its `map[string]PackEntry`), and
- a small unbounded map of **locally-written entries** — what `prepareFlushLocked`
  produces this run — which are authoritative, not recoverable from a footer
  until the pack is uploaded, and bounded by the run rather than the repository.

A lookup checks the active buffer, then local entries, then the LRU, then
resolves through the shards/footers.

### 2. A key → pack index is still needed

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

### 5. Restore writes in pack order

Measured locality says the bound is not what decides restore's cost — the write
order is. `topoSort` orders parent-before-child, which bears no relation to pack
layout, and that alone takes restore from 0.62% misses in its metadata phase to
55.47% in its write phase.

The topological constraint is narrower than the current ordering assumes. A
folder must precede its contents, but a regular file is a leaf: nothing is ever
restored into a file. If that holds across every source model (open question 2),
then ordering folders topologically and files by pack satisfies both constraints,
and the same lookups cost 0.047% instead of 28%.

This is worth doing **independently of the bound**, and probably before it — see
the note below on what a body-cache miss currently costs.

### 6. A prerequisite this uncovered

`PackStore.Get` resolves an object by downloading the **whole packfile** and
caching it in a 4-entry LRU:

```go
packData, err := s.ObjectStore.Get(ctx, entry.PackRef)
```

There is no ranged read on this path, although `readPackFooter` already uses the
optional `RangeGetter` interface for exactly this reason. So an access pattern
that misses the 4-pack body cache re-transfers up to `maxPackSize` to retrieve
one small object.

At the measured repository this is invisible — 4 packs of 8 MB, so the cache
holds everything. It stops being invisible as soon as a repository has more packs
than the cache, which is where scattered access and whole-pack downloads
multiply: on a remote backend, egress proportional to lookups × pack size.

That is a throughput and cost problem in its own right, not a memory one, and it
is tracked separately. It is named here because it changes the priority: §5 helps
today, under the existing unbounded catalog, and helps more once misses are
served by ranged reads.

## Alternatives considered

**Leave it, and cap repository size in documentation.** Rejected: the failure is
silent until an operation cannot open a repository at all, and the limit would
land on exactly the users with the most data.

**Compress the resident catalog.** A more compact encoding (sorted arrays, prefix
compression on keys) is a constant-factor win and much less invasive. It does not
change the growth, so it postpones the problem rather than removing it — but it
may be the right first step if the miss cost in §2 turns out to be prohibitive.

**Memory-map the index.** Pushes residency onto the OS page cache and would work
for `local`, but the store contract is remote-first; there is no file to map on
S3 or B2.

## Open questions

1. ~~What is the miss rate in practice?~~ **Answered** — see "Measured access
   locality". `check`, `ls` and `prune` are under 1% at a third of packs cached;
   `restore` is 28% at the same size, entirely because of its write-phase
   ordering.
2. **Is a regular file ever a parent?** The reordering in §5 depends on it never
   being one, across every source model — local, gdrive, onedrive, sftp. Google
   Drive shortcuts are the case worth checking hardest.
3. **What is the bound, and is it configurable?** A fixed pack count is simplest.
   A byte budget is more honest given entries-per-pack varies with object size,
   and the worst case — a pack full of the smallest packable objects — is roughly
   twice the typical footer.
4. **Does `prune` regress?** It streams instead of reading one resident map. On a
   remote backend that could be many more requests. It must be measured, not
   assumed, before this ships.
5. **Does reordering cost restore anything else?** Grouping writes by pack means
   touching many output directories in an interleaved fashion, which trades store
   locality for filesystem locality. Probably a good trade, but it is a trade.

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

1. ~~Prototype to measure the miss rate.~~ **Done** — recorded above. The
   instrumentation was throwaway and is not proposed for merge.
2. Confirm open question 2, then reorder restore's write phase (§5). This stands
   on its own: `PackStore` keeps only 4 packfiles in its body cache and
   re-downloads a whole packfile on a miss (§6), so scattered access already
   costs transfer today, under the unbounded catalog.
3. Convert the enumeration sites to a streaming pass. This is independently
   useful and can merge before the cache changes.
4. Introduce the bound.
5. Separate RFC for a streaming `List` and the `prune` half.

Steps 3 and 4 are separable and should be separate pull requests; step 3 touches
five call sites and no representation, step 4 touches the representation and no
call sites.
