# RFC 0027: Decoupling Read and Write Layout

- **Status:** Proposed. No implementation, and none recommended yet — the
  evidence points at a measurement first, and at a real possibility that the
  answer is "do nothing". This document exists to force a decision, not to
  record one.
- **Date:** 2026-08-28
- **Affects:** repository format (major version), `internal/hamt`,
  `internal/storelayer`, `pkg/crypto`, `docs/compatibility.md`
- **Builds on:** [RFC 0026](0026-repository-format-v3.md), whose measurements
  are the evidence here.

## Abstract

Three months of work on format v3 produced real wins — a flat aging curve, a
fifth of the bytes on `check`, a smaller pruned repository — and one stubborn
pattern: **every remaining option is a different point on the same curve.**
Leaf budget 2/4/8 MB, routing arity 1/2/3 bits, inline threshold up or down,
chunk promotion. Each traded requests against stored bytes against write
amplification, and each time the answer was that you cannot have all three.

That is not bad luck. It is the read/update/memory trade, and re-deriving it
by sweeping a constant is the wrong instrument. This RFC names the assumption
that keeps us on the curve, records the constraints that rule out most of the
escapes — including the one an earlier framing of this work assumed was
cheapest — and asks for a decision rather than another sweep.

## Context: what the sweeps established

From RFC 0026's implementation outcome and issue #525:

- **Object count drives read cost.** Requests fall as `1/budget`; nothing else
  moves them.
- **Leaf size drives retention cost.** A backup rewrites about one leaf per
  *directory* it touched, so a retained snapshot keeps
  `directories touched x mean leaf size` — independent of repository size. At
  20,000 files that is ~23 MB per snapshot against the packfile format's
  ~1.4 MB.
- **The two are the same dial.** Halving the budget halves retention cost and
  doubles restore requests (422 → 918 at 2 MB).
- **Moving content out of leaves loses.** Chunk promotion: −2% stored bytes for
  +46% restore requests (#514). Lowering the inline threshold to 16 KB: restore
  3,710 requests against 213.

So the dial is spent. What is left is the assumption underneath it.

## The assumption

**An object is the unit of both reading and writing.**

Format v2 inherits it from git's packfile model, as do restic, borg and kopia.
Format v3 inherits it too — it only changed what goes in an object. Because the
unit is shared, one size has to serve two opposed purposes: reads want it large
(few requests, good locality), incremental writes want it small (little
rewritten per change). There is no size that is both, which is exactly the
shape of every measurement above.

Object storage does not require the assumption. Two asymmetries are unexploited:

1. **A ranged GET costs the same as a GET.** Cost is dominated by request
   count, not bytes. v3 fetches a whole 4 MB leaf to read one file's metadata.
2. **Nothing forces the write layout to be the read layout.** v3 compacts
   eagerly: every backup rewrites whole leaves so that reads stay fast. That
   eagerness *is* the retention cost.

## The constraint that decides the order

**Asymmetry 1 is not available above the crypto layer, and that is a format
question rather than an optimisation.**

The store chain is
`CompressedStore → EncryptedStore → MeteredStore → <backend>`. A node is
compressed as one zstd stream and sealed as one AES-256-GCM box
(`crypto.Encrypt(data, key)`), so byte *k* of the stored object has no
relationship to byte *k* of the node, and the tag authenticates the whole
object or nothing. Neither layer implements `store.RangeGetter`, and neither
structurally can.

`PackStore` reads ranges only because it sits *below* `EncryptedStore` and
seals its own footers with a separately derived key
(`crypto.HKDFInfoPackIndexV1`). That is the shape any ranged read needs: its
own framing and its own key, beneath the layer that would otherwise make the
object atomic.

So "add a leaf header and range-read it" is not a small change. It requires the
leaf to be internally segmented — each segment independently compressed and
independently sealed with its own nonce and tag, plus an offset table — which
is a new node encoding and a new key derivation. It is a v4, not a patch.

*(An earlier framing of this work called ranged leaf reads the cheap first
step. That was wrong, and this section is why.)*

## The candidates

Three families, not three points. The first two are the two asymmetries; the
third is both.

### Family 1 — segmentation: spend the format on ranged reads

A leaf gains an internal directory: entries grouped into independently
compressed and sealed segments, with an offset table at a known end. A targeted
read fetches the table and one segment — kilobytes rather than megabytes.

**It cannot help the write side, and that is structural.** Two arrangements are
possible and neither does:

- *Segments as separate objects* — reading a leaf costs one request per
  segment, which is exactly "smaller leaves", the dial we already have and have
  already swept.
- *Segments inside one object* — the node is content-addressed as a whole, so
  changing one segment changes the node's hash and rewrites the object. Reads
  improve; writes do not move at all.

**Its beneficiaries are narrower than they look.** A full backup's change
detection does a targeted `LookupEntry` per scanned entry
(`internal/engine/backup_scan.go:319`) — but it looks up *every* entry, so it
needs every segment anyway; the node cache is sized to hold the whole tree for
exactly this reason. Full restore, `check`, `prune` and `find` all walk
everything. What is genuinely targeted is `cat`, `ls <path>`, path-scoped
restore, `diff` (which skips identical subtrees by ref) and the incremental
sources' `WalkChanges`, where only changed entries are looked up. Those are
real, and none of them is what the benchmark measures.

### Family 2 — deferred compaction: spend the format on write amplification

Write only what changed; merge periodically. The choice is *granularity*, and
it decides the blast radius:

**2a. Tree-level deltas.** A snapshot names a base root plus an ordered list of
delta objects; reads merge. This is the textbook LSM answer and the most
invasive: every read path learns to merge, and a snapshot root stops being a
content address of its contents, which costs the structural short-circuit
`diff` depends on (`internal/hamt/hamt.go:1060` — "Identical clean subtrees have
identical refs, which is what makes an unchanged subtree free to skip").

**2b. Per-leaf content bundles.** The leaf keeps its structure and its content
address; only the payload moves behind a reference. A leaf holds metadata plus
references to *content bundles*, and a backup that changes 5 of a leaf's 200
entries writes a small metadata leaf and one small bundle, keeping the
reference to the old bundle for the other 195.

The measurement that makes 2b interesting: **metadata is 1–3% of a leaf's
encoded bytes at every profile** (see the table below). If a rewrite carried
metadata only, the retention cost would fall by roughly that factor.

2b is much less invasive than 2a — routing is unchanged, leaves stay
content-addressed, `diff`'s short-circuit survives, and prune stays a
set-membership problem. Its cost is that reading a leaf's content becomes
`1 + bundles-per-leaf` requests, so the bundle count is the new dial and needs
the same bounding as 2a.

Note what 2b is *not*: issue #514 rejected promoting content to `chunk/`
objects because a chunk shared by nine files is fetched nine times. A bundle is
per *(leaf, generation)*, not per file content, so it is fetched once per leaf
regardless of how many entries it serves. The rejected design's failure mode
does not apply — but the request count still rises, which is the trade to
measure.

### Family 3 — extents: both, and the honest version

A snapshot is a list of extents over immutable blobs. Unchanged bytes are never
rewritten *and* reads are large sequential ranges. This is how copy-on-write
filesystems solved the same problem.

It inherits every cost above: it needs Family 1's crypto framing to address
into a blob, it needs Family 2's compaction under the name defragmentation, and
it turns prune from set membership into interval overlap — `internal/objkey`'s
`Set`, which exists to make the reachable set fit in memory, does not express
ranges. It is a better operating point, not an escape from the trade.

## Edge cases

### Constraints already verified in the code

1. **Ranged reads are unavailable above the crypto layer.** Whole-object zstd
   inside whole-object AES-GCM; neither `CompressedStore` nor `EncryptedStore`
   implements `store.RangeGetter`, and neither structurally can.
2. **A partial read cannot be verified.** A node's ref is the SHA-256 of its
   plaintext bytes, checked in `NodeStore.load` for every consumer. Segmentation
   must put per-segment hashes in the offset table and cover the table with the
   node hash, or the Merkle chain breaks on exactly the reads it was added for.
3. **Nonce budget.** `crypto.Encrypt` uses a random 96-bit nonce per object.
   Segmenting multiplies nonce count by segments-per-object, moving the
   birthday bound closer. Not fatal at any plausible scale, but it is a
   cryptographic parameter that changes.
4. **Whole-leaf compression is a measured win.** zstd over 4 MB beats zstd over
   sixteen independent 256 KB segments. Family 1 spends stored size — the axis
   issue #525 was about — to buy read locality.
5. **`diff`'s short-circuit depends on content-addressed subtrees**
   (`hamt.go:1060`). 2a loses it; 2b keeps it.
6. **Reads must never require a write.** `LoadRepoConfig` deliberately does not
   stamp a version on read paths, because restore runs under read-only
   credentials. Compaction can therefore never be a precondition for reading,
   so an arbitrarily long chain has to stay readable.
7. **"Cannot decode" is never "empty."** `docs/compatibility.md` is normative.
   An unreadable delta or bundle must fail its operation, and `prune` must
   abort rather than treat shadowed entries as unreachable.
8. **Prune reachability spans the whole chain.** Every retained snapshot's
   deltas or bundles must be read before anything is deleted.
9. **The delta count must be bounded on the write path, not only at prune.**
   This repo has already made the opposite mistake once: `packIndexCompactThreshold`
   (`internal/engine/packindex.go`) exists because shard count "grows with the
   number of backups a repository has ever taken, and only `prune` ever bounded
   it." Repeating it is the single most likely way Family 2 fails.
10. **Compaction must not delete what a concurrent reader listed.** The same
    rule the pack-catalog compaction already follows: remove only what the
    store has itself absorbed.
11. **WORM mode** (RFC 0020, draft) cannot delete a superseded base, so
    compaction there is pure growth. It needs an explicit answer, most likely
    "do not compact; accept the read cost."
12. **`copy` between repositories** (RFC 0017) must transfer a whole chain or
    compact in flight.
13. **A no-op backup must not extend the chain.** `backup-incremental-noop`
    costs 4 KB today; a thousand no-op backups must not become a thousand
    deltas.
14. **Check's blast radius grows.** A corrupt delta invalidates every snapshot
    after the base it sits on, where today a corrupt leaf invalidates the
    entries in that leaf.

### Edge cases in the measurements themselves

Leaf composition, measured with `internal/cmd/leafstat` after six backups:

| profile | tree | leaves | inline share | metadata-only leaves |
|---|---|---:|---:|---|
| `source` | 2,000 files, 23 MB | 19 | 97% | 0 of 19 |
| `source` | 20,000 files, 357 MB | 219 | 97% | 13 of 219 (7.5 KB) |
| `media` | 2,000 files, 1.9 GB | 137 | 100% | 4 of 137 (7.0 KB) |
| `mixed` | 2,000 files, 313 MB | 55 | 99% | 1 of 55 (442 B) |

1. **That composition holds across every profile, which is stronger than what
   issue #525 claimed.** Metadata-only leaves do not exist in useful numbers
   anywhere. Even `media`, whose bulk is chunked, fills its leaves with the
   sub-threshold tail: median file size 444.7 KB against a 512 KiB inline
   threshold. A byte budget can only ever be filled by inline content, so
   metadata is structurally a rounding error — which is what makes 2b's premise
   sound and a metadata-only budget pointless.
1. **`media` is a different regime entirely.** 2.8 GB stored for six snapshots
   of a 1.9 GB tree, because most bytes live in `chunk/` objects that dedup
   across snapshots and never enter a leaf. The retention problem is a
   small-file problem; a media repository barely has it.
1. **Directory renames re-key an entire subtree, for path-identity sources.**
   `AffinityKey(parentID, fileID)`, and a local source's `FileID` is the
   normalized path (`pkg/source/local/source.go:221`), so renaming a directory
   changes every descendant's routing key: a delete plus an insert for each, at
   a completely different part of the tree. Content still dedups; leaves are
   rewritten twice over. `gentree`'s churn performs one directory rename per
   round, so this is *inside* the numbers issue #525 reported. Sources with
   stable IDs (Drive, OneDrive) are free on rename but still re-key on *move*.
1. **Directory locality is 16 bits wide.** `parentHash[:4]` is four hex
   characters, so at v3's 2 bits per level the parent determines the first
   **eight** levels and there are 65,536 parent buckets. Two consequences: a
   large directory cannot split until level 8, giving it a deep narrow spine;
   and above ~65k directories, distinct directories necessarily share leaves.
   The retention law therefore has a ceiling —
   `min(directories touched, 65536, leaves) x mean leaf size` — and its
   locality degrades on trees far larger than any measured here.
1. **The churn model is one model.** 200 files Zipf-clustered into ~47
   directories. A user backing up a single working directory pays far less; a
   user whose tooling bumps mtimes tree-wide pays a full copy per snapshot. The
   law is a property of the format; its inputs are a property of the user, and
   only one set of inputs has been measured.
1. **Saturation is the small-repository regime.** Below
   `leaves > directories touched`, every leaf holds a change and each snapshot
   costs a full copy whatever the design. No candidate here changes that,
   because it is arithmetic.

## Prior art: what has been built, and what it broke

### Kopia's epoch manager is Family 2a, in production

Kopia already ships tree-level deferred compaction, for its content index.
Index blobs are append-only and grouped into epochs; a reader starts from the
most recent "state of the world" checkpoint and merges only what follows.
A new epoch is started when **more than 20 index blobs exist and more than 24
hours have passed**, and past epochs compact automatically. The stated
motivation was ransomware resistance — never deleting a blob — but the
structure is the one Family 2a would need.

Its issue tracker is, usefully, a list of the edge cases this RFC predicted
independently:

- **The bound can fail, and then cannot recover.** Kopia #5057: a repository
  reached **28,467 index blobs** against a healthy baseline under 1,000, and
  ten consecutive full maintenance runs *increased* the count by 123. Two
  corrupted session blobs had blocked deletion for a week; afterwards the
  epoch manager's own safety window — 24 hours to advance plus a 4 hour margin
  — classified the backlog as "too recent to delete" indefinitely. Kopia warns
  "Found too many index blobs (28468), this may result in degraded
  performance." This is edge case 9, realised: a chain bounded only by
  maintenance, with maintenance itself able to fail silently.
- **Compaction on a read path breaks read-only credentials.** Kopia #3224: the
  epoch manager attempts to advance the epoch and compact opportunistically
  *while loading an index*, and hangs against read-only storage. The
  maintainers' own conclusion is that this "conflates read and write
  semantics." This is edge case 6, realised.
- **Compaction racing readers needs a safety window,** which is what created
  #5057. The two constraints are in direct tension, and Kopia resolves it with
  time; this repository resolves the equivalent with absorption ("remove only
  index objects the store has itself absorbed"), which does not have a clock in
  it.

**We have also already built a small version of this ourselves.** The v2 pack
catalog is append-only shards under `index/packmap/` compacted past
`packIndexCompactThreshold`, and AGENTS.md records why the threshold exists:
shard count "grows with the number of backups a repository has ever taken, and
only `prune` ever bounded it." Kopia #5057 is what that failure looks like when
it is allowed to run for a week.

One difference matters and cuts in 2b's favour: **Kopia's LSM is over a flat
index**, a `hash → location` map that merges trivially. Family 2a would need to
merge a *tree*. Nobody has the easy version of that problem.

### Prolly trees do not offer anything v3 lacks

The standard answer to "content-addressed tree with cheap diffs" is a prolly
tree (Noms, Dolt): content-defined chunk boundaries, history independence,
structural sharing, and diff by comparing content addresses. v3's
hash-routed HAMT already has all four — an entry's position is a pure function
of its key, and `diff` skips identical subtrees by ref (`hamt.go:1060`).

More to the point, prolly trees do not escape the trade either: modifying a
value rewrites its containing chunk, and Dolt's own documentation puts the
cost at "4 kilobytes times the size of the tree" per edit. **Their chunks are
4 KB where ours are 4 MB — a thousandfold difference that is entirely a
cost-model difference**, local disk against per-request billing. That is the
clearest external confirmation that our dial is the right dial and that it is
simply set for a different machine.

### SlateDB inverts one of our assumptions

SlateDB is an LSM built directly on object storage, and its design notes say
write amplification "is not much of a concern from a cost perspective, as major
cloud providers don't charge for data transfers to/from the standard tier."
That is worth internalising: **our write amplification is not paid in transfer,
it is paid in storage rent** — which is a very different, and much cheaper,
unit than the one the request counts are in. See the next section.

### Two mechanisms worth knowing about

- **S3 conditional writes** (`If-Match` / `If-None-Match`, generally available
  since November 2024) give compare-and-swap on a key. That is a lock-free
  answer to compaction racing a concurrent writer, and a better one than a time
  window. It is S3-only, so it would be an optimisation over the existing lock
  rather than a replacement.
- **`UploadPartCopy`** composes a new object server-side from byte ranges of
  existing objects, which is exactly what compaction wants and would move zero
  bytes through the client. **It does not apply at our sizes**: the minimum
  part is 5 MB and a range source must exceed 5 MB, while a v3 leaf is ~800 KB
  stored. It would only become available if compaction operated on aggregates
  much larger than a leaf — which is Family 3, and is worth remembering as an
  argument for it.

### A note on patents

Several of the obvious designs here appear in granted patents — sparse metadata
segment trees for reducing read-modify-write amplification in Merkle-represented
deduplication systems, extent-based global deduplication, and metadata/data
write coalescing among them. This is a flag for counsel before implementing,
not a legal opinion, and not a reason to stop thinking.

## What the cost actually is, in money

The retention law says a snapshot costs `directories touched x mean leaf size`,
**independent of repository size**. That has a consequence worth stating plainly
before anyone spends a format on it: the overhead is an *absolute* quantity, so
its share of a repository shrinks as the repository grows.

Daily backups kept for a year, churn in ~47 directories as measured, at S3
Standard's $0.023/GB-month:

| source | v3 base | v3 history / yr | packfile history / yr | v3 overhead | at $0.023/GB-mo |
|---|---:|---:|---:|---:|---:|
| 357 MB | 83 MB | ~8.4 GB | ~0.5 GB | ~7.9 GB | **~$0.18/mo** |
| 35 GB | ~8 GB | ~8.4 GB | ~0.5 GB | ~7.9 GB | **~$0.18/mo** |
| 1 TB | ~230 GB | ~8.4 GB | ~0.5 GB | ~7.9 GB | **~$0.18/mo** |

The ratio is alarming and the absolute number is not: 14x more history on the
small repository, 3% more repository on the large one, and the same fixed
~$2/year either way. The 2x figure in issue #525 is what this looks like on a
357 MB tree at six snapshots — the least favourable point on the curve.

Two things would change that conclusion, and both are about the *input* rather
than the format:

- **A user who churns far more directories.** At 1,000 touched directories per
  backup the same law gives ~1.4 GB per snapshot and ~500 GB/year — around
  $11/month, and no longer noise. Directory *breadth* of churn, not volume, is
  the variable that matters, and it is not something the current benchmark
  varies.
- **Anything that makes every directory look touched.** Tooling that rewrites
  mtimes tree-wide, a `chmod -R`, or a directory rename under a path-identity
  source (which re-keys the whole subtree) all push towards the saturated
  regime, where every snapshot costs a full copy.

So the case for spending a format break is not "v3 stores 2x"; it is "v3 stores
`dirs x leaf` per snapshot, and some real workloads make `dirs` large." That is
a much narrower claim, and it is testable before any format work — by varying
churn breadth in the harness, which is a `gentree` change, not a format change.

## Open questions

1. **Is `directories touched` large for any workload we care about?** This now
   gates everything else. The cost is `dirs x leaf size` per snapshot, fixed in
   absolute terms at roughly $2/year, so a format break is justified only if
   real churn is far broader than the ~47 directories measured. Answering it
   needs a `gentree` knob for churn *breadth* — independent of volume — and one
   sweep. It is days of work, not a format, and it can say "no".
1. **What is the target?** RFC 0026 set ≤1.2x requests at 80 backups and
   reached 1.8x. Nothing here should start without naming the number it moves.
   Note that the request axis and the retention axis have very different price
   tags, and only the first has been treated as a headline.
1. **How many bundles does a leaf accumulate under real churn (2b)?** The
   premise is that a rewrite carrying 1–3% metadata instead of 100% of a leaf
   cuts retention cost by that factor; the cost is `1 + bundles-per-leaf`
   requests to read a leaf's content. Both are measurable by instrumenting what
   a backup *would* have written, before any format exists.
1. **Can the chain be bounded without a clock?** Kopia bounds epochs on count
   *and* elapsed time, and #5057 is what happens when the time half turns a
   backlog into a permanent one. This repository's pack-catalog compaction
   bounds on absorption instead, which has no clock in it. Any Family 2 design
   should inherit the second, not the first.
1. **Does Family 1 pay for itself when nothing ranges?** Per-segment framing,
   tags and lost cross-segment compression cost bytes on every leaf, while the
   beneficiaries — `cat`, `ls <path>`, path-scoped restore, `diff`, incremental
   sources — are none of the ones the benchmark measures. It may be worth doing
   for interactive latency rather than for a benchmark number. That is a
   legitimate answer; it should be said out loud rather than smuggled in.
1. **Can anything ship without a v4?** Every candidate changes the node
   encoding. v3 is opt-in and not yet the default (#517), so a second format
   break is cheaper now than it will ever be again — an argument for deciding
   before flipping the default, not after.

## Sequencing

**No format work is proposed.** The evidence assembled here points at a cheaper
first step and at a real possibility that the answer is "do nothing".

1. **Vary churn breadth in the harness** (question 1). A `gentree` knob and one
   sweep. If broad churn is rare, the retention cost is a fixed ~$2/year and
   this RFC closes with a note in RFC 0026 rather than an implementation.
1. **If it is not rare, instrument 2b's two quantities** (question 3) against
   the existing harness, still without writing a format.
1. **Only then** decide 2b against 2a, on measured numbers, inheriting the
   absorption-based bound rather than a time-based one.
1. **Treat Family 1 separately**, justified — or not — by interactive latency
   on its own terms.

What should not happen is another constant sweep. The dial is spent, issue #525
is the record of the last turn of it, and Dolt setting the same dial a
thousandfold lower for a different cost model is the clearest sign that it is
the right dial rather than a missing idea.

## References

- [RFC 0026](0026-repository-format-v3.md) — format v3, its measurements, and
  the "What v3 stores, and when" section this builds on.
- [RFC 0025](0025-traversal-order-and-pack-contiguous-reads.md) §7 — why
  variants must be compared against one aged repository.
- Issue #525 — the retention measurement, and the sweeps it closed out.
- Issue #514 — chunk promotion, measured and rejected.
- `docs/compatibility.md` — the rules any format change is bound by.
- [Kopia #1090](https://github.com/kopia/kopia/issues/1090) — the epoch
  manager's design: append-only index blobs, epoch advance on >20 blobs and
  >24 hours, "state of the world" checkpoints.
- [Kopia #5057](https://github.com/kopia/kopia/issues/5057) — 28,467 index
  blobs, maintenance unable to reduce them, the safety window holding the
  backlog permanently.
- [Kopia #3224](https://github.com/kopia/kopia/issues/3224) — the epoch manager
  compacting on a read path, and hanging against read-only storage.
- [Dolt: Prolly Trees](https://www.dolthub.com/blog/2024-03-03-prolly-trees/) —
  content-defined chunk boundaries, history independence, and a 4 KB target
  chunk size.
- [SlateDB design overview](https://slatedb.io/docs/design/overview/) — an LSM
  on object storage, and its point that byte-level write amplification is not
  where the cost is.
- [S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html)
  and [`UploadPartCopy`](https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPartCopy.html)
  — compare-and-swap on a key, and server-side composition from ranges with a
  5 MB minimum.
