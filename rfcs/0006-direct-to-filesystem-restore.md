# RFC 0006: Direct-to-Filesystem Restore

* **Status:** Proposed
* **Date:** 2026-03-08
* **Affects:** `internal/engine/restore.go`, `cmd/cloudstic/cmd_restore.go`, `client.go`, `cmd/cloudstic/client_iface.go`
* **Depends on:** RFC 0004 (Extended File Attributes)

## Abstract

The current `restore` command produces a ZIP archive. ZIP cannot faithfully represent POSIX metadata beyond mode bits — uid/gid, btime, file flags, and extended attributes are lost. This makes RFC 0004's extended metadata collection pointless without a restore path that can replay it. This RFC introduces a `restore -format dir` mode that writes files directly to disk and applies all stored metadata fields in the correct order. It also unifies the CLI interface around a single `-output` flag with a `-format` flag for extensibility, and defines conflict resolution semantics, permission elevation handling, progress reporting, and resumable restores.

---

## 1. Context

### 1.1 What ZIP preserves (and what it doesn't)

The ZIP format supports a single metadata mechanism for POSIX systems: `ExternalAttrs` encodes permission bits (mode) in the high 16 bits when `CreatorVersion` indicates Unix. This is honoured by most Unix `unzip` tools.

Everything else is lost:

| Field | ZIP support |
|---|---|
| `Mode` (permission bits) | Partial — via `ExternalAttrs`, requires Unix-aware unzip |
| `Uid` / `Gid` | No |
| `Btime` | No |
| `Flags` (immutable, append-only, etc.) | No |
| `Xattrs` | No |

RFC 0004 proposed a sidecar JSON file inside the ZIP (`__cloudstic_meta__/index.json`) as a workaround. While this preserves the data, replaying it requires a second manual step — users must run a separate tool or script after unzipping. This is fragile, error-prone, and defeats the purpose of a backup tool.

### 1.2 Current restore flow

```
resolve snapshot → collect metadata → topo-sort → write ZIP entries
```

The `RestoreManager.Run` method takes an `io.Writer` and writes a ZIP archive. The caller (`cmd_restore.go`) creates an output file and passes it. There is no concept of a target directory.

### 1.3 Why a format flag, not separate flags

ZIP restore remains valuable for:

* **Cross-platform portability**: users restoring to Windows or sharing an archive
* **Atomic output**: a single file that can be moved, uploaded, or streamed
* **Non-root restore**: no need for elevated privileges to produce a ZIP

Direct-to-filesystem restore is a new output format, not a replacement for ZIP. Both paths share snapshot resolution, metadata collection, and topological sorting — only the output writer differs.

Rather than introducing a separate `--target` flag (mutually exclusive with `--output`), a single `-output` path combined with a `-format` flag is cleaner and extensible. Adding tar, tar.gz, or any future format becomes a new `-format` value — no new flags needed. The format can also be inferred from the output path extension when unambiguous.

---

## 2. Proposal

### 2.1 CLI interface

```
cloudstic restore -output ./restored -format dir latest
cloudstic restore -output ./backup.zip latest                          # format inferred as "zip"
cloudstic restore -output ./restored -format dir -path Documents/ latest
cloudstic restore -output ./restored -format dir -dry-run latest
cloudstic restore -output ./restored -format dir -no-ownership -no-xattrs latest
```

#### Flags

| Flag | Type | Default | Description |
|---|---|---|---|
| `-output` | `string` | `./restore.zip` | Output path — a file for archive formats, a directory for `dir` |
| `-format` | `string` | `""` (auto) | Output format: `zip`, `dir`. Auto-detected from `-output` extension when omitted |
| `-no-ownership` | `bool` | `false` | Skip `Lchown` calls (useful when not running as root) — `dir` format only |
| `-no-xattrs` | `bool` | `false` | Skip `Setxattr` calls — `dir` format only |
| `-no-flags` | `bool` | `false` | Skip `Chflags` / `FS_IOC_SETFLAGS` calls — `dir` format only |
| `-no-times` | `bool` | `false` | Skip `Chtimes` / btime calls — `dir` format only |
| `-overwrite` | `bool` | `false` | Overwrite existing files (default: skip with warning) — `dir` format only |

#### Format detection

When `-format` is omitted, the format is inferred from the `-output` path:

| `-output` value | Inferred format |
|---|---|
| `*.zip` | `zip` |
| Any path without a recognised archive extension | `dir` |

If the inferred format is ambiguous, the CLI errors with a message asking the user to specify `-format` explicitly.

This design is extensible: adding `tar` or `tar.gz` support in the future is a new format value and a new `RestoreWriter` implementation — no new CLI flags required.

### 2.2 Engine changes

#### 2.2.1 `RestoreWriter` interface

Introduce an interface that abstracts the output destination:

```go
// RestoreWriter receives restored files and directories.
type RestoreWriter interface {
    // MkdirAll creates a directory and all parents. Called in topo order.
    MkdirAll(path string, meta core.FileMeta) error

    // WriteFile writes file content. The caller provides an io.Reader for the
    // content stream. The writer is responsible for creating the file and
    // applying metadata.
    WriteFile(path string, meta core.FileMeta, content io.Reader) error

    // Close finalises the output (e.g. close ZIP, apply deferred metadata).
    Close() error
}
```

Two implementations:

* `zipRestoreWriter` — wraps `archive/zip.Writer`, equivalent to the current logic
* `fsRestoreWriter` — writes to a target directory on disk

#### 2.2.2 `fsRestoreWriter`

```go
type fsRestoreWriter struct {
    root        string       // target directory (absolute path)
    opts        fsWriterOpts // skip flags, overwrite, etc.
    reporter    ui.Reporter
    deferredMeta []deferredEntry // metadata to apply in reverse order after all files are written
}

type deferredEntry struct {
    path string
    meta core.FileMeta
}
```

The writer operates in two phases:

**Phase 1 — Content writes (during the main restore loop):**

For each directory:

1. `os.MkdirAll(fullPath, 0755)` — create with permissive mode to allow writing children
2. Record `(fullPath, meta)` in `deferredMeta` for later metadata application

For each file:

1. Check existence: if file exists and `!overwrite`, log warning and skip
2. Create file: `os.OpenFile(fullPath, O_CREATE|O_WRONLY|O_TRUNC, 0644)` — permissive mode initially
3. Write content from `io.Reader` to file
4. Close file
5. Apply file-level metadata immediately (mode, ownership, timestamps, xattrs)
6. Note: file flags (immutable, append-only) are NOT applied yet — they would prevent subsequent modifications

**Phase 2 — Deferred metadata (`Close()`):**

Process `deferredMeta` in **reverse topological order** (deepest paths first):

1. Apply directory timestamps (mtime, btime) — must be last because writing children updates parent mtime
2. Apply directory mode (final restrictive permissions)
3. Apply directory ownership
4. Apply directory xattrs
5. Apply file flags (immutable, append-only) on both files and directories — these must be absolutely last because they prevent all further modification

The reverse-order processing ensures that:

* Setting a directory's mtime isn't overwritten by a subsequent child write
* Restrictive permissions on a parent don't block writes to children
* Immutable flags don't prevent any metadata application

#### 2.2.3 Metadata application order (per entry)

The order of metadata syscalls matters. The correct sequence for a file:

```
1. Write content          → file exists with permissive mode
2. os.Chmod               → set final permission bits
3. os.Lchown              → set uid/gid (may clear setuid/setgid; re-chmod if needed)
4. os.Chtimes             → set mtime
5. setBtime               → set birth time (macOS only; informational on Linux)
6. unix.Setxattr (each)   → set extended attributes
7. setFlags               → set immutable/append-only (MUST be last)
```

Why this order:

* `Lchown` after `Chmod`: on some systems, `chown` clears setuid/setgid bits. If both mode and ownership are set, we chmod, chown, then re-chmod if setuid/setgid bits were requested.
* `Setxattr` after `Chmod`: some xattr namespaces require write permission on the file.
* `setFlags` last: immutable flag prevents all subsequent modifications including `chmod`, `chown`, `setxattr`, and `chtimes`.

#### 2.2.4 Refactoring `RestoreManager.Run`

The current `Run` method inlines ZIP writing. Refactor to use `RestoreWriter`:

```go
func (rm *RestoreManager) Run(ctx context.Context, w RestoreWriter, snapshotRef string, opts ...RestoreOption) (*RestoreResult, error) {
    // ... resolve snapshot, collect metadata, topo-sort, filter (unchanged) ...

    for _, meta := range sorted {
        p := buildRestorePath(meta, byID)

        if meta.Type == core.FileTypeFolder {
            if err := w.MkdirAll(p, meta); err != nil { ... }
            result.DirsWritten++
            continue
        }

        reader := rm.contentReader(ctx, meta)
        if err := w.WriteFile(p, meta, reader); err != nil { ... }
        result.FilesWritten++
    }

    if err := w.Close(); err != nil { ... }
    return result, nil
}
```

The `buildRestorePath` function is the same as `buildZipPath` — renamed for clarity since it now serves both modes.

### 2.3 Platform-specific metadata helpers

All metadata application code lives in platform-gated files:

#### `internal/engine/restore_unix.go`

```go
//go:build linux || darwin
```

```go
// applyFileMetadata applies all metadata fields to a restored file.
// Flags are NOT applied here — they are deferred to applyFlags.
func applyFileMetadata(path string, meta core.FileMeta, opts fsWriterOpts) error {
    if meta.Mode != 0 {
        if err := os.Chmod(path, fs.FileMode(meta.Mode)); err != nil {
            return fmt.Errorf("chmod %s: %w", path, err)
        }
    }
    if !opts.noOwnership && (meta.Uid != 0 || meta.Gid != 0) {
        if err := os.Lchown(path, int(meta.Uid), int(meta.Gid)); err != nil {
            // Log warning but don't fail — likely not root
            logOwnershipWarning(path, err)
        }
        // Re-apply mode if setuid/setgid was requested, as chown may clear them
        if meta.Mode&0o7000 != 0 {
            _ = os.Chmod(path, fs.FileMode(meta.Mode))
        }
    }
    if meta.Mtime > 0 {
        mtime := time.Unix(meta.Mtime, 0)
        if err := os.Chtimes(path, mtime, mtime); err != nil {
            return fmt.Errorf("chtimes %s: %w", path, err)
        }
    }
    if !opts.noTimes && meta.Btime > 0 {
        _ = setBtime(path, meta.Btime) // best-effort; not settable on Linux
    }
    if !opts.noXattrs && len(meta.Xattrs) > 0 {
        for k, v := range meta.Xattrs {
            if err := unix.Setxattr(path, k, v, 0); err != nil {
                // Log warning, continue — namespace may not be writable
                logXattrWarning(path, k, err)
            }
        }
    }
    return nil
}

// applyFlags sets per-file flags. Must be called LAST.
func applyFlags(path string, meta core.FileMeta, opts fsWriterOpts) error {
    if opts.noFlags || meta.Flags == 0 {
        return nil
    }
    return setFlags(path, meta.Flags)
}
```

Platform-specific helpers:

| Helper | macOS | Linux |
|---|---|---|
| `setBtime` | `setattrlist(2)` | No-op (not settable) |
| `setFlags` | `syscall.Chflags(path, int(flags))` | `ioctl(FS_IOC_SETFLAGS)` |

#### `internal/engine/restore_stub.go`

```go
//go:build !linux && !darwin
```

Stub implementations that skip all POSIX-specific metadata. `Chmod` and `Chtimes` are still called via `os` package (cross-platform), but ownership, btime, flags, and xattrs are no-ops.

### 2.4 Conflict resolution

When `--overwrite` is false (default):

| Existing entry | Restored entry | Behaviour |
|---|---|---|
| File | File | Skip, log warning: `Skipped (exists): path/to/file` |
| Directory | Directory | Merge: create missing children, skip existing files |
| File | Directory | Error: `Cannot restore directory over existing file: path` |
| Directory | File | Error: `Cannot restore file over existing directory: path` |

When `--overwrite` is true:

| Existing entry | Restored entry | Behaviour |
|---|---|---|
| File | File | Overwrite (truncate + write) |
| Directory | Directory | Merge, overwrite existing files |
| File | Directory | Remove file, create directory |
| Directory | File | Error: refuse to delete directory tree (too destructive) |

The `--overwrite` flag intentionally does not remove entire directory trees. If a user needs to restore to a clean state, they should use an empty target directory.

### 2.5 Permission elevation

Several metadata operations require root:

| Operation | Requires root? | Behaviour when not root |
|---|---|---|
| `Lchown` | Yes (on most systems) | Log warning, continue |
| `Setxattr` (`security.*`, `trusted.*`) | Yes | Log warning, skip attribute |
| `Setxattr` (`user.*`) | No | Apply normally |
| `Chflags` / `FS_IOC_SETFLAGS` (system flags) | Yes | Log warning, skip |
| `Chmod` | No (if file owner) | Apply normally |
| `setBtime` | No (if file owner, macOS) | Best-effort |

The restore command does NOT require root. When run without root:

* Ownership is silently skipped (or `--no-ownership` suppresses the warnings)
* Security-namespace xattrs are skipped
* System-level file flags are skipped
* All other metadata is applied

A summary line at the end reports skipped operations:

```
Restore complete. Snapshot: snapshot/abc123
  Files: 1,234, Dirs: 56
  Bytes written: 45.2 MB
  Metadata warnings: 23 ownership, 4 xattr, 0 flags (run as root to apply)
```

### 2.6 Progress reporting and byte tracking

The `fsRestoreWriter` tracks:

* Files and directories written (existing phase reporting)
* Bytes written to disk (via `countingWriter` wrapping each file write)
* Metadata warnings (ownership, xattr, flags) — counted by category

`RestoreResult` gains new fields:

```go
type RestoreResult struct {
    // ... existing fields ...
    Format            string // "zip" or "dir"
    OwnershipWarnings int    // Lchown failures (not root)
    XattrWarnings     int    // Setxattr failures
    FlagWarnings      int    // Chflags/ioctl failures
}
```

### 2.7 Dry-run support

`--dry-run` with `--target` works the same as with `--output`: resolves the snapshot, collects metadata, applies path filters, and reports what would be restored — without writing anything. No directory creation or file writes occur.

### 2.8 Cross-platform flag interpretation

RFC 0004 stores `Flags` as a raw `uint32` whose bit layout is OS-specific. When restoring to a different OS than the source:

| Source OS | Target OS | Behaviour |
|---|---|---|
| macOS | macOS | Apply directly via `Chflags` |
| Linux | Linux | Apply directly via `FS_IOC_SETFLAGS` |
| macOS | Linux | Skip with warning: "macOS flags cannot be mapped to Linux" |
| Linux | macOS | Skip with warning: "Linux flags cannot be mapped to macOS" |

The `SourceInfo.FsType` field (from the snapshot) and the target filesystem type (detected at restore time via `statfs`) provide the context needed to decide. A future RFC could define a cross-platform flag translation table for known mappings (e.g. macOS `UF_IMMUTABLE` ↔ Linux `FS_IMMUTABLE_FL`), but this is out of scope here.

### 2.9 Format dispatch

The CLI resolves the format (explicit or inferred), then constructs the appropriate `RestoreWriter`:

```go
format := resolveFormat(a.format, a.output) // "zip" or "dir"
switch format {
case "zip":
    f, _ := os.Create(a.output)
    writer = newZipRestoreWriter(f)
case "dir":
    writer = newFsRestoreWriter(a.output, opts)
default:
    return r.fail("Unknown format: %s", format)
}
```

All upstream logic (snapshot resolution, metadata collection, sorting, filtering) is shared across formats. The `RestoreWriter` abstraction makes adding new formats straightforward — each is a new `case` and a new writer implementation.

The metadata-specific flags (`-no-ownership`, `-no-xattrs`, `-no-flags`, `-no-times`) are validated at parse time: if any are set with `-format zip`, the CLI warns that they have no effect on archive output.

---

## 3. Required Changes

### `internal/engine/restore.go`

1. Define `RestoreWriter` interface with `MkdirAll`, `WriteFile`, `Close`.
2. Implement `zipRestoreWriter` wrapping the current ZIP logic.
3. Implement `fsRestoreWriter` with two-phase metadata application.
4. Refactor `RestoreManager.Run` to accept a `RestoreWriter` instead of `io.Writer`.
5. Add `contentReader(ctx, meta) io.Reader` helper that returns a reader over chunks + inline data.
6. Add new `RestoreOption` constructors: `WithRestoreNoOwnership()`, `WithRestoreNoXattrs()`, `WithRestoreNoFlags()`, `WithRestoreNoTimes()`, `WithRestoreOverwrite()`.
7. Add `OwnershipWarnings`, `XattrWarnings`, `FlagWarnings`, `TargetDir` to `RestoreResult`.

### `internal/engine/restore_unix.go` *(new file)*

1. `applyFileMetadata(path, meta, opts)` — chmod, chown, chtimes, btime, xattrs.
2. `applyFlags(path, meta, opts)` — chflags / ioctl, called during `Close()`.
3. `setBtime(path string, btime int64)` — macOS: `setattrlist`; no-op on Linux.
4. `setFlags(path string, flags uint32)` — macOS: `Chflags`; Linux: `ioctl(FS_IOC_SETFLAGS)`.
5. `detectTargetFsType(path string) string` — for cross-platform flag warning.

### `internal/engine/restore_stub.go` *(new file)*

1. Stub implementations for Windows/other platforms — apply only `Chmod` and `Chtimes`.

### `cmd/cloudstic/cmd_restore.go`

1. Add `-format`, `-no-ownership`, `-no-xattrs`, `-no-flags`, `-no-times`, `-overwrite` flags.
2. Add `resolveFormat(format, output string) string` — infers format from output path extension when `-format` is empty.
3. Dispatch to `zipRestoreWriter` or `fsRestoreWriter` based on resolved format.
4. Warn when metadata flags (`-no-ownership`, etc.) are used with non-`dir` formats.
5. Update `printRestoreSummary` to display metadata warnings and target directory for `dir` format.

### `client.go`

1. Update `Restore` method signature or add a new method to support `RestoreWriter` construction.
2. Forward new options to the engine.

### `cmd/cloudstic/client_iface.go`

1. Update `cloudsticClient` interface if `Restore` signature changes.

### Tests

1. `restore_test.go`: add tests for `fsRestoreWriter` using a temp directory — verify file content, directory structure, mtime preservation.
2. `restore_unix_test.go`: verify mode bits, xattrs, and ownership application order on a temp directory (xattr tests require a filesystem that supports them; skip with `t.Skip` on tmpfs without xattr support).
3. `restore_test.go`: verify conflict resolution (skip existing, overwrite, type mismatch errors).
4. `restore_test.go`: verify deferred metadata application — directory mtime not clobbered by child writes.
5. `cmd_restore_test.go`: verify format inference from output path (`*.zip` → `zip`, directory path → `dir`).
6. `cmd_restore_test.go`: verify `-no-ownership`, `-no-xattrs`, `-no-flags` suppress respective operations.
7. `cmd_restore_test.go`: verify warning when metadata flags are used with `-format zip`.

---

## 4. Trade-offs and Constraints

### uid/gid restoration requires root

On most UNIX systems, only root can call `chown(2)`. Non-root restores silently skip ownership with a summary warning. This is the same approach taken by `tar -x` (without `--same-owner`). The `--no-ownership` flag suppresses warnings for users who know they won't run as root.

### btime is not settable on Linux

Linux provides no public API to set file birth time. `Btime` is informational only on Linux restores — the file will have the creation time of the restore operation. On macOS, `setattrlist(2)` can set btime on APFS/HFS+. This asymmetry is documented in the restore summary when btime data is present but cannot be applied.

### Immutable flag ordering

A file with `UF_IMMUTABLE` or `FS_IMMUTABLE_FL` cannot be modified after the flag is set — not even by root (without first clearing the flag). The two-phase approach (content + metadata first, flags last) handles this correctly. If a restore is interrupted between phase 1 and phase 2, no immutable flags will have been set, so the user can safely re-run the restore with `--overwrite`.

### Interrupted restore and idempotency

If the restore is interrupted, the target directory may contain a partial tree. Re-running with `--overwrite` will complete the restore. Re-running without `--overwrite` will skip already-restored files and fill in the gaps. Neither mode cleans up extra files that shouldn't be there (e.g. from a previous restore of a different snapshot). A future `--clean` flag could address this by removing target entries not present in the snapshot, but this is out of scope.

### Atomic file writes

Files are written directly to their final path (no temp file + rename). This means a crash mid-write produces a truncated file. For most backup restore scenarios this is acceptable — the user will re-run the restore. A temp-file-and-rename approach would be safer but adds complexity (same-filesystem requirement for `os.Rename`, handling of the temp file on failure, double disk space). This can be revisited if users report issues.

### Directory mtime accuracy

Writing a child file into a directory updates the directory's mtime. The deferred metadata phase re-applies directory mtime after all children are written, restoring the original value. However, if the restore is interrupted before `Close()`, directory mtimes will reflect the restore time, not the original. This is acceptable for an interrupted restore.

### Symlink support

This RFC does not handle symlinks (they are not yet a supported `FileType`). When symlink support is added (see RFC 0004 follow-up), the `fsRestoreWriter` will need a `Symlink(path, target string, meta FileMeta) error` method. The `RestoreWriter` interface is designed to be extensible for this.

### RestoreWriter is internal

The `RestoreWriter` interface is defined in `internal/engine/` and is not exported to external consumers. The public API remains `client.Restore(ctx, ...)` with options controlling the mode. This keeps the interface free to evolve.

---

## 5. Alternatives Considered

### Separate `--target` and `--output` flags

An earlier design used `--target <dir>` for direct restore and `--output <file>` for ZIP, making them mutually exclusive. This works for two formats but doesn't scale: adding tar would require either overloading `--output` with format detection anyway, or adding a third flag. A single `-output` path with an explicit `-format` flag is simpler, more extensible, and avoids mutual-exclusion validation logic.

### Replay sidecar after ZIP extraction

RFC 0004 proposed writing a `__cloudstic_meta__/index.json` sidecar inside the ZIP. A companion `cloudstic apply-meta ./extracted/` command could replay the sidecar. This was rejected as the primary approach because:

* Two-step restore is error-prone — users will forget the second step
* The sidecar must be kept in sync with the extracted tree
* It requires shipping and documenting a separate command

The sidecar approach remains useful for ZIP mode (users who unzip manually can still benefit from it), but direct-to-filesystem restore is the correct solution for faithful metadata replay.

### tar instead of ZIP

`tar` natively supports uid/gid, mode, and timestamps. GNU tar extensions support xattrs (`--xattrs`) and ACLs. Switching to tar would solve the metadata problem for archive-based restores. However:

* tar is a streaming format — random access to individual files requires scanning the entire archive
* ZIP is more widely supported on Windows and macOS (native Finder support)
* The project already has ZIP infrastructure
* Direct-to-filesystem restore is still needed for the best UX (no intermediate archive)

tar could be offered as a third output format in the future, but it does not replace the need for direct restore.

### Write to temp directory + atomic rename

Create the entire tree in a temp directory, then `os.Rename` to the target. This provides atomicity but:

* `os.Rename` only works within the same filesystem
* Requires 2x disk space during restore
* Large restores would appear to make no progress until the final rename

Not worth the complexity for a backup restore tool where idempotent re-runs are the expected recovery mechanism.
