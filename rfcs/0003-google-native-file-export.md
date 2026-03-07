# RFC 0003: Google Native File Export

* **Status:** Proposed
* **Date:** 2026-03-07
* **Affects:** `pkg/source/gdrive.go`, `pkg/source/gdrive_changes.go`, `internal/engine/backup_scan.go`

## Abstract

Google Drive stores certain files in a proprietary format (Google Docs, Sheets, Slides, Drawings, etc.) that cannot be downloaded via the standard Drive `files.get` download endpoint. Both `gdrive` and `gdrive-changes` sources currently include these files in their `Walk` output, but `GetFileStream` fails on them because `call.Download()` returns a 403 from the API. As a result, native files are never backed up. This RFC defines a detection and export strategy so that native files are converted to open formats at backup time and treated as regular files from the engine's perspective.

## 1. Context

### 1.1 What are Google native files?

Files whose MIME type matches `application/vnd.google-apps.*` (except `.folder`) are Google native files. They have no binary content stored by Drive — they exist only as a structured document editable in Google's editors. The Drive API exposes them but refuses binary downloads:

```
GET /drive/v3/files/{id}?alt=media
→ 403 cannotDownloadFile
```

Instead, the API provides an Export endpoint:

```
GET /drive/v3/files/{id}/export?mimeType={targetMimeType}
→ 200 OK with file body in the target format
```

The set of native types that appear in practice:

| Google MIME Type | Description |
|---|---|
| `application/vnd.google-apps.document` | Google Docs |
| `application/vnd.google-apps.spreadsheet` | Google Sheets |
| `application/vnd.google-apps.presentation` | Google Slides |
| `application/vnd.google-apps.drawing` | Google Drawings |
| `application/vnd.google-apps.form` | Google Forms |
| `application/vnd.google-apps.script` | Apps Script |
| `application/vnd.google-apps.site` | Google Sites |
| `application/vnd.google-apps.jam` | Jamboard |
| `application/vnd.google-apps.map` | My Maps |

### 1.2 Current behaviour

`Walk` queries `mimeType != 'application/vnd.google-apps.folder'`, so all native types except folders are enumerated and emitted as `FileMeta` entries. The engine queues them for upload and calls `GetFileStream(fileID)`, which calls `call.Download()` → 403 → backup error or silent drop.

### 1.3 Size reporting

The Drive API returns `size = 0` for native files (they have no blob). After export the actual byte count is known only once the response body has been fully streamed. This means the pre-backup size estimate (`Source.Size`) will under-count bytes from native files, but this is acceptable — the engine uses `Size()` only for progress reporting.

## 2. Proposal

### 2.1 Export MIME type mapping

Define a fixed mapping from Google native MIME types to their preferred export formats:

| Native MIME Type | Export MIME Type | File Extension |
|---|---|---|
| `application/vnd.google-apps.document` | `application/vnd.openxmlformats-officedocument.wordprocessingml.document` | `.docx` |
| `application/vnd.google-apps.spreadsheet` | `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet` | `.xlsx` |
| `application/vnd.google-apps.presentation` | `application/vnd.openxmlformats-officedocument.presentationml.presentation` | `.pptx` |
| `application/vnd.google-apps.drawing` | `image/svg+xml` | `.svg` |
| `application/vnd.google-apps.script` | `application/vnd.google-apps.script+json` | `.json` |
| `application/vnd.google-apps.form` | `application/pdf` | `.pdf` |
| `application/vnd.google-apps.site` | `text/plain` | `.txt` |
| `application/vnd.google-apps.jam` | `application/pdf` | `.pdf` |
| `application/vnd.google-apps.map` | `application/pdf` | `.pdf` |

Types not in the mapping (unknown future native types) fall back to `application/pdf`.

The export format is a reasonable best-effort: Microsoft Office formats are chosen for Docs/Sheets/Slides because they are widely supported and round-trip better than PDF for structured documents. SVG is chosen for Drawings because it is lossless and preserves vector elements. PDF is the safe fallback for everything else.

### 2.2 `GetFileStream` changes

`GetFileStream` must detect native files and call `Export` instead of `Download`. The MIME type is stored in `FileMeta.Extra["mimeType"]` during Walk, so `GetFileStream` needs to be able to look it up by `fileID`.

Two implementation options:

**Option A — extra API call:** Call `files.get` with `fields=mimeType` before exporting to learn the type. Simple, but adds one API call per native file.

**Option B — inject MIME type into GetFileStream:** Extend the `Source.GetFileStream` signature to accept extra context, or add a separate method like `GetFileStreamWithMeta(fileID, mimeType string)`. This avoids the extra call but requires an interface change.

**Option C — thin cache on GDriveSource:** During `Walk`, record a `mimeType` map (`fileID → mimeType`) on the `GDriveSource` struct. `GetFileStream` reads from that map to decide download vs export. No interface change; works naturally for both full and incremental scans.

**Recommendation: Option C.** The map is populated for free during Walk (mimeType is already fetched in the existing `files.list` field list). For incremental backups (`gdrive-changes`), `WalkChanges` also emits the full `FileMeta` for upserts, so the map can be populated there too. The map is scoped to the current backup session and does not persist.

```go
type GDriveSource struct {
    // ...existing fields...
    mimeTypes map[string]string // fileID → mimeType; populated during Walk/WalkChanges
}

func (s *GDriveSource) GetFileStream(fileID string) (io.ReadCloser, error) {
    if mimeType, ok := s.mimeTypes[fileID]; ok && isGoogleNativeMimeType(mimeType) {
        return s.exportFile(fileID, nativeExportMimeType(mimeType))
    }
    // existing download path
    var resp *http.Response
    err := retry.Do(context.Background(), retry.DefaultPolicy(), func() error {
        call := s.service.Files.Get(fileID).SupportsAllDrives(true)
        var err error
        resp, err = call.Download()
        if err != nil {
            if isRetryableGoogleErr(err) {
                return &retry.RetryableError{Err: err}
            }
            return err
        }
        return nil
    })
    if err != nil {
        return nil, err
    }
    return resp.Body, nil
}

func (s *GDriveSource) exportFile(fileID, exportMimeType string) (io.ReadCloser, error) {
    var resp *http.Response
    err := retry.Do(context.Background(), retry.DefaultPolicy(), func() error {
        var err error
        resp, err = s.service.Files.Export(fileID, exportMimeType).Download()
        if err != nil {
            if isRetryableGoogleErr(err) {
                return &retry.RetryableError{Err: err}
            }
            return err
        }
        return nil
    })
    if err != nil {
        return nil, err
    }
    return resp.Body, nil
}
```

### 2.3 `toFileMeta` changes

When the file is a native type, two adjustments are needed:

1. **Name**: Append the export extension so the restored file has a meaningful name. E.g., `"Budget"` becomes `"Budget.xlsx"`.
2. **Extra fields**: Store the original Google MIME type, the export MIME type, and the `headRevisionId` so the metadata record is self-describing and supports change detection:

   ```json
   {
     "mimeType": "application/vnd.google-apps.spreadsheet",
     "exportMimeType": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
     "headRevisionId": "AAAAABBBBBCCCCC"
   }
   ```

`headRevisionId` must be added to the `fields` parameter of the `files.list` call so it is fetched during Walk without an additional API request.

### 2.4 Change detection for native files

**The existing `detectChange` fast-path does not work for native files.** There are two separate problems:

**Problem 1 — size mismatch.** `metadataEqual` compares `Size`. For native files:

* Walk-reported `Size` is always **0** (Drive API reports no size for native files)
* Stored `oldMeta.Size` is the **actual exported byte count** written by the previous backup

So `metadataEqual` always returns false for native files, the fast-path never fires, and the engine re-exports and re-uploads every native file on every run regardless of whether it changed.

**Problem 2 — non-deterministic export output.** Even if the size comparison were fixed, it would not be safe to compare `ContentHash` across export runs. `.docx`/`.xlsx`/`.pptx` are ZIP archives — the Drive serializer embeds timestamps and may vary XML attribute ordering or compression output between runs. PDF exports embed creation timestamps. The binary output is non-deterministic even when the document content is identical: two consecutive exports of an unmodified Doc will almost certainly produce different hashes.

**Fix: `headRevisionId`-based fast-path in `detectChange`.** The Drive API provides `headRevisionId` on every file resource — a stable opaque token that changes exactly when the document is modified. We store it in `FileMeta.Extra["headRevisionId"]` during Walk and use it as the sole change signal for native files in the engine:

```go
// detectChange — native file fast-path
if isGoogleNativeMeta(meta) {
    newRevID, _ := meta.Extra["headRevisionId"].(string)
    oldRevID, _ := oldMeta.Extra["headRevisionId"].(string)
    if newRevID != "" && newRevID == oldRevID {
        // Unchanged: carry forward content ref without re-exporting
        meta.ContentHash = oldMeta.ContentHash
        meta.ContentRef = oldMeta.ContentRef
        meta.Size = oldMeta.Size
        return false, oldRef, nil
    }
    return true, oldRef, nil
}
// existing fast-path for regular files follows...
```

`isGoogleNativeMeta` checks `meta.Extra["mimeType"]` against the known native prefix. When `headRevisionId` is unavailable (e.g. first backup, or a source that doesn't populate it), the function returns `changed = true` and the file is exported normally.

This replaces the broken `mtime`/`size` comparison for native files. `mtime` is kept in `FileMeta` for human-readable restore metadata, but is not used for dedup decisions.

### 2.5 `skipNativeFiles` toggle

Native file export is opt-out via a `WithSkipNativeFiles` source option. When set, native files are excluded from `Walk` entirely — they are never emitted as `FileMeta` entries and never reach the engine. This is equivalent to adding `AND NOT mimeType LIKE 'application/vnd.google-apps.%'` to the `files.list` query.

```go
// WithSkipNativeFiles excludes Google-native files (Docs, Sheets, Slides, etc.)
// from the backup. They will not appear in the snapshot at all.
func WithSkipNativeFiles() GDriveOption {
    return func(o *gDriveOptions) {
        o.skipNativeFiles = true
    }
}
```

The corresponding CLI flag is `-skip-native-files` on the `backup` command for both `gdrive` and `gdrive-changes` source types.

When `skipNativeFiles` is false (the default), export is attempted for all native types. Users who hit the 10 MB export limit on large files and want a clean backup rather than warnings can use this flag to exclude native files until a proper partial-skip mechanism is implemented.

### 2.6 `Walk` query changes

No changes needed to the query. The existing filter `mimeType != 'application/vnd.google-apps.folder'` correctly includes all native files. The `Size()` query also remains unchanged — native files contribute 0 bytes to the estimate, which is acceptable.

### 2.6 `mimeTypes` map population

In `Walk`: `toFileMeta` already has access to `f.MimeType`, so `visitEntryWithPath` (or `toFileMeta` itself) should populate `s.mimeTypes[f.Id] = f.MimeType` for every non-folder entry. `headRevisionId` must be added to the `fields` string in the `files.list` call so it is fetched alongside the existing fields at no extra cost:

```
"nextPageToken, files(id, name, parents, mimeType, size, modifiedTime, owners, trashed, sha256Checksum, headRevisionId)"
```

In `WalkChanges` (`gdrive_changes.go`): the change event includes the full file record, so the same population path applies.

The map is initialized lazily in `Walk`/`WalkChanges` with `make(map[string]string)` before the first page is processed.

## 3. Required Changes

### `pkg/source/gdrive.go`

1. Add `skipNativeFiles bool` and `mimeTypes map[string]string` fields to `GDriveSource` (and `gDriveOptions`).
2. Add `WithSkipNativeFiles() GDriveOption`.
3. When `skipNativeFiles` is true, filter native MIME types out of the `files.list` query in `Walk` (and from `WalkChanges` change processing).
4. Add helpers:
   * `isGoogleNativeMimeType(mimeType string) bool`
   * `nativeExportMimeType(mimeType string) string` — returns the export MIME type
   * `nativeExportExtension(mimeType string) string` — returns the file extension (e.g. `.docx`)
   * `exportFile(fileID, exportMimeType string) (io.ReadCloser, error)`
5. Add `headRevisionId` to the `fields` string in both `files.list` calls (folder pass and file pass).
6. Update `toFileMeta` to:
   * Append the export extension to `Name` for native files
   * Set `Extra["exportMimeType"]` for native files
   * Set `Extra["headRevisionId"]` from `f.HeadRevisionId` for all files
7. Update `visitEntryWithPath` to record `s.mimeTypes[f.Id] = f.MimeType` for non-folder entries.
8. Update `GetFileStream` to route native files through `exportFile`.
9. Initialize `s.mimeTypes` at the start of `Walk`.

### `pkg/source/gdrive_changes.go`

1. In `WalkChanges`, populate `s.mimeTypes` from change events (same logic as Walk); skip native change events when `skipNativeFiles` is true.

### `internal/engine/backup_scan.go`

1. Add `isGoogleNativeMeta(meta *core.FileMeta) bool` helper (checks `Extra["mimeType"]`).
2. In `detectChange`, insert a native-file fast-path before the existing `metadataEqual` check: compare `Extra["headRevisionId"]` between `meta` and `oldMeta`; if equal and non-empty, carry forward `ContentHash`, `ContentRef`, and `Size` from `oldMeta` and return `changed = false`.

### `cmd/cloudstic/main.go`

1. Add `-skip-native-files` boolean flag for `gdrive` and `gdrive-changes` source types; pass it as `WithSkipNativeFiles()` when constructing the source.

### Tests

1. Unit tests for `isGoogleNativeMimeType`, `nativeExportMimeType`, `nativeExportExtension`.
2. Unit test for the `detectChange` native fast-path: verify that matching `headRevisionId` suppresses re-export and mismatched/empty `headRevisionId` triggers it.
3. Table-driven unit test with a mock Drive service verifying that `GetFileStream` routes to `Export` for native MIME types and to `Download` for regular files.
4. Unit test verifying that `Walk` emits no native `FileMeta` entries when `skipNativeFiles` is true.

## 4. Trade-offs and Constraints

### Export size limit

The Drive Export API imposes a **10 MB limit** on exported files. Files larger than 10 MB (e.g. large Sheets) will fail with a 403 `exportSizeLimitExceeded`. These files should be skipped with a warning rather than failing the entire backup. The engine currently treats any `GetFileStream` error as fatal; a future RFC can address partial-skip semantics. For now, the recommended approach is to log a warning and return a sentinel error that the engine can distinguish from a hard failure — or simply log and return an empty reader so the file is recorded as 0-byte in the snapshot (to be decided at implementation time).

### No round-trip restore to Google format

Restored files are in the export format (e.g. `.docx`), not in Google's native format. Re-importing them to Google Drive would re-create them as regular Office files, not native Google Docs. This is an inherent limitation of the export approach and is acceptable for a backup tool whose primary goal is data preservation.

### Incremental dedup for native files

Export output for Office and PDF formats is non-deterministic: two consecutive exports of an unmodified document will produce different byte streams (ZIP timestamps, XML attribute ordering, PDF metadata). Relying on `ContentHash` comparison across backup runs is therefore unreliable — it would always signal a change even for unmodified files.

The `headRevisionId`-based fast-path (section 2.4) is the correct solution. The remaining false-positive case is a collaborator making and then reverting a change between two backups such that `headRevisionId` advances but the logical content is identical to the previous backup. In that case the file is re-exported; the content-addressed chunker will deduplicate the stored chunks if the export happens to be byte-identical (unlikely for non-deterministic formats, but the storage cost is bounded). This is an acceptable trade-off.

### `mimeTypes` map lifetime

The map is only valid for the duration of a single backup session. It is not persisted to the snapshot. If `GetFileStream` is called outside of a Walk context (e.g. for a standalone download command), the map will be empty and the call will fall through to `Download()`, which will fail for native files. A future `GetFileStream` override (e.g. querying `files.get?fields=mimeType` as a fallback) can handle this case.

## 5. Alternatives Considered

### Always export as PDF

PDF is universally viewable and always supported by the Export API. However, PDF is lossy for structured formats (tables, formulas, code): a Sheets file exported as PDF loses all formula data. Office formats (docx/xlsx/pptx) preserve structure and are the right choice for document fidelity.

### Filter native files out of Walk (always-on)

Permanently excluding all `application/vnd.google-apps.*` types from the Walk query would mean users lose all Google-native content from backups entirely, which is unacceptable as the default. The `skipNativeFiles` toggle (section 2.5) provides this as an explicit opt-out for users who prefer it.

### User-configurable export format

Allow users to specify preferred export formats per type via config. This adds complexity for limited benefit — the proposed defaults are sensible for almost all use cases. Format configuration can be added later as an option if demand exists.
