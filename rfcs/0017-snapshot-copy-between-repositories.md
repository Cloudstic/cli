# RFC 0017: Snapshot Copy Between Repositories

- **Status:** Draft
- **Date:** 2026-04-03
- **Affects:** `cmd/cloudstic`, `client.go`, `internal/engine`, `pkg/store`, docs

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

This feature is expected to re-encrypt and rewrite data
for the destination repository. It is not a backend-side object clone.

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

Cloudstic's storage model makes this a real feature, not just a convenience:

- chunks and metadata are content-addressed inside one repository's encryption
  domain
- `content/`, `filemeta/`, `node/`, and `snapshot/` objects are all encrypted
  with the destination repository's master key
- packfiles and indexes are repository-local implementation details

So "copy snapshot history" must be an engine-level operation.

## Goals

- Add a supported way to copy snapshots between repositories.
- Preserve snapshot semantics, including timestamp, tags, source identity,
  account, and path lineage.
- Support both "copy everything" and filtered/exact snapshot selection.
- Make repeated runs idempotent enough to skip snapshots already copied.
- Keep the command compatible with Cloudstic's current store/profile/auth model.
- Expose the feature both in the CLI and the public `Client` API.

## Non-goals

- No backend-native server-side object cloning in v1.
- No cross-repository dedup guarantee in the initial version.
- No requirement that source and destination share passwords, master keys, or
  key slots.
- No attempt to merge or reconcile histories from unrelated repositories beyond
  snapshot-level copying.
- No in-place conversion of an existing repository's chunking behavior or object
  layout.

## Proposal

### 1. Add `cloudstic copy`

Introduce a new top-level command:

```bash
cloudstic copy --from-store <uri-or-ref> [snapshot selectors...]
```

Examples:

```bash
# Copy every snapshot from one store into another
cloudstic copy \
  -store s3:dest-bucket/prod \
  -from-store local:/tmp/cloudstic-src

# Copy only one profile lineage
cloudstic copy \
  -store-ref remote-prod \
  -from-store-ref local-seed \
  -source local:./Documents

# Copy explicitly selected snapshots
cloudstic copy \
  -store-ref archive \
  -from-store-ref laptop-local \
  410b18a2 4e5d5487 latest
```

The destination repository is configured the same way as other commands, using
existing global destination flags such as `-store`, `-store-ref`, `-password`,
`-encryption-key`, and related auth settings.

The source repository is provided through a parallel set of `from` flags.

### 2. Add source repository configuration flags

The source repository requires its own repository location and unlock
credentials.

Initial CLI shape:

```bash
-from-store <uri>
-from-store-ref <name>
-from-password <pw>
-from-password-secret <ref>
-from-encryption-key <hex>
-from-encryption-key-secret <ref>
-from-recovery-key <words>
-from-recovery-key-secret <ref>
-from-kms-key-arn <arn>
-from-kms-region <region>
-from-kms-endpoint <url>
-from-s3-endpoint <url>
-from-s3-region <region>
-from-s3-profile <name>
-from-s3-access-key <key>
-from-s3-secret-key <secret>
-from-store-sftp-password <pw>
-from-store-sftp-password-secret <ref>
-from-store-sftp-key <path>
-from-store-sftp-key-secret <ref>
-from-store-sftp-known-hosts <path>
-from-store-sftp-insecure
-from-profiles-file <path>
```

Design rule:

- destination flags keep their current names
- source repository flags are explicit `from-*` mirrors

This avoids ambiguous mixed configuration and keeps scripting predictable.

CLI credential handling should follow the same preference order already used by
the rest of the CLI:

- explicit secret reference first (`env://`, `keychain://`, etc.)
- explicit raw value second
- interactive prompt only where already supported by the destination-side flow

Example:

```bash
cloudstic copy \
  -store-ref remote-prod \
  -password-secret keychain://cloudstic/store/remote-prod/password \
  -from-store-ref laptop-local \
  -from-password-secret keychain://cloudstic/store/laptop-local/password
```

### 3. Snapshot selection model

By default, `copy` copies all source snapshots.

It may be narrowed in two ways:

- explicit positional snapshot IDs: `cloudstic copy ... <snapshot_id>...`
- existing snapshot filters:
  - `-source`
  - `-account`
  - `-tag`
  - `-group-by` where applicable for selection helpers

If explicit snapshot IDs are provided, they define the candidate set and filter
flags further constrain it.

`latest` is accepted as a selector, using the source repository's `index/latest`
resolution behavior.

Open question:

- whether `copy` should also accept `-profile` as a selector shortcut in v1, or
  keep selection purely snapshot-oriented

Recommendation:

- v1 should stay snapshot-oriented and reuse the existing source/account/tag
  filters already understood by `list` and `forget`

### 4. Copy semantics

For each selected source snapshot:

1. Load the source snapshot object.
2. Walk the full reachable object graph from that snapshot root:
   - `snapshot/*`
   - `node/*`
   - `filemeta/*`
   - `content/*`
   - `chunk/*`
3. Materialize plaintext payloads through the source repository stack.
4. Write those logical objects through the destination repository stack.
5. Write a destination snapshot object preserving the snapshot metadata.
6. Update the destination `index/snapshots` catalog.
7. Update destination `index/latest` if the copied snapshot becomes the newest
   snapshot under the existing latest-pointer rules.

The copy operation is therefore a logical rewrite of the repository graph, not
an opaque transfer of encrypted objects.

### 5. Snapshot identity and idempotency

Repeated copy runs should skip snapshots that were already copied.

This requires recording source-to-destination provenance on copied snapshots.

Proposal:

- extend copied destination snapshots with extra metadata:

```json
{
  "copied_from_repo_id": "<source repo id or marker>",
  "copied_from_snapshot": "snapshot/<source hash>"
}
```

Skip rule:

- before copying a source snapshot, scan the destination snapshot catalog for a
  snapshot whose provenance matches the same `(source repository, source
  snapshot ref)`

If found:

- print a skip message
- do not rewrite the snapshot again

This is stronger than trying to infer equivalence from timestamp, source, tags,
and path metadata.

Open question:

- whether Cloudstic currently has a stable repository ID suitable for this
  provenance key, or whether v1 should derive one from `config`

### 6. Encryption and bandwidth behavior

`copy` must be explicit that it is expensive compared with normal incremental
backup.

Important properties:

- source repository objects must be fully read and decrypted
- destination repository objects must be fully re-encrypted and written
- this can incur substantial bandwidth and API cost
- small metadata objects may be repacked differently in the destination because
  packfiles are repository-local

### 7. Deduplication expectations

Re-encryption does not by itself prevent deduplication in the destination
repository.

What matters is not whether the copied objects retain their source encrypted
identities, but whether the destination write path recomputes destination
logical object identities from the plaintext content.

Expected v1 behavior:

- source objects are decrypted and materialized as logical plaintext payloads
- destination chunk/content objects are addressed in the destination
  repository's own dedup/encryption domain
- if identical plaintext chunks or content objects already exist in the
  destination repository, the copy should reuse those destination identities
  naturally

In other words:

- copied data is not expected to retain the same object refs as in the source
  repository
- but it should still deduplicate against equivalent data already present in
  the destination repository, assuming the logical chunk/content structure is
  the same

The remaining caveat is about storage-layout parity, not logical dedup:

- packfile grouping is repository-local and may differ
- snapshot objects will differ because they are rewritten in the destination
  repository
- indexes and catalogs are destination-local implementation details

So v1 should promise destination-side logical deduplication where content
matches, but should not promise identical storage footprint or object naming
between source and destination repositories.

### 8. Resume and interruption behavior

Copying large repositories can take a long time.

V1 should be restart-safe at the snapshot level:

- objects already written to the destination may remain
- rerunning `copy` should re-check provenance and existing object presence
- completed snapshots are skipped
- partially copied snapshots may be retried from scratch

This is sufficient for an initial implementation because Cloudstic already uses
content-addressed objects and "put if missing" behavior in much of the write
path.

Fine-grained resumable progress within one snapshot is explicitly not required
for v1.

### 9. Client API

The client API should follow the existing construction model:

```go
client, err := cloudstic.NewClient(ctx, destinationStore, opts...)
```

That means the destination repository is already bound to the `Client`, so the
copy entrypoint should accept the source store explicitly:

```go
func (c *Client) Copy(ctx context.Context, from store.ObjectStore, opts ...CopyOption) error
```

At the client layer, repository credentials should remain opaque and reuse the
existing `keychain.Chain` abstraction rather than exposing password vs
keychain vs recovery-key detail in the option surface.

That implies:

- destination credentials continue to be supplied when constructing the client
- source credentials are supplied through `CopyOption`

For example:

```go
client, err := cloudstic.NewClient(
    ctx,
    destinationStore,
    cloudstic.WithKeychain(keychain.Chain{
        keychain.WithPassword("destination-password"),
    }),
)
if err != nil {
    return err
}

err = client.Copy(
    ctx,
    sourceStore,
    cloudstic.WithFromKeychain(keychain.Chain{
        keychain.WithPassword("source-password"),
    }),
    cloudstic.WithCopySnapshotIDs("410b18a2", "latest"),
    cloudstic.WithCopyTag("workstation"),
)
```

Representative option names:

- `WithKeychain(...)` for destination repository credentials on `NewClient`
- `WithFromKeychain(...)` for source repository credentials on `Copy`
- `WithCopySnapshotIDs(...)`
- `WithCopySource(...)`
- `WithCopyAccount(...)`
- `WithCopyTag(...)`

The exact credential material inside each `keychain.Chain` is intentionally not
part of the copy API contract. Callers may provide passwords, recovery keys,
platform keys, or other supported credentials the same way they do elsewhere.

This keeps repository migration aligned with the current client API design
instead of introducing a one-off `CopyOptions` struct, duplicating store
construction inside copy options, or leaking secret-ref transport details into
the library surface.

### 10. Progress and output

The command should report progress at snapshot granularity first:

- source snapshot being copied
- whether it is skipped or copied
- counts of copied/skipped snapshots

If practical, object and byte progress may be added later, but snapshot-level
output is the minimum useful UX.

Example:

```text
snapshot 410b18a2 of [local:./Documents] at 2026-04-01 20:15:03 +0200
  copy started, this may take a while...
snapshot a1b2c3d4 saved

snapshot 4e5d5487 of [local:./Documents] at 2026-03-30 18:22:11 +0200
skipping snapshot 4e5d5487, already copied as snapshot e5f6a7b8
```

## Compatibility

This feature is additive.

- existing repositories remain valid
- existing commands are unchanged
- copied snapshots remain ordinary destination snapshots after the operation

Compatibility caveat:

- if provenance metadata is added to `Snapshot.Extra` or a new optional field,
  older clients must tolerate it as an unknown field

## Security considerations

- `copy` requires access to unlock both source and destination repositories.
- Source and destination credentials must remain strictly separated in CLI
  parsing, logs, and config handling.
- Error messages must not leak source or destination secrets.
- The command increases blast radius for operator mistakes because it can write
  substantial history into the wrong destination repository; destination
  identification should therefore be printed clearly before writes begin.

## Testing strategy

- Unit tests for source/destination flag parsing and validation.
- Unit tests for snapshot provenance matching and skip behavior.
- Hermetic e2e tests copying between:
  - local -> local
  - local -> MinIO
  - MinIO -> local
  - SFTP -> local where supported in the existing matrix
- E2E tests for:
  - copy all snapshots
  - filtered copy by source/tag/account
  - explicit snapshot selection
  - rerun idempotency (already copied snapshots are skipped)
  - interrupted copy followed by rerun
- Compatibility tests ensuring copied snapshots are listable, diffable, and
  restorable in the destination repository.

## Rollout plan

1. Add RFC and CLI/API surface design.
2. Implement source repository config parsing and validation.
3. Implement snapshot selection and provenance-aware skip logic.
4. Implement logical object graph copy between repositories.
5. Add hermetic e2e migration tests.
6. Document operational caveats, especially bandwidth and dedup expectations.

## Open questions

- Should the command be named `copy`, `migrate`, or `replicate`?
  Recommendation: `copy`, because it is direct and consistent with other tools like restic.
- Should provenance live in a dedicated snapshot field or under extensible
  extra metadata?
- Should v1 support destination-side filtering to avoid importing snapshots that
  would conflict with existing retention policies?
- Should `copy` support `--dry-run` in v1, or can that wait until the command
  shape is stable?
