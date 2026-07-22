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
| `RepoFormatVersion` | The version stamped into repositories this build creates |
| `MaxSupportedRepoFormat` | The highest version this build will open |

`LoadRepoConfig` refuses any repository whose version exceeds
`MaxSupportedRepoFormat`, with a message telling the user to upgrade. Every path
that opens a repository funnels through that function, so it is the single gate.

A missing or zero `version` is accepted: the oldest repositories must not be
stranded by a field they predate.

### When to raise the version

Raise `RepoFormatVersion` and `MaxSupportedRepoFormat` together when a change
makes a repository **unreadable or misreadable by earlier builds**.

Do **not** raise them for a change earlier builds can still read correctly. A
needless bump locks users out of their own data for no benefit, which is the
same harm the gate exists to prevent, arriving from the other direction.

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
- the pack catalog is re-sealed on the next flush; the footers of old packs stay
  as they were
- `index/latest` stops being packed, but an already-packed one keeps being read
  from where it is

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

- **"Stamp it whenever a newer binary touches the repository."** This locks
  older builds out of data they can still read perfectly well. An unencrypted
  repository, for instance, never seals its pack index, so a newer build writing
  to it produces nothing an older build would struggle with. Stamping there
  causes exactly the harm the gate exists to prevent, arriving from the other
  direction.
- **"Stamp it once migration is complete."** Migration is never complete, so
  this never fires.

The correct rule is narrower:

> Stamp the repository at the moment a write first stores something an older
> build would **misread** — not when it is opened, and not when migration
> finishes.

`UpgradeRepoFormat` (`client.go`) does this. It raises the recorded version,
never lowers it, and refuses to stamp a version the running build could not
itself read.

Nothing calls it yet: everything written today is readable by every build that
validates versions at all, because the version gate and the pack index sealing
that motivated it ship in the same release. The next change that is not readable
by earlier builds must call it from its write path.

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

Generate fixtures with a neutral identity — a container with a fixed hostname
and generic paths. A repository records the hostname and source path of the
machine that wrote it, inside encrypted metadata that the fixture's published
password makes readable, and that content is invisible in review.

### Guaranteed baselines

| Release | What makes it distinct |
|---------|------------------------|
| `v1.14.0` | Plaintext `index/packs`, footerless packfiles, and `index/latest` stored inside a packfile. Predates RFC 0018 footers and pack index sealing. |

## What is not covered

- **Profiles, config files, and the CLI surface.** This contract is about
  repository data. Flags and profile schemas have their own compatibility
  expectations, documented with those features.
- **Cross-repository operations.** Copying between repositories (RFC 0017)
  re-encrypts and rewrites; it is not a format compatibility path.
- **Backend behaviour.** Object stores make their own consistency guarantees;
  see `docs/storage-model.md`.
