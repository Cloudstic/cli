# RFC 0005: Portable Drive Identity

* **Status:** Implemented
* **Date:** 2026-03-07
* **Affects:** `internal/core/models.go`, `internal/engine/backup.go`, `internal/engine/policy.go`, `pkg/source/local_source.go`, `cmd/cloudstic/main.go`

## Abstract

The `local` source identifies a backup source by `(type, account, path)` where `account` is the hostname and `path` is the absolute mount point. For portable and external drives this is wrong: the mount point changes across machines (`/Volumes/MyDrive` on macOS A, `/media/user/MyDrive` on Linux B) and the hostname differs entirely. As a result, `findPreviousSnapshot` never finds a match when the drive moves between machines, causing a full re-upload of every file on every run regardless of what actually changed. This RFC introduces volume-level identity via a `VolumeUUID` field on `SourceInfo`, making the previous-snapshot lookup machine-agnostic and enabling true cross-machine incremental backup for portable drives.

---

## 1. Context

### 1.1 Current source identity and previous-snapshot lookup

`SourceInfo` is stored in every snapshot and used by `findPreviousSnapshot` to locate the baseline for an incremental run:

```go
// internal/engine/backup.go
func (bm *BackupManager) findPreviousSnapshot(info core.SourceInfo) *core.Snapshot {
    for _, e := range entries {
        if e.Snap.Source.Type    == info.Type    &&
           e.Snap.Source.Account == info.Account &&
           e.Snap.Source.Path    == info.Path    {
            return &snap
        }
    }
    return nil
}
```

All three fields must match exactly. For a portable drive:

| Field | Machine A | Machine B | Stable? |
|---|---|---|---|
| `Type` | `local` | `local` | ✓ |
| `Account` | `mac-studio.local` | `macbook-pro.local` | ✗ |
| `Path` | `/Volumes/MyDrive` | `/media/user/MyDrive` | ✗ |

`findPreviousSnapshot` returns `nil` → `oldRoot` is empty → full scan → every file is treated as new → full re-upload.

Content deduplication at the chunk level still prevents re-storing identical bytes, but **all metadata objects (`filemeta/`, `node/`) are rewritten from scratch** and the progress reporting shows every file as "new". For a 500 GB drive with 200,000 files, this means scanning and hashing all 200,000 files and writing 200,000 new `filemeta/` objects even when nothing changed.

### 1.2 Mount point instability on the same machine

Even on a single macOS machine, the mount point is not stable:

* If a second disk with the same label is attached, macOS mounts it at `/Volumes/MyDrive 1`.
* A user can rename the volume or the mount point, changing `Path`.
* After a macOS update, some volumes are remounted at different paths.

Hostname+path is a poor proxy for "which drive". It is only reliable for the root filesystem of a fixed machine.

### 1.3 What "same drive" means

A drive has a stable identity when its **volume UUID** matches. The volume UUID is embedded in the filesystem superblock at format time:

* It survives: remounts, mount point changes, machine changes, renaming the volume label.
* It changes: reformatting (`diskutil eraseDisk`, `mkfs`), which correctly starts a new snapshot lineage.
* Edge cases: cloning a disk image may duplicate the UUID (VM snapshots, `dd` copies). This is rare and documented but not handled specially.

### 1.4 Remote sources are unaffected

Remote and network sources already produce machine-agnostic identity without any changes:

| Source | `Account` | `Path` | Cross-machine stable? |
|---|---|---|---|
| `gdrive` / `gdrive-changes` | Gmail address (`user@gmail.com`) | Drive/folder ID (`drivePath(driveID, rootFolderID)`) | ✓ |
| `onedrive` / `onedrive-changes` | User principal name (`user@company.com`) | `onedrive://` | ✓ |
| `sftp` | `user@host` | Root path on server | ✓ (tied to server, not client) |

`Account` for cloud sources is the authenticated account identifier, not the hostname of the machine running the backup. Plugging in from a different machine with the same OAuth token produces an identical `SourceInfo`, so `findPreviousSnapshot` finds the previous snapshot normally. **No changes are required for any remote source.**

`VolumeUUID` in `SourceInfo` is a `local`-only concept. The UUID-first pass in `findPreviousSnapshot` is guarded by `VolumeUUID != ""`, so it is never attempted for `gdrive`, `onedrive`, or `sftp` entries — those fall straight through to the existing `account+path` match, which already works correctly.

The one edge case worth noting for `sftp`: if the server's hostname or IP changes (DNS rename, server migration), `Account = user@oldhost` no longer matches. This is analogous to the local source's `Path` instability, but for SFTP the host is the stable identity marker and migration is an explicit, operator-controlled event. A future RFC could introduce a `-sftp-server-id` override similar to `-volume-uuid` proposed here.

### 1.5 The cross-machine workflow

A user has a 1 TB external drive they back up from both their Mac and their Linux workstation at different times. With the current model:

```
Day 1 — Mac:   full scan, 200,000 filemeta written, snapshot S1
Day 2 — Linux: full scan (no S1 found), 200,000 filemeta written, snapshot S2
Day 3 — Mac:   full scan (S2 not matched, S1 found), only changed files...
```

With volume-UUID-based matching:

```
Day 1 — Mac:   full scan, 200,000 filemeta written, snapshot S1
Day 2 — Linux: UUID match → S1 found, incremental scan, only changed files
Day 3 — Mac:   UUID match → S2 found, incremental scan, only changed files
```

---

## 2. Proposal

### 2.1 `SourceInfo` new fields

```go
type SourceInfo struct {
    Type        string `json:"type"`
    Account     string `json:"account,omitempty"`  // hostname (informational)
    Path        string `json:"path,omitempty"`      // volume-relative path when VolumeUUID is set; absolute otherwise
    VolumeUUID  string `json:"volume_uuid,omitempty"` // stable identity across mounts/machines
    VolumeLabel string `json:"volume_label,omitempty"` // human-readable name (e.g. "MyDrive")
    FsType      string `json:"fs_type,omitempty"`   // proposed in RFC 0004
}
```

`VolumeUUID` is the RFC 4122 UUID string as returned by the OS (e.g. `A1B2C3D4-1234-5678-ABCD-EF0123456789`). `VolumeLabel` is the volume name set at format time (e.g. `MyDrive`). Both are optional; when absent the engine falls back to the existing `account+path` matching.

These fields are backward-compatible: old snapshots have neither field; the engine's fallback logic handles them transparently (see section 2.3).

### 2.2 Obtaining the volume UUID per platform

UUID collection is isolated in platform-specific files (`local_source_darwin.go`, `local_source_linux.go`, `local_source_windows.go`, `local_source_stub.go`).

The primary goal is **cross-OS stability**: the same physical drive should produce the same UUID regardless of which operating system performs the backup. To achieve this, the implementation prefers the **GPT partition UUID** over platform-specific volume UUIDs. The GPT partition UUID is a standard UUID stored in the GUID Partition Table and is identical on every OS that reads the same partition table.

**Detection priority (per platform):**

1. GPT partition UUID (cross-OS stable) — preferred
2. Platform-specific volume/filesystem UUID — fallback
3. Manual override via `-volume-uuid` — always takes final precedence

#### macOS

1. **GPT partition UUID** (preferred): Use `syscall.Statfs` to obtain the BSD device name from `Mntfromname` (e.g. `/dev/disk2s1`), then run `diskutil info -plist <device>` and parse the `DiskUUID` key from the XML plist output. This returns the GPT partition UUID, which is identical to the value Linux reads from the partition table.

2. **Volume UUID** (fallback): Use `getattrlist(2)` with `ATTR_VOL_UUID`. This returns a macOS-specific 128-bit UUID that differs from what Linux reports for the same drive. Used only when the GPT partition UUID is unavailable (e.g. MBR-formatted drives).

3. **Volume label**: `getattrlist(2)` with `ATTR_VOL_NAME` provides the human-readable volume name.

#### Linux

1. **GPT partition UUID** (preferred): Find the device via `/proc/mounts` + `syscall.Stat` (matching `st.Dev`), then scan `/dev/disk/by-partuuid/` symlinks for a matching device. The `by-partuuid` directory contains GPT partition UUIDs.

2. **Filesystem UUID** (fallback): Scan `/dev/disk/by-uuid/` symlinks. This returns the filesystem-level UUID (e.g. ext4 superblock UUID, FAT32 volume serial). Used when `by-partuuid` has no match (MBR-formatted drives).

3. **Volume label**: Scan `/dev/disk/by-label/` symlinks.

All UUIDs are normalized to uppercase for consistent matching across platforms.

#### Windows

1. **GPT partition UUID** (preferred): Use `GetVolumePathName` to determine the volume mount point, then open the volume with `CreateFile` (`\\.\C:`) and call `DeviceIoControl` with `IOCTL_DISK_GET_PARTITION_INFO_EX`. For GPT partitions, `PARTITION_INFORMATION_EX.Gpt.PartitionId` contains the GPT partition UUID — the same UUID that macOS and Linux detect from the partition table. Returns empty for MBR partitions.

2. **Volume label**: `GetVolumeInformation` provides the human-readable volume name.

3. **Mount point**: `GetVolumePathName` returns the drive root (e.g. `C:\`).

No elevation is required — `CreateFile` with zero access rights and `FILE_SHARE_READ | FILE_SHARE_WRITE` is sufficient for partition info queries.

#### Stub (plan9, etc.)

Return `"", "", ""` — UUID, label, and mount point are left empty, existing matching logic applies. Users can use `-volume-uuid` for manual override.

#### Cross-OS compatibility matrix

| Partition table | UUID source | macOS ↔ Linux | macOS/Linux ↔ Windows |
|---|---|---|---|
| GPT | Partition UUID (`DiskUUID` / `by-partuuid` / `DeviceIoControl`) | ✓ Identical | ✓ Identical |
| MBR | Platform-specific (getattrlist / `by-uuid` / none) | ✗ Different | ✗ Different |
| MBR + `-volume-uuid` | Manual override | ✓ User-controlled | ✓ User-controlled |

Modern portable drives (formatted as exFAT or APFS on GPT) produce matching UUIDs automatically. Older MBR-formatted FAT32 drives require `-volume-uuid` for cross-OS use.

#### When UUID is unavailable

UUID collection fails silently. If `VolumeUUID` cannot be determined (unmounted image, procfs unavailable, exotic filesystem), it remains empty and matching falls back to `account+path`. A debug-level log entry is emitted.

### 2.3 `findPreviousSnapshot` — UUID-first matching

```go
func (bm *BackupManager) findPreviousSnapshot(info core.SourceInfo) *core.Snapshot {
    entries, err := LoadSnapshotCatalog(bm.store)
    if err != nil {
        return nil
    }

    // Pass 1: UUID + path match (cross-machine, mount-point-agnostic)
    if info.VolumeUUID != "" {
        for _, e := range entries {
            if e.Snap.Source != nil &&
                e.Snap.Source.Type == info.Type &&
                e.Snap.Source.VolumeUUID == info.VolumeUUID &&
                e.Snap.Source.Path == info.Path {
                snap := e.Snap
                return &snap
            }
        }
    }

    // Pass 2: legacy match (type + account + path)
    for _, e := range entries {
        if e.Snap.Source != nil &&
            e.Snap.Source.Type == info.Type &&
            e.Snap.Source.Account == info.Account &&
            e.Snap.Source.Path == info.Path {
            snap := e.Snap
            return &snap
        }
    }

    return nil
}
```

**Pass 1** is tried first whenever `VolumeUUID` is non-empty. It finds the most recent snapshot for the same drive **and the same sub-directory** (by matching `Type + VolumeUUID + Path`). Because `Path` is stored relative to the volume mount point when a UUID is present (e.g. `"."` for the drive root, `"Photos"` for a sub-directory), this match works regardless of which machine performed the backup or where the drive was mounted. Users can independently back up different sub-directories of the same drive, and each sub-directory maintains its own snapshot lineage.

**Pass 2** is the existing logic. It activates when:

* `VolumeUUID` is empty (UUID detection failed, or Windows stub).
* The drive has no previous snapshot from any machine (pass 1 found nothing).
* The source is not a local drive (e.g. `gdrive`, `sftp`) — those never set `VolumeUUID`.

### 2.4 Retention policy grouping

`internal/engine/policy.go` groups snapshots by source identity for `forget` policies. The current grouping key is `(type, account, path)`. With this RFC, the key becomes:

```go
func sourceKey(s *core.SourceInfo) string {
    if s == nil {
        return ""
    }
    if s.VolumeUUID != "" {
        // Group all backups of the same volume together, regardless of machine
        return s.Type + ":" + s.VolumeUUID
    }
    // Legacy: group by machine + path
    return s.Type + ":" + s.Account + ":" + s.Path
}
```

**Implication**: all snapshots of the same external drive — from any machine — share one retention group. A `--keep-daily 7` policy keeps the 7 most recent daily snapshots across all machines combined. This is the correct semantic: the drive is the thing being retained, not the machine.

### 2.5 `list` command display

The `list` command currently shows `Source: local@hostname:/path`. With `VolumeLabel` available, a more useful display is:

```
Snapshot   Created               Source                              Files
────────   ──────────────────    ─────────────────────────────────   ──────
snap/a1b2  2026-03-07 09:00      local: MyDrive (mac-studio.local)   212,450
snap/c3d4  2026-03-06 21:00      local: MyDrive (macbook-pro.local)  212,447
```

When `VolumeLabel` is empty, fall back to the existing `account:path` display.

### 2.6 `LocalSource.Info()` changes

`LocalSource.Info()` must populate `VolumeUUID` and `VolumeLabel` at source construction time, calling the platform helper once on `rootPath`:

```go
func NewLocalSource(rootPath string, opts ...LocalOption) *LocalSource {
    // ...
    uuid, label, mountPoint := detectVolumeIdentity(rootPath) // platform helper
    return &LocalSource{
        rootPath:         rootPath,
        volumeUUID:       uuid,
        volumeLabel:      label,
        volumeMountPoint: mountPoint,
        // ...
    }
}

func (s *LocalSource) Info() core.SourceInfo {
    hostname, _ := os.Hostname()
    absPath, _  := filepath.Abs(s.rootPath)

    // When volume UUID is present, store the path relative to the volume
    // mount point. This makes path matching work across machines where
    // mount points differ, and allows independent backups of different
    // sub-directories on the same drive.
    infoPath := absPath
    if s.volumeUUID != "" && s.volumeMountPoint != "" {
        realAbs, errA := filepath.EvalSymlinks(absPath)
        realMount, errM := filepath.EvalSymlinks(s.volumeMountPoint)
        if errA == nil && errM == nil {
            if rel, err := filepath.Rel(realMount, realAbs); err == nil {
                infoPath = filepath.ToSlash(rel)
            }
        }
    }

    return core.SourceInfo{
        Type:        "local",
        Account:     hostname,
        Path:        infoPath,
        VolumeUUID:  s.volumeUUID,
        VolumeLabel: s.volumeLabel,
        FsType:      s.fsType, // RFC 0004
    }
}
```

`detectVolumeIdentity` is called once at construction, not on every `Info()` call. It also returns the volume mount point, which is used to compute the relative path.

The `volumeMountPoint` field stores the detected mount point (e.g. `/Volumes/MyDrive` on macOS, `/media/user/MyDrive` on Linux). Symlinks are resolved before computing the relative path (important on macOS where `/var` → `/private/var`).

### 2.7 Explicit override flag

Users can override the detected UUID via `-volume-uuid <uuid>`. This handles:

* Filesystems where UUID detection is unsupported.
* Users who want to tie backups of two physically different drives to the same snapshot lineage (e.g. a mirrored pair where the mirror was created with `dd`).

```
cloudstic backup -source local -path /mnt/backup -volume-uuid "A1B2C3D4-..."
```

When an explicit UUID is provided it takes precedence over the detected value.

---

## 3. Required Changes

### `internal/core/models.go`

1. Add `VolumeUUID string` and `VolumeLabel string` to `SourceInfo`.

### `internal/engine/backup.go`

1. Update `findPreviousSnapshot` with two-pass logic (UUID-first, legacy fallback) as shown in section 2.3.

### `internal/engine/policy.go`

1. Update `sourceKey` (or equivalent grouping logic) to use `VolumeUUID` when present.

### `pkg/source/local_source.go`

1. Add `volumeUUID string` and `volumeLabel string` fields to `LocalSource`.
2. Call `detectVolumeIdentity(rootPath)` in `NewLocalSource`.
3. Populate both fields in `Info()`.
4. Add `WithVolumeUUID(uuid string) LocalOption` for the explicit override.

### `pkg/source/local_source_unix.go` → platform-specific files

Implemented as three separate files (one per platform):

1. **`local_source_darwin.go`**: `detectVolumeIdentity` tries GPT partition UUID via `Statfs` + `diskutil info -plist`, falls back to `getattrlist(ATTR_VOL_UUID)`. Includes `extractPlistValue` XML parser.
2. **`local_source_linux.go`**: `detectVolumeIdentity` tries `/dev/disk/by-partuuid/` (GPT), falls back to `/dev/disk/by-uuid/` (filesystem UUID).
3. **`local_source_stub.go`**: Returns `"", ""` for unsupported platforms.

### `cmd/cloudstic/main.go`

1. Add `-volume-uuid` flag for the `backup` command when source is `local`.

### Tests

1. `local_source_unix_test.go`: verify `detectVolumeIdentity` returns a non-empty UUID for a temp directory on the test runner's native filesystem; verify it is a valid UUID format.
2. `backup_test.go`: unit test for `findPreviousSnapshot` UUID-first pass: create two mock snapshots for different machines with the same `VolumeUUID` and volume-relative `Path`; verify the most recent one is returned regardless of `Account`.
3. `backup_test.go`: verify legacy fallback: snapshots without `VolumeUUID` are still found by `account+path`.
4. `backup_test.go`: verify that two snapshots with the same `VolumeUUID` but different sub-directory paths (e.g. `"Photos"` vs `"Documents"`) do **not** match each other.
5. `policy_test.go`: verify that two snapshots from different machines but same `VolumeUUID` and `Path` are grouped together under the same retention key.
6. `policy_test.go`: verify that two snapshots with the same `VolumeUUID` but different `Path` values are in **separate** retention groups.

---

## 4. Trade-offs and Constraints

### fileID stability across machines

`fileID` in the `local` source is the path relative to `rootPath`, **normalized to forward slashes** (via `filepath.ToSlash`). For a portable drive mounted at `/Volumes/MyDrive` on macOS and `/media/user/MyDrive` on Linux, the relative paths are identical (`Photos/img.jpg`). On Windows, backslashes are converted to forward slashes before storage so the same file produces the same `fileID` regardless of OS. The HAMT lookup, change detection, and dedup all work correctly without any changes to `fileID` semantics.

Similarly, the `Path` field in `SourceInfo` is stored as a **volume-relative path** (normalized to forward slashes) when `VolumeUUID` is set — e.g. `"."` for the drive root, `"Photos"` for a sub-directory. This ensures snapshot matching works across machines regardless of mount point. Without `VolumeUUID`, `Path` retains the absolute path for backward compatibility with non-portable sources.

### Concurrent access from multiple machines

If machine A and machine B both attempt to back up the same drive simultaneously, `backup` acquires a **shared lock** (`index/lock.shared/<timestamp>`) at run start. Multiple shared locks can coexist, so two simultaneous backups are allowed — each writes its own snapshot and both complete normally. A `prune` run holds an **exclusive lock** (`index/lock.exclusive`) and cannot start until all active shared locks are released; conversely, `backup` and `restore` fail immediately if an exclusive lock is held. Lock TTL is 1 minute with a 30-second background refresh; a crashed holder's lock expires automatically. In practice, two machines cannot have the same external drive plugged in simultaneously, so concurrent backup of the same portable drive is not a realistic scenario. Network drives shared via NFS/SMB are out of scope.

### `index/latest` with multiple machines

`index/latest` always points to the globally most recent snapshot regardless of source. When the drive alternates between machines, `index/latest` correctly points to whichever machine ran last. Commands like `restore --latest` do not filter by source and will restore the most recent snapshot, which is the intended behavior.

### Retention across machines

Grouping all backups of a drive into one retention group means a retention policy of `--keep-daily 7` keeps only 7 daily snapshots total across all machines. If machine A runs daily and machine B runs weekly, A's 7 most recent dailies are kept and B's runs are included in the count. Users who want per-machine retention can use tags (`--tag mac-studio`, `--tag macbook-pro`) and filter `forget` by tag.

### UUID unavailability on non-standard filesystems

`tmpfs`, `ramfs`, `procfs`, `sysfs`, and some network filesystems (NFS, SMB) do not have volume UUIDs. UUID detection returns empty, and the legacy `account+path` matching applies. This is correct: these are not portable drives.

### Directories spanning multiple drives

The volume UUID is determined once at source construction from the backup root path. If the backup directory contains mount points for other filesystems (e.g. `/data/external` is a separate partition mounted under `/data`), `filepath.Walk` crosses into those sub-mounts transparently — the files are included in the backup, but the volume UUID and mount-relative path reflect only the root path's filesystem. This is acceptable because backup and dedup operate on file content, not on volume boundaries. However, users should back up each portable drive as a separate source rather than backing up a parent directory that spans multiple drives, since the volume identity would not accurately represent the sub-mounted volumes.

Symlinks to other volumes are **not followed** by `filepath.Walk` (which uses `os.Lstat`). Only direct mount points within the directory tree are traversed.

### VM disk images and `dd` copies

If a drive is cloned with `dd` or a VM snapshot duplicates a disk image, the clone has the same UUID as the original. The engine would treat them as the same source and attempt incremental backup against the other's snapshot. This may produce correct results (if the files are identical) or spurious diffs (if they diverged). Users in this scenario should use `-volume-uuid` with a manually assigned distinct UUID.

---

## 5. Alternatives Considered

### Use volume label as stable identity

Volume labels are user-settable and not guaranteed to be unique (two drives can both be named "Backup"). UUID is a 128-bit random value; collision probability is astronomically small. Label is kept as a display field but not used for matching.

### Match by `(type, path)` ignoring `account`

Dropping the hostname from the match would make cross-machine backups work for fixed mount points (e.g. always mounted at `/mnt/backup`). But mount points are themselves unstable for portable drives (macOS auto-mounts at `/Volumes/<label>` and appends a counter on conflict). UUID is strictly more reliable.

### Separate repositories per machine

Users could maintain a separate backup repository per machine. This avoids the cross-machine matching problem entirely but forfeits content-level deduplication between machines (the same unchanged file on the drive is stored twice) and complicates retention management. A single shared repository with UUID-based matching is the better default.

### Store UUID in `FileMeta.Extra` instead of `SourceInfo`

UUID is a property of the source as a whole, not of individual files. Storing it per-file in `FileMeta` would be redundant and would add overhead to every metadata object. `SourceInfo` in the snapshot is the correct location.
