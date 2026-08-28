# RFC 0024: Metadata in the Tree

- **Status:** Absorbed into [RFC 0026](0026-repository-format-v3.md). This
  RFC's design — metadata in binary HAMT leaves, inline small-file content —
  is carried forward unchanged, but as repository format v3 (the next free
  `config.version` — today's builds already stamp 2) rather than an
  opportunistic in-place layout change. The v3 decision removes the
  constraints this document worked under (dual JSON/binary decoders held
  forever, mixed-era repositories, packs retained "to be measured"): v3 has
  one leaf form and no pack layer. The identity-model constraint section and
  the incremental-cost analysis remain binding and are referenced from RFC
  0026 rather than restated.
- **Date:** 2026-08-04, revised 2026-08-08, absorbed 2026-08-28
- **Affects:** repository format, `internal/hamt`, `internal/engine`, `internal/storelayer`
- **See also:** [RFC 0025](0025-traversal-order-and-pack-contiguous-reads.md), which
  covers how a snapshot is *read*. It was split out of this document because it
  needs no format change and can proceed independently.

## Abstract

A snapshot of 50,000 files is about 109,600 objects. Almost all of them are one
`filemeta/` and one `content/` object per file, each a separate JSON document
that has to be fetched, decoded and indexed individually.

This RFC proposes storing file metadata **inside the HAMT leaves that already
point at it**, in a compact binary encoding, and inlining small-file content
alongside it. The same snapshot becomes about 9,100 objects.

Every structure this workstream has spent months trying to bound — the pack
catalog, the pack body cache, `check`'s verified set, `prune`'s reachable set —
is proportional to object count, so a ~12x reduction does more for memory than
bounding any of them individually.

It deliberately does not change the identity model. That is the point of the
constraint section below.

## Context

Issue #441 tracked "peak memory grows with repository size for every operation".
Seven merged changes later, the growth from 5,000 to 50,000 files is down about
a third and `check` is nearly flat, but every remaining structure is still
O(objects):

| structure | at 50k files |
|---|---|
| pack catalog | ~109,600 entries, 18.2 MB on disk, 73 B/entry resident |
| pack body cache | bounded, but sized against pack count |
| `CheckManager.verified` | one entry per object verified |
| `prune` reachable set | one entry per object |
| `ObjectStore.List` result | one string per object, materialised |

Each was attacked separately. The pattern across all of them, and the reason the
last several attempts measured as no-gain, is that they are symptoms of the same
cause: **the format produces an enormous number of small objects, and everything
downstream carries one entry per object.**

Two measurements from that work bound the design space:

- Peak RSS is dominated by transient allocation around I/O, not by resident
  structures. 100,000 JSON decodes cost more than the maps holding their results.
- Trading residency for re-reads is only a good trade when the re-read is cheap.
  A pack-cache miss re-reads ~9 MB to return ~200 bytes.

Both point the same way: reduce the number of objects rather than shrink the
bookkeeping about them.

## The constraint this design must respect

An earlier draft proposed keying the tree by path. That is wrong for this
product, for three reasons that are worth stating because they are not
recoverable from the code:

1. **A file can have several parents.** A Google Drive file lives in two folders
   at once. `fileMetaPaths` exists precisely because callers need *every* path by
   which an entry can be reached. Path is not an identity.
1. **Path keying unbalances the tree.** A directory with a million files becomes a
   million siblings under one prefix. The HAMT is hash-balanced to avoid exactly
   that.
1. **Path keying destroys rename stability.** With `FileID` identity, renaming a
   folder leaves every descendant's key untouched and nothing below it is
   rewritten. With path identity, one rename rewrites the entire subtree — the
   worst possible behaviour for an incremental backup of a cloud source.

So identity stays exactly as it is: `FileID` assigned by the source, `Parents` a
list, routing by `AffinityKey(primaryParentID, FileID)`.

**Identity and layout are separable.** Everything below changes only where bytes
are placed, never what names them.

## Proposal

### 1. Leaf entries carry the metadata

Today a leaf entry points at a separate object:

```go
type leafEntry struct {
    Key     string `json:"key"`                // source file ID
    PathKey string `json:"path_key,omitempty"` // routing key (affinity)
    Value   string `json:"filemeta"`           // "filemeta/<sha256>"
}
```

Reading one file's metadata is therefore two round trips: the node, then the
`filemeta/` object it names. For a full traversal that is one extra object read
per file, plus one more for `content/`.

The proposal replaces `Value` with the metadata itself. A leaf then answers for
every entry it holds, and the `filemeta/` and `content/` namespaces disappear for
all but chunked files.

Deduplication moves from per-file to per-leaf granularity: changing one file
rewrites its leaf-mates' metadata rather than one file's. What that costs on an
incremental is worked through in "What subsequent backups cost" below rather than
waved away.

#### What replaces the `filemeta/` ref

The ref is load-bearing beyond storage. `internal/engine/backup_scan.go` reads
`oldRef` from `tree.Lookup` and compares it against a freshly computed
`FileMetaRef` to decide whether a file changed — a cheap equality test on a
content address, with no decode. Removing the ref removes that test, so the
replacement has to be stated:

- `Lookup` returns the decoded entry rather than a ref. Change detection compares
  the fields that define a change (size, mtime, mode, content identity) directly.
  Those are fixed-offset fields in the record, so the comparison stays
  allocation-free and does not require decoding the arena.
- A per-entry content address is still needed for `check` to verify metadata
  integrity. The leaf as a whole is content-addressed, which covers every entry
  in it; per-entry addresses are not reintroduced.
- **Mixed-era repositories.** A leaf is either the JSON form or the binary form,
  distinguished by its magic. Readers accept both. Writers emit the new form
  only. An old leaf is converted when a write happens to pass over it, which is
  the same opportunistic upgrade `docs/compatibility.md` already mandates —
  a repository stays a mixture of eras indefinitely and that is the steady
  state. `filemeta/` and `content/` objects referenced by surviving JSON leaves
  stay readable and are never rewritten in place.

### 2. Leaves are a binary record array, not JSON

A leaf is a fixed-width record array plus a string arena. Variable-length fields
are `(offset, length)` spans into the arena, so a reader can locate any field
without parsing, and can decode one entry without touching the others.

All integers are **little-endian**. All arena offsets are relative to the start
of the arena, not to the start of the object.

```text
leaf object
┌────────────────────────────────────────────────────────────┐
│ magic "CSLF"          4 bytes                              │
│ version               u8                                   │
│ (reserved)            u8                                   │
│ entry count           u16                                  │
│ parents table offset  u32   absolute, from object start    │
│ arena offset          u32   absolute, from object start    │
│                             header is 16 bytes, padded     │
├────────────────────────────────────────────────────────────┤
│ entry[0..n]           56 bytes each, sorted by routing     │
├────────────────────────────────────────────────────────────┤
│ parents table         (u32 arena-offset, u16 len) pairs    │
├────────────────────────────────────────────────────────────┤
│ arena                 concatenated bytes, no padding       │
└────────────────────────────────────────────────────────────┘
```

```text
entry — 56 bytes, 8-byte aligned, no pointers, no allocation to read
┌──────────────┬─────────┬────────────────────────────────────────┐
│ routing      │ u64     │ first 8 bytes of the affinity key       │
│ fileID       │ u32+u16 │ arena offset, length                    │
│ name         │ u32+u16 │ arena offset, length                    │
│ content      │ u32+u32 │ arena offset, length — see below        │
│ parents      │ u32+u16 │ parents-table index, count              │
│ size         │ u64     │ logical file size                       │
│ mtime        │ i64     │ unix nanoseconds                        │
│ mode         │ u32     │                                         │
│ type         │ u8      │ file, folder, symlink                   │
│ flags        │ u8      │ inline-content, has-xattrs, …           │
└──────────────┴─────────┴────────────────────────────────────────┘
                            8+6+6+8+6+8+8+4+1+1 = 56
```

The `content` span is one field serving both cases, which is what makes inline
bytes addressable at all:

- **inline flag set** — the span locates the file's bytes in the arena.
- **inline flag clear, length non-zero** — the span locates the
  `content/<hash>` ref string in the arena.
- **length zero** — no content: a folder, or an empty file.

It is `u32+u32` rather than `u32+u16` for the same reason: a `u16` length caps at
65,535 bytes and could not express an inline file of any interesting size. The
other spans keep `u16` lengths, which bounds `fileID` and `name` at 65,535 bytes
each — far above any real source identifier or filename, and enforced at encode
time rather than assumed.

Entries are sorted by `routing`, so a lookup inside a leaf is a binary search
over a contiguous array, and a range scan over a directory is sequential.

The 8-byte `routing` prefix is a filter, not the key: a match is confirmed
against the full `fileID` in the arena. That keeps the record fixed-width while
`FileID` stays an arbitrary-length source-assigned string.

#### Validation

A decoder rejects a leaf rather than trusting it, because a leaf is read from a
store and `docs/compatibility.md` requires that "cannot decode" never degrades to
"empty":

- magic is `CSLF`; version is known, else reject naming the version.
- `parents table offset` and `arena offset` are within the object, and ordered
  header < entries < parents table < arena.
- entry count × 56 fits between the header and the parents table.
- every span satisfies `offset + length <= arena length`, checked before any read.
- every parents-table index plus count is within the parents table.
- `routing` is non-decreasing across entries, so binary search is sound.

### 3. Small-file content moves inline

A file whose content fits the inline budget carries its bytes in the arena and
sets the inline flag. Only chunked files keep a `content/` object listing chunk
refs.

The budget is **not** inherited from `maxObjectSize` (512 KB). At `maxLeafSize`
= 32 entries, a 512 KB budget would allow a 16 MB leaf, and a one-byte edit
would rewrite all of it. See "What subsequent backups cost": something in the
1–4 KB range keeps a full leaf near ~128 KB, and the right value is a
measurement, not a choice.

For a tree of very small files this removes one object per file on its own.

## Worked example

The tree the memory benchmark builds: 50,000 files of ~35 bytes in 500
directories, one snapshot.

| | today | proposed |
|---|---|---|
| `filemeta/` objects | 50,500 | 0 |
| `content/` objects | 50,000 | 0 (inline) |
| `node/` objects | 9,096 | ~9,100 |
| `snapshot/` objects | 1 | 1 |
| **total objects** | **~109,600** | **~9,100** |
| pack catalog on disk | 18.2 MB | ~0.7 MB |
| pack catalog resident | ~8 MB | ~0.3 MB |
| objects read for a full traversal | ~109,600 | ~9,100 |
| JSON documents decoded | ~109,600 | 0 |

A full `restore` today issues 50,000 filemeta reads and 50,000 content reads on
top of the tree walk. Proposed, the tree walk *is* the metadata read: ~9,100 node
fetches, each yielding several files' metadata with no per-file decode.

Object count is not request count — packs bundle many objects into one transfer,
which is why a 5,000-file snapshot restores in 58–112 requests today rather than
tens of thousands. Fewer objects reduces requests indirectly, by making each
backup's packs smaller and its catalog cheaper, not one-for-one. RFC 0025 covers
what actually decides the request count.

### What a leaf looks like

Two entries sharing a leaf, with every offset computed. `FileID` values are
opaque, as a source assigns them — they are deliberately not paths.

```text
header    @0    magic "CSLF" | version 1 | count 2 | parents@128 | arena@140

entry[0]  @16   routing 0x8f3a91c4d2e07b15
                fileID  (arena+0,  6)   name (arena+6,  8)
                content (arena+35, 12)  flags=inline
                parents (index 0, count 1)   size 12   type=file
entry[1]  @72   routing 0x8f3b02a7e1554c9d
                fileID  (arena+14, 6)   name (arena+20, 9)
                content (arena+47, 71)  flags=0        (a content/ ref)
                parents (index 1, count 1)   size 4194304   type=file

parents   @128  [0] = (arena+29, 6)      ← both name the same directory
                [1] = (arena+29, 6)

arena     @140  +0   "f_9a3c"          fileID of entry 0
                +6   "notes.md"        name of entry 0
                +14  "f_9a3d"          fileID of entry 1
                +20  "photo.jpg"       name of entry 1
                +29  "d_41f0"          the parent directory's FileID
                +35  <12 bytes>        inline content of notes.md
                +47  "content/7b2e…"   71-byte ref for photo.jpg
```

`d_41f0` appears once in the arena and is referenced by both entries. Today it is
repeated in two separate JSON documents, along with two copies of the
`"parents":[…]` key.

## What subsequent backups cost

The headline numbers above are an initial backup. Incrementals are where
embedding metadata could plausibly make things worse, so they are worked through
here rather than asserted to be free.

`maxLeafSize` is 32, so a leaf holds up to 32 entries and changing one file
dirties its whole leaf. The naive reading is 32x write amplification; the
measured baseline says otherwise, because **the HAMT spine already dominates a
single-file incremental today**:

| one file changed | today | metadata in leaf | metadata + inline at 3.6 KB median |
|---|---|---|---|
| `filemeta/` + `content/` | ~400 B | 0 | 0 |
| leaf (metadata) | — | ~6.4 KB | ~6.4 KB |
| leaf (inline content) | — | — | ~115 KB |
| dirty spine | ~5 KB | ~4 KB | ~4 KB |
| **total written** | **~5 KB** | **~10 KB** | **~125 KB** |

Two conclusions, pointing in opposite directions:

- **Metadata embedding is roughly 2x on a single-file incremental**, not 32x. The
  spine was always the larger term, and 2x of 5 KB is not a number worth changing
  the design for.
- **Inline content is the real cost**, scaling with `inline budget × maxLeafSize`.
  It is why §3 refuses to inherit the 512 KB budget.

**Clustered churn inverts the comparison.** Files changed in one directory tend
to share a leaf, so they cost one leaf rewrite rather than one `filemeta` plus one
`content/` object each. Real churn clusters — that is why `gentree` models it with
a Zipf distribution over directories — so the common case is a win and the worst
case is scattered single-file edits across many directories.

Both cases are already in the benchmark: `backup-incremental-1` is the scattered
single edit, `backup-incremental-1000` the clustered batch. Whichever way the
design lands, they will show it, and neither should be accepted on reasoning
alone.

**A caveat on "its leaf".** Affinity routing makes leaf-mates *likely* to be
siblings, not certainly. `maxLeafSize` is 32, so a directory with more than 32
children spans several leaves; and the affinity prefix is 16 bits, so directories
collide and a leaf can mix entries from more than one. Rewrite projections should
therefore be stated over *all affected leaves*, not "the leaf" — for a directory
of `n` children that is `ceil(n/32)` leaves at minimum, plus whatever collides.

## Do packfiles still earn their place?

Worth asking directly, because this design changes their cost model.

Packs exist because writing ~109,600 small objects to S3 is ~109,600 PUTs. At
~9,100 objects that argument weakens considerably, and two things follow:

- **`maxObjectSize` is 512 KB** — anything larger bypasses packing already. Leaf
  size is a tunable. Tuned large enough, the metadata path stops being packed at
  all, and the pack catalog stops existing for it.
- **Chunk data was never the problem.** Chunks of large files are already large,
  already content-addressed, and already random-access by nature.

This is a question to measure, not a recommendation to remove packs. Pack
membership is not neutral to transfer volume: a pack holding unrelated leaves
overfetches relative to standalone objects, and a pack holding a directory's
leaves together underfetches relative to them. Whether packing pays for the
metadata path at 9,100 objects depends on which of those the packing policy
produces, which is exactly what should be measured rather than assumed.

## What this does not solve

- **`prune` still materialises every key**, because `ObjectStore.List` returns a
  slice. ~9,100 strings instead of ~109,600 is a large improvement and still
  O(n). A streaming enumeration is a separate change on a public interface
  (a non-goal in RFC 0023).
- **The ~150 MB floor stays.** It is Argon2id's 64 MB doubled by `GOGC=100`, and
  it is correct behaviour. Releasing it explicitly after unlock is worth doing
  and is independent of this RFC.
- **Chunked files still cost a `content/` object each.** For a repository of
  large files the object count is dominated by chunks, and this design does
  nothing for it. It targets the many-small-files case, which is where the
  measured growth was.
- **Read cost is not addressed here at all.** Object count is one factor; the
  order objects are read in and what the cache knows about them are others, and
  they turn out to matter more for a restore. See RFC 0025.

  The two do compose at one specific seam, worth naming because it is not
  obvious. A pack mixes namespaces, and on today's format a restore of small
  files reads roughly as many `content/` objects as metadata objects — read in a
  different phase, so RFC 0025's pack grouping forms them into a separate set and
  a pack holding both is fetched once per phase. Inlining small-file content here
  collapses the two classes into one, at which point the traversal set *is* the
  read set and that double fetch disappears. Neither RFC needs the other; each
  makes the other worth more.

## Open questions

1. **What is the right inline budget?** The parameter with the most cost behind
   it: metadata embedding is ~2x on a single-file incremental while inline
   content at 512 KB could reach 16 MB per leaf. It has to be set against a full
   leaf staying comparable to the spine, and measured on `backup-incremental-1`
   and `backup-incremental-1000` rather than reasoned about.
1. **Is `maxLeafSize = 32` still right once leaves carry metadata?** Bigger leaves
   mean fewer objects and more rewritten bytes per change; it also decides whether
   the metadata path clears `maxObjectSize` and stops being packed. It has been an
   internal HAMT detail; this makes it a format-level trade.
1. **Does the arena want compression?** Sibling file names share long prefixes.
   Front-coding within a leaf is cheap and might halve the arena, but it costs
   the zero-copy property for names.
1. **How are xattrs and ACLs carried?** They are rare and variable-length. A
   side-table keyed by entry index, present only when the flag is set, keeps the
   common record at 56 bytes.
1. **Is `routing` as a u64 prefix enough?** It is a filter over entries that
   already share a leaf, so collisions cost a comparison, not a correctness
   problem. Worth confirming against a source that assigns adversarial IDs.
1. **What happens to content-addressed filemeta dedup?** Two identical files in
   different directories share a `content/` object today and would still share
   chunk data, but their metadata now lives in two leaves. That is a storage cost,
   and it should be quantified.
1. **How is the JSON→binary conversion paced?** Opportunistic upgrade means a
   repository holds both forms indefinitely, so every reader carries both decoders
   and `check` must accept both. Whether an explicit bulk conversion is also
   wanted — and whether `prune`'s existing rewrite is the place for it — is open.

## Why this over the alternatives

**Bounding the pack catalog** (RFC 0023) attacks the index rather than what it
indexes. Measurement showed the bounded-cache trade is a bad one when a miss is
expensive, and that the catalog is not the largest term anyway.

**A sorted on-disk index with fence pointers** — an earlier sketch — would make
the catalog nearly free to hold. But lookups are by content hash and therefore in
random order, so a block-cached on-disk index thrashes the same way the pack body
cache did. It solves residency and creates a read-amplification problem.

**Reducing object count** makes both of those problems smaller rather than
solving them in place, and it is the only one of the three that also reduces
what a remote backend is asked to do. It is not sufficient on its own: RFC 0025
covers the read path, and the two are independent.
