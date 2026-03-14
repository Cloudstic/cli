# RFC 0009: Unified Source Identity

- **Status:** Implemented
- **Date:** 2026-03-14
- **Affects:** `internal/core/models.go`, `pkg/source/*`, `internal/engine/backup.go`, `internal/engine/policy.go`

## Abstract

`SourceInfo` currently mixes stable lineage identity with display metadata. For example, `account` is used both as a matching key and as a human label. This causes fragile behavior for cloud accounts and creates field overloading (`volume_uuid`/`volume_label`) across unrelated providers.

This RFC introduces two explicit lineage fields, `identity` (container identity) and `path_id` (stable root-location identity), keeps `account` for friendly display only, and adds `drive_name` as a dedicated container label. The rollout is backward compatible for repositories with existing snapshots: new binaries can continue from old backups without rewriting old snapshot objects.

## Context

Today, previous snapshot lookup and retention grouping use legacy combinations:

- `type + volume_uuid + path` when `volume_uuid` is present.
- Otherwise `type + account + path`.

This works but is semantically inconsistent:

- `account` can be hostname, email, or `user@host`.
- `volume_uuid` can mean a local partition UUID or a cloud drive ID.
- `volume_label` can mean a disk label or a shared drive name.

We want clear separation:

- **Identity**: stable container key for lineage.
- **PathID**: stable root-location key inside a container.
- **Account / DriveName / Path**: human-friendly display fields.

## Goals

- Define one provider-agnostic lineage key per backup source.
- Keep display metadata separate from lineage identity.
- Preserve backward compatibility with existing repositories.
- Avoid snapshot migrations and avoid dual-writing to legacy fields.

## Non-goals

- No rewrite of existing snapshot objects.
- No immediate CLI flag redesign.
- No change to chunk/content dedup behavior.

## Proposal

### 1. SourceInfo schema

Add three fields:

- `identity`: stable source identity.
- `path_id`: stable identity of the selected root location inside the source container.
- `drive_name`: friendly container label.

```go
type SourceInfo struct {
    Type      string `json:"type"`
    Account   string `json:"account,omitempty"`    // friendly account/host label
    Path      string `json:"path,omitempty"`       // display path within the container
    Identity  string `json:"identity,omitempty"`   // stable lineage identity
    PathID    string `json:"path_id,omitempty"`    // stable selected-root identity
    DriveName string `json:"drive_name,omitempty"` // friendly drive/container name

    // Legacy fields kept for reading old snapshots.
    VolumeUUID  string `json:"volume_uuid,omitempty"`
    VolumeLabel string `json:"volume_label,omitempty"`
}
```

### 2. Identity mapping by source type

#### Local portable drive

- `identity`: partition GUID.
- `path_id`: absolute-style path from drive root (for example `/Photos`, `/`).
- `account`: hostname.
- `drive_name`: disk label.
- `path`: absolute-style display path from drive root (for example `/Photos`, `/`).

#### Local (non-portable)

When stable partition identity is unavailable or not applicable (for example root
filesystem backups or platforms/filesystems where UUID discovery is not
available):

- `identity`: hostname.
- `path_id`: absolute source path.
- `account`: hostname.
- `drive_name`: empty (or platform volume label when available).
- `path`: absolute source path.

This preserves existing behavior while still using the new `identity` field.

#### Google My Drive

- `identity`: stable Google account ID (opaque user identifier).
- `path_id`: root folder ID resolved from the selected path.
- `account`: account email (friendly display).
- `drive_name`: `My Drive`.
- `path`: backup path.

#### Google Shared Drive

- `identity`: shared drive ID.
- `path_id`: root folder ID resolved from the selected path.
- `account`: account email used to access the drive.
- `drive_name`: shared drive name.
- `path`: backup path.

#### SFTP

- `identity`: `user@host`.
- `path_id`: source path.
- `account`: `user@host`.
- `drive_name`: empty.
- `path`: source path.

### 3. Lineage key decision: use `path_id` to survive cloud folder rename/move

We do **not** concatenate `path` into `identity`.

Instead, lineage uses a dedicated `path_id` field so cloud folder rename/move does not break incremental continuity.

Lineage matching uses:

- `type + identity + path_id` when `identity` and `path_id` exist.

Rationale:

- Keeps `identity` as pure container identity (drive/account), reusable across paths.
- Avoids delimiter/escaping/parsing concerns in a composite string.
- Keeps display path (`path`) independent from lineage key.
- Makes Google/OneDrive lineage robust to folder rename and move because folder IDs are stable.

## Backward compatibility

No dual-writing to legacy fields.

New binary behavior:

1. If current source has `identity` and `path_id`, attempt `type + identity + path_id` match first.
2. If step 1 has no match, fallback to `type + identity + path` to bridge early rollouts where `path_id` may be missing.
3. Fallback to legacy `type + volume_uuid + path` for old portable-drive snapshots.
4. Fallback to legacy `type + account + path` for old snapshots.

Implications:

- New versions continue from old backups.
- Old versions may not recognize new `identity` semantics, which is acceptable.

## Engine changes

### Previous snapshot lookup (`backup.go`)

Preferred matching order:

1. `type + identity + path_id`
2. `type + identity + path` (bridge fallback when `path_id` absent)
3. `type + volume_uuid + path`
4. `type + account + path`

### Retention grouping (`policy.go`)

When grouping by account/path semantics, use:

1. `identity` as the account-like grouping token when present.
2. Else `volume_uuid`.
3. Else `account`.

For path grouping token, use:

1. `path_id` when present.
2. Else `path`.

## Implementation plan

1. Add `identity`, `path_id`, and `drive_name` to `core.SourceInfo`.
2. Populate new fields in source adapters:
   - `pkg/source/local_source.go`
   - `pkg/source/gdrive.go`
   - `pkg/source/gdrive_changes.go`
   - `pkg/source/onedrive.go`
   - `pkg/source/onedrive_changes.go`
   - `pkg/source/sftp_source.go`
3. Update matching logic in `internal/engine/backup.go`.
4. Update grouping/filter logic in `internal/engine/policy.go`.
5. Keep list output display-oriented (`account`, `drive_name`, `path`), while matching/grouping uses `identity`/`path_id`.
6. Add compatibility tests for old/new/mixed snapshot catalogs.

## Test matrix

- Local portable drive backed up on host A then host B, same partition GUID.
- Google My Drive with stable account ID and mutable email display.
- Google Shared Drive accessed by different accounts, same drive ID.
- Google folder rename and move with unchanged folder ID; incremental lineage must continue.
- Mixed repositories with old snapshots (no `identity`) and new snapshots (`identity` present).
- Mode switch continuity (`gdrive` and `gdrive-changes`) with same effective source.

## Open questions

- Confirm exact Google field for stable account identity in My Drive mode.
- Confirm exact OneDrive stable account field (`id` vs UPN fallback behavior).
- Confirm whether `path_id` should be emitted for all providers immediately or staged by provider.
- Decide whether to expose `identity` in default `list` output or JSON-only initially.
