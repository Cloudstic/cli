# Source API

A **source** is the read-only origin of files during a backup. All sources implement the same Go interface, producing a uniform stream of `FileMeta` entries that the backup engine processes identically regardless of where the files come from.

## Interfaces

### Source

Every source must implement the `Source` interface (`pkg/store/interface.go`):

```go
type Source interface {
    Walk(ctx context.Context, callback func(core.FileMeta) error) error
    GetFileStream(fileID string) (io.ReadCloser, error)
    Info() core.SourceInfo
    Size(ctx context.Context) (*SourceSize, error)
}
```

| Method | Description |
|--------|-------------|
| `Walk` | Enumerate every file and folder. Parents **must** be emitted before their children. |
| `GetFileStream` | Return a readable stream for a file, identified by its source-specific `fileID`. |
| `Info` | Return metadata about the source (type, account, path) stored in the snapshot. |
| `Size` | Return the total size of the source (used for progress reporting). |

### IncrementalSource

Sources that support delta-based backups extend `Source` with `IncrementalSource`:

```go
type IncrementalSource interface {
    Source
    GetStartPageToken() (string, error)
    WalkChanges(ctx context.Context, token string, callback func(FileChange) error) (newToken string, err error)
}
```

| Method | Description |
|--------|-------------|
| `GetStartPageToken` | Return an opaque token representing the current head of the change stream. Called before a full `Walk` to capture the baseline. |
| `WalkChanges` | Emit only entries that changed since `token`. Returns the new token to store in the snapshot for the next run. |

Change entries carry a type:

| `ChangeType` | Description |
|--------------|-------------|
| `upsert` | File or folder was created or modified. Full `FileMeta` is provided. |
| `delete` | File or folder was removed. Only `FileMeta.FileID` is populated. |

### SourceInfo

Returned by `Info()` and stored in the snapshot's `source` field:

```go
type SourceInfo struct {
    Type    string // e.g. "gdrive", "local", "sftp", "onedrive", "gdrive-changes"
    Account string // Google email, hostname, user@host, etc.
    Path    string // drive path, filesystem path, etc.
}
```

The engine uses `SourceInfo` to:

- Find the previous snapshot from the same source (for incremental comparison)
- Group snapshots in retention policies (`forget --group-by source,account,path`)

### FileMeta

The common file metadata model emitted by all sources during `Walk` or `WalkChanges`:

```go
type FileMeta struct {
    Version     int
    FileID      string                 // Source-specific unique ID (HAMT key)
    Name        string                 // Display name
    Type        FileType               // "file" or "folder"
    Parents     []string               // Parent references (source-specific IDs during Walk)
    ContentHash string                 // SHA-256 of file content (if available from source)
    Size        int64                  // File size in bytes
    Mtime       int64                  // Last modified time (Unix timestamp)
    Owner       string                 // Owner identifier
    Extra       map[string]interface{} // Source-specific metadata (MIME type, download URL, etc.)
}
```

---

## Implementations

### `local` — Local Filesystem

| | |
|---|---|
| **Struct** | `LocalSource` |
| **Interface** | `Source` |
| **FileID** | Relative path from root (e.g. `subdir/file.txt`) |
| **Parents** | Parent directory's relative path |
| **ContentHash** | Not provided (computed by the engine during upload) |
| **SourceInfo.Account** | Machine hostname |
| **SourceInfo.Path** | Absolute path to the backed-up directory |

Walks the directory tree using `filepath.Walk`. Symbolic links are not followed.

### `sftp` — Remote SFTP Server

| | |
|---|---|
| **Struct** | `SFTPSource` |
| **Interface** | `Source` |
| **FileID** | Relative path from root (e.g. `subdir/file.txt`) |
| **Parents** | Parent directory's relative path |
| **ContentHash** | Not provided (computed by the engine during upload) |
| **SourceInfo.Account** | `user@host` |
| **SourceInfo.Path** | Remote root directory path |

Walks the remote directory tree via SFTP. Supports password, SSH private key, and ssh-agent authentication.

### `gdrive` — Google Drive (Full Scan)

| | |
|---|---|
| **Struct** | `GDriveSource` |
| **Interface** | `Source` |
| **FileID** | Google Drive file ID (e.g. `1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgV`) |
| **Parents** | Google Drive parent folder IDs |
| **ContentHash** | SHA-256 checksum from the Drive API (avoids re-downloading unchanged files) |
| **SourceInfo.Account** | Google account email |
| **SourceInfo.Path** | `my-drive://` or `<driveID>://<rootFolderID>` |

Lists all files and folders via `files.list`, then topologically sorts folders so parents are emitted before children. Supports My Drive and Shared Drives (via `gdrive://<Drive Name>`), with optional folder scoping (via `gdrive://<Drive Name>/path/to/folder`).

### `gdrive-changes` — Google Drive (Changes API)

| | |
|---|---|
| **Struct** | `GDriveChangeSource` (embeds `GDriveSource`) |
| **Interface** | `IncrementalSource` |
| **FileID** | Same as `gdrive` |
| **Parents** | Same as `gdrive` |
| **ContentHash** | Same as `gdrive` |
| **SourceInfo.Account** | Same as `gdrive` |
| **SourceInfo.Path** | Same as `gdrive` |
| **Change token** | Google Drive Changes API start page token |

This is the **recommended** source for Google Drive backups. Embeds `GDriveSource` and reuses its `Walk`, `GetFileStream`, and metadata conversion. On the first run (no previous token), the engine calls `GetStartPageToken` + full `Walk`. On subsequent runs, only `WalkChanges` is called, fetching the delta since the stored token.

Folder changes are topologically sorted before file changes, ensuring parent references resolve correctly.

### `onedrive` — Microsoft OneDrive

| | |
|---|---|
| **Struct** | `OneDriveSource` |
| **Interface** | `Source` |
| **FileID** | OneDrive item ID |
| **Parents** | OneDrive parent item ID |
| **ContentHash** | Not provided (computed by the engine during upload) |
| **SourceInfo.Account** | User principal name from Microsoft Graph `/me` |
| **SourceInfo.Path** | `onedrive://` |

Walks the drive recursively starting from the root item via the Microsoft Graph API. Folders are visited depth-first, ensuring parents are emitted before children.

### `onedrive-changes` — Microsoft OneDrive (Delta API)

| | |
|---|---|
| **Struct** | `OneDriveChangeSource` (embeds `OneDriveSource`) |
| **Interface** | `IncrementalSource` |
| **FileID** | Same as `onedrive` |
| **Parents** | Same as `onedrive` |
| **ContentHash** | Same as `onedrive` |
| **SourceInfo.Account** | Same as `onedrive` |
| **SourceInfo.Path** | Same as `onedrive` |
| **Change token** | Microsoft Graph delta link |

Embeds `OneDriveSource` and reuses its `Walk`, `GetFileStream`, and metadata conversion. On the first run (no previous token), the engine calls `GetStartPageToken` + full `Walk`. On subsequent runs, only `WalkChanges` is called, fetching the delta since the stored token.

---

## Engine integration

The backup engine (`internal/engine/backup.go`) interacts with sources as follows:

1. **Detect source type** — check if the source implements `IncrementalSource`
2. **Load previous state** — find the most recent snapshot with a matching `SourceInfo`
3. **If incremental and a previous token exists** — call `WalkChanges(token)` to get a delta, then apply upserts and deletes to the previous HAMT
4. **Otherwise** — call `GetStartPageToken()` (if incremental) then `Walk()` for a full scan, comparing each entry against the previous HAMT
5. **Upload changed files** — call `GetFileStream(fileID)` for each file that needs uploading
6. **Persist** — save the snapshot with the source's `Info()` and the change token (if any)

```
┌─────────┐   Walk / WalkChanges   ┌────────┐   GetFileStream   ┌───────┐
│  Source  │ ────────────────────►  │ Engine │ ◄──────────────── │ Store │
│          │   FileMeta / Changes   │        │   Upload chunks   │       │
└─────────┘                        └────────┘                   └───────┘
```

## Implementing a new source

To add a new source:

1. Create a struct in `pkg/store/` that implements `Source` (or `IncrementalSource` for delta support)
2. `Walk` must emit parents before children
3. `FileID` must be a stable, unique identifier within the source — it's used as the HAMT key
4. `GetFileStream` must return the raw file bytes for the given `FileID`
5. `Info()` should return a unique `SourceInfo` so snapshots from different sources are distinguishable
6. Register the source type in `cmd/cloudstic/main.go` in the `initSource` function
