# Repository Compatibility Contract

This document is normative. It defines what Cloudstic guarantees about reading
and writing repositories across versions, and what a change to the on-disk
format is required to do.

Cloudstic is a backup tool. Restoring an old backup is not a nice-to-have — it
is the product. A repository written years ago by a version nobody runs any more
must still restore today. That asymmetry shapes everything below.

## The contract

### Backward compatibility — guaranteed, permanently

**Any repository written by any released version of Cloudstic must remain
readable by every later version.**

This has no expiry. There is no deprecation window after which a legacy read
path may be deleted. Legacy handling may be refactored, moved, or made faster,
but it may not be removed, and the behaviour it implements may not change.

"Readable" means the full recovery path works: `list`, `ls`, `check`, `cat`,
`diff`, and `restore`. Writing to an old repository (`backup`, `prune`,
`forget`) is also supported, and may upgrade the repository in place.

### Forward compatibility — not guaranteed, but failure must be safe

**An older binary is not guaranteed to read a repository written by a newer
one.** Formats change; that is allowed.

What is *not* allowed is failing dangerously. A build that cannot fully
understand a repository must refuse to operate on it. Specifically:

1. **Never read "cannot decode" as "empty."** An index that fails to load is an
   error. It is not an index with no entries. This distinction is the whole
   ballgame: an empty index means "nothing is referenced", and "nothing is
   referenced" is what makes a garbage collector delete a live repository.
2. **Never let a destructive operation proceed on incomplete information.**
   `prune`, `forget`, and repacking must abort when the data they would act on
   could not be fully read.
3. **Refuse unknown formats up front**, via the version gate below, rather than
   proceeding on the parts that happen to parse.

The distinction that matters is between *"I cannot read this"* — acceptable, the
user upgrades — and *"I read this as empty and deleted your data"* — never
acceptable.

## The version gate

`config` carries a `version` field, and two constants in `internal/core/models.go`
govern it:

| Constant | Meaning |
|----------|---------|
| `RepoFormatVersion` | The version stamped into repositories this build **creates** |
| `MaxInPlaceUpgradeFormat` | The highest version an **existing** repository is raised to by a write |
| `MaxSupportedRepoFormat` | The highest version this build will open |

`LoadRepoConfig` refuses any repository whose version exceeds
`MaxSupportedRepoFormat`, with a message telling the user to upgrade. Every path
that opens a repository funnels through that function, so it is the single gate.

A missing or zero `version` is accepted: the oldest repositories must not be
stranded by a field they predate.

### When to raise the version

Raise `MaxSupportedRepoFormat` when a change makes a repository **unreadable or
misreadable by earlier builds**, and stamp affected repositories with
`UpgradeRepoFormat` from the write that introduces it.

Do **not** raise it for a change earlier builds can still read correctly. A
needless bump locks users out of their own data for no benefit, which is the
same harm the gate exists to prevent, arriving from the other direction.

Every repository this build creates is stamped at `RepoFormatVersion`, and every
existing repository it **writes to** is raised to `MaxInPlaceUpgradeFormat`.

Those are two different numbers, and the difference is load-bearing. Creating is
free to pick the newest layout; raising an existing repository is not, because a
stamp changes the marker and nothing else. That works only for a change whose
structures an older era can already hold — v1 to v2 was one. v2 to v3 is not: a
v3 repository holds fat leaves and blobs where a v2 one holds packs, so stamping
3 onto a packfile repository would claim a layout that is not there. Older
builds would refuse a repository they can in fact read, and this build would
open it as v3, build its chain without `PackStore`, and fail to read the packs
it is made of. So `MaxInPlaceUpgradeFormat` stays 2 and a backup onto a packfile
repository leaves it packfile forever.

That is a deliberate choice, and a stronger one than "record only what the bytes
require". The version is not purely a claim about the data present — it is the
signal that tells other machines sharing the repository to upgrade. A fleet
running mixed versions against one repository is the dangerous state, and the
narrower rule would leave those machines writing happily alongside each other
until the day a format change made that fatal.

The cost is accepted knowingly: a repository that contains nothing an older
build could not read may still refuse to open on one, because a newer build
wrote to it. That is a lockout the user can fix by upgrading, traded against a
silent divergence they cannot see.

### Format 3 is the default, and the only crossing is explicit

Format 3 (RFC 0026) is different in kind from the version bumps before it: it
is not a new era inside a mixed repository but a distinct layout — file
metadata and small content live inside binary HAMT leaves, and the packfile
layer (`packs/*`, `index/packs`, `index/packmap/*`), the `filemeta/` namespace,
and standalone `content/` manifests do not exist.

`init` creates v3 since #517. `init -format 2` still creates a packfile
repository, for anyone who needs one a build older than v3 support can read.
The rules:

- **An existing repository never changes format on its own.** A v3 repository
  is created by `init`, or reached from a packfile one by an explicit
  `cloudstic migrate`, and by nothing else. `UpgradeRepoFormat` stamps at most
  `MaxInPlaceUpgradeFormat`, and the in-process format view only ever rises, so
  no mutation can move a repository into or out of v3.
- **Adopting keeps the format it finds.** `init -adopt-slots` on an existing
  repository defaults to that repository's own version rather than to this
  build's default, so adopting a packfile repository does not attempt — and
  fail — to re-initialize it as v3.
- A v3 repository contains only v3 structures. There is no mixed era and no
  opportunistic conversion; `init -adopt-slots -format 3` on a lower-format
  repository is refused, because rewriting the marker cannot change the stored
  structures. Migration is the only crossing, and it is a separate tool
  (RFC 0026).
- **Older builds fail safely, and this now matters to everyone.** Every build
  released so far enforces `MaxSupportedRepoFormat = 2`, so it refuses a v3
  repository with the upgrade message rather than misreading it. Verified by
  running one: v1.18.0 against a v3 repository prints "repository format
  version 3 is newer than this build supports (up to 2): upgrade cloudstic to
  work with this repository" and stops. Because v3 is now what `init` creates,
  a user whose machines are on different versions will meet this; the answer is
  to upgrade the older machine, or to create the repository with
  `init -format 2`.
- `copy` crosses the two formats in either direction. It reads whichever form
  the source stores its entries in and writes whichever the destination
  records, so the destination never ends up holding a mixture, and the source
  is only ever read. That is what makes migrating a packfile-era repository an
  ordinary copy into one created with `init -format 3`.

### The version is a floor

`UpgradeRepoFormat` raises the recorded version and never lowers it, and the
gate keeps an older build from opening — and therefore from writing to — a
repository it does not understand.

`init --adopt-slots` is the exception that needs guarding explicitly. It rewrites
the marker, and it reads the existing one directly rather than through
`LoadRepoConfig`, so the gate does not apply on that path. Left alone, adopting a
repository from a newer build would stamp it back down to a version this build
understands while leaving data it does not — turning a repository that fails
safely into one that is silently misread. Adoption therefore refuses a format
above `MaxSupportedRepoFormat`, and never writes a version lower than the one
already recorded.

### Writes stamp, reads do not

The stamp is tied to mutation — `backup`, `prune`, `forget` — never to opening a
repository.

Two reasons, one principled and one practical. A read changes nothing, so
locking out another machine on the strength of it has no cause behind it: "a
newer build has written here" is a real signal about divergence, "a newer build
looked at it" is not. And `LoadRepoConfig` runs on every operation including
`restore`, `cat`, and `ls`, so stamping there would make read-only work perform
a write — breaking restores for anyone holding read-only credentials.

### What the gate cannot do

The gate protects against *future* format changes only. Builds released before
it existed ignore `version` entirely, so no marker written today can make them
fail safe.

This is not theoretical. Cloudstic v1.14.0, given a repository whose pack index
it could not decode, reported `0 snapshots` with no error and its `prune` then
deleted the packfiles — a total loss, from one command, with no warning. That is
the failure mode rules 1 and 2 above exist to prevent, and the reason the gate
was added.

The practical consequence: **when a release changes the format, sequence it.**
Ship the release that can fail safely before the release that produces the new
format, so a user who upgrades one machine and not another gets an error instead
of a deletion.

## How repositories are upgraded

Upgrades are **in place, opportunistic, and permanently partial**. There is no
migration command, no conversion step, and no moment at which a repository
becomes "fully migrated".

What actually happens when a newer build writes to an older repository:

- new packfiles get footers; packfiles written before footers existed keep none,
  until a `prune` happens to repack them — which for a cold pack may be never
- new pack index entries are written as shards; the pre-shard monolithic
  `index/packs` is read alongside them and folded in by the next `prune`
- the pack index is sealed from the next flush; the footers of old packs stay
  as they were
- `index/latest` stops being packed, but an already-packed one keeps being read
  from where it is
- objects are written inside a compression frame once the repository records
  format 2; objects stored before the frame existed keep none, and are read by
  detecting a gzip or zstd stream as before

So a long-lived repository is a mixture of eras indefinitely, and that is the
intended steady state rather than a transitional one. Because backward
compatibility is permanent, nothing needs the mixture to resolve: no legacy read
path is ever going to be deleted, so no code needs to know whether migration
"finished". That is a deliberate simplification bought by the guarantee above.

### What the version means, and when to stamp it

`config.version` is the **minimum reader version**: the oldest build that can
read everything the repository currently contains. It is *not* a statement that
migration completed, and it is *not* a record of which build last wrote.

That distinction decides when to raise it. Two tempting rules are both wrong:

- **"Stamp it once migration is complete."** Migration is never complete, so
  this never fires.
- **"Stamp it whenever a newer binary opens the repository."** A read would
  then mutate the repository and lock out other machines — a fleet-wide side
  effect of running `list`.

The correct rule is narrower:

> Stamp the repository when a build **writes** to it — not when it is opened,
> and not when migration finishes.

### Before the write, when the write depends on it

"When a build writes to it" leaves the timing open, and the timing matters.

The default is to stamp *after* a successful mutation: the data is already
written, a failed stamp is not fatal, and the next mutation stamps again. That
is right for a change whose output older builds can still read — `prune` and
`forget` write JSON manifests, which are unremarkable to any reader.

It is wrong when the write itself is gated on the format. The compression frame
is the case that forced this distinction:

- an object written unframed is unframed **permanently** — content-addressed
  objects are never rewritten, so a later framed backup finds the object already
  present and skips it
- writers must not frame while the repository still records an older format, or
  a build predating the frame will read a framed object as opaque bytes

Stamp afterwards and the two combine badly. Every repository ever released is
below the framing format, so the first backup after upgrading would write
unframed objects — permanently — and only then raise the format. For an
already-compressed file that is unrecoverable data loss, inflicted once on every
existing user.

So the rule for this class of change is:

> When a write is only correct at a given format, raise the format **before** the
> first object is written, and treat the failure as fatal to the operation.

`Client.prepareFramedWrites` implements that for `backup`. It stays within the
"writes stamp, reads do not" rule — `backup` is a mutation — and it costs one
thing: a mutation that fails early leaves the repository stamped, locking out
older builds from a repository that gained nothing. That is recoverable by
upgrading the other machine. The alternative is not recoverable at all.

`UpgradeRepoFormat` (`client.go`) does this. It raises the recorded version,
never lowers it, and refuses to stamp a version the running build could not
itself read.

It is called after a successful `backup`, `prune`, or `forget`. Sealing happens
inside the flush those operations perform, so the write-path stamp covers it
without a separate hook in the store layer.

An operation that fails after sealing but before returning leaves a sealed
repository still claiming the older format. The next successful write corrects
it, and in the meantime a build that cannot read the seal fails loudly rather
than misreading — the safe direction.

### Why a single version number, and not feature flags

A richer scheme — a set of required feature names, as Git does with
`extensions.*` — is the right answer when several implementations support
different subsets of features, so a reader needs to ask "do I understand
*these particular* extensions?".

Cloudstic has one implementation and a linear release history, so any build
supports a prefix of the feature list and a monotonic integer expresses the
question exactly. The case that looks like it needs feature flags — sealing
applies to encrypted repositories but not unencrypted ones — is handled by *when*
the stamp happens rather than by *what* is recorded: an unencrypted repository
never seals, so it never triggers the stamp.

Revisit this if a second implementation appears, or if an optional feature is
ever introduced that some builds deliberately do not support.

## Changing the on-disk format

A change to the format must do all of the following.

1. **Keep every older format readable.** Old and new layouts coexist; detect
   which one you have and handle both. Never require a migration step in order
   to read.
2. **Upgrade opportunistically, never destructively.** If a write path can
   rewrite old structures into the new shape, it should — but a repository that
   is only ever read must keep working untouched, forever.
3. **Add a fixture.** Commit a repository generated by the last release that
   used the *old* format, under `e2e/testdata/legacy-repo-<tag>/` with a
   `manifest.json`. See "Fixtures" below.
4. **Decide on the version gate.** Raise both constants if older builds would
   misread the result; document why if not. If you raise them, call
   `UpgradeRepoFormat` from the write path that introduces the incompatibility,
   or repositories upgraded in place will keep advertising a version that no
   longer describes their contents — and the gate will not fire for exactly the
   repositories that most need it.
5. **Record it here.** Add the baseline to the table below.
6. **State the failure mode.** If older builds will encounter the new format,
   say in the pull request what they do when they meet it — and verify that
   claim by running an old binary, rather than reasoning about it.

## What older builds actually do

Verified by running the released binaries, as the rules below require, rather
than by reasoning about the code.

### v1.14.0 against a repository written by v1.15.0

Fully readable. `list`, `check`, and `restore` all succeed, and `prune` does not
lose data — self-describing packfile footers are trailing bytes that a reader
resolving objects by explicit offset and length never touches, and an unpacked
`index/latest` is found by a normal `Get`.

There is one non-obvious interaction. An older build **erodes footers as it
writes**:

- its `Repack` computes waste as `physicalSize - activeSize`, and the footer is
  not in the catalog, so the footer itself reads as waste. Above the repack
  threshold it rewrites the pack — without a footer, because it does not know
  about them.
- every pack it writes is footerless by construction.

No data is lost, and those packs simply revert to pre-RFC-0018 behaviour, which
is handled correctly: a rebuild reports them as unrecoverable rather than
returning a partial catalog. But the ability to rebuild the catalog from footers
is quietly lost for them, with nothing to signal it. A fleet split across
versions therefore pays a slow cost in recoverability even while nothing appears
wrong — a reason to converge rather than to linger.

### v1.14.0 against a repository with a sealed pack index

Destructive, and unfixable from this side. v1.14.0 reads the sealed catalog as
unparseable, discards the error, reports `0 snapshots`, and its `prune` then
deletes the packfiles. See "What the gate cannot do" above.

### v1.15.0 against a repository written by a later build

Refused cleanly, on `list`, `check`, and `restore` alike:

```text
repository format version 2 is newer than this build supports (up to 1):
upgrade cloudstic to work with this repository
```

### v1.16.0 against a sealed repository config

Refused cleanly, and — importantly — refused *before* doing anything. v1.16.0
parses the marker as JSON, and a sealed marker is ciphertext, so every command
stops at the same point with the same error:

```text
Failed to init store: parse repo config: invalid character '\x01'
looking for beginning of value
```

Verified by building the released v1.16.0 binary from its tag and running it
against a repository this build had initialized and backed up into. `list`,
`check`, `ls`, and `prune` each exit 1; `prune` in particular gets nowhere near
the object store, and the repository is byte-for-byte intact afterwards. That
is the outcome the forward-compatibility rule asks for: an older build must fail
safely, never misread, and never delete on a partial read.

Note what this costs. The recorded version now lives *inside* the sealed marker,
so an older build can no longer read it in order to say "format version N is
newer than this build supports". It reports a parse failure instead. The refusal
is equally safe but less actionable, and no version bump can improve it — an
older build cannot read the number that would tell it to upgrade. That is why
sealing does **not** raise `RepoFormatVersion`: the bump would be invisible to
exactly the builds it is meant to warn, while locking unencrypted repositories
(whose marker stays plaintext and readable) out of older builds for a feature
they do not use.

### v1.15.0 against a compression frame

It never meets one. This was verified rather than assumed, because the frame is
the one structure an older build would misread instead of refusing: v1.15.0
recognises neither the magic nor the algorithm byte, so it would return the
framed bytes verbatim — header included — and write them to a restored file.

Two runs establish that it cannot reach that state:

- A repository this build created is stamped format 2, and v1.15.0 refuses it at
  the gate before reading any object.
- A repository v1.15.0 created (format 1), backed up into by this build, is
  raised to format 2 *before* the first object is written, so no framed object
  is ever added to a repository still recording format 1. Both machines see a
  consistent repository: either format 1 with no frames, or format 2, which
  v1.15.0 declines.

Confirmed on the second path by inspecting the store after the backup: the
chunk, packfile, snapshot catalog, and `index/latest` are all framed, and the
repository records version 2. The one unframed object is the pack index shard
under `index/packmap/`, which `PackStore` writes *below* the compression layer
and which therefore never carries a frame by construction.

## Fixtures

Backward compatibility is enforced against real repositories produced by the
releases themselves, not simulations of them. A simulation only encodes what we
*believe* an old version wrote; a fixture records what it actually did.

Fixtures live in `e2e/testdata/legacy-repo-<tag>/` and are exercised by
`TestCLI_Feature_ReadsLegacyRepositories`, which enumerates every directory
matching that pattern. Adding a baseline is a directory drop with no code
change.

Each fixture needs a `manifest.json`:

```json
{
  "release": "v1.14.0",
  "password": "fixture-password",
  "snapshot": "<snapshot hash>",
  "files": { "doc.txt": "expected contents\n" },
  "sealed_index": false,
  "notes": "what makes this baseline distinct"
}
```

`TestCompatibilityDocListsEveryFixture` fails if a fixture is not named in the
table below, so the written guarantee and the enforced one cannot drift apart.

Generate fixtures with a **neutral identity**. A repository records the hostname
and source path of the machine that wrote it, inside encrypted metadata that the
fixture's published password makes readable — so a fixture generated on a
workstation publishes a developer's machine name, invisibly to review.

Either route works, since both leave the on-disk format untouched:

- a container with a fixed hostname (`docker run --hostname fixture-host`) and
  paths under a generic root, which needs no code changes at all; or
- a throwaway worktree at the tag with the `os.Hostname()` call sites stubbed to
  a fixed value, which is what `legacy-repo-v1.14.0` used. Those are identity
  values rather than encodings, so the format being pinned is unaffected.

Whichever is used, record it in the fixture's README, and verify afterwards that
the real hostname appears nowhere in the fixture bytes and that the decrypted
listing shows the neutral identity.

### Guaranteed baselines

| Release | What makes it distinct |
|---------|------------------------|
| `v1.14.0` | Plaintext `index/packs`, footerless packfiles, and `index/latest` stored inside a packfile. Predates RFC 0018 footers and pack index sealing. |
| `v1.15.0` | Format 1 with self-describing packfile footers, and the fixes that make an unreadable index fail loudly rather than read as empty. The last release that does **not** produce a sealed pack index. |
| `v1.16.0` | Format 2 with framed objects and a sharded, sealed pack index. The last release whose encrypted `config` marker is plaintext rather than sealed. |

## What is not covered

- **Profiles, config files, and the CLI surface.** This contract is about
  repository data. Flags and profile schemas have their own compatibility
  expectations, documented with those features.
- **Cross-repository operations.** Copying between repositories (RFC 0017)
  re-encrypts and rewrites; it is not a format compatibility path.
- **Backend behaviour.** Object stores make their own consistency guarantees;
  see `docs/storage-model.md`.
