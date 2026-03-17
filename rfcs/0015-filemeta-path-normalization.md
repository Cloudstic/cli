# RFC 0015: FileMeta Path Normalization

- **Status:** Draft
- **Date:** 2026-03-17
- **Affects:** `internal/core`, `internal/engine`, `pkg/source`, docs
- **Related:** [RFC 0001](./0001-hamt-evolution.md), [RFC 0006](./0006-direct-to-filesystem-restore.md)

## Abstract

RFC 0001 identified `FileMeta.Paths` as redundant metadata that inflates
`filemeta/` objects and causes avoidable hash churn. This RFC defines a follow-up
implementation plan: treat full paths as normalized, ephemeral scan-time data,
but stop persisting them in newly written `FileMeta` objects.

New snapshots continue to store `Name` and `Parents` as the canonical location
signal. Restore and other path-based workflows reconstruct paths dynamically by
walking the parent chain. Older snapshots that still contain `Paths` remain
fully readable.

## Context

`core.FileMeta` currently persists:

- `Name`
- `Parents`
- `Paths`

For local, SFTP, Google Drive, and OneDrive sources, `Paths[0]` is typically a
single slash-separated display path that can already be derived from the first
parent chain plus `Name`.

This has several costs:

- repeated path prefixes in every descendant object
- larger JSON payloads for `filemeta/` objects
- additional metadata hash churn when path strings differ even though parent
  lineage already captures the same location
- confusion between scan-time path needs and persisted snapshot schema

The current engine already proves the model is viable: restore can reconstruct a
path from `Parents` and `Name` when `Paths` is absent.

## Goals

- Stop persisting redundant path strings in newly written `FileMeta` objects.
- Keep path-based filtering and exclude evaluation working during backup scans.
- Preserve backward compatibility for existing snapshots containing `Paths`.
- Define one normalization model for ephemeral relative paths used during scan,
  diff, and restore logic.
- Reduce `filemeta/` size and metadata rewrite overhead without introducing a
  repository format version bump.

## Non-goals

- No change to the HAMT key (`FileID`) in this RFC.
- No change to source identity or snapshot lineage semantics.
- No multi-path deduplication or hard-link model in this RFC.
- No user-visible path aliasing feature.

## Proposal

### 1. Make persisted `FileMeta.Paths` optional and omit it for new writes

`core.FileMeta.Paths` remains in the schema for compatibility, but new backups
should write it as empty/nil in persisted metadata objects.

Interpretation becomes:

- scan-time/in-memory `Paths`: optional helper data for source filtering,
  excludes, and UI
- persisted `Paths`: legacy compatibility field, not required for correctness

This keeps the on-disk JSON schema backward compatible while moving the system
toward parent-chain-derived paths.

### 2. Define normalized path semantics for ephemeral use

When a path is carried in memory, it must be normalized as follows:

- relative to the selected source root
- slash-separated (`/`) on all platforms
- no leading slash in `FileMeta.Paths`
- no `.` segments
- no empty segments except the implicit root
- directories represented by the same path form as files, without trailing `/`

Examples:

- local file `Documents\\notes.txt` on Windows becomes `Documents/notes.txt`
- source root folder itself is represented as `Project`, not `./Project` or
  `Project/`
- OneDrive and Google Drive scoped roots follow the same relative convention

This normalization applies to ephemeral paths used during source traversal and
change filtering even though those paths are no longer persisted in new
snapshots.

### 3. Reconstruct persisted paths from `Parents` + `Name`

Path reconstruction becomes the primary model for snapshot consumers.

Rules:

- if `Paths` exists on a loaded `FileMeta`, consumers may use `Paths[0]` as an
  optimization only
- if `Paths` is empty, consumers reconstruct from the first parent chain and
  `Name`
- reconstruction should use the same normalized slash-separated relative path
  semantics defined above

This matches existing restore behavior and formalizes it as the default path
resolution strategy rather than a fallback curiosity.

### 4. Separate scan-time metadata from persisted metadata

The backup pipeline should treat path strings as transient scan context.

Recommended model:

- sources may emit normalized `Paths` in memory for filtering convenience
- backup scan logic may synthesize a path when incremental sources only emit a
  partial change set
- before hashing and persisting `FileMeta`, the engine clears `Paths`

This makes the persisted object reflect only durable identity and metadata,
while allowing source adapters to keep practical path-aware behavior.

### 5. Update restore, diff, and filter helpers to prefer derived paths

Any engine helper that needs a path should operate through a shared
reconstruction helper instead of directly assuming `meta.Paths[0]` exists.

That includes:

- restore path building
- path-filtered restore selection
- future diff/list formatting helpers that need a display path

The fast path for legacy snapshots may remain, but correctness must not depend
on stored `Paths`.

## Detailed behavior by source type

### Local and SFTP

- continue to compute normalized relative paths during `Walk()`
- use those paths for exclude evaluation and user-facing scan output
- persist only `Name`, `Parents`, and other durable metadata

### Google Drive and OneDrive full scans

- continue to build path maps during traversal for exclusion and subtree scoping
- emit normalized relative paths in memory where helpful
- persist metadata without `Paths`

### Incremental cloud sources

- may reconstruct paths from the previous tree when a change event does not
  include enough ancestry context
- use reconstructed normalized paths only for scan-time filtering
- persist final metadata without `Paths`

## Backward compatibility

- Existing snapshots containing `Paths` remain fully readable.
- New snapshots omit `Paths`, but old binaries that already reconstruct by
  parent chain for restore should continue to function for common cases.
- No repository format version bump is required because the JSON field already
  exists and may legally be empty.

Compatibility caveat:

- codepaths that still assume `Paths[0]` is always populated must be updated
  before the new write behavior is enabled broadly

## Performance impact

Expected benefits:

- smaller `filemeta/` objects, especially for deep trees
- less metadata hash churn from redundant path-string changes
- reduced object-store traffic for metadata-heavy snapshots

Expected costs:

- more parent-chain reconstruction during restore/list/diff paths

The extra reconstruction cost is acceptable because:

- restore already implements it
- path reconstruction is bounded by directory depth
- metadata size savings apply to every written entry

## Security considerations

- No new secret handling or credential behavior is introduced.
- Persisting fewer path strings slightly reduces duplicated sensitive path data
  in stored metadata objects.
- Snapshot consumers must continue to sanitize restore output paths against
  traversal and root escape attacks; this RFC does not weaken those checks.

## Testing strategy

- Unit tests for path normalization rules across local, SFTP, and cloud sources.
- Unit tests for path reconstruction from parent chains when `Paths` is absent.
- Regression tests for restore path filtering with snapshots that omit `Paths`.
- Mixed-compatibility tests covering old snapshots with stored `Paths` and new
  snapshots without them.
- Incremental-source tests ensuring exclude filtering still works when paths are
  reconstructed in memory.

## Rollout plan

1. Add shared helpers for normalized ephemeral path handling and derived-path
   reconstruction.
2. Update engine consumers so correctness no longer depends on persisted
   `Paths`.
3. Change backup persistence to clear `Paths` before hashing and writing new
   `FileMeta` objects.
4. Update docs/spec examples to show `paths` as optional legacy compatibility
   data.
5. Validate mixed old/new snapshot behavior in integration tests.

## Open questions

- Should `Paths` remain in the JSON schema indefinitely as a compatibility field,
  or be removed in a later schema-cleanup RFC?
- Should the engine cache reconstructed paths during restore/list operations to
  avoid repeated parent walks in very large trees?
- Should `FileMeta.Version` advance when new snapshots begin omitting `Paths`,
  even if the change is backward compatible?
