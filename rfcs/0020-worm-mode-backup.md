# RFC 0020: WORM Mode Backup

- **Status:** Draft
- **Date:** 2026-07-27
- **Affects:** `pkg/store`, `internal/engine`, `internal/core`, `cmd/cloudstic`, docs

## Abstract

This RFC proposes a repository mode in which Cloudstic never overwrites and
never deletes, so that a repository can live on WORM (write-once, read-many)
storage: an S3 bucket with Object Lock, a B2 bucket with file lock, an
immutable-mounted filesystem, or a WORM appliance.

The motivation is ransomware. Every protection Cloudstic has today assumes the
attacker cannot reach the credentials. Once they can, `prune` and `forget` are
sufficient to destroy every snapshot, and they are ordinary, documented
commands. WORM storage removes that capability from the credential rather than
from the attacker — but only if the backup tool can actually operate without
mutating anything.

Cloudstic cannot, today. Twelve object keys are written more than once, and the
worst of them is not an index: it is the repository lock, which overwrites
itself every 30 seconds for the entire duration of every backup.

The central design claim is that **almost none of this state needs to be
mutable**. `index/latest` is derivable and can be dropped outright.
`index/snapshots` is already documented as a rebuildable cache and can become
append-only shards, exactly as `index/packs` did in RFC 0018. The lock's only
job is to prevent concurrent mutation, which is precisely what WORM already
prevents — so under WORM it becomes advisory and create-only. What genuinely
cannot be made append-only — credential revocation, `prune`, `forget`, repack —
must be refused loudly rather than allowed to appear to succeed.

## Context

### What WORM storage actually does

The design hinges on a detail that is easy to get wrong, so it comes first.
"Immutable storage" is not one behaviour. There are two regimes, and they fail
in opposite directions.

**Regime 1 — reject-on-mutate.** An immutable-mounted filesystem, a WORM
appliance, tape, or an S3 bucket whose policy denies `s3:PutObject` on an
existing key. Here `Put` to an existing key fails with a permission error, and
`Delete` fails. Mutation is loud.

**Regime 2 — version-preserving.** S3 Object Lock and B2 file lock. Object Lock
*requires* bucket versioning, and it protects object *versions*, not keys. This
has consequences that invert the naive expectation:

- `PUT` to an existing key **succeeds**. It creates a new version. The locked
  version is untouched and still there.
- `DELETE` without a version id **succeeds**. It writes a delete marker. The
  bytes remain, and remain billed.
- Only `DELETE` with an explicit version id, against a version still inside its
  retention window, fails.

So on the storage most users will actually reach for, today's Cloudstic does not
error. It runs, reports success, and quietly does three harmful things: it
accumulates a retained version for every overwrite (a one-hour backup writes 120
lock versions, each billed until retention expires); `prune` reports reclaimed
bytes that were never reclaimed; and a `Delete` of `index/latest` writes a
delete marker that makes the pointer read as absent while the data is still
present.

That last one matters more than it looks. Regime 2 means an attacker with
credentials **can still make a repository unusable** — delete markers hide
current versions — even though they cannot destroy the bytes. Recovery is
possible but requires reaching past Cloudstic to the versioning API. A design
that only avoids hard errors is therefore not enough; the goal has to be that
Cloudstic issues no mutating request at all.

### Every key that is written more than once

| Key | Written by | Mutation | Class |
|-----|-----------|----------|-------|
| `chunk/<hash>`, `content/<hash>`, `filemeta/<hash>`, `node/<hash>`, `snapshot/<hash>` | `backup` | none — content-addressed | A |
| `packs/<hash>` | `PackStore` flush | none, but **deleted** by repack | A/E |
| `index/packmap/<hash>` | `PackStore` flush | none, but **deleted** by `CompactCatalog` | A/E |
| `index/latest` | every `backup`, `forget` | overwritten every backup; **deleted** when the last snapshot goes | C |
| `index/snapshots` | `backup`, `forget`, read-path self-heal | full read-modify-write overwrite | B |
| `index/packs` | pre-shard builds | full overwrite | B |
| `index/lock.exclusive` | `prune`, `forget`, `check` | created, **overwritten every 30 s**, deleted | D |
| `index/lock.shared/<nonce>` | `backup`, `restore` | created, **overwritten every 30 s**, deleted | D |
| `keys/password-default` | `key passwd` | overwritten in place | E |
| `keys/<slot>` | `key add-recovery` | new key, but slot removal is a delete | E |
| `config` | `init`, `UpgradeRepoFormat` | overwritten on format upgrade | F |

`KeyCacheStore`'s bbolt database is a local temporary file, not a repository
object, and is unaffected throughout.

### The indexes are the smaller half of the problem

The instinct that indexes and caches are the blocker is right about *where* the
mutation is, but the volume and the severity both sit elsewhere.

By request count, the lock dominates. `lockTTL` is one minute and `refreshRate`
is 30 seconds (`internal/engine/repolock.go:23-24`), and `refreshLoop` rewrites
the same key on every tick for as long as the operation runs. A nightly backup
that takes 40 minutes writes 80 lock versions and two index versions. Under
Regime 1 it does not get that far: `AcquireSharedLock` fails on the very first
backup into a fresh WORM repository, and nothing works at all.

By severity, the indexes are the *easiest* class, because RFC 0018 already did
the hard thinking. Its own table states the recovery path for each one:

| Index | Recovery |
|-------|----------|
| `index/snapshots` | `LIST snapshot/` and rebuild — already self-heals on load |
| `index/latest` | read snapshots, take the highest `seq` |
| `index/packs` | none — which is why it got footers and shards |

The one index with no recovery path is already solved. The two that remain are
both *derived*, which means WORM mode does not need to version them. It needs to
stop writing them.

The genuinely hard problems this RFC has to confront are not indexes at all:

1. **Credential revocation is impossible.** `ChangePasswordSlot` overwrites
   `keys/password-default` (`pkg/keychain/keychain.go:69-82`). Under WORM the old
   slot survives, so the old password still opens the repository until retention
   expires. This is not a bug to fix; it is a property of immutable storage.
2. **`prune` and `forget` cannot work.** Under Regime 1 they error partway
   through, having already written a new snapshot catalog. Under Regime 2 they
   report success and reclaim nothing.
3. **The format version stamp is a write.** `UpgradeRepoFormat` overwrites
   `config`, and the compatibility contract leans on that stamp to tell other
   machines in a fleet to upgrade.

### Prior art in this repository

RFC 0018 converted `index/packs` from one mutable object into content-addressed,
append-only shards under `index/packmap/`, merged on read, with a separate
`CompactCatalog` step that is the only operation permitted to remove index
material. That is the shape every remaining mutable index should take, and this
RFC generalises it rather than inventing a second mechanism.

## Goals

- A repository can be created, backed up to, listed, verified, and fully
  restored while Cloudstic issues **zero** overwriting or deleting requests.
- WORM discipline is a property of the **repository**, not of a client
  invocation, so a second client cannot mutate a repository the first one is
  treating as immutable.
- The discipline is **enforced at the store layer**, so a future code path
  cannot quietly reintroduce a mutation.
- Operations that cannot be made append-only fail with an actionable message
  *before* doing partial work, and never report success for work they did not do.
- Cost is bounded and predictable: object count grows with data written, not with
  operation duration.
- Every property above is verified against a real Object Lock bucket in e2e,
  not reasoned about.

## Non-goals

- **Automatically configuring the bucket.** Enabling versioning, setting an
  Object Lock retention mode, and writing bucket policies stay with the operator
  and their IaC. Cloudstic verifies and reports; it does not provision.
- **Making `prune` work under WORM.** Reclaiming space inside a retention window
  is definitionally impossible. Lifecycle expiry after retention is the
  operator's tool, not ours.
- **Deleting a WORM repository.** That is a bucket-lifecycle operation.
- **Compliance certification.** This RFC does not claim SEC 17a-4 or similar
  conformance; it makes the client-side behaviour a precondition for it.
- **Changing default (non-WORM) behaviour.** Nothing in this RFC alters what a
  normal repository writes, beyond one shared refactor noted below.

## Proposal

### 1. WORM is recorded in the repository, not passed as a flag

`cloudstic init -worm` records the mode in the `config` marker:

```json
{
  "version": 3,
  "created": "2026-07-27T10:00:00Z",
  "encrypted": true,
  "worm": {
    "mode": "strict",
    "declared_at": "2026-07-27T10:00:00Z",
    "retention_days": 30
  }
}
```

A per-invocation flag would be worse than useless: one client without it would
mutate the repository, and the operator would discover this only when the
retained-version bill arrived or a `prune` silently did nothing. Because the
marker is read by `LoadRepoConfig` on every open, every client learns the
discipline from the repository itself.

`retention_days` is informational — it is what the operator told us the bucket
enforces — and is used only to explain to the user when space could next be
reclaimed. Cloudstic never treats it as permission to delete.

A WORM repository cannot be converted back in place, because the objects an
immutable repository has already accumulated cannot be cleaned up. Converting
means a new repository and a snapshot copy (RFC 0017).

At `init`, Cloudstic makes a **best-effort** check that the backend actually
enforces what was declared — `GetObjectLockConfiguration` on S3, the equivalent
on B2 — and warns on mismatch. It warns rather than refuses: the permission to
read a bucket's lock configuration is not one every backup credential should
need, and a least-privileged credential failing the check is not evidence the
bucket is unlocked.

### 2. `WORMStore`: the invariant is enforced, not documented

A new decorator sits at the **top** of the chain when the repository declares
WORM:

```text
WORMStore → CompressedStore → EncryptedStore → MeteredStore → [PackStore] → KeyCacheStore → backend
```

It rejects, with a typed `store.ErrWORMViolation`:

- every `Delete`, unconditionally;
- every `Put` to a key that is not content-addressed, when that key already
  exists.

Content-addressed prefixes (`chunk/`, `content/`, `filemeta/`, `node/`,
`snapshot/`, `packs/`, `index/packmap/`) are exempt from the second rule because
rewriting one writes byte-identical content. That is still a wasted retained
version, so `WORMStore` skips such a `Put` when the key already exists rather
than forwarding it.

The top of the chain is where policy belongs, because that is where logical keys
are still visible. But `PackStore` sits *below* it and writes packs, shards, and
footers on its own behalf, so a top-level guard alone would not cover them.
`PackStore` therefore also takes an append-only mode (§9), and a second
`WORMStore` is installed directly above the backend **in tests only**, as an
assertion that no layer smuggles a mutation past the first guard.

Placing the guard at the top has one more consequence worth stating plainly: it
makes the failure mode identical on Regime 1 and Regime 2 storage. Cloudstic
refuses the request itself, so S3's version-preserving leniency never gets a
chance to turn a bug into a silent cost.

### 3. Class A — content-addressed objects are already correct

No change. Chunks, contents, filemetas, HAMT nodes, snapshots, and packfiles are
named by their content and written once. This is the great majority of a
repository by both bytes and object count, and it is the reason WORM mode is
tractable at all.

### 4. Class C — `index/latest` is dropped, not versioned

`index/latest` has exactly three consumers, and all three have a derivation
already available:

- `loadLatestSeq` (`internal/engine/backup.go:325`) wants the global sequence
  counter. It is `max(seq) + 1` over the snapshot catalog.
- `check`, `diff`, `ls`, and `restore` want a default snapshot when the user
  names none. That is the newest entry in the catalog.
- `findPreviousSnapshot` (`internal/engine/backup.go:338`) — which is the one
  that actually matters for incremental correctness — **already** goes through
  `LoadSnapshotCatalog` and filters by source identity. It never reads
  `index/latest` at all.

So the pointer is pure redundancy, and WORM mode simply does not write it.
`resolveLatest` gains a fallback that derives the answer from the catalog when
the key is absent, which is behaviour every build needs anyway for a repository
whose pointer was lost.

This is the single largest simplification in the RFC, and it is worth noting it
is not a WORM-specific hack. `index/latest` is the one key whose `Delete` path
(`updateLatest` with an empty ref, `internal/engine/snapshots.go:309-317`) can
make a repository with live snapshots look empty to a reader that trusts it.
Deriving it is better everywhere.

### 5. Class B — `index/snapshots` becomes append-only shards

The catalog moves from one read-modify-write object to content-addressed shards,
mirroring `index/packmap/` precisely:

```text
index/snapcat/<hash-of-plaintext>
```

Each shard holds the `core.SnapshotSummary` values one operation learned. A
reader lists the prefix, merges every shard plus the legacy `index/snapshots`
object if the repository still has one, and keys by snapshot ref. The merge is
order-independent: a ref appearing in two shards names the same immutable
snapshot object, so either copy is correct.

Shards cannot express a removal, which is the same limitation `index/packmap/`
has — and it is not a problem here, because the reconciliation step in
`LoadSnapshotCatalog` already drops entries with no matching `snapshot/` object.
The catalog stays honest by intersecting with `LIST snapshot/`, exactly as it
does today.

Two properties of the existing code carry over unchanged and must not be lost:

- A read failure that is not `ErrNotFound` must not be treated as an empty
  catalog. `loadCatalogForUpdate` is careful about this
  (`internal/engine/snapshots.go:142-160`) and the sharded reader must be too.
  Under WORM the stakes are lower — nothing gets overwritten — but a fabricated
  empty catalog would still make `backup` do a full rescan and lose the sequence
  counter.
- Shards must be sealed with the same HKDF-derived index key that `index/packmap/`
  uses, for the same reason: they sit outside `EncryptedStore`'s reach in the
  layering, and snapshot summaries carry source paths and hostnames.

Non-WORM repositories get the sharded catalog too, plus `CompactCatalog`-style
consolidation under the exclusive lock. Maintaining two catalog implementations
to keep a race in the default path would be the wrong trade.

### 6. Class D — locking becomes advisory and create-only

This is where the RFC makes its most aggressive claim, so the reasoning is
explicit.

The repository lock exists to stop two concurrent operations from corrupting
shared mutable state: two backups racing on the pack catalog, or a `prune`
sweeping objects a `backup` is still attaching. Under WORM:

- there is no shared mutable state left — every index is append-only and merges;
- `prune`, `forget`, and repack are refused (§8, §9), so the destructive half of
  every dangerous pair does not exist.

Two concurrent backups of the same source under WORM produce two valid snapshots
whose catalog shards merge. That is a fork, not corruption — the same outcome
RFC 0018's shards already accepted for the pack catalog. **The corruption the
lock prevents is not reachable in WORM mode.**

So WORM mode keeps locks for *visibility* and drops them for *correctness*:

- One lease object per operation, at `index/lease/<nonce>`, written once.
- No refresh loop. The TTL is the operation's declared maximum rather than one
  minute, because a stale lease is now merely a misleading `list -locks` entry,
  not a blocked repository.
- Release writes `index/lease/<nonce>.done` — a tombstone — instead of deleting.
  Readers treat a lease with a matching tombstone as finished.
- `break-lock` writes tombstones rather than deleting leases, and says so.
- Acquisition never blocks a `backup`. A live lease is reported, not enforced.

The object-count arithmetic is the point: a 40-minute backup goes from 80 lock
writes to two, and neither is an overwrite. Lease and tombstone objects are tiny
and land in packfiles, so the marginal cost is close to zero.

`internal/engine/repolock.go` keeps its current behaviour for non-WORM
repositories unchanged; the lease path is a second implementation behind the same
`AcquireSharedLock` / `AcquireRepoLock` signatures, selected by the repository's
declared mode.

### 7. Class E — key slots, and a limitation that must be stated, not hidden

`key add-recovery` is already append-only: it writes a new `keys/<label>` object.
It works under WORM unchanged.

`key passwd` does not, and cannot. `ChangePasswordSlot` overwrites
`keys/password-default`. Making it write `keys/password-default.<generation>`
and having unlock try slots newest-first is straightforward — but it does not
achieve what the user asked for. **The old slot survives, so the old password
still unlocks the repository until the retention window expires.**

This is inherent to immutable storage and no amount of client-side cleverness
removes it. The honest response is to refuse by default:

```text
$ cloudstic key passwd
error: cannot rotate the password on a WORM repository

  The existing password slot cannot be removed from immutable storage. Adding a
  new slot would leave the old password working until retention expires
  (approximately 2026-08-26, 30 days from the slot's creation).

  If the old password is compromised, rotating it here does not revoke it.
  Initialise a new repository and copy your snapshots into it:

      cloudstic init -worm -store <new-store>
      cloudstic copy -store <old> -to <new>

  To add a second password anyway, understanding it does not revoke the first:

      cloudstic key passwd -add-slot
```

`-add-slot` writes a new generation and reports the date the old slot stops
working. It is spelled as an addition because that is what it is.

Slot *removal* (`keys/<label>` deletion) is refused outright under WORM.

### 8. Class F — the format version stamp

`UpgradeRepoFormat` overwrites `config`, and per the compatibility contract that
stamp is how a repository tells other machines in a fleet to upgrade.

Under WORM, `config` is written once at `init` and never again. The consequences
are acceptable because `init` stamps `core.RepoFormatVersion`, so a WORM
repository is born at the newest format its creating build knew. There is no
mixed-era WORM repository to migrate *forward* into, because forward migration in
this codebase is opportunistic rewriting — and WORM forbids rewriting.

Therefore:

- `raiseRepoFormat` is a no-op on a WORM repository, and says so in debug output
  rather than failing.
- Any write path gated on a recorded format version (`FramedCompressionFormat`
  is the existing example) reads the `init`-stamped version, which is already
  correct.
- A build newer than the repository's recorded format writes in the repository's
  format, never above it. This is already the rule; WORM just makes it
  permanent.

The cost is that a WORM repository never gains a newer on-disk format. Given
that its whole purpose is to be an unchanging archive, that is the right
outcome, and it should be documented as such rather than treated as a
deficiency.

### 9. Operations that are refused

| Operation | Under WORM | Why |
|-----------|-----------|-----|
| `prune` | refused | Deletes unreachable objects. Impossible inside retention. |
| `forget` | refused | Deletes `snapshot/` objects and rewrites the catalog. |
| `key passwd` | refused unless `-add-slot` | Cannot revoke; see §7. |
| `PackStore` repack | disabled | Deletes superseded packfiles. |
| `CompactCatalog` | disabled | The one operation designed to remove index material. |

Refusal happens at the **command** layer with an explanation, and is *also*
guaranteed at the **store** layer by `WORMStore`. The double enforcement is
deliberate: the command-layer check gives a good message, and the store-layer
check means a code path someone adds in 2027 without reading this RFC fails
closed.

`prune`'s message should name the alternative, because there is one:

```text
$ cloudstic prune
error: prune is not available on a WORM repository

  Objects cannot be deleted from immutable storage. Space is reclaimed by the
  bucket's lifecycle policy once the retention window expires, not by Cloudstic.

  To see what would be reclaimable:

      cloudstic prune -dry-run
```

`prune -dry-run` stays available and is genuinely useful — it reports what a
lifecycle policy could eventually collect — but its output must be reworded so
it never implies Cloudstic will act.

**Packfiles under WORM.** Packing is *more* valuable here, because retained
versions are counted per object: bundling small objects cuts retained-object
count by orders of magnitude. But repack is forbidden, so fragmentation is
permanent — a packfile whose objects become unreachable stays at full size until
retention expires. Packing stays on by default; `check` reports accumulated
fragmentation so the operator can size retention with real numbers.

### 10. `check` reports WORM-specific truth

`cloudstic check` gains a WORM section, because the questions an operator has
about an immutable repository are not the questions the current output answers:

- whether the backend's enforcement matches the declared mode, when the
  credential can read it;
- how many objects exist that no live snapshot reaches, their total size, and the
  earliest date a lifecycle policy could collect them;
- permanent pack fragmentation;
- any object that appears more than once at the same logical key, which is
  positive evidence that something mutated the repository — the exact signal a
  WORM operator most wants and cannot currently get.

That last check requires listing object versions and is available only where the
backend exposes them and the credential permits it. It is reported as
"unverified" rather than "passed" when it cannot run. Claiming a check passed
because it was skipped would be the worst possible failure in this feature.

### 11. CLI surface

```bash
cloudstic init -worm [-worm-retention-days N] -store s3://bucket/prefix
```

No other command grows a WORM flag. Every other command reads the mode from
`config` — which is the entire point of §1. Commands whose behaviour changes
(`prune`, `forget`, `key passwd`, `check`) change it based on the repository,
and `list -locks` renders leases and tombstones instead of TTL-refreshed locks.

## Alternatives considered

**Version every mutable key with a generation suffix.** Uniform, and it keeps
`prune` and `forget` structurally intact. Rejected as the primary approach
because it preserves state that does not need to exist: `index/latest` is
derivable, so versioning it adds an object per backup forever to reproduce
information already in the catalog. Generations are still used where the state is
irreducible (`keys/<slot>.<gen>`).

**Auto-detect WORM from the backend, no flag.** Attractive, and it cannot be the
mechanism of record. Detection needs a permission the backup credential should
not require, it cannot see a filesystem-level immutable mount at all, and a
detection failure would silently downgrade a repository the operator believes is
protected. Detection is kept as a best-effort *verification* of a declared mode.

**Keep locks by putting them in a separate mutable prefix or bucket.** Preserves
today's semantics exactly. Rejected: it reintroduces a writable surface into a
repository whose value proposition is having none, and that surface is precisely
where an attacker would go — deleting locks is not destructive, but a mutable
prefix inside a WORM repository invites the next feature to use it.

**Refuse to support Regime 2 (Object Lock) and only support reject-on-mutate.**
Simpler to reason about, and it abandons the storage nearly every user has.
Rejected. Handling both is what §2's top-of-chain guard buys, since refusing the
request client-side makes the two regimes behave identically.

**A separate `worm-backup` command.** Rejected: it would fork every read path in
the codebase and guarantee drift.

## Compatibility

Per `docs/compatibility.md`, which is normative here.

**Backward compatibility.** Every change is additive. A current build reading a
pre-WORM repository sees no `worm` field, an `index/snapshots` object with no
shards, and a present `index/latest` — all handled by the existing paths, which
this RFC keeps rather than replaces. The sharded snapshot catalog reader merges
the legacy monolithic object exactly as the pack catalog reader does.

**Forward compatibility, and the version gate.** This RFC proposes raising
`core.RepoFormatVersion` and `core.MaxSupportedRepoFormat` to **3**.

The justification needs care, because the contract says to raise the gate when a
repository would be *unreadable or misreadable* by earlier builds, and to leave
it alone otherwise — a needless bump locks users out of their own data.

An older build's *reads* of a WORM repository mostly degrade safely: absent
`index/latest` makes `resolveLatest` return `("", 0, nil)`, which older builds
treat as a fresh repository — a lost sequence counter and a full rescan, not
corruption; and an absent `index/snapshots` falls back to `LIST snapshot/`,
which is the documented self-heal.

The gate is needed for *writes*. An older build ignores the `worm` field
entirely and will cheerfully overwrite `index/latest`, refresh locks every 30
seconds, and run `prune` — generating retained garbage that cannot be cleaned up
and reporting reclaimed space that was never reclaimed. There is precedent for
gating on this basis: `FramedCompressionFormat` gates writes rather than reads,
for the same reason. Silently misusing immutable storage is a worse outcome than
a clean refusal to open, which is what raising the gate produces.

**What older builds do.** Per the contract this must be verified by running the
old binary, not by reasoning. The claim to be tested is that `v1.16.0` against a
format-3 WORM repository refuses at `LoadRepoConfig` with the
version-above-supported error, before issuing any mutating request. If the
observed behaviour differs, this section changes to match the observation.

**Fixtures.** A `legacy-repo-v1.17.0` baseline is committed at release, distinct
as the last format-2 repository — the last one with a monolithic snapshot
catalog and a live `index/latest`. It is added to the table in
`docs/compatibility.md`, without which `TestCompatibilityDocListsEveryFixture`
fails.

## Security considerations

**What WORM defends against, stated precisely.** An attacker holding valid
repository credentials cannot destroy snapshot data inside the retention window.
That is the whole guarantee, and it is worth writing down because it is narrower
than "ransomware-proof":

- They **can** still write new snapshots, consuming storage that cannot be
  reclaimed until retention expires. WORM turns data destruction into a billing
  attack. Retention length is therefore also a cost-exposure decision, and the
  docs must say so.
- On Regime 2 storage they **can** write delete markers if they bypass Cloudstic
  and use the S3 API directly, hiding current versions. The data is recoverable
  through the versioning API, and `check`'s duplicate-version detection (§10) is
  how an operator notices.
- They **can** read everything the credential can read. WORM is an integrity
  control, not a confidentiality one.

**Credential revocation is unavailable.** §7 exists because this is the sharpest
security consequence of the whole design, and the temptation to paper over it
with a versioned slot that "rotates" the password is exactly what must not
happen. A compromised WORM repository password is compromised until retention
expires, full stop.

**Least privilege.** The backup credential needs `PutObject` and `GetObject` and
should have neither `DeleteObject` nor `DeleteObjectVersion` nor
`BypassGovernanceRetention`. Documenting the minimal policy is part of this work,
because a credential that *can* delete makes governance-mode Object Lock nearly
decorative.

**Shard sealing.** Snapshot catalog shards (§5) carry hostnames and source paths
and sit outside `EncryptedStore`'s reach, so they are sealed with the same
HKDF-derived index key `index/packmap/` uses. Writing them plaintext would leak
source layout to anyone with read access to the bucket.

**Lease objects.** Leases carry a holder identity, as locks do today, and are
sealed on the same basis.

## Testing strategy

**A WORM-enforcing test store.** `storetest` gains a store that fails every
`Delete` and every `Put` to an existing key, and separately a version-preserving
mode that records overwrites instead of failing them. Both modes run the full
hermetic suite. The second is the one that catches the dangerous class of bug,
because it is the mode where a mutation does not announce itself — a test that
only checks for errors would pass while the repository accumulated versions.

**The core assertion, as an invariant rather than a case.** Wrap a local store,
run `init` → `backup` ×3 → `list` → `ls` → `check` → `diff` → `restore`, and
assert the recorded request log contains **no** `Delete` and no `Put` to a key
already present. Asserting on the request log rather than on the absence of
errors is what makes this test meaningful under Regime 2.

**Concurrency.** Two simultaneous backups of the same source into a WORM
repository: both snapshots exist, the merged catalog contains both, and both
restore correctly. This is the test that validates §6's claim that a fork is
acceptable, and it must fail loudly if either snapshot becomes unreachable.

**Derivation.** With no `index/latest` present, `loadLatestSeq` returns
`max(seq)+1`, and `check`/`diff`/`ls`/`restore` each pick the same default
snapshot they would have picked from the pointer.

**Refusals happen before side effects.** `prune`, `forget`, and `key passwd`
each exit non-zero having issued no mutating request — verified from the request
log, not from the exit code alone. A refusal that first rewrote the snapshot
catalog would be worse than no refusal.

**Live e2e against real Object Lock.** A `live`-mode test against an S3 bucket
with Object Lock enabled and a real B2 bucket with file lock. The hermetic store
encodes our belief about what Object Lock does; only the real bucket tests
whether that belief is right, and §"What WORM storage actually does" is exactly
the kind of vendor-semantics claim that deserves verification.

**Old-binary verification.** `v1.16.0` and `v1.17.0` binaries run against a
format-3 WORM repository, per the compatibility contract's requirement that this
be observed rather than argued.

## Rollout plan

Each stage is independently shippable and useful on its own.

1. **Derive `index/latest`.** Add the catalog-based fallback to `resolveLatest`
   and route the default-snapshot consumers through it. Ships to all
   repositories; makes a lost pointer a non-event. No format change.
2. **Shard the snapshot catalog.** `index/snapcat/` shards, sealed, merged on
   read, legacy object still merged. Consolidation under the exclusive lock for
   non-WORM repositories. Removes a read-modify-write race in the default path.
   No format change.
3. **`WORMStore` and the store-layer guard**, with the `storetest` WORM stores
   and the request-log invariant test. Nothing user-visible yet.
4. **`init -worm`, the `config` field, and the version gate to 3.** Command-layer
   refusals for `prune`, `forget`, `key passwd`. `PackStore` append-only mode.
   This is the release that commits the format.
5. **Leases.** The create-only lock path and `list -locks` rendering.
6. **`check` WORM reporting**, including version-duplication detection where the
   backend supports it.
7. **Docs.** `docs/worm.md` covering bucket setup per backend, minimal IAM
   policy, retention sizing as a cost decision, and the revocation limitation
   from §7 stated without softening. Plus the `docs/compatibility.md` baseline
   row and `docs/user-guide.md` flag rows.

Stages 1 and 2 are worth landing regardless of whether WORM mode itself
proceeds, which is a deliberate property of this ordering.

## Open questions

1. **Does the version gate bump to 3 actually earn its cost?** The argument in
   *Compatibility* is that an old build's *writes* are harmful even though its
   *reads* degrade safely. The counter-argument is that the contract reserves the
   gate for read hazards, and that locking users out of a repository an older
   build can read perfectly well is the specific harm the contract warns about.
   An alternative is to leave the gate at 2 and rely on the `worm` config field,
   accepting that pre-format-3 builds ignore it. This needs a decision before
   stage 4, and it is the most consequential open item here.
2. **Should `restore` take a lease at all?** It writes nothing to the repository.
   Today it takes a shared lock purely so that `prune` cannot sweep underneath
   it, and `prune` does not exist in WORM mode. Dropping it entirely would make
   restore a pure-read operation, which has independent appeal.
3. **Retention interaction with incremental backup.** If a lifecycle policy
   expires objects after retention while snapshots still reference them, the
   repository loses data — a chunk from day 1 that day 400's snapshot still
   dedupes against. This is the sharpest *operational* footgun in the whole
   design and it is not addressed above. Options: `check` warns when a live
   snapshot references an object approaching expiry; a periodic forced full
   backup that rewrites all referenced objects; or documenting that WORM
   repositories need retention longer than their oldest retained snapshot. This
   probably needs its own RFC, and stage 7's docs must not ship without at least
   naming it.
4. **Does R2 support object lock, and with what semantics?** Cloudflare R2 is a
   first-class backend for this project and its Object Lock support and exact
   semantics need verification against current documentation before any claim is
   made about it. The same applies to MinIO for hermetic testing.
5. **Should `-worm` imply packing regardless of `CLOUDSTIC_DISABLE_PACKFILE`?**
   Packing dramatically reduces retained-object count, and the env var is
   normally an escape hatch. Overriding a user's explicit setting is
   uncomfortable; warning loudly may be enough.
6. **Governance versus compliance mode.** Governance mode permits deletion by a
   privileged caller. Should Cloudstic report a governance-mode bucket as
   satisfying WORM, warn, or refuse? Reporting it as protected overstates the
   guarantee; refusing rejects a configuration many operators deliberately
   choose.
