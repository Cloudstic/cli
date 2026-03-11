# RFC 0004: Extended File Attributes

* **Status:** Implemented
* **Date:** 2026-03-07
* **Affects:** `pkg/source/local_source.go`, `pkg/source/sftp_source.go`, `internal/core/models.go`, `internal/engine/backup_scan.go`, `internal/engine/restore.go`

## Abstract

POSIX extended attributes (xattrs), UNIX file permission mode, numeric ownership (uid/gid), file creation time (btime), and per-file flags (immutable, hidden, append-only) are first-class filesystem metadata that Cloudstic currently silences. The `local` source emits only name, size, mtime, and a string owner; the `sftp` source emits the same subset. After a restore, all of this metadata is lost. This RFC extends `FileMeta` with six new fields — `mode`, `uid`, `gid`, `btime`, `flags`, and `xattrs` — and introduces `SourceInfo.FsType` to record the filesystem type at snapshot time. It describes how the `local` and `sftp` sources collect each field, how change detection incorporates them, and how the restore path replays them.

---

## 1. Context

### 1.1 What are extended attributes?

Extended attributes (xattrs) are arbitrary, named byte-string pairs attached to filesystem inodes. They complement the fixed POSIX metadata (owner, group, permissions, timestamps) with application-defined information. Common real-world uses:

| Attribute name | Set by | Purpose |
|---|---|---|
| `user.checksum` | backup tools, package managers | content integrity tag |
| `com.apple.quarantine` | macOS Gatekeeper | download origin quarantine flag |
| `com.apple.metadata:kMDItemWhereFroms` | macOS Spotlight | download URL |
| `security.selinux` | SELinux | mandatory access control label |
| `security.capability` | Linux kernel | POSIX capabilities |
| `user.mime_type` | various | content type hint |

On Linux, xattrs are grouped into namespaces: `user.*`, `system.*`, `security.*`, `trusted.*`. User-space programs can only read/write `user.*` without elevated privileges. On macOS there are no namespaces; all attributes live in a single flat key space.

### 1.2 UNIX file mode and ownership

The POSIX `mode_t` encodes:

* **permission bits**: owner/group/other read/write/execute (rwxrwxrwx)
* **setuid / setgid / sticky bits**

These 12 bits are currently not captured in `FileMeta`. After a restore, every file lands with default `0644`/`0755` umask-governed permissions, which is incorrect for scripts, configuration files, or anything with setuid bits.

The `Owner` field on `FileMeta` stores a display string (Google account email, hostname) rather than numeric POSIX `uid`/`gid`. Numeric IDs are what `chown(2)` requires; without them, ownership cannot be restored on POSIX targets.

### 1.3 Birth time (btime)

`FileMeta` records `mtime` (last modification time) but not `btime` (file creation / birth time). Availability:

| Platform | Source | Notes |
|---|---|---|
| macOS | `syscall.Stat_t.Birthtimespec` | Always available on HFS+/APFS |
| Linux | `unix.Statx` with `STATX_BTIME` | Available on ext4, btrfs, XFS ≥ 5.10; kernel ≥ 4.11 |
| Linux (older) | not available | `statx` returns zero; field omitted |
| SFTP | not in SFTPv3 Attrs | Omitted |

`btime` is important for applications that rely on file creation time (e.g. photo managers, build systems that key on creation date).

### 1.4 File flags

Both macOS/BSD and Linux expose per-inode flags that are independent of mode bits:

| Platform | API | Key flags |
|---|---|---|
| macOS / BSD | `chflags(2)`, `stat.Flags` | `UF_IMMUTABLE`, `UF_APPEND`, `UF_HIDDEN`, `UF_NODUMP`, `SF_IMMUTABLE`, `SF_APPEND` |
| Linux | `ioctl(FS_IOC_GETFLAGS)` | `FS_IMMUTABLE_FL`, `FS_APPEND_FL`, `FS_NODUMP_FL`, `FS_COMPR_FL` |

An immutable file restored without its immutable flag is silently wrong. These flags are not recoverable from mode bits or xattrs.

### 1.5 Filesystem type

The filesystem type (e.g. `apfs`, `ext4`, `btrfs`, `zfs`, `ntfs`) is not recorded anywhere in a snapshot. Knowing the source filesystem enables:

* **Capability auditing**: understanding which metadata fields were meaningfully populated at backup time (e.g. btime is always present on APFS, never on FAT32).
* **Restore fidelity warnings**: alerting the user when the restore target cannot represent the source metadata.
* **Future optimisations**: FS-native features (btrfs send, ZFS send, APFS cloning) can be gated on this field.

### 1.6 Current gaps

`LocalSource.Walk` builds `FileMeta` using only `os.FileInfo`:

```go
meta := core.FileMeta{
    FileID:  relPath,
    Name:    filepath.Base(path),
    Type:    fileType,
    Parents: parents,
    Paths:   []string{relPath},
    Size:    info.Size(),
    Mtime:   info.ModTime().Unix(),
}
```

`Mode`, `Uid`, `Gid`, `Btime`, `Flags`, and `Xattrs` are never populated. `SourceInfo` carries no filesystem type. On restore, `ZipWriter` writes file content but applies no metadata beyond timestamps.

### 1.7 SFTP scope

The SSH File Transfer Protocol v3 (the universal baseline) carries `permissions`, `uid`, `gid`, `atime`, and `mtime` in its `Attrs` struct. Extended attributes are defined only in drafts of SFTPv6, which virtually no server implements; btime and file flags are similarly absent. This RFC scopes xattr and flag collection to the `local` source only. The `sftp` source gains `Mode`, `Uid`, and `Gid` capture from `Attrs` — the fields SFTPv3 does carry — but nothing beyond that.

---

## 2. Proposal

### 2.1 `FileMeta` changes

Add six new optional fields to `FileMeta`:

```go
type FileMeta struct {
    // ... existing fields ...
    Mode   uint32            `json:"mode,omitempty"`   // POSIX permission bits (st_mode & 0xFFF)
    Uid    uint32            `json:"uid,omitempty"`    // POSIX user ID
    Gid    uint32            `json:"gid,omitempty"`    // POSIX group ID
    Btime  int64             `json:"btime,omitempty"`  // birth/creation time, Unix seconds; 0 = not available
    Flags  uint32            `json:"flags,omitempty"`  // per-file flags (chflags / FS_IOC_GETFLAGS)
    Xattrs map[string][]byte `json:"xattrs,omitempty"` // attribute name → raw bytes
}
```

**`Mode`** stores the lower 12 bits of the POSIX mode (`permissions | setuid | setgid | sticky`). The file type bits are excluded — they are already encoded in `FileMeta.Type`. Value `0` means "not captured".

**`Uid` / `Gid`** store the numeric POSIX user and group IDs from `stat`. Both `0` means "not captured" (root ownership is indistinguishable from "not set", but is acceptable — root-owned files are rare in user backups, and the field is best-effort).

**`Btime`** stores the file birth time as a Unix timestamp. `0` means "not available on this platform/filesystem".

**`Flags`** stores the raw per-file flags integer. The meaning of bits differs between macOS (`UF_*`/`SF_*`) and Linux (`FS_*_FL`); the source filesystem type (see section 2.5) provides the interpretation context. `0` means "not captured or no flags set".

**`Xattrs`** stores raw attribute values as `[]byte`. Values are serialised in JSON as base64-encoded strings (standard Go `encoding/json` behaviour for `[]byte`). Keys are the full attribute names including namespace prefix (e.g. `user.myapp`, `com.apple.quarantine`).

All six fields are backward-compatible: snapshots written before this RFC have none of them set, and the engine treats missing fields as "do not restore".

**Impact on `FileMeta` hashing:** All six fields are included in the canonical JSON used to compute the `filemeta/<sha256>` object key. A change in any of them produces a new `filemeta` object and a new HAMT leaf, exactly like a name change. File content is unaffected and continues to be deduplicated by the chunk/content layer.

### 2.2 `LocalSource` changes

#### 2.2.1 Mode, uid, gid — single `Lstat` call

Replace the `os.FileInfo`-only stat with a `syscall.Stat_t` call. All three fields are retrieved in the same syscall at no extra cost:

```go
var st syscall.Stat_t
if err := syscall.Lstat(path, &st); err == nil {
    meta.Mode = uint32(st.Mode & 0xFFF) // lower 12 bits: permissions + setuid/setgid/sticky
    meta.Uid  = st.Uid
    meta.Gid  = st.Gid
}
```

`Lstat` is used (not `stat`) so that symlinks are visited as themselves rather than their targets. Symlinks are not currently a supported `FileType`, but using `Lstat` is defensive.

#### 2.2.2 Birth time (`btime`)

On macOS, `syscall.Stat_t` carries `Birthtimespec`:

```go
meta.Btime = st.Birthtimespec.Sec
```

On Linux, `Birthtimespec` is absent from `syscall.Stat_t`. Use `unix.Statx` with `unix.STATX_BTIME`:

```go
var stx unix.Statx_t
if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &stx); err == nil {
    if stx.Mask&unix.STATX_BTIME != 0 && stx.Btime.Sec != 0 {
        meta.Btime = stx.Btime.Sec
    }
}
```

If `statx` returns an error (kernel < 4.11) or the filesystem does not populate `STATX_BTIME`, `Btime` remains `0` and is omitted from JSON. Both branches are in the `local_source_unix.go` platform file.

#### 2.2.3 File flags

On macOS, `syscall.Stat_t` exposes `Flags` directly:

```go
meta.Flags = st.Flags
```

On Linux, flags require a separate `ioctl`:

```go
f, err := os.Open(path)
if err == nil {
    var flags uint32
    if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), unix.FS_IOC_GETFLAGS, uintptr(unsafe.Pointer(&flags))); errno == 0 {
        meta.Flags = flags
    }
    f.Close()
}
```

`FS_IOC_GETFLAGS` fails silently on filesystems that don't support it (FAT, NFS, procfs). Errors are swallowed; `Flags` remains `0`. The ioctl is skipped when `skipFlags` is true (see section 2.2.6).

Note: `FS_IOC_GETFLAGS` requires opening the file, which is an extra file descriptor per entry. On large trees this may be noticeable. The `WithSkipFlags` opt-out exists for this reason.

#### 2.2.4 Xattr capture

Xattr collection uses `golang.org/x/sys/unix` on Linux and macOS:

```go
// pseudocode — see platform-specific helpers in local_source_unix.go
keys, err := unix.Listxattr(path)
if err != nil {
    // ENOTSUP: filesystem does not support xattrs (e.g. FAT, exFAT) — skip silently
    // EPERM: no permission — skip silently
    // all other errors — log warning, skip
    return nil
}
xattrs := make(map[string][]byte, len(keys))
for _, key := range keys {
    val, err := unix.Getxattr(path, key)
    if err != nil {
        continue // attribute may have been removed between list and get; skip
    }
    xattrs[key] = val
}
if len(xattrs) > 0 {
    meta.Xattrs = xattrs
}
```

Platform-specific syscall wrappers are isolated behind a build-tag boundary:

| File | Build tags | Notes |
|---|---|---|
| `local_source_unix.go` | `//go:build linux \|\| darwin` | `unix.Listxattr`, `unix.Getxattr` |
| `local_source_stub.go` | `//go:build !linux && !darwin` | returns `nil, nil` (Windows, plan9, etc.) |

This keeps the Windows build clean with no behaviour change.

#### 2.2.5 Filter: which namespaces to collect

By default, collect **all** namespaces. The `WithXattrNamespaces` option allows the caller to restrict collection to a list of prefixes (e.g. `["user."]`):

```go
func WithXattrNamespaces(prefixes []string) LocalOption {
    return func(o *localOptions) {
        o.xattrNamespaces = prefixes
    }
}
```

When `xattrNamespaces` is non-empty, only attributes whose name has one of the specified prefixes are collected. When empty (default), all readable attributes are collected.

The corresponding CLI flag is `-xattr-namespaces user.,com.apple.` (comma-separated prefixes).

#### 2.2.6 Toggle: `WithSkipXattrs`

Xattr collection is opt-out for users who want faster backups or are on filesystems where xattr calls are slow:

```go
func WithSkipXattrs() LocalOption {
    return func(o *localOptions) {
        o.skipXattrs = true
    }
}
```

CLI flag: `-skip-xattrs`.

When `skipXattrs` is true, neither `Listxattr` nor `Getxattr` is called. `Mode` is still captured unless also explicitly disabled.

#### 2.2.7 Toggle: `WithSkipMode`

```go
func WithSkipMode() LocalOption {
    return func(o *localOptions) {
        o.skipMode = true
    }
}
```

CLI flag: `-skip-mode`. When set, `Mode`, `Uid`, `Gid`, `Btime`, and `Flags` are all left at zero (omitted from JSON). These are grouped under one flag because they all come from the same stat/statx/ioctl calls and are typically wanted or skipped together.

#### 2.2.8 Toggle: `WithSkipFlags`

```go
func WithSkipFlags() LocalOption {
    return func(o *localOptions) {
        o.skipFlags = true
    }
}
```

CLI flag: `-skip-flags`. Skips the `FS_IOC_GETFLAGS` ioctl on Linux only, without disabling mode/uid/gid/btime. Useful on large trees where the extra file descriptor per entry is measurable. On macOS, flags come free from `Stat_t.Flags` so this option has no effect.

### 2.3 `SFTPSource` changes

`pkg/sftp` exposes `os.FileInfo` via `client.Stat`. The underlying `sftp.FileStat` carries `Mode`, `UID`, and `GID` directly from the SFTPv3 `Attrs` struct:

```go
if fi, ok := info.(*sftp.FileStat); ok {
    meta.Mode = fi.Mode & 0xFFF
    meta.Uid  = fi.UID
    meta.Gid  = fi.GID
}
```

`Btime`, `Flags`, and `Xattrs` are not available in SFTPv3 and are omitted.

### 2.4 `SourceInfo.FsType`

Add `FsType string` to `SourceInfo`:

```go
type SourceInfo struct {
    Type    string `json:"type"`
    Account string `json:"account,omitempty"`
    Path    string `json:"path,omitempty"`
    FsType  string `json:"fs_type,omitempty"` // e.g. "apfs", "ext4", "btrfs", "zfs", "ntfs"
}
```

**Collection**: call `statfs(2)` once on the source root path at the start of `Walk`. The platform helper maps the numeric `f_type` magic (Linux) or `f_fstypename` string (macOS) to a canonical lowercase name:

| Magic (Linux) | Name |
|---|---|
| `0xEF53` | `ext4` |
| `0x9123683E` | `btrfs` |
| `0x58465342` | `xfs` |
| `0x2FC12FC1` | `zfs` |
| `0x6969` | `nfs` |
| `0x5346544E` | `ntfs` |
| unknown | `unknown:<hex>` |

On macOS, `statfs.F_fstypename` already returns a human-readable string (`apfs`, `hfs`, `msdos`, `nfs`, etc.) — no mapping needed.

**Multi-mount edge case**: `LocalSource` may be rooted at a path that contains subdirectories on different mount points. The `FsType` field in `SourceInfo` reflects the root mount. Files on a different mount are not annotated individually — this is an edge case not worth the per-file `statfs` overhead. A future `ls` or `diff` command could detect and warn about it.

**SFTP**: the `sftp` source sets `FsType = "sftp"` (the protocol, not the remote filesystem type, which is not knowable from SFTPv3).

**Cloud sources** (`gdrive`, `onedrive`): set `FsType` to the service name (`google-drive`, `onedrive`) rather than a local filesystem type.

### 2.5 Change detection

`internal/engine/backup_scan.go` currently runs a `metadataEqual` fast-path that compares a fixed set of fields. It must be extended to include all new fields:

```go
func metadataEqual(a, b *core.FileMeta) bool {
    return a.Name == b.Name &&
        a.Size == b.Size &&
        a.Mtime == b.Mtime &&
        a.Type == b.Type &&
        a.Mode == b.Mode &&
        a.Uid == b.Uid &&
        a.Gid == b.Gid &&
        a.Btime == b.Btime &&
        a.Flags == b.Flags &&
        xattrsEqual(a.Xattrs, b.Xattrs) &&
        slices.Equal(a.Parents, b.Parents)
}

func xattrsEqual(a, b map[string][]byte) bool {
    if len(a) != len(b) {
        return false
    }
    for k, v := range a {
        if bv, ok := b[k]; !ok || !bytes.Equal(v, bv) {
            return false
        }
    }
    return true
}
```

**Consequence:** if a file's content is unchanged but any metadata field changes (mode, ownership, flags, or an xattr), `metadataEqual` returns false, the file is queued, and a new `filemeta` object is written. File content is deduplicated by the chunk/content layer — only the metadata object is new. This is the correct and expected behaviour.

### 2.6 Restore

The `restore` command currently writes a ZIP archive. Two restore paths need updating:

#### 2.6.1 ZIP archive restore

The `archive/zip.FileHeader` type carries `ExternalAttrs`, which stores POSIX metadata in the high 16 bits when `CreatorVersion` is `0x0300` (Unix):

```go
header.CreatorVersion = 0x0300 | header.CreatorVersion&0xFF
header.ExternalAttrs = uint32(meta.Mode) << 16
```

This is the convention used by Info-ZIP, GNU tar, and most Unix-aware unzip implementations. Unzipping with `unzip -X` or `python zipfile` on Unix will restore the mode bits.

The ZIP `ExternalAttrs` field encodes only mode bits. uid/gid, btime, flags, and xattrs cannot be stored natively in ZIP. For those, two options:

**Option A — sidecar file:** Write a companion file `__xattrs__/<relpath>.json` inside the ZIP containing a JSON object mapping attribute names to base64-encoded values. A future `restore --apply-xattrs` flag can replay these after extracting the archive. Simple, no ZIP format changes.

**Option B — ZIP extra field:** Encode xattrs in a custom ZIP extra field (tag `0x6584`, private-use range). Compact, no extra entries, but requires a custom ZIP writer and custom `unzip` support for consumers.

**Recommendation: Option A (sidecar file).** Zero dependency on ZIP extensions, easy to inspect, easy to replay with a helper script.

The sidecar format covers all metadata that cannot be expressed in ZIP headers:

```json
{
  "version": 1,
  "fs_type": "apfs",
  "entries": [
    {
      "path": "projects/myapp/script.sh",
      "uid": 501,
      "gid": 20,
      "btime": 1710000000,
      "flags": 0,
      "xattrs": {
        "user.checksum": "c2hhMjU2OmFiY2Q=",
        "com.apple.quarantine": "MDA4MTtl..."
      }
    }
  ]
}
```

The sidecar is only written when at least one file in the snapshot has any non-zero metadata beyond mode. The `fs_type` field at the top level records the source filesystem for interpretation of `flags` values.

#### 2.6.2 Direct-to-filesystem restore (future)

When a future `restore --target <dir>` command is implemented that writes directly to disk rather than a ZIP, it should replay all metadata fields:

| Field | API |
|---|---|
| `Mode` | `os.Chmod(path, fs.FileMode(meta.Mode))` |
| `Uid` / `Gid` | `os.Lchown(path, int(meta.Uid), int(meta.Gid))` |
| `Btime` | `setattrlist` (macOS) / not settable on Linux (informational only) |
| `Flags` | `syscall.Chflags` (macOS) / `ioctl(FS_IOC_SETFLAGS)` (Linux) |
| `Xattrs` | `unix.Setxattr(path, key, val, 0)` |

This RFC does not implement direct restore but defines the contract so the future implementation has the data it needs.

### 2.7 `Size()` accounting

Xattrs contribute a small but nonzero number of bytes to backup size. All new fields are stored inside the `filemeta/` object — not counted toward the source `Size()`. This is acceptable: `Size()` is used only for progress reporting, and the overhead is negligible. No change needed.

---

## 3. Required Changes

### `internal/core/models.go`

1. Add `Mode uint32`, `Uid uint32`, `Gid uint32`, `Btime int64`, `Flags uint32`, and `Xattrs map[string][]byte` to `FileMeta`.
2. Add `FsType string` to `SourceInfo`.
3. Verify (or enforce) that `ComputeJSONHash` sorts map keys deterministically — required for stable hashing of `Xattrs`.

### `pkg/source/local_source.go`

1. Add `skipMode bool`, `skipFlags bool`, `skipXattrs bool`, `xattrNamespaces []string` to `localOptions`.
2. Add `WithSkipMode()`, `WithSkipFlags()`, `WithSkipXattrs()`, `WithXattrNamespaces([]string)` option constructors.
3. In `Walk`, call `syscall.Lstat` to populate `Mode`, `Uid`, `Gid`; call btime helper to populate `Btime`; call flags helper to populate `Flags`; call `listxattrs` to populate `Xattrs` — unless respective skip flags are set.
4. Call `statfs` on the root path once before the walk to populate `Info().FsType`.

### `pkg/source/local_source_unix.go` *(new file)*

```go
//go:build linux || darwin
```

1. `func readBtime(path string, st *syscall.Stat_t) int64` — macOS reads `st.Birthtimespec`; Linux calls `unix.Statx`.
2. `func readFlags(path string) uint32` — macOS reads `st.Flags`; Linux calls `ioctl(FS_IOC_GETFLAGS)`.
3. `func listxattrs(path string, namespaces []string) (map[string][]byte, error)` — wraps `unix.Listxattr` + `unix.Getxattr`, applies namespace filter.
4. `func setxattrs(path string, xattrs map[string][]byte) error` — wraps `unix.Setxattr` (for future direct restore).
5. `func detectFsType(path string) string` — calls `unix.Statfs` and maps `f_type` (Linux) or reads `f_fstypename` (macOS).

### `pkg/source/local_source_stub.go` *(new file)*

```go
//go:build !linux && !darwin
```

1. Stub implementations of all platform helpers that return zero values / `nil, nil`.

### `pkg/source/sftp_source.go`

1. When building `FileMeta` from `sftp.FileStat`, set `meta.Mode`, `meta.Uid`, `meta.Gid` from `Attrs`.
2. Set `SourceInfo.FsType = "sftp"`.

### `internal/engine/backup_scan.go`

1. Add `xattrsEqual` helper.
2. Update `metadataEqual` to compare all six new fields.

### `internal/engine/restore.go` (or equivalent ZIP writer)

1. Set `header.CreatorVersion` and `header.ExternalAttrs` to encode mode bits.
2. When any file in the snapshot has non-zero uid/gid/btime/flags or non-empty xattrs, accumulate a sidecar manifest and write it as `__cloudstic_meta__/index.json` before closing the ZIP. Include `fs_type` from the snapshot's `SourceInfo`.

### `cmd/cloudstic/main.go`

1. Add `-skip-mode`, `-skip-flags`, `-skip-xattrs`, `-xattr-namespaces` flags for the `backup` command when source is `local`.

### Tests

1. `local_source_unix_test.go`: table-driven tests for `listxattrs` (known xattrs, namespace filter), `readBtime` (known file, compare against `stat`), `readFlags` (set an immutable-equivalent attribute and verify it round-trips).
2. `local_source_test.go`: verify `Walk` populates all six fields; verify each skip option zeroes the respective fields.
3. `backup_scan_test.go`: verify `metadataEqual` returns false when any single new field differs; returns true when all match.
4. Restore test: verify ZIP `ExternalAttrs` encodes mode bits; verify sidecar JSON contains uid/gid/btime/flags/xattrs and `fs_type`.

---

## 4. Trade-offs and Constraints

### uid/gid 0 ambiguity

`Uid = 0` and `Gid = 0` are both valid (root) and the sentinel for "not captured". On most user backups root-owned files are absent, so the ambiguity is harmless. For explicit system-level backups where root ownership must be faithfully recorded, users should ensure the backup agent runs as root so `Lstat` populates the fields — or accept that ownership `0` will be restored as root on a direct-to-filesystem restore, which is correct in that case.

### btime availability on Linux

`STATX_BTIME` requires kernel ≥ 4.11 and filesystem support. On older kernels or unsupported filesystems (NFS, tmpfs, older ext3), `Btime` is silently `0`. The `FsType` field in `SourceInfo` gives consumers enough context to know whether a zero `Btime` is meaningful.

### Flag bits are OS-specific

`Flags` stores a raw `uint32` whose bit layout differs between macOS (`UF_*`/`SF_*`) and Linux (`FS_*_FL`). The `SourceInfo.FsType` field is the authoritative way to interpret them. A restore tool targeting a different OS should not blindly replay flags — it must either translate known bits or skip unknown ones. This complexity is deferred to the future direct-restore RFC.

### Binary xattr values

Xattr values are raw bytes — they are not necessarily valid UTF-8. Storing them as `[]byte` and letting `encoding/json` base64-encode them is correct and round-trip safe. Tools that inspect the snapshot JSON will see base64 strings, which is slightly opaque but unambiguous.

### Attribute volatility

Some attributes change on every read (e.g. macOS `com.apple.lastuseddate#PS`). Backing these up faithfully will cause those files to appear modified on every incremental backup even when content and meaningful metadata are unchanged. Users who see excessive churn should use `-xattr-namespaces user.` to collect only the `user.*` namespace or `-skip-xattrs` to disable collection.

An alternative mitigation is an explicit denylist of known-volatile attribute names (e.g. `com.apple.lastuseddate#PS`, `com.apple.quarantine` for some users). This could be a follow-up `WithXattrDenylist([]string)` option; it is not proposed here to keep the initial scope simple.

### Large xattr values

The Linux `ext4` and macOS HFS+/APFS filesystems cap xattr values at 64 KiB. A file with the maximum number of xattrs at maximum size is unusual but theoretically ~1 MB. This overhead is stored in the `filemeta/` object, which is a pack-buffered small object. An unexpectedly large `filemeta/` (above the 512 KB pack threshold) will be stored as a direct object rather than in a packfile — this is handled automatically by the existing pack store size gate.

### Mode restoration via ZIP

The ZIP `ExternalAttrs` convention is widely supported but not universally honoured. Standard `unzip` on some embedded systems strips permission bits. This is a best-effort mechanism; users who need guaranteed mode restoration must use the future direct-to-filesystem restore path. The limitation is documented in the restore output when mode data is present.

### `Xattrs` map and `FileMeta` hash stability

`map[string][]byte` serialises non-deterministically in Go (`encoding/json` iterates maps in random key order). The `FileMeta` hash is computed via `ComputeJSONHash`, which must sort map keys before serialising to guarantee a stable hash. This RFC requires that `ComputeJSONHash` (or the canonical serialisation path) sorts `Xattrs` keys. If `ComputeJSONHash` already canonicalises map keys (e.g. via `json.Marshal` with a sorted wrapper), no change is needed; otherwise a custom marshaller for `FileMeta` must be introduced.

### Windows

All new fields are left at zero/nil on Windows. The stub build file ensures clean compilation. Windows Alternate Data Streams (ADS) are a superficially similar concept to xattrs but require a completely different API and are not addressed here (see follow-up RFCs).

---

## 5. Alternatives Considered

### Store xattrs in `FileMeta.Extra`

`Extra map[string]interface{}` already exists and is used by the GDrive source for `mimeType`. Xattrs could be stored as `Extra["xattrs"]`. Rejected because:

* `interface{}` values are serialised as JSON `any`, which means `[]byte` values would need explicit base64 encoding at the application layer rather than leveraging Go's built-in `[]byte` → base64 behaviour.
* Change detection code already special-cases `Extra` for Google-native files; mixing filesystem xattrs into the same map would require more branching.
* A dedicated field is self-documenting and easier to validate.

### Store xattrs as a separate `xattrmeta/<hash>` object type

Xattr payloads are typically small (a few hundred bytes). Introducing a new object prefix for them would add round-trips and packfile fragmentation for negligible benefit. Inline storage in `FileMeta.Xattrs` is correct.

### Always collect all namespaces, no filter option

Simpler implementation, but `security.*` attributes (SELinux labels, capabilities) require elevated privileges on many Linux systems, and `system.*` / `trusted.*` are root-only. Attempting to read them without privilege yields `EPERM`. The namespace filter allows users to scope collection to `user.*` to avoid the noise of permission errors on security-namespaced attributes. The default (collect all readable attributes) is still sensible because `unix.Getxattr` returning `EPERM` is silently skipped per section 2.2.2.

### Encode mode as an octal string (e.g. `"0755"`)

Human-readable but requires a custom serialiser/deserialiser. `uint32` is compact, unambiguous, and directly usable with `os.Chmod(path, fs.FileMode(meta.Mode))`. Octal display can be added in the `ls`/`diff` output layer without affecting storage.

---

## 6. Follow-up RFCs

The items below are explicitly out of scope for this RFC. Each is a meaningful body of work that interacts with multiple subsystems and warrants its own proposal.

### RFC Symlink support

`FileType` currently has `file` and `folder`. Symlinks (`os.ModeSymlink`) are silently followed or skipped by `filepath.Walk`. Proper support requires:

* A new `FileTypeSymlink` constant and a `LinkTarget string` field on `FileMeta`.
* Switching `LocalSource.Walk` to `filepath.WalkDir` with `fs.DirEntry.Type().IsIrregular()` detection, or using `os.ReadDir` recursively with `Lstat` to detect symlinks before following them.
* Restoring symlinks via `os.Symlink` in the ZIP sidecar or direct-restore path.
* Deciding how to handle cycles (symlink → ancestor directory) to avoid infinite walk loops.
* Clarifying whether the backup follows the symlink (backs up the target content) or stores the link itself — most backup tools offer both modes.

### RFC Hard link deduplication

When two paths share the same inode (`stat.Nlink > 1`), the current engine backs up each path's content independently, storing duplicate chunks. Correct handling requires:

* Tracking `(st_dev, st_ino)` pairs during a Walk session in a `map[uint64]string` (inode → first-seen `FileID`).
* Emitting subsequent hard links as a reference to the first-seen path (a new `HardLinkTarget string` field on `FileMeta`, or a dedicated `FileTypeHardLink`).
* Restoring hard links via `os.Link` rather than writing duplicate content.
* Handling cross-snapshot inode reuse: inode numbers are not stable across backups and cannot be used for change detection across sessions.

### RFC POSIX ACL support

POSIX ACLs extend the traditional rwx model with per-user and per-group access control entries. They are widely used on enterprise Linux systems and macOS (NFSv4-style ACLs). Implementing them requires:

* A structured `Acl` field on `FileMeta` (not a raw byte blob) to support cross-platform interpretation and `diff` display.
* Platform-specific collection: Linux `libacl` / `getxattr("system.posix_acl_access")`, macOS `acl_get_file(3)`.
* Canonical serialisation for stable hashing (ACL entries have ordering rules).
* Restore via `acl_set_file` (Linux/macOS) or the ZIP sidecar.

### RFC Sparse file support

Files with large zero-filled regions (database images, VM disk files, container layers) are stored inefficiently because the chunker reads every byte including the holes. Sparse-aware backup requires:

* Using `lseek(SEEK_HOLE, SEEK_DATA)` to enumerate data extents before chunking.
* Encoding the extent map in the `Content` object so the restore path can re-punch holes via `fallocate(FALLOC_FL_PUNCH_HOLE)` or `ftruncate`.
* Fallback to full read on filesystems that don't support `SEEK_HOLE`.
* Interaction with FastCDC: chunking should operate only over data extents, not holes.

### RFC Windows-native local source

The current `local` source compiles on Windows (via the stub build file) but is untested and captures no Windows-specific metadata. A dedicated RFC would address:

* Windows Alternate Data Streams (ADS): conceptually similar to xattrs, accessed via `CreateFile` with `::streamname` path syntax.
* NTFS security descriptors (ACLs, owner SID, group SID): encoded as binary blobs via `BackupRead` or `GetSecurityInfo`.
* Windows file attributes (`FILE_ATTRIBUTE_HIDDEN`, `FILE_ATTRIBUTE_READONLY`, `FILE_ATTRIBUTE_SYSTEM`, etc.): analogous to POSIX flags.
* Reparse points (symlinks, junction points, OneDrive placeholders).
* Volume Shadow Copy integration for open-file backup.

### RFC Direct-to-filesystem restore

The current restore path produces a ZIP archive. A `restore --target <dir>` mode that writes directly to disk would enable faithful metadata replay for all fields defined in this RFC. Key concerns:

* Applying `Mode`, `Uid`, `Gid`, `Btime`, `Flags`, and `Xattrs` in the correct order (`Setxattr` after `Chmod` to avoid permission errors; immutable flag last).
* Handling permission elevation: `Lchown` requires root on most systems.
* Conflict resolution: what to do when target paths already exist.
* Progress reporting and resume on interrupted restores.

### RFC Cloud source metadata

Google Drive and OneDrive expose metadata that doesn't map to POSIX attributes but is worth preserving for round-trip fidelity. This would extend the `Extra` map rather than adding top-level `FileMeta` fields:

* **Birth time (`Btime`)**: Google Drive exposes `createdTime`, OneDrive exposes `createdDateTime`. The `Btime` field proposed in this RFC covers all sources — cloud sources should populate it alongside local/SFTP.
* **MIME type**: Google Drive already stores `mimeType` in `Extra`; OneDrive deserializes `file.mimeType` but discards it. Both should be consistent.
* **Content hash**: OneDrive exposes `file.hashes.sha256Hash` but we don't capture it in `ContentHash`. Google Drive already populates `sha256Checksum`.
* **File owner**: OneDrive exposes `createdBy`/`lastModifiedBy` but doesn't populate `FileMeta.Owner`. Google Drive already captures the first owner's email.
* **Google Drive properties**: `file.Properties` and `file.AppProperties` are user/app-defined key-value pairs, conceptually similar to filesystem xattrs. These could be stored under `Extra["properties"]` if round-trip preservation is desired.
* **OneDrive media facets**: `image`, `photo`, `video`, and `audio` facets contain EXIF-like metadata (dimensions, camera model, duration). Useful for media-heavy backups but increases object size.

Implementation should be a separate effort since it doesn't require schema changes beyond what this RFC already proposes (`Btime`) and consistent use of `Extra`.
