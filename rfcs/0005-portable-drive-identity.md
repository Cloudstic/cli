# RFC 0005: Portable Drive Identity

* **Status:** Proposed
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
    Path        string `json:"path,omitempty"`      // mount point at time of backup (informational)
    VolumeUUID  string `json:"volume_uuid,omitempty"` // stable identity across mounts/machines
    VolumeLabel string `json:"volume_label,omitempty"` // human-readable name (e.g. "MyDrive")
    FsType      string `json:"fs_type,omitempty"`   // proposed in RFC 0004
}
```

`VolumeUUID` is the RFC 4122 UUID string as returned by the OS (e.g. `A1B2C3D4-1234-5678-ABCD-EF0123456789`). `VolumeLabel` is the volume name set at format time (e.g. `MyDrive`). Both are optional; when absent the engine falls back to the existing `account+path` matching.

These fields are backward-compatible: old snapshots have neither field; the engine's fallback logic handles them transparently (see section 2.3).

### 2.2 Obtaining the volume UUID per platform

UUID collection is isolated in `local_source_unix.go` (established by RFC 0004):

#### macOS

Use `getattrlist(2)` with `ATTR_VOL_UUID` on the mount point path. The result is a 16-byte UUID. Additionally, `statfs.F_vol_name` or a second `getattrlist(ATTR_VOL_NAME)` call gives the volume label.

```go
// macOS: getattrlist on the mount root
type volAttrs struct {
    length uint32
    uuid   [16]byte
    name   [256]byte
}
attrList := syscall.AttrList{
    Bitmapcount: syscall.ATTR_BIT_MAP_COUNT,
    Volattr:     syscall.ATTR_VOL_UUID | syscall.ATTR_VOL_NAME,
}
// syscall.Getattrlist(path, &attrList, unsafe.Pointer(&buf), ...)
```

#### Linux

Linux `statfs(2)` does not expose a UUID. The UUID is stored on-disk and readable from two stable locations:

1. **`/dev/disk/by-uuid/`**: a directory of symlinks `{uuid} → ../../sdXN`. Find the device for the path via `statfs.Fsid` or `/proc/mounts`, then look for a matching symlink.
2. **`/proc/mounts` + `blkid`**: parse `/proc/mounts` to find the device for the mount point, then read the UUID from the kernel via `ioctl(BLKID)` or by reading the filesystem superblock directly (ext4: offset 0x468, 16 bytes; btrfs: different offset).

**Recommended implementation for Linux**: read from `/dev/disk/by-uuid/`. It requires no additional privileges and is stable on all modern distributions:

```go
// Find mount point for path via statfs → Fsid → /proc/mounts
// Then scan /dev/disk/by-uuid/ for a symlink pointing to that device
func findVolumeUUIDLinux(path string) (string, error) {
    device, err := deviceForPath(path)     // parses /proc/mounts
    if err != nil {
        return "", err
    }
    entries, _ := os.ReadDir("/dev/disk/by-uuid")
    for _, e := range entries {
        target, _ := os.Readlink("/dev/disk/by-uuid/" + e.Name())
        if filepath.Base(target) == filepath.Base(device) {
            return e.Name(), nil
        }
    }
    return "", nil
}
```

This approach does not require `blkid`, `libblkid`, or root privileges.

#### Stub (Windows, plan9)

Return `"", ""` — UUID and label are left empty, existing matching logic applies.

#### When UUID is unavailable

UUID collection fails silently. If `VolumeUUID` cannot be determined (unmounted image, procfs unavailable, exotic filesystem), it remains empty and matching falls back to `account+path`. A debug-level log entry is emitted.

### 2.3 `findPreviousSnapshot` — UUID-first matching

```go
func (bm *BackupManager) findPreviousSnapshot(info core.SourceInfo) *core.Snapshot {
    entries, err := LoadSnapshotCatalog(bm.store)
    if err != nil {
        return nil
    }

    // Pass 1: UUID match (cross-machine, mount-point-agnostic)
    if info.VolumeUUID != "" {
        for _, e := range entries {
            if e.Snap.Source != nil &&
                e.Snap.Source.Type == info.Type &&
                e.Snap.Source.VolumeUUID == info.VolumeUUID {
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

**Pass 1** is tried first whenever `VolumeUUID` is non-empty. It finds the most recent snapshot for this drive regardless of which machine performed it or where it was mounted. The `entries` slice is already sorted newest-first by `LoadSnapshotCatalog`, so the first match is the correct previous snapshot.

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
    uuid, label := detectVolumeIdentity(rootPath) // platform helper
    return &LocalSource{
        rootPath:    rootPath,
        volumeUUID:  uuid,
        volumeLabel: label,
        // ...
    }
}

func (s *LocalSource) Info() core.SourceInfo {
    hostname, _ := os.Hostname()
    absPath, _  := filepath.Abs(s.rootPath)
    return core.SourceInfo{
        Type:        "local",
        Account:     hostname,
        Path:        absPath,
        VolumeUUID:  s.volumeUUID,
        VolumeLabel: s.volumeLabel,
        FsType:      s.fsType, // RFC 0004
    }
}
```

`detectVolumeIdentity` is called once at construction, not on every `Info()` call.

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

### `pkg/source/local_source_unix.go` *(extended from RFC 0004)*

1. Add `func detectVolumeIdentity(path string) (uuid, label string)`:
   * macOS: `getattrlist` with `ATTR_VOL_UUID | ATTR_VOL_NAME`.
   * Linux: `deviceForPath` + `/dev/disk/by-uuid/` scan.

### `pkg/source/local_source_stub.go` *(extended from RFC 0004)*

1. Stub `detectVolumeIdentity` returning `"", ""`.

### `cmd/cloudstic/main.go`

1. Add `-volume-uuid` flag for the `backup` command when source is `local`.

### Tests

1. `local_source_unix_test.go`: verify `detectVolumeIdentity` returns a non-empty UUID for a temp directory on the test runner's native filesystem; verify it is a valid UUID format.
2. `backup_test.go`: unit test for `findPreviousSnapshot` UUID-first pass: create two mock snapshots for different machines with the same `VolumeUUID`; verify the most recent one is returned regardless of `Account`/`Path`.
3. `backup_test.go`: verify legacy fallback: snapshots without `VolumeUUID` are still found by `account+path`.
4. `policy_test.go`: verify that two snapshots from different machines but same `VolumeUUID` are grouped together under the same retention key.

---

## 4. Trade-offs and Constraints

### fileID stability across machines

`fileID` in the `local` source is the path relative to `rootPath`. For a portable drive mounted at `/Volumes/MyDrive` on macOS and `/media/user/MyDrive` on Linux, the **relative** paths are identical. A file at `/Volumes/MyDrive/Photos/img.jpg` has `fileID = Photos/img.jpg` on both machines. The HAMT lookup, change detection, and dedup all work correctly without any changes to `fileID` semantics.

### Concurrent access from multiple machines

If machine A and machine B both attempt to back up the same drive simultaneously, the repository lock (`AcquireSharedLock`) prevents concurrent writes. However, two machines cannot physically have the same external drive plugged in simultaneously under normal circumstances. Network drives shared via NFS/SMB are a different case and are out of scope.

### `index/latest` with multiple machines

`index/latest` always points to the globally most recent snapshot regardless of source. When the drive alternates between machines, `index/latest` correctly points to whichever machine ran last. Commands like `restore --latest` do not filter by source and will restore the most recent snapshot, which is the intended behavior.

### Retention across machines

Grouping all backups of a drive into one retention group means a retention policy of `--keep-daily 7` keeps only 7 daily snapshots total across all machines. If machine A runs daily and machine B runs weekly, A's 7 most recent dailies are kept and B's runs are included in the count. Users who want per-machine retention can use tags (`--tag mac-studio`, `--tag macbook-pro`) and filter `forget` by tag.

### UUID unavailability on non-standard filesystems

`tmpfs`, `ramfs`, `procfs`, `sysfs`, and some network filesystems (NFS, SMB) do not have volume UUIDs. UUID detection returns empty, and the legacy `account+path` matching applies. This is correct: these are not portable drives.

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
