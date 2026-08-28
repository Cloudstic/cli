# RFC 0027: Decoupling Read and Write Layout

- **Status:** Proposed. No implementation. This document exists to force a
  decision, not to record one.
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
that keeps us on the curve, records a constraint that rules out the cheapest
escape, and asks for a decision between the two that remain.

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

## The two candidates

### A. Segmented leaves — spend the format on asymmetry 1

A leaf gains an internal directory: entries grouped into independently
compressed and sealed segments, with an offset table at a known end. A targeted
read (`cat`, `ls`, single-file restore, change detection's per-entry lookup)
fetches the table and one segment — kilobytes rather than megabytes.

- **Breaks:** bytes transferred is no longer tied to leaf size.
- **Does not break:** requests still scale with object count, and *writes* still
  rewrite the whole leaf. Retention cost is untouched.
- **Cost:** new node encoding, a derived segment key, and per-segment framing
  overhead on every leaf whether or not anything ranges it.

### B. Deferred compaction — spend the format on asymmetry 2

A backup writes only what changed, as a delta object. A snapshot names a base
root plus an ordered list of deltas; reads merge. Compaction folds deltas into
a new base periodically — at `prune`, which is already a maintenance operation
that rewrites the world.

- **Breaks:** write amplification stops being a function of leaf size. A
  snapshot retains its churn, not `dirs x leaf size`.
- **Does not break:** a read now costs base + deltas, which is v2's linear term
  returning — bounded only by how aggressively compaction runs.
- **Cost:** every read path (`Lookup`, `Walk`, `Diff`) learns to merge, and
  `prune` acquires a second job. The bound on delta count becomes the new dial,
  and it is a better dial than leaf size only if compaction is cheap enough to
  run often.

**These are not alternatives in principle** — a mature system does both, which
is what an LSM tree with block-level indexes is. They are alternatives in
sequencing, and B is the one that attacks the number we cannot currently move.

### C. Extent-based snapshots — the version worth being honest about

A snapshot is a list of extents over immutable blobs: "bytes 0–4 MB of blob A,
then new blob B, then bytes 4.2–9 MB of blob A". Unchanged bytes are never
rewritten *and* reads are large sequential ranges — both asymmetries at once.
This is how copy-on-write filesystems solved the same problem.

It inherits both costs: it needs asymmetry 1 (so it needs A's crypto framing),
and fragmentation accrues with churn, so it needs defragmentation (which is B's
compaction under another name). It is not a way around the trade; it is a
better operating point on it, and a considerably larger project.

Recording it here so that A and B are chosen as steps toward something rather
than as another pair of points on the curve.

## What this RFC is not claiming

None of this is a new algorithm. Deferred compaction is an LSM tree; segmented
objects are a block-indexed file; extents are a copy-on-write filesystem. What
would be new is applying them to content-addressed backup over object storage,
where the cost model — requests not bytes, ranges free, no batch GET or PUT
(RFC 0026 §"Why batch APIs cannot substitute for layout") — differs enough from
a local disk that the inherited packfile design is simply the wrong shape.

Claiming more than that would be dishonest, and the sweeps have already cost
enough without adding a story to them.

## Open questions

1. **Is B's delta count boundable in practice?** v2's linear term came from a
   snapshot's entries scattering across every pack that contributed. Deltas
   would reintroduce a bounded version of the same thing. The bound has to be
   measured, not argued, and the aging harness now reports the retained-size
   half of it directly (#531).
2. **Does A pay for itself when nothing ranges?** Per-segment framing and tags
   cost bytes on every leaf. A tree whose reads are all full traversals gets
   only the overhead.
3. **Can either ship without a v4?** Both change the node encoding. v3 is
   opt-in and unreleased-by-default (#517), so the cost of a second format
   break is lower now than it will ever be again — which is an argument for
   deciding this before flipping the default, not after.
4. **What is the actual target?** RFC 0026 set ≤1.2x at 80 backups and reached
   1.8x. Neither A nor B should be started without saying which number it is
   supposed to move and by how much.

## Sequencing

No implementation is proposed until question 4 is answered. The next step is a
decision on A versus B, not a branch.

## References

- [RFC 0026](0026-repository-format-v3.md) — format v3, its measurements, and
  the "What v3 stores, and when" section this builds on.
- [RFC 0025](0025-traversal-order-and-pack-contiguous-reads.md) §7 — why
  variants must be compared against one aged repository.
- Issue #525 — the retention measurement, and the sweeps it closed out.
- Issue #514 — chunk promotion, measured and rejected.
- `docs/compatibility.md` — the rules any format change is bound by.
