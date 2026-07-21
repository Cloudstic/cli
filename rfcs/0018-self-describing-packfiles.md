# RFC 0018: Self-Describing Packfiles

- **Status:** Draft
- **Date:** 2026-07-21
- **Affects:** `pkg/store`, `internal/engine`, `cmd/cloudstic`, docs

## Abstract

This RFC proposes adding a footer index to every packfile, so that a packfile
describes its own contents.

Today `index/packs` is the only record of where a packed object lives. Packfiles
are a bare concatenation of object bytes with no framing, so the boundary
between two packed objects exists nowhere else. Lose or corrupt that one JSON
blob and the data remains physically present, correctly hashed, and permanently
unreachable.

With a footer, `index/packs` becomes a *cache* — an optimisation to avoid
touching every packfile on open — rather than the sole source of truth. That
single change converts the remaining packfile failure modes from unrecoverable
data loss into recoverable staleness, and makes a `repair-index` command
possible.

This RFC also proposes replacing the monolithic catalog with per-pack shards, to
remove the read-modify-write race between concurrent backups.

## Context

`PackStore` (`pkg/store/pack.go`) bundles small objects (<512 KB) into 8 MB
`packs/<hash>` objects to avoid issuing hundreds of thousands of per-object
requests against S3 or B2. The saving is real and the feature should stay.

The physical format is:

```
packs/9f2a...  ┌──────────┬───────────────┬──────────┬─────────────┐
               │ filemeta │     node      │ filemeta │   content   │
               └──────────┴───────────────┴──────────┴─────────────┘
               0        412            1907       2311          4002
```

No magic marker, no per-entry framing, no footer. Those offsets live only in
`index/packs`.

### Why this is different from the other indexes

The repository has several mutable, non-content-addressed index objects. All but
one are *derived*, and can be discarded and recomputed:

| Index | Recovery |
|-------|----------|
| `index/snapshots` | `LIST snapshot/` and rebuild — already self-heals on load |
| `index/latest` | read snapshots, take the highest `seq` |
| `index/packs` | **none** |

`index/packs` cannot be rebuilt by listing, because a packed object has no
object of its own at its logical key. `Put` either packs an object or writes it
through, never both (`pkg/store/pack.go:71`), so `filemeta/abc` inside a pack is
a purely logical name. Recovering the map would mean guessing both offset and
length for every object out of 8 MB of undifferentiated bytes.

### Observed failure modes

Tracked in #287. Three were reproduced with a fault-injecting `ObjectStore`
that fails `Get("index/packs")` once; two follow from code ordering. The
immediate data-loss paths are fixed in #293 and #294, but those fixes make the
system *fail loudly* rather than removing the single point of failure. The
underlying property — one copy, not rebuildable — is what this RFC addresses.

The concurrency race is the clearest example of what cannot be fixed in place.
Backups take *shared* locks by design (`internal/engine/repolock.go:126`), so
two runs both load catalog `C0`, append their own entries, and flush `C0+A` and
`C0+B`. Last writer wins, and the loser's entries — including its own snapshot,
since snapshots are always packed — become unaddressable. No compare-and-swap
is available: the `ObjectStore` interface cannot assume any backend provides
one.

### Why not put the location in the reference

The alternative design is to make refs point at physical locations —
`packs/9f2a@412+1495` instead of `filemeta/abc` — removing the need for a map.

This is unworkable, because refs are stored *inside* content-addressed objects.
A HAMT node contains its children's refs, and the node's own key is the hash of
its content (`internal/core/models.go:94`). Embedding a location means a repack
that moves an object changes the content of every node referencing it, which
changes those nodes' hashes, which changes their parents' content, cascading to
the snapshot root. One repack would rewrite the entire tree of every snapshot
that touches it, and cross-snapshot structural sharing would collapse.

The general rule: **in a Merkle DAG, physical location must never contribute to
an object's identity**, because identity propagates upward. Location is mutable;
identity is not.

Deduplication reinforces this. "Do I already have this content?" is a lookup
keyed by hash, so a hash-to-location map is required regardless. Embedding
locations in refs does not remove the map — it adds a second, more brittle
mechanism beside it.

The existing indirection is therefore correct. The defect is narrower than the
design: the map has exactly one copy and nothing can regenerate it.

## Goals

- Make every packfile self-describing, so the location map is derivable from the
  packfiles themselves.
- Demote `index/packs` to a rebuildable cache with the same status as
  `index/snapshots`.
- Remove the read-modify-write race between concurrent backups.
- Give `check` a way to verify pack integrity, and make a `repair-index` command
  possible.
- Keep existing repositories readable, with no flag day.

## Non-goals

- Changing the object model, key naming, chunking, or the HAMT.
- Changing which objects are eligible for packing.
- Removing packfiles or making them mandatory.
- Introducing a compare-and-swap or locking requirement on backends.
- Encrypting packfile *contents* differently — they are already ciphertext by
  the time `PackStore` sees them.

## Proposal

### 1. Packfile footer

Append a trailer to every packfile:

```
┌────────────────────────────────────────────────┐
│ object bytes (concatenated, unchanged)          │
├────────────────────────────────────────────────┤
│ footer payload (JSON entry list)                │
├────────────────────────────────────────────────┤
│ footer length     uint32 big-endian   4 bytes   │
│ format version    uint8               1 byte    │
│ magic "CSPACK"                        6 bytes   │
└────────────────────────────────────────────────┘
```

The fixed 11-byte trailer is read first; it yields the footer length, and the
footer yields every entry:

```json
{
  "v": 1,
  "entries": [
    { "k": "filemeta/<hash>", "o": 0, "l": 412 },
    { "k": "node/<hash>", "o": 412, "l": 1495 }
  ]
}
```

The packfile key stays content-addressed: the hash is computed over the complete
object including its footer, so packfiles remain immutable and
self-verifying.

Object bytes keep their current position and meaning, so a reader that already
knows an offset and length is unaffected by the footer's presence.

### 2. `index/packs` becomes a cache

The read path is unchanged in the common case: consult the catalog, fetch the
pack, slice out the object. The catalog remains the fast path, because reading
it is one request whereas reading N footers is N.

What changes is the failure path. When the catalog is missing, unreadable, or
does not contain a key that should exist, the store can now recover:

1. `LIST packs/`
2. read each pack's trailer and footer
3. merge the entries
4. optionally write the reconstructed catalog back

This is the same relationship `index/snapshots` already has with
`LIST snapshot/`.

### 3. Sharded, append-only catalog

Replace the single mutable `index/packs` object with one immutable shard per
packfile, written once at flush time:

```
index/packmap/<pack-hash>
```

Readers `LIST index/packmap/` and merge every shard. Writers never
read-modify-write: each run writes only shards for packs it created. Two
concurrent backups write disjoint shards and neither can erase the other, which
removes the race without needing a compare-and-swap.

Merging is idempotent and order-independent. Keys are content-addressed, so if
the same key appears in two shards pointing at two different packs, both copies
are byte-identical and either location is correct.

`prune`, which already holds the exclusive lock, compacts shards: write a
consolidated shard first, then delete the shards it subsumes. A reader that
lists mid-compaction sees both and merges them harmlessly.

**Naming note.** The shard prefix is deliberately *not* `index/packs/`. On
`LocalStore` keys map to filesystem paths, and a file named `index/packs` cannot
coexist with a directory named `index/packs/`. `index/packmap/` avoids
colliding with the legacy object during migration.

### 4. Ranged reads

Rebuilding from footers should not require downloading whole 8 MB packs. The
`ObjectStore` interface (`pkg/store/interface.go:9`) has no ranged read today.

Add an optional interface, following the existing `ConcurrencyHinter` and
`Unwrapper` pattern:

```go
// RangeGetter is an optional interface for backends that can read a byte range
// without transferring the whole object.
type RangeGetter interface {
    GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error)
}
```

S3, B2, and local filesystem support this natively; SFTP via `ReadAt`. Backends
that do not implement it fall back to a full `Get`, which keeps recovery correct
everywhere and merely slower on those backends.

This also benefits the normal read path: fetching a single small object from a
cold 8 MB pack currently transfers the whole pack.

### 5. `check` and `repair-index`

With footers, `check` can verify that every catalog entry agrees with the
footer of the pack it names, and that every pack's footer is well-formed and
its length consistent with the object's size.

A `repair-index` command rebuilds `index/packmap/` from `LIST packs/` plus
footers. This is the recovery path for a repository damaged by any of the
failure modes in #287, and it is only implementable once footers exist.

### 6. Migration

No flag day. Three stages, each independently shippable:

1. **Write footers.** New packs get footers; `index/packs` continues to be
   written and read exactly as today. Footers are inert but accumulate.
2. **Shard and merge.** Write `index/packmap/` shards; readers merge shards
   *and* the legacy `index/packs`. Recovery from footers becomes available, and
   `repair-index` ships.
3. **Retire the monolith.** Once `repack` has rewritten all footerless packs, a
   repository can be marked as fully migrated and the legacy `index/packs` read
   dropped.

Footerless packs stay readable throughout via the legacy catalog. A repository
written by a newer client stays readable by an older one during stage 1, since
the footer is simply trailing bytes an old reader never looks at — and never
reads, because it addresses objects by explicit offset and length.

Whether to record the format state in the repository `config` object, or infer
it by checking whether any footerless pack remains, is an open question below.

## Compatibility

- **Old client, new repository:** readable through stage 1 and stage 2, because
  `index/packs` is still written. Stage 3 is a breaking change for old clients
  and must be gated behind an explicit repository upgrade.
- **New client, old repository:** fully readable. Footerless packs resolve via
  the legacy catalog; no footer is assumed to exist.
- **Object model:** unchanged. No snapshot, HAMT node, filemeta, or chunk
  changes, and no snapshot identity changes.
- **`CLOUDSTIC_DISABLE_PACKFILE`:** unchanged; a repository with packing
  disabled is unaffected throughout.

## Security considerations

The footer contains object keys and per-object byte lengths. `PackStore` sits
*below* `EncryptedStore` in the chain (`client.go:256-273`), so it has no access
to the master key and cannot seal its own output.

This is not a new exposure introduced by this RFC. `index/packs` is **already**
written unencrypted today, for exactly the same reason — verified against the
real chain, see #295. The footer would relocate that same information, not
reveal anything additional.

It is, however, the natural point to fix it. `filemeta/`, `node/`, and
`snapshot/` keys are plain SHA-256 of their serialized JSON, not HMAC-keyed like
`chunk/` and `content/`, so a plaintext index lets an adversary holding the
bucket confirm the presence of a metadata object whose exact field values they
can guess.

Two options:

- **Seal both**, by passing a sealer into `PackStore`. This inverts a layering
  assumption — the encryption boundary currently sits above the component
  writing this metadata — so it needs care, and a migration for existing
  plaintext catalogs.
- **Document the exposure** in `docs/encryption.md`, alongside `keys/` and
  `config`, and treat it as deliberate.

Resolving #295 decides this, and the footer must follow the same decision.

## Testing strategy

- Round-trip: write a pack, read its footer, assert the entries match the
  catalog exactly.
- Rebuild: delete `index/packs` entirely, assert every object remains readable
  and the catalog is reconstructed from footers.
- Fault injection: with `Get("index/packs")` failing, assert reads still succeed
  via the footer path rather than failing outright.
- Concurrency: two processes backing up simultaneously, asserting both
  snapshots are fully restorable afterwards — this fails today and is the
  acceptance test for sharding.
- Compaction: assert a reader that lists mid-compaction, seeing both the
  consolidated shard and its inputs, resolves every key correctly.
- Migration: a repository containing both footered and footerless packs, read
  by a stage-2 client.
- Backwards compatibility: a stage-1 repository read by a client that ignores
  footers.

## Rollout plan

1. `RangeGetter` optional interface plus backend implementations. Independently
   useful, no format change.
2. Footer write path and footer-aware reads (stage 1).
3. Rebuild-from-footers, plus `repair-index` and `check` integration.
4. `index/packmap/` shards and compaction in `prune` (stage 2).
5. Retire the monolithic catalog behind an explicit repository upgrade
   (stage 3).

Steps 1–3 are strictly additive and carry no compatibility risk.

## Open questions

1. **Footer encoding.** JSON is consistent with the rest of the repository and
   debuggable by hand. A packed binary encoding would be meaningfully smaller
   for packs holding thousands of entries. Is the size worth the opacity?
2. **Footer encryption.** Follows #295 — seal it, or document the exposure? If
   sealed, how does `PackStore` obtain a sealer without inverting the store
   layering more than necessary?
3. **Format state.** Record the pack format in the `config` object, or infer
   migration completeness by scanning for footerless packs?
4. **Compaction threshold.** At what shard count should `prune` compact? A
   repository with many small backups accumulates one shard per pack, and
   `LIST index/packmap/` grows linearly.
5. **Does `index/packs` survive at all after stage 3?** A single consolidated
   shard is nearly the same object under a different name. The distinction that
   matters is append-only-ness, not cardinality.
6. **Cache invalidation.** If the catalog and a footer disagree, which wins? The
   footer is inside a content-addressed, immutable object and should be
   authoritative — but that makes every disagreement a signal that the catalog
   is corrupt, which `check` should probably report loudly.
