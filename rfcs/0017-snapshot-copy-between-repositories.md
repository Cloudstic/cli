# RFC 0017: Snapshot Copy Between Repositories

- **Status:** Draft
- **Date:** 2026-04-03 (revised 2026-08-03)
- **Affects:** `cmd/cloudstic`, `client.go`, `internal/engine`, `internal/hamt`,
  `internal/core`, `internal/storelayer`, `pkg/config`, `pkg/open`, docs

The 2026-08-03 revision rebases the CLI and API surface on what the codebase
actually has after RFC 0018 (self-describing packfiles) and RFC 0022 (public Go
API boundaries), and replaces the hand-waving in §4, §5 and §8 with a specified
algorithm. See "Revision notes" at the end for what changed and why.

## Abstract

This RFC proposes a `copy` command for transferring snapshots from one
Cloudstic repository to another.

The primary use cases are:

- seeding a new repository from an existing one
- migrating from one backend to another
- promoting local snapshots into a remote repository
- consolidating snapshots selected by source, tags, account, or explicit IDs

The command is repository-to-repository, not source-to-repository. It copies
existing snapshot history and the reachable data needed to restore those
snapshots in the destination repository.

Copy is a **logical rebuild**, not a transfer. Every object reference in a
Cloudstic repository is derived from that repository's master key, so nothing
can be moved verbatim: the destination graph is reconstructed bottom-up from
plaintext and re-addressed in the destination's own domain. §4 specifies this.

## Context

Cloudstic already supports:

- multiple repository backends (`local:`, `s3:`, `b2:`, `sftp://`)
- encrypted repositories with independent key slots
- snapshot catalogs via `index/snapshots`
- explicit profile/store configuration

What it does not support yet is moving backup history between repositories in a
first-class way.

Today, users wanting to migrate repositories effectively have two bad options:

- rerun backups against the new repository, which loses original snapshot times
  and only works if the original source is still available
- manually copy backend objects, which is incorrect for encrypted repositories
  because repositories do not share keys and may not share identical object
  layouts

### Why object refs cannot be preserved

The second option is not merely inconvenient, it cannot work. Object identity
cascades off the master key at every level:

| Object | Key derivation | Source |
|--------|----------------|--------|
| `chunk/<h>` | `HMAC(dedupKey, plaintext)`, or `SHA256(plaintext)` when unencrypted | `internal/engine/chunker.go` |
| `content/<h>` | `HMAC(dedupKey, SHA256(stream))` | `internal/engine/chunker.go` |
| `filemeta/<h>` | `ComputeJSONHash(FileMeta)` — embeds the content ref | `internal/core/models.go` |
| `node/<h>` | `ComputeJSONHash(storedNode)` — embeds filemeta refs | `internal/hamt/node.go` |
| `snapshot/<h>` | `ComputeJSONHash(Snapshot)` — embeds the root node ref | `internal/core/models.go` |

`dedupKey` is HKDF-derived from the repository master key
(`crypto.HKDFInfoDedupV1`). Two repositories therefore assign different names to
identical bytes, and a single differing chunk ref propagates all the way to the
snapshot ref. Copy must rebuild the graph, not relabel it.

So "copy snapshot history" must be an engine-level operation.

## Goals

- Add a supported way to copy snapshots between repositories.
- Preserve snapshot semantics that describe *what was backed up*: `Created`,
  `Source`, `Tags`, `Meta`, `ChangeToken`, `ExcludeHash`, and path lineage.
- Support both "copy everything" and filtered/exact snapshot selection.
- Make repeated runs idempotent: already-copied snapshots are skipped without
  re-reading their data.
- Keep the command compatible with Cloudstic's current store/profile/auth model.
- Expose the feature both in the CLI and the public `Client` API.

## Non-goals

- No backend-native server-side object cloning in v1.
- No preservation of source object refs — see "Why object refs cannot be
  preserved" above; this is impossible, not merely deferred.
- No requirement that source and destination share passwords, master keys, or
  key slots.
- No attempt to merge or reconcile histories from unrelated repositories beyond
  snapshot-level copying.
- No in-place conversion of an existing repository's chunking behavior or object
  layout. A snapshot chunked under older CDC parameters keeps those boundaries
  in the destination (§4.3).
- No fine-grained resume *within* one snapshot (§8).
- No write of any kind to the source repository. Copy runs against read-only
  source credentials.

## Proposal

### 1. Add `cloudstic copy`

Introduce a new top-level command:

```bash
cloudstic copy -from-store <uri> [snapshot selectors...]
```

Examples:

```bash
# Copy every snapshot from one store into another
cloudstic copy \
  -store s3:dest-bucket/prod \
  -from-store local:/tmp/cloudstic-src

# Copy one profile's repository into another profile's repository
cloudstic copy \
  -profile remote-prod \
  -from-profile local-seed \
  -source local:./Documents

# Copy explicitly selected snapshots
cloudstic copy \
  -profile archive \
  -from-profile laptop-local \
  410b18a2 4e5d5487 latest
```

The destination repository is configured exactly as for every other repository
command, through the existing global flag groups (`repoFlagSpecs`,
`storeSFTPFlagSpecs`, `encryptionFlagSpecs` in `cmd/cloudstic/flags.go`) — so
`-store`, `-profile`, `-password`, `-encryption-key`, `-s3-*`, `-b2-*`,
`-store-sftp-*`, `-kms-*` and the rest keep their current meanings.

The source repository is configured through a parallel `-from-*` set.

Both repositories must already be initialized. `copy` never runs `init` on the
destination: creating a repository is a decision about key slots and encryption
that must not happen as a side effect of a migration command.

### 2. Source repository configuration flags

**The `-from-*` flags are generated, not hand-written.** `copy` binds a second
`globalFlags` value and derives its flag specs by mapping the three repository
flag groups through a `withPrefix("from-")` transform:

```go
func fromFlagSpecs(from *globalFlags) []flagSpec {
    var specs []flagSpec
    for _, group := range []flagGroup{repoFlagSpecs, storeSFTPFlagSpecs, encryptionFlagSpecs} {
        specs = append(specs, prefixed("from-", group(from))...)
    }
    return specs
}
```

This is the single most important structural decision in the CLI half of the
RFC. Hand-listing the mirrors — as the original draft did, in a 25-line block
that already omitted `-b2-key-id`, `-b2-app-key` and `-disable-packfile` —
guarantees drift the first time a store flag is added. Generation makes
`TestCopyMirrorsEveryRepositoryFlag` a one-line assertion over
`repoCommandGroups`, and the shell-completion generators pick the mirrors up
for free because they already walk `flagSpec`s.

`prefixed` rewrites three things per spec: the flag name, the placeholder-bearing
usage string, and — see below — the environment binding.

**Design rules:**

- Destination flags keep their current names. Source flags are `from-` mirrors.
- **`-from-*` flags carry no environment bindings.** `prefixed` strips
  `withEnv`. This is deliberate: `CLOUDSTIC_PASSWORD` in the ambient environment
  means "the repository I am operating on", and silently applying it to *both*
  repositories in a two-repository command is how an operator unlocks the wrong
  one, or believes they did. Ambient `CLOUDSTIC_*` continues to configure the
  destination only; the source must be named explicitly.
- The supported non-interactive path for source credentials is
  **`-from-profile`**, whose profile entry may carry `env://` secret refs like
  any other. That is already the repo's blessed mechanism for scripted
  credentials and needs no new surface.
- Consequently there are no `-from-password-secret`-style flags. The original
  draft proposed them, but they have no destination counterpart to mirror:
  `-password-secret` and friends are `store` subcommand flags
  (`cmd/cloudstic/cmd_store.go`), not global ones.
- Because the mirrors bind no environment, they add **no rows** to the
  `docs/user-guide.md` environment table and cannot trip
  `TestUserGuideDocumentsEveryEnvVar`.

`-from-prompt` is accepted, and interactive prompts are labelled with which
repository they unlock (`Password for source repository (local:/seed):`). With
`-no-prompt`, a missing source credential is a hard error naming
`-from-password` / `-from-profile`.

Example:

```bash
cloudstic copy \
  -profile remote-prod \
  -from-profile laptop-local
```

Both profiles resolve their own store, auth, and secret refs through
`pkg/profile` and `pkg/config` exactly as a single-repository command does.

### 3. Snapshot selection model

By default, `copy` selects all source snapshots.

It may be narrowed in three ways:

- explicit positional snapshot IDs: `cloudstic copy ... <snapshot_id>...`
- the filters `list` and `forget` already understand: `-source`, `-account`,
  `-tag`
- `-since <timestamp>`, selecting snapshots whose `Created` is at or after the
  given time (§7 explains why this one earns its place in v1)

If explicit snapshot IDs are provided, they define the candidate set and the
filter flags further constrain it. `latest` is accepted as a selector and
resolves through the source repository's normal `resolveLatest` behavior
(`internal/engine/snapshots.go`).

`-group-by` is **not** a copy flag. It is a grouping input to retention policy
in `forget`, not a selector, and mirroring it here would imply copy applies a
policy.

**On `-profile` as a selector.** The original draft left open whether `copy`
should accept `-profile` as a selection shortcut, and recommended against it.
That recommendation stands, but the reason needs stating because the word is now
overloaded: `-profile` and `-from-profile` select **repositories**, and adding a
third meaning ("snapshots belonging to this profile's source") to the same flag
would be unreadable. A user who wants a profile's lineage narrows with
`-source`, which is what the profile's source resolves to anyway.

### 4. Copy semantics

Copy is a bottom-up rebuild with a reference-remapping table. This section
specifies it because it is the whole feature; "walk the graph and write the
objects" is not an implementable description given the ref cascade above.

#### 4.1 Ordering

Selected snapshots are copied in **ascending source `Created`**, ties broken by
ascending source `Seq`. This matters for §4.5 and §4.6, and it makes a
partially-completed run leave the destination in a state that looks like normal
backup history rather than a shuffled one.

#### 4.2 Per-snapshot rebuild

For each selected source snapshot, in order:

1. Load the source snapshot object and its root `node/` ref.
2. Locate the destination's existing snapshot for the same source identity, if
   any, using `findPreviousSnapshot` semantics. Its HAMT root seeds the
   destination tree, so unchanged subtrees are **shared, not rewritten** — a
   copy of 200 daily snapshots of a mostly-static tree must not write 200 full
   trees.
3. Walk the source HAMT from the root. For each `(path, source filemeta ref)`:
   1. If the source filemeta ref is in the remap table, reuse the mapped
      destination ref and continue.
   2. Load the source `FileMeta`; resolve its `content/` ref.
   3. Rebuild the content object (§4.3), obtaining a destination content ref.
   4. Rewrite the `FileMeta` with the destination content ref, leaving every
      other field byte-identical, and write it through the destination stack.
      Record `source ref -> destination ref` in the remap table.
   5. Insert `(path, destination filemeta ref)` into the destination HAMT via
      `hamt.TransactionalStore`.
4. Flush the transactional store, yielding the destination root node ref. Only
   nodes reachable from that root are written, as in backup.
5. Write the destination snapshot object (§4.5).
6. Update the destination `index/snapshots` catalog (§4.6).
7. Reconcile `index/latest` (§4.6).

**The remap table is per-run, not per-snapshot**, and is keyed on source refs at
the filemeta, content and chunk levels. Snapshots in one run share the
overwhelming majority of their graph; a per-snapshot table would re-read every
file that appears in more than one snapshot.

**Snapshots after the first of a lineage are applied as a diff**, not as a full
re-walk. `Tree.Diff` between the previous source root and this one yields just
the added, modified and removed entries, which are applied to the destination
tree the previous snapshot produced. Re-filing every entry instead is correct
but expensive in a way that read volume does not reveal: `Txn.Insert` rewrites a
leaf entry unconditionally, so re-inserting an identical value still dirties the
spine and `Commit` rewrites the tree — one full tree rewrite per snapshot.

A diff is also the only way to carry **deletions** across. A tree reused and
merely inserted over keeps every file deleted since, so the destination's copy
of a snapshot would contain files that snapshot does not. Reuse and diffing are
therefore the same decision: a destination tree may only be reused when the
source tree it was translated from is known.

That pairing is recovered across runs from provenance the destination already
records — a copied snapshot names its source snapshot, and the source catalog
still knows that snapshot's root — so a scheduled copy does not rebuild a whole
tree before it can go incremental.

Measured on a 2000-file tree (`copy_bench_test.go`):

| Change | Effect |
|--------|--------|
| Per-snapshot instead of per-run remap tables | 2.2 MB → 7.1 MB read, 89 ms → 198 ms, at 8 snapshots |
| Full re-walk instead of a source diff | 4.1 MB → 14.8 MB written, 115 ms → 370 ms, at 64 snapshots |
| No cross-run pairing (scheduled catch-up of one snapshot) | 1.6 KB → 707 KB read, 7.6 KB → 217 KB written |

Note that bulk data is protected independently of all this: `copyContent`
checks the destination before reading anything, so chunk data is never re-read
even with no table at all. What these optimisations govern is metadata reads,
destination round trips, and tree rewrites — which is exactly the part that a
benchmark measuring bytes transferred would miss.

#### 4.3 Chunk and content rebuild

Chunk boundaries are **reused, not recomputed**.

The source `Content` object already records the exact boundaries, and FastCDC in
this codebase is parameterised by fixed constants with no key-derived seed
(`cdcMinSize`/`cdcAvgSize`/`cdcMaxSize`, `internal/engine/chunker.go`), so
re-running the chunker over the same plaintext is guaranteed to reproduce the
same split. Running it anyway would burn CDC over every byte of the repository
to arrive at the answer already written down.

So, for each source content ref:

1. Read the source `Content` object. If it carries `DataInlineB64`, the payload
   is inline and no chunk work is needed.
2. Otherwise, for each source chunk ref in order: if it is in the remap table,
   reuse the mapping. Else read the chunk plaintext through the source stack,
   compute the destination ref (`HMAC(destDedupKey, plaintext)`, or
   `SHA256(plaintext)` for an unencrypted destination), `Put` it through the
   destination stack — which is already put-if-missing — and record the mapping.
3. Build the destination `Content` with the remapped chunk list, preserving
   `Size` and the inline payload, and write it. Its ref is
   `HMAC(destDedupKey, SHA256(stream))`, so the source's recorded stream hash is
   reused directly and the plaintext need not be re-hashed end to end.

**Deduplication against pre-existing destination data follows from this**, and
only from this: identical plaintext yields identical boundaries and therefore
identical destination refs, so a chunk already present in the destination is
recognised and skipped. This is the load-bearing assumption of §7 and it is
worth stating as a maintenance constraint: *if the CDC parameters are ever
changed, copy's boundary reuse stops matching freshly-backed-up data in the
destination, and copy must gain a re-chunking mode.* A test should pin the
parameters against this RFC.

#### 4.4 Locking

Copy acquires a **shared** repository lock on the source, then a shared lock on
the destination (`internal/engine/repolock.go`), and holds both for the
duration.

**The source lock is best-effort, and only because taking one is itself a
write.** This RFC lists read-only source credentials as supported, and placing a
lock contradicts that — so a source that refuses the write is copied from
anyway, with the loss of protection logged. Finding a lock *already held* is
different and still fatal: it means a `prune` or `forget` is running and objects
are being collected as the walk proceeds. The distinction is available because
`ErrRepoLocked` is wrapped into "held by another operation" and not into an I/O
failure.

- The source lock stops a concurrent `prune`/`forget` from collecting objects
  out from under an in-flight walk.
- The destination lock stops a concurrent `prune` from collecting objects copy
  has written but not yet made reachable from a snapshot.
- Shared locks do not exclude one another, so two copies in opposite directions
  between the same pair of repositories cannot deadlock on the acquisition
  order.
- The existing lost-lock behavior applies to both: if the refresh goroutine can
  no longer prove ownership of *either* lock, the operation context is
  cancelled and copy aborts rather than continue writing.

#### 4.5 Snapshot metadata: what is preserved and what is not

| Field | Treatment |
|-------|-----------|
| `Created` | preserved verbatim — the point of the feature |
| `Source` | preserved verbatim |
| `Tags` | preserved verbatim |
| `Meta` | preserved, plus reserved provenance keys (§5) |
| `ChangeToken` | preserved (see caveat below) |
| `ExcludeHash` | preserved |
| `Version` | preserved |
| `Root` | **rewritten** — destination HAMT root |
| `Seq` | **reassigned** — see below |

`Seq` cannot be preserved. It is a *global, monotonic write counter* allocated
from `resolveLatest` at backup time (`internal/engine/backup.go`), not a
per-source or per-snapshot identity, so a source `Seq` would collide with
destination history the moment the destination is not empty. Copied snapshots
are allocated fresh `Seq` values, increasing in the §4.1 order.

The consequence must be stated plainly because it is invisible otherwise:
**`Seq` records write order, and copy writes old snapshots late.** After a copy,
the destination's `Seq` ordering no longer agrees with `Created` ordering for
the copied set. `find` version ordering and `forget`'s latest-selection both
sort on `Seq` (`internal/engine/find_collect.go`, `internal/engine/forget.go`),
so a copy that dumps a decade of history into a live repository would otherwise
make the oldest imported snapshot outrank everything already there.

**Caveat on `ChangeToken`.** A change token is a delta cursor for the *data
source* (Google Drive, OneDrive), not for the repository, so preserving it is
correct for a full copy. For a *filtered* copy it is not: the destination's
newest snapshot for that source identity would carry a token whose delta assumes
history that was not copied, and a subsequent `backup` against the destination
would build an incremental on a chain with holes. Copy therefore **clears
`ChangeToken` on the newest copied snapshot per source identity whenever the
selection was filtered** (anything other than "all snapshots for that identity"),
forcing the next backup to do a full walk. Correctness over a saved rescan.

#### 4.6 `index/snapshots` and `index/latest`

The catalog is updated per snapshot, as backup does, so an interrupted run
leaves every completed snapshot listable.

`index/latest` is reconciled once, **after** the run, and this is the one place
copy deliberately departs from the repository's "highest `Seq` wins" rule.
Applying that rule literally would repoint `index/latest` at whichever snapshot
copy happened to write last — which, under §4.1 ordering, is the newest of the
*copied* set, and may still be years older than what the destination already
had.

The rule is therefore: after the run, `index/latest` points at the copied
snapshot with the greatest `Created` **only if** that is newer than the
`Created` of the current latest. Otherwise `index/latest` is left alone. A copy
into an empty repository trivially sets it; a copy of old history into a live
repository leaves the live head in place.

### 5. Snapshot identity, provenance, and idempotency

Repeated copy runs must skip snapshots already copied, without re-reading their
data.

#### 5.1 Repository identity

`core.RepoConfig` has no repository ID today. This RFC adds one:

```go
type RepoConfig struct {
    Version   int    `json:"version"`
    Created   string `json:"created"`
    Encrypted bool   `json:"encrypted"`
    ID        string `json:"id,omitempty"` // 128-bit random, hex
}
```

Written by `init`. **This is not a format-version bump.** The marker is a JSON
object decoded with `json.Unmarshal` and is not content-addressed, so an older
build ignores the field and is unaffected — the two conditions
`docs/compatibility.md` requires for a silent addition. (Note that the marker is
*sealed* with the repository key for an encrypted repository, not plaintext, as
`internal/repoconfig` describes. That is orthogonal: sealing covers whatever
JSON is inside, so the added field is protected automatically, and copy holds
the source key anyway.)

**Repositories created before this** have no `id`, and copy must not write to
the source to give them one (§Non-goals — read-only source credentials are a
supported configuration). For those, the repo-id component of provenance is
recorded as **empty**, and the skip rule matches on the source snapshot ref
alone.

There is deliberately **no derived fallback identity**. The obvious candidate —
hashing the stored marker — is unstable: for an encrypted repository the marker
is resealed with a fresh nonce every time `UpgradeRepoFormat` stamps a version,
so the derived id would change after the first `backup`, `prune` or `forget`
following an upgrade, and copy would re-import the entire history. Hashing the
decoded fields is no better, since `Version` moves on upgrade and `Created` is
reset by `init --adopt`. An unstable provenance key is strictly worse than none:
it fails by silently duplicating history rather than by declining to skip.

Matching on the snapshot ref alone is sound for encrypted repositories: source
snapshot refs are content-addressed under the source master key, so a collision
between two *different* repositories requires a shared master key **and**
identical content **and** identical `Seq` and `Created` — at which point the two
snapshots genuinely are the same snapshot and skipping is correct. For
unencrypted repositories the refs are plain SHA-256, and a same-content,
same-seq, same-second collision between two unrelated legacy repositories is
possible in principle. Copy prints a one-line warning when the source has no
`id`, naming `-allow-ambiguous-provenance` for the case where a user knowingly
copies from two legacy unencrypted repositories into one destination.

#### 5.2 Where provenance lives

Provenance is recorded in the **existing `Snapshot.Meta` map**, under a reserved
namespace:

```json
{
  "meta": {
    "cloudstic.copy.from_repo": "9f2c…",
    "cloudstic.copy.from_snapshot": "snapshot/410b18a2…"
  }
}
```

`Meta` rather than a new `Snapshot` field, for two reasons. First, `Snapshot` is
content-addressed through `ComputeJSONHash`, which marshals struct fields in
declaration order, so adding a field is a repository-format change; `Meta` is an
existing `map[string]string` and `encoding/json` sorts map keys, so it stays
canonical for free. Second, `Snapshot.Extra` — which the original draft's
compatibility section was written against — does not exist.

Because `Meta` is writable by users through `WithMeta(key, value)` on backup,
**the `cloudstic.` prefix is reserved**: `WithMeta` rejects keys under it, and
copy refuses to run if a *source* snapshot carries reserved keys it did not
write (which would mean copying an already-copied snapshot's provenance
onward — see §5.4).

#### 5.3 Making the skip check cheap

Provenance in `Meta` alone is not enough, because the skip scan runs against
`index/snapshots`, and `core.SnapshotSummary` carries no `Meta`. Scanning
provenance would mean fetching every destination snapshot object on every run —
precisely the cost the catalog exists to avoid.

So provenance is **denormalized into the catalog**:

```go
type SnapshotSummary struct {
    // … existing fields …
    CopiedFrom string `json:"copied_from,omitempty"` // "<repo-id>:<snapshot-ref>"
}
```

`omitempty` means non-copied snapshots add zero bytes and existing catalogs are
unchanged. The authoritative copy stays in `Snapshot.Meta`; the summary field is
a cache, and the catalog's existing self-healing reconciliation with
`LIST snapshot/` can rebuild it from the snapshot object.

**What older builds do with this** (required by `docs/compatibility.md`): they
ignore `copied_from` on read. If an older build *rebuilds* the catalog, it drops
the field, and a later copy run would re-copy those snapshots — duplicated
history, not corruption. To bound that, the skip check falls back to loading the
snapshot object and reading `Meta` **only** for summaries that lack
`copied_from` but match a candidate on `Created` and `Source`. That keeps the
common case at zero extra fetches and makes the degraded case correct rather
than merely cheap.

No format-version bump: an added `omitempty` field on a rebuildable,
non-content-addressed catalog is readable by every earlier build, and the
degradation above is safe.

#### 5.4 Skip rule

Before copying a source snapshot, look for a destination snapshot whose
provenance matches the source snapshot. If found, print a skip line and do not
re-read the source snapshot's data.

**Matching is on the snapshot ref, with the repo id as a disqualifier rather
than as half of a compound key.** Two provenances match when their snapshot refs
are equal *and* their repo ids do not conflict — where two non-empty, unequal
ids conflict, and an empty id on either side means "unknown" and does not.

The asymmetry is not a convenience. An older build that rewrites the repository
marker **drops `RepoConfig.ID`**, because it decodes the marker into its own
three-field struct and re-encodes it. This is verified, not inferred: a v1.18.0
`backup` against a format-1 repository upgrades it to format 2 and writes back
`{"version":2,"created":…,"encrypted":false}`, with the id gone. A strict
`(id, ref)` key would then treat every already-copied snapshot as unseen and
re-import the entire history. Treating an unknown id as unknown reduces that to
what it actually is — missing information — and the residual collision risk is
exactly the one §5.1 already accepts for legacy sources.

Copy is **not transitive**: copying an already-copied snapshot onward would
require deciding whether provenance names the original or the intermediate
repository. v1 records the *immediate* source and refuses to run when a source
snapshot carries reserved `cloudstic.copy.*` keys, with an error pointing at
`-allow-copied`, which opts into re-stamping provenance to the immediate source.

### 6. Repository format gating

Both repositories are opened through `LoadRepoConfig` (`repo.go`) and gated
against `core.MaxSupportedRepoFormat`. A source above the gate is **refused**,
not partially read.

This is the "never treat cannot-decode as empty" rule applied to a new position.
A copy that read an unreadable source as "no snapshots" would exit 0 having
migrated nothing, and an operator who then decommissioned the source would have
destroyed the only copy. The refusal must be loud and must name the source
repository.

Copy never calls `UpgradeRepoFormat` on the source — it performs no writes
there. The destination is stamped through the ordinary post-write path once the
run completes successfully.

### 7. Interaction with destination retention

**This is the sharpest operational edge in the feature and v1 must address it,
not defer it.**

With "copy everything" as the default and provenance-based skipping, a
*scheduled* copy — which RFC 0013's daemon makes the obvious deployment —
resurrects snapshots the destination deliberately forgot. The skip rule only
matches snapshots that still exist; once destination retention deletes a copied
snapshot, the next copy run faithfully re-imports it, forever.

v1 mitigations, in order of preference:

1. **`-since <timestamp>`** (§3), the explicit and composable answer: a
   scheduled copy passes the last run's start time.
2. **A high-water mark.** Copy records, per source repo id, the greatest
   source-snapshot `Created` it has successfully copied, in a destination-local
   `index/copyhigh/<repo-id>` object, and by default does not copy snapshots at
   or below it. `-ignore-high-water` overrides. This makes the scheduled case
   correct with no flags, at the cost of one small mutable object per source
   repository.
3. Documenting the interaction and leaving it to the operator.

**Recommendation: implement (1) and (2).** (3) alone is how a user ends up with
a destination whose retention policy visibly does nothing.

Note that the high-water mark is an optimisation layered *over* provenance
skipping, not a replacement: provenance handles out-of-order and backfill
copies, the water mark handles the steady state.

### 8. Resume and interruption behavior

v1 is restart-safe at snapshot granularity.

- Objects already written to the destination remain; they are content-addressed
  and put-if-missing, so a rerun re-derives the same refs and skips them.
- A completed snapshot has provenance recorded and is skipped on rerun.
- A partially copied snapshot has no destination snapshot object, so it is
  retried from scratch — but its already-written chunks, contents, filemetas and
  nodes are all still present and are recognised, so the retry re-reads the
  source but re-writes almost nothing.
- Orphaned objects from an interrupted run are unreachable and are collected by
  the destination's normal `prune`.

Fine-grained resumable progress within one snapshot is explicitly out of scope.

### 9. Client API

Copy is exposed as a method on the **destination** client, taking the **source**
client:

```go
func (c *Client) CopyFrom(ctx context.Context, src *Client, opts ...CopyOption) (*CopyResult, error)
```

`CopyFrom` rather than `Copy` because `client.Copy(ctx, other)` does not say
which direction data flows; `CopyFrom` does.

Taking a `*Client` rather than a bare `store.ObjectStore` is the substantive
change from the original draft, and it resolves several problems at once:

- **The decorator chain is never composed by a caller.** RFC 0022 made
  `internal/storelayer` internal precisely because its order is a silent
  correctness and security invariant — a chain assembled without
  `WithPackIndexKey` yields a plaintext pack index with no error at any layer. A
  source `*Client` has already had its chain layered by `NewClient`.
- **Credentials stay out of the copy option surface.** No `WithFromKeychain`,
  no second keychain vocabulary; the source client was constructed with
  `WithKeychain` like any other.
- **The source is format-gated for free** (§6), because `NewClient` runs
  `LoadRepoConfig`.

Constructing the two clients is one call each through `pkg/open`, which RFC 0022
established as the place a configuration becomes a live client:

```go
dst, err := open.Client(ctx, dstCfg)   // or open.FromProfile(ctx, path, "remote-prod")
if err != nil {
    return err
}
src, err := open.FromProfile(ctx, path, "laptop-local")
if err != nil {
    return err
}

res, err := dst.CopyFrom(ctx, src,
    cloudstic.WithCopySnapshotIDs("410b18a2", "latest"),
    cloudstic.WithCopyFilterTag("workstation"),
    cloudstic.WithCopyDryRun(),
)
```

Option names follow the established convention in `internal/engine` —
`With<Verb>DryRun` and `WithFilter*` — disambiguated by verb because the
existing `WithFilterTag` is a `ForgetOption`:

- `WithCopySnapshotIDs(...)`
- `WithCopyFilterSource(...)`, `WithCopyFilterAccount(...)`, `WithCopyFilterTag(...)`
- `WithCopySince(time.Time)`
- `WithCopyDryRun()`
- `WithCopyAllowCopied()`, `WithCopyIgnoreHighWater()`

Per RFC 0022, every exported `With*` in `internal/engine` must be mirrored at
the root package (`TestEveryEngineOptionIsReExported`), and `CopyResult` must be
re-exported as a type alias (`TestPublicAPIHasNoUnaliasedInternalTypes`).

`CopyFrom` returns a `*CopyResult`, not a bare `error`: every other `Client`
operation returns a result value, and §10's counts have to live somewhere.

```go
type CopyResult struct {
    Copied  []CopiedSnapshot // source ref, destination ref, created, source info
    Skipped []SkippedSnapshot // source ref, destination ref, reason
    BytesRead    int64
    BytesWritten int64
    DryRun  bool
}
```

### 10. Progress and output

Progress is reported at snapshot granularity, with byte progress within a
snapshot where the reporter supports it.

Written bytes come free from the destination's `MeteredStore`. **Read bytes do
not** — `MeteredStore` tracks `bytesWritten` only
(`internal/storelayer/metered.go`), so copy either counts plaintext through its
own walk or `MeteredStore` gains a read counter. Counting in the copy engine is
preferable: it measures the plaintext copy actually materialised rather than
whatever the layer below happened to fetch, which is the number an operator
comparing against their egress bill wants.

Before the first write, copy prints both repository identities — the security
section's blast-radius concern is answered by making the destination impossible
to miss:

```text
copying from local:/tmp/cloudstic-src (repo 9f2c1a…)
            to s3:dest-bucket/prod    (repo 41ba07…)
3 snapshots selected, 1 already copied

snapshot 410b18a2 of [local:./Documents] at 2026-04-01 20:15:03 +0200
  copy started, this may take a while...
snapshot a1b2c3d4 saved

snapshot 4e5d5487 of [local:./Documents] at 2026-03-30 18:22:11 +0200
skipping snapshot 4e5d5487, already copied as snapshot e5f6a7b8

copied 2 snapshots, skipped 1 (read 4.2 GiB, wrote 1.1 GiB)
```

`-json` emits the `CopyResult` on stdout, as other commands do.

`-dry-run` is **in v1**, not deferred. The original draft left it as an open
question while simultaneously arguing in its security section that copy "can
write substantial history into the wrong destination repository". Those two
positions are incompatible, and every comparable operation already has one
(`WithBackupDryRun`, `WithForgetDryRun`, `WithPruneDryRun`, `WithRestoreDryRun`).
A dry run resolves selection, performs the provenance skip check, and reports
what would be copied without writing.

### 11. Guards

- **Same repository.** Copy refuses. This matters more than it sounds: a
  self-copy is not a no-op, because each source snapshot is rewritten as a *new*
  snapshot object carrying provenance. No data is duplicated — every object
  re-addresses to the ref it already has — but the history doubles, and
  retention grouping, `latest` and every count are wrong afterwards.

  Repository ids settle it when both sides have one. When either does not, the
  two stores are **probed**: a uniquely named object is written to the
  destination and looked for through the source. Cheaper tests were tried and
  are not sufficient — comparing store URIs misses `local:/srv/repo` against
  `local:/srv/repo/.`, a symlink, a bind mount, or one bucket reachable under
  two endpoints; comparing the stored markers collides in practice, because
  once the id is stripped two repositories created in the same second are
  byte-identical, which refuses legitimate copies between distinct id-less
  repositories. The probe writes only to the destination and removes what it
  wrote, so read-only source credentials remain sufficient.

  The CLI additionally compares the two resolved store URIs, which catches the
  common slip before either repository is opened and so before any credential
  prompt. That check is an early convenience, not the guarantee.
- **Uninitialized destination.** Refuse, pointing at `cloudstic init`.
- **Empty selection.** Exit 0 with an explicit "no snapshots selected" line, not
  silently.

## Compatibility

The feature is additive. Existing repositories remain valid and existing
commands are unchanged. Copied snapshots are ordinary destination snapshots
afterwards — listable, diffable, restorable, and subject to retention.

Three on-disk additions, none of which bump `core.RepoFormatVersion`:

| Addition | Older builds | Why no bump |
|----------|--------------|-------------|
| `RepoConfig.ID` | ignore it on read; **drop it** when they rewrite the marker | marker is JSON, not content-addressed; sealing covers it automatically |
| `SnapshotSummary.CopiedFrom` | ignore the field; a rebuild drops it | `index/snapshots` is a rebuildable cache; §5.3 specifies the safe degradation |
| `Snapshot.Meta["cloudstic.copy.*"]` | ignore the keys | `Meta` already exists; map keys are sorted by `encoding/json`, so the hash stays canonical |

Per `docs/compatibility.md`, the claim that older builds cope must be **verified
by running an old binary**, not reasoned about, and a fixture repository
containing copied snapshots should be committed to `e2e/testdata` and listed in
the compatibility doc's table.

Results of that verification against `v1.18.0`, the last release predating these
fields:

- `list`, `ls`, `check` and `backup` all operate normally on a repository whose
  marker carries an `id`. A write that does not rewrite the marker leaves the
  id intact.
- A write that **does** rewrite the marker — a format-1 repository upgraded to
  format 2 — silently drops the `id`. This is why §5.4 matches on the snapshot
  ref and treats an unknown id as unknown rather than as a distinct value. With
  a strict compound key the same scenario would re-import an entire history.
- The catalog field degrades the same way and is recovered on the next rebuild
  (§5.3).

None of these is a misread, so the version gate stays where it is. The
`internal/core` forward-compatibility tests pin the wire shapes so that a future
edit has to face this question deliberately.

## Security considerations

- Copy requires the ability to unlock both repositories.
- Source and destination credentials are strictly separated in parsing: they
  bind to different structs, and the `-from-*` mirrors carry no environment
  bindings (§2), so no ambient variable can silently unlock both.
- Every `-from-*` credential flag inherits `asSecret()` from the spec it
  mirrors, so `TestSecretEnvValuesNeverAppearInHelp` covers them and no live
  value reaches `-h`.
- Error messages must name *which* repository failed without echoing
  credentials.
- Copy holds plaintext for both repositories in one process. That is inherent —
  §4 requires materialising plaintext to re-address it — and is the same
  exposure as `restore`, but it is worth stating that a copy is not a
  zero-knowledge operation and cannot be delegated to a party trusted with
  neither key.
- Blast radius is addressed by §10's pre-write identity banner and §11's
  same-repository and uninitialized-destination guards.

## Testing strategy

Unit:

- `-from-*` flag generation: `TestCopyMirrorsEveryRepositoryFlag` asserts every
  spec in `repoCommandGroups` has a `from-` mirror, and that no mirror carries
  an environment binding.
- Provenance matching and skip behavior, including the §5.3 fallback path when
  `copied_from` is absent from a summary.
- `Seq` reassignment and the §4.6 `index/latest` rule, including the case where
  the destination's existing head is newer than everything copied.
- Selection: explicit IDs, filters, `-since`, `latest`, empty selection.
- Guards (§11).

Golden:

- `copy -h`, and the §10 progress output.

Hermetic e2e:

- local → local, local → MinIO, MinIO → local, SFTP → local.
- unencrypted → encrypted, encrypted → unencrypted, and encrypted → encrypted
  with **different** master keys — this is the case that proves the ref
  cascade is handled rather than accidentally passed through.
- copy all; filtered by source/tag/account; explicit selection.
- rerun idempotency: a second identical run copies nothing and issues no `Get`
  against source chunk data (assert on a counting store wrapper, not just on
  output text).
- interrupted copy followed by rerun: the retry writes substantially fewer
  bytes than the original — measurable directly on the destination
  `MeteredStore` — proving §8's object reuse.
- copy of N snapshots of a mostly-static tree writes O(tree), not O(tree × N) —
  the §4.2 remap-table property, which is the one performance claim that will
  silently regress.
- destination `check` (including `-read-data`) passes after copy. This is a
  stronger assertion than "listable, diffable, restorable" for an operation
  that rewrites the entire graph, and it is the one the original draft omitted.
- `backup` against the destination after a *filtered* copy does a full walk
  (§4.5 `ChangeToken` clearing).
- copy from a committed legacy-format fixture repository.

## Rollout plan

1. Add `RepoConfig.ID` and `SnapshotSummary.CopiedFrom`, with the
   compatibility fixtures and the old-binary verification.
2. Add the `-from-*` flag mirroring mechanism and source configuration
   resolution through `pkg/config` / `pkg/open`.
3. Implement snapshot selection, provenance skip, and the high-water mark.
4. Implement the bottom-up graph rebuild (§4), remap table, and locking.
5. Add `CopyFrom` to the client, mirror the options, add the type aliases.
6. Add hermetic e2e migration tests.
7. Documentation: `docs/user-guide.md` command reference; `docs/storage-model.md`
   note on cross-repository re-addressing; `docs/compatibility.md` fixture table
   row; regenerate `testdata/usage_root.golden` and `testdata/help_copy.golden`;
   confirm the completion generators picked up the mirrored flags
   (`TestCompletionCoversEveryCommandFlag`). Pair with a `Cloudstic/doc` update
   so `internal/apicheck/docs_test.go` stays green on the new public API.

## Open questions

- **Naming.** `copy` remains the recommendation: it matches restic's
  `copy --from-repo`, which is the tool most users will be migrating from or
  comparing against, and `migrate` wrongly implies the source is decommissioned
  while `replicate` implies an ongoing relationship the feature does not
  maintain.
- **High-water mark storage.** §7 proposes `index/copyhigh/<repo-id>`. Should
  this instead live in the destination's `config`, or be derived at read time
  as `max(Created)` over destination snapshots carrying provenance from that
  repo id? The derived form needs no new object and no new format surface, at
  the cost of a full catalog scan — which §5.3 already performs. **Deriving it
  is probably right**; the object is listed as the proposal only because it
  survives destination retention deleting every copied snapshot.
- **Should copy ever be transitive?** §5.4 refuses by default. If A → B → C
  becomes a real topology, provenance needs to become a chain rather than a
  pair, which is a format decision better made with a concrete use case.
- **Parallelism.** Copy is embarrassingly parallel at the file level and
  bottlenecked on two networks at once. v1 can ship serial, but the
  `ConcurrencyHinter` capability on both stores is the obvious input and the
  option should be reserved now.

## Revision notes (2026-08-03)

The original draft predates RFC 0018 and RFC 0022 and described flags, types and
an API surface that do not exist. Corrections, and the design changes that
followed from them:

- **`-store-ref` does not exist.** The repository is located with `-store` or
  `-profile`. Every example is rebased, and §2 replaces the hand-written
  25-flag mirror list with generation from the existing flag groups — the
  original list already omitted `-b2-key-id`, `-b2-app-key` and
  `-disable-packfile`. The `-from-*-secret` flags are dropped: they had no
  global destination counterpart to mirror.
- **`Snapshot.Extra` does not exist.** Provenance moves to the existing
  `Snapshot.Meta` map, which also avoids a content-addressing format change
  (§5.2), and the `cloudstic.` key prefix becomes reserved because `Meta` is
  user-writable through `WithMeta`.
- **The skip rule could not use the catalog it named.** `SnapshotSummary`
  carries no `Meta`, so the original §5 implied fetching every destination
  snapshot object per run. §5.3 denormalizes provenance into the summary and
  specifies the degraded path.
- **There is no repository ID.** Promoted from an open question to a specified
  addition (§5.1). Legacy repositories get no derived fallback identity: the
  marker is resealed with a fresh nonce on every format stamp, so any hash of it
  is unstable, and an unstable provenance key duplicates history rather than
  failing to skip. Provenance degrades to matching the source snapshot ref
  alone, which §5.1 shows is sound for encrypted repositories.
- **§4 described a transfer, not a rebuild.** Object refs cascade off the master
  key at every level, so nothing can be copied verbatim. §4 now specifies the
  bottom-up rebuild, the per-run remap table (without which cost is
  O(tree × snapshots)), and boundary reuse instead of re-chunking.
- **`Seq` and `index/latest` were unaddressed.** `Seq` is a global write
  counter, and `index/latest` resolves by highest `Seq` — so the original step 7
  would have repointed the destination head at the oldest imported snapshot.
  §4.5 and §4.6 specify reassignment, ordering, and the one deliberate departure
  from the `Seq` rule.
- **Locking and source format gating were absent** (§4.4, §6).
- **§9 takes a `*Client`, not a `store.ObjectStore`.** A bare backend would
  force callers to compose `internal/storelayer` themselves, which RFC 0022 made
  internal precisely because getting the order wrong fails silently. This also
  removes `WithFromKeychain` and gives source format gating for free. `Copy`
  becomes `CopyFrom` and returns a `*CopyResult` like every other operation, and
  option names follow the existing `WithFilter*` / `With<Verb>DryRun`
  convention.
- **`-dry-run` moves into v1**, since the security section already argued for
  it, and destination-retention conflict (§7) is promoted from an open question
  to a specified mitigation — it is a correctness trap for scheduled copies, not
  a nicety.
- **`ChangeToken` handling on filtered copies** (§4.5) and the transitive-copy
  refusal (§5.4) are new; both were unstated gaps rather than errors.
