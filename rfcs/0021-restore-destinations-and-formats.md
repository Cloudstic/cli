# RFC 0021: Restore Destinations and Formats

- **Status:** Draft
- **Date:** 2026-07-27
- **Affects:** `cmd/cloudstic`, `client.go`, `internal/engine/restore.go`,
  `pkg/destination`, cloud authentication, raw backend upload clients, docs
- **Depends on:** RFC 0003 (Google Native File Export), RFC 0006
  (Direct-to-Filesystem Restore), RFC 0007 (Cloud Subdirectory Backup), RFC
  0008 (Drive Identity by Name), RFC 0016 (Secure Auth Material Storage)

## Abstract

Cloudstic can back up Google Drive and OneDrive and can store repositories on
S3 and B2, but restore currently ends at the local machine: it writes either a
ZIP archive or a filesystem directory. Recovering into a remote system requires
a second tool and a second upload, loses empty directories, and gives Cloudstic
no way to report partial upload failures.

This RFC separates two choices that the first draft conflated:

- **format** controls the recovered artifact: one ZIP file or an expanded tree
- **destination** controls where that artifact is written: local filesystem,
  Google Drive, OneDrive, S3, or B2

The two axes are independent. A ZIP can be uploaded as one file to Google Drive,
OneDrive, S3, or B2. An expanded tree can be recreated as cloud folders/files
or as object keys below an S3/B2 prefix. Existing direct local paths remain a
shortcut for a `local:` destination, and existing `.zip` inference remains
backward compatible.

The proposal extends the existing `restore -output/-format` model instead of
adding a parallel command, mirrors URI and authentication conventions already
used elsewhere in the CLI, and composes artifact formats with destination
transports behind the restore-engine boundary.

The default is deliberately conservative for remote and expanded output:
existing destination files or objects are skipped, as existing local files are
in directory restore mode. Existing local ZIP overwrite behavior remains
compatible. A repeated remote/tree command can resume a partially completed
restore without replacing files already written. Explicit conflict modes permit
replacement, renaming, or failure when the caller needs a different policy.

## Context

### Current restore surface

`cloudstic restore` currently accepts:

- an optional snapshot reference, defaulting to `latest`
- `-output`, defaulting to `./restore.zip`
- `-format zip|dir`, inferred from `-output` when omitted
- `-path` for one exact file or a trailing-slash subtree
- `-dry-run`
- `-no-verify`
- the existing global repository, profile, output, and verbosity flags

The engine resolves the snapshot, reconstructs paths from `FileMeta` parent
chains, filters the plan, and sends each entry to an
`internal/engine.RestoreWriter`. ZIP and filesystem writers implement that
interface. Filesystem restore creates parent directories, skips existing
entries with warnings, writes files concurrently, and replays supported local
metadata.

That separation is the right extension point. Cloud restore changes the output
writer and authentication, not snapshot resolution, path selection, repository
locking, content reconstruction, or integrity verification.

### Current cloud backup surface

Cloud backup selects an account, drive, and subdirectory in one source URI:

```text
gdrive
gdrive:/Projects
gdrive://Company Data/Finance
gdrive-changes://Company Data/Finance
onedrive:/Documents
onedrive://Company Data/Finance
onedrive-changes://Company Data/Finance
```

The host component is a friendly drive name and the path component is a folder
path. The CLI resolves both to provider IDs. Cloud credentials can come from a
named `-auth-ref`, provider-specific flags, environment variables, or the
provider default auth entries `google-default` and `onedrive-default`.

Restore should reuse these conventions. A user should not need to find opaque
folder or drive IDs, and credentials stored for backup should not need a second
configuration shape.

The `-changes` schemes are intentionally absent from destinations. They choose
an incremental enumeration API for a backup source; they do not describe a
place to upload restored files.

### Current object-store URI surface

Repository configuration already addresses object stores as:

```text
s3:<bucket>[/<prefix>]
b2:<bucket>[/<prefix>]
```

Those URI shapes are suitable for restore destinations too, but the meaning is
different. `-store s3:repo/prod` opens a Cloudstic repository with its object
layout and decorators. `-output s3:recovery/incident-42` writes ordinary
recovered artifacts. Sharing URI grammar must not imply sharing encoding,
credentials, or namespaces.

### Restore is not backup in reverse

A snapshot stores file contents and a portable subset of metadata. It does not
store a complete provider-side object graph:

- Google-native Docs, Sheets, and Slides are exported to `.docx`, `.xlsx`, and
  `.pptx` during backup under RFC 0003.
- OneDrive package items that cannot be downloaded are not present as file
  content.
- sharing permissions, links, comments, revisions, retention labels, and
  provider-specific ownership are not captured as restorable state.
- Google Drive can give an object multiple parents, while the current restore
  model chooses the first reconstructed path.

Remote restore recreates the snapshot's portable recovery artifact. It does not
promise a byte-for-byte reconstruction of provider-internal state.

## Goals

- Keep output format and destination transport independent.
- Restore any supported snapshot directly into My Drive, a Google Shared
  Drive, a personal OneDrive, a named OneDrive drive, S3, or B2.
- Support both one-file ZIP output and expanded-tree output on every
  destination.
- Accept explicit local destination URIs while preserving ordinary local paths
  as a shortcut.
- Preserve the current `restore` command, snapshot selector, path filtering,
  verification, dry-run, JSON, progress, and repository-lock behavior.
- Mirror the cloud URI and auth conventions already used by `backup -source`.
- Recreate directory structure, empty directories where representable, file
  bytes, names, MIME type, and modification time metadata where the destination
  permits it.
- Make the safe, repeatable behavior the default when destination names already
  exist.
- Retry transient failures and use resumable provider uploads for large files.
- Expose the same capability through the public `Client` API.
- Leave the Cloudstic repository and its format untouched.

## Non-goals

- Recreating Google-native editor objects from exported Office/PDF files.
- Restoring OneNote or other package objects that were not downloadable at
  backup time.
- Restoring sharing permissions, owners, comments, versions, shortcuts,
  provider labels, or public links.
- Synchronizing the destination by deleting files not present in the snapshot.
- Persisting upload sessions across process restarts in the first version.
- Recreating every Google multi-parent link. The first restore path remains the
  canonical output path, matching ZIP and filesystem restore.
- Adding Dropbox, Box, SharePoint, or SFTP as restore destinations in this RFC.
- Treating an S3 or B2 output prefix as a Cloudstic repository. Restored
  artifacts are ordinary unencrypted destination objects.

## Proposal

### 1. Keep format and destination independent

`-format` remains an artifact choice:

| Format | Result |
|--------|--------|
| `zip` | One ZIP file containing the filtered restore plan |
| `dir` | An expanded file tree |

`dir` keeps its existing name for CLI compatibility. In the implementation and
documentation it means **expanded tree**, not specifically a POSIX directory.
On Google Drive and OneDrive it creates folders and files; on S3 and B2 it
creates object keys below a prefix.

`-output` selects the destination and target path:

| Destination | Example |
|-------------|---------|
| local shortcut | `./restore.zip`, `./restored` |
| explicit local | `local:./restore.zip`, `local:./restored` |
| Google Drive | `gdrive:/Recovery/restore.zip` |
| named Google drive | `gdrive://Company Data/Recovery` |
| OneDrive | `onedrive:/Recovery/restore.zip` |
| named OneDrive drive | `onedrive://Company Data/Recovery` |
| S3 | `s3:recovery-bucket/incidents/410b18a2` |
| B2 | `b2:recovery-bucket/incidents/410b18a2` |

Every format/destination combination is valid:

| Destination | `zip` | `dir` |
|-------------|-------|-------|
| local | One local ZIP file | Local directory tree |
| Google Drive | One uploaded ZIP file | Drive folder/file tree |
| OneDrive | One uploaded ZIP file | Drive folder/file tree |
| S3 | One ZIP object | One object per file below a prefix |
| B2 | One ZIP object | One object per file below a prefix |

Examples:

```bash
# Existing shortcuts remain unchanged.
cloudstic restore -output ./restore.zip
cloudstic restore -format dir -output ./restored

# Explicit local URI forms are equivalent.
cloudstic restore -format zip -output local:./restore.zip
cloudstic restore -format dir -output local://./restored

# Upload one ZIP artifact to Google Drive.
cloudstic restore 410b18a2 \
  -format zip \
  -output "gdrive://Company Data/Recovery/410b18a2.zip" \
  -auth-ref google-work

# Expand the same snapshot into a Google Drive folder tree.
cloudstic restore 410b18a2 \
  -format dir \
  -output "gdrive://Company Data/Recovery/410b18a2" \
  -auth-ref google-work

# Upload one ZIP object to S3.
cloudstic restore 410b18a2 \
  -format zip \
  -output "s3:recovery-bucket/incidents/410b18a2.zip"

# Expand files as ordinary objects under an S3 prefix.
cloudstic restore 410b18a2 \
  -format dir \
  -output "s3:recovery-bucket/incidents/410b18a2/"
```

This two-axis model leaves room for future artifact formats such as `tar` or
`tar.zst` without multiplying them by every destination.

### 2. Preserve format inference and local shortcuts

When `-format` is explicit, it always wins. The destination scheme never
overrides it.

When `-format` is omitted, Cloudstic preserves the current inference rule using
the target's final component:

1. A final component ending in `.zip`, case-insensitively, selects `zip`.
2. Every other target selects `dir`.

The rule applies after parsing the destination:

| `-output` | Inferred format |
|-----------|-----------------|
| `./restore.zip` | `zip` |
| `./restored` | `dir` |
| `local:./restore.zip` | `zip` |
| `gdrive:/Recovery/restore.zip` | `zip` |
| `gdrive:/Recovery/410b18a2` | `dir` |
| `s3:recovery/410b18a2.zip` | `zip` |
| `s3:recovery/410b18a2/` | `dir` |

The default remains `-output ./restore.zip`, so an existing command never
becomes a network write.

Only recognized destination schemes receive URI treatment. Any other string
continues to be a local path, including relative paths, absolute paths, and
Windows drive-letter paths. This is important because `C:\restore` must not be
interpreted as an unknown `c:` provider.

Explicit local forms are:

```text
local:<path>
local://<path>
```

`local:<path>` is canonical because it matches the existing source and store
URI syntax. `local://` is accepted as a convenience alias and treats everything
after the prefix as an opaque local path rather than applying URL authority
semantics:

```text
local://./restored       -> ./restored
local:///var/restore     -> /var/restore
```

A bare path is exactly equivalent to the corresponding `local:` URI.

### 3. Destination paths depend on the format

For `zip`, `-output` names one destination file or object. It must have a
non-empty final component and must not end in `/`. A `.zip` suffix is
recommended but is not required when `-format zip` is explicit.

For `dir`, `-output` names a destination container:

- a local directory
- a Google Drive or OneDrive folder
- an S3 or B2 prefix

Missing local or drive folders are created in order. Cloudstic restores the
snapshot entries directly below that container; it does not add an implicit
snapshot-ID wrapper.

S3 and B2 have no directories. In `dir` format:

- every file is stored at `<prefix>/<relative restore path>`
- an empty snapshot directory is represented by a zero-byte `<path>/` marker
- directory markers carry `cloudstic-entry-type=directory` metadata
- ordinary file keys never receive a trailing slash

Object keys are built from opaque path segments, not from a prejoined display
path. Within one segment, `%` becomes `%25` and `/` becomes `%2F`; literal `.`
and `..` segments are percent-escaped as well. This keeps the mapping
reversible and prevents one provider-valid filename from turning into extra
object-prefix levels. The original name is also recorded in object metadata
when escaping occurs.

The source and destination parsers share helpers for Google/OneDrive drive
names and for the existing `s3:<bucket>[/<prefix>]` and
`b2:<bucket>[/<prefix>]` forms. `parseSourceURI` remains responsible for
source-only schemes such as `local`, `sftp`, and `*-changes`.

### 4. Reuse Google Drive and OneDrive authentication

The restore command accepts the same provider auth inputs as cloud backup:

- `-auth-ref`
- `-google-credentials`
- `-google-credentials-ref`
- `-google-credentials-json`
- `-google-token-file`
- `-google-token-ref`
- `-onedrive-client-id`
- `-onedrive-token-file`
- `-onedrive-token-ref`
- `-profiles-file`

Resolution order remains:

1. explicit command flag
2. named `-auth-ref`
3. provider default auth entry
4. provider default token location and interactive login where allowed

`-profile` continues to select the Cloudstic repository configuration. Its
backup source and `auth_ref` are not implicitly repurposed as the restore
destination; callers use `-auth-ref` when they want a non-default destination
identity. This avoids surprising behavior when restoring a local-source
profile to cloud storage or restoring between providers.

The existing provider default entries are reused:

- Google Drive uses `google-default`.
- OneDrive uses `onedrive-default`.

No duplicate `google-restore-default` or `onedrive-restore-default` entries are
introduced.

### 5. Make OAuth write-scope escalation explicit

Current backup tokens are read-only:

- Google requests `drive.readonly`.
- OneDrive requests `Files.Read`, `Files.Read.All`, `User.Read`, and
  `offline_access`.

A refresh token cannot silently acquire permissions that were not granted
during consent. Restore therefore validates scopes before it creates a target
folder.

`auth login` gains:

```text
-access read|read-write
```

`read` remains the default for backup-only credentials. `read-write` requests
the least provider scopes that support the configured destination:

- Google uses `drive` when path traversal into an arbitrary existing My Drive
  or Shared Drive is required. `drive.file` alone cannot reliably discover
  arbitrary pre-existing target folders.
- OneDrive uses `Files.ReadWrite` for the signed-in drive and
  `Files.ReadWrite.All` when named drives require it, together with `User.Read`
  and `offline_access`.

When an existing token lacks the required scope, restore fails before any
remote mutation with an actionable command, for example:

```text
auth "google-default" does not grant Google Drive write access;
run: cloudstic auth login google-default -access read-write
```

An interactive restore may offer to run the consent flow, but it must identify
the scope increase and receive confirmation first. A non-interactive restore
never opens a browser or replaces a token implicitly.

Service-account credentials do not use a cached delegated-consent token. Their
provider client is constructed with the write scope, and normal provider-side
folder or Shared Drive permissions remain authoritative.

The source and destination adapters must share authentication/session builders.
The destination implementation must not instantiate a read-oriented
`GDriveSource` or `OneDriveSource` merely to obtain an HTTP client.

### 6. Separate S3/B2 destination credentials from repository credentials

`-store` identifies the Cloudstic repository being read. An `s3:` or `b2:`
`-output` identifies a raw artifact destination. They may use different
accounts, endpoints, buckets, regions, and credentials in the same command.

Existing unprefixed flags continue to configure only the repository. Restore
adds output-specific mirrors:

```text
-output-s3-endpoint
-output-s3-region
-output-s3-profile
-output-s3-access-key
-output-s3-access-key-secret
-output-s3-secret-key
-output-s3-secret-key-secret
-output-b2-key-id
-output-b2-key-id-secret
-output-b2-app-key
-output-b2-app-key-secret
```

The corresponding environment variables use the
`CLOUDSTIC_OUTPUT_{S3,B2}_*` prefix. When no output-specific value is present,
the destination may use provider-standard resolution such as an AWS profile,
standard AWS environment variables, instance identity, or the B2 environment
variables already supported by the backend. It must not copy an explicit
repository credential merely because both URIs use the same provider.

This separation prevents a command such as:

```bash
cloudstic restore \
  -store s3:private-repository/prod \
  -output s3:recovery-drop/incident-42.zip \
  -format zip
```

from ambiguously applying one bucket's flags to the other.

The first version does not add `-output-ref`. Reusing top-level `stores:` entries
would blur the important difference between a Cloudstic repository and a raw
artifact destination. A later configuration RFC may introduce a distinct
`destinations:` map with reusable credentials and base URIs.

### 7. Compose an artifact format with a destination transport

Introduce `pkg/destination` with two provider-neutral transport capabilities:

- `BlobTarget` stores one named, seekable artifact such as a completed ZIP.
- `TreeTarget` creates directories or markers and stores individual file
  entries.

Google Drive, OneDrive, S3, and B2 implement both. The local adapter implements
both through a file or directory. Format selection happens above the transport:

```text
                    +-> ZIP packager -----> BlobTarget
Restore plan -------|
                    +-> tree dispatcher --> TreeTarget
```

This avoids one `RestoreWriter` implementation per Cartesian-product pair. ZIP
construction is identical whether its final transport is local, Drive, S3, or
B2; only the final blob upload changes.

The package exports an `Entry` value. At minimum, an entry carries:

- opaque path segments
- the display path used for progress and filtering
- file or directory type
- size and modification time
- portable MIME hints

Opaque segments matter. A provider object name is not necessarily safe to split
and rejoin as a slash-separated local path. The engine must derive target
segments from the `FileMeta` parent chain and keep each `Name` as one segment.
The existing normalized display path remains the value used by `-path`,
progress, and JSON output.

Destination constructors live in the new package:

```go
target, err := destination.NewLocal(opts...)
target, err := destination.NewGoogleDrive(ctx, opts...)
target, err := destination.NewOneDrive(ctx, opts...)
target, err := destination.NewS3(ctx, opts...)
target, err := destination.NewB2(ctx, opts...)
```

The root client adds a general entry point with format separate from target:

```go
func (c *Client) RestoreTo(
    ctx context.Context,
    target destination.Target,
    format RestoreFormat,
    snapshotRef string,
    opts ...RestoreOption,
) (*RestoreResult, error)
```

`RestoreFormat` has `FormatZIP` and `FormatDir` values. Keeping format as an
explicit argument preserves the same independence in the library API and lets
the existing selection and verification options remain unchanged.

Existing methods remain source compatible:

- `Client.Restore` adapts a ZIP target.
- `Client.RestoreToDir` adapts a filesystem target.

The current internal `RestoreWriter` becomes the tree-dispatch adapter.
`zipRestoreWriter` remains the packager but writes to a staging file before the
`BlobTarget` receives it. The public interfaces must not expose
`internal/core.FileMeta`, which external library users cannot import.

### 8. Prepare the destination before content upload

Every remote target has a read-only preparation phase:

1. Resolve the account, endpoint, bucket, and drive name to stable destination
   coordinates as applicable.
2. Resolve the longest existing prefix of the destination.
3. Determine which missing destination containers would be created.
4. Validate every planned name or object key against destination rules before
   mutation.
5. Read existing entries needed to apply the conflict policy.
6. For `dir`, create missing folders or record the object prefix.

For `zip`, the plan has one conflict point: the final destination file or
object. The target does not create a folder tree for entries inside the archive.

For `dir`, validation happens across the complete filtered restore plan before
step 6. A local snapshot can contain names that OneDrive rejects even though a
snapshot originally backed up from OneDrive cannot. The first version reports
such names and stops; it does not silently rewrite them.

S3/B2 preparation validates the complete derived key set, including directory
markers. A key collision caused by two different entry paths normalizing to the
same key is fatal before upload.

Google Drive permits duplicate sibling names. Every folder lookup therefore has
three outcomes:

- zero matches: create the folder
- one matching folder: reuse it
- more than one matching folder: fail as ambiguous and print the matching IDs

A file where a folder is required, or a folder where a file is required, is a
type conflict and never qualifies for automatic replacement.

Drive targets cache `relative path -> provider folder ID` for the run. Files are
uploaded by parent ID, not by repeatedly resolving a path string.

### 9. Define conflict behavior

Add:

```text
-conflict auto|skip|replace|rename|fail
```

The flag applies to every destination, including local output. The default is
`auto`, which preserves current behavior while remaining conservative remotely:

| Combination | `auto` resolves to |
|-------------|--------------------|
| local + `zip` | `replace` (current `os.Create` behavior) |
| any destination + `dir` | `skip` (current local-directory behavior) |
| remote destination + `zip` | `skip` |

In `zip` format the resolved policy applies once to the destination ZIP
file/object. In `dir` format it applies independently to each destination
entry. An explicit non-`auto` value has identical meaning everywhere.

| Mode | Existing same-name file/object |
|------|--------------------------------|
| `skip` | Leave it untouched, increment skipped and warning counts |
| `replace` | Replace content of the one unambiguous existing file |
| `rename` | Create a sibling with a deterministic available suffix |
| `fail` | Record an entry error and continue with independent entries |

Directory conflicts are handled separately:

- an existing same-name directory is reused
- a same-name file is a type conflict
- `replace` never deletes and recreates a directory
- an ambiguous Google folder match is an error in every mode
- an existing S3/B2 directory marker is reused

For `rename`, Cloudstic chooses the first available name using a
provider-independent rule:

```text
report.pdf
report (restored 1).pdf
report (restored 2).pdf
```

The suffix is inserted before the final extension. Provider-side automatic
renaming is not used because Google Drive permits exact duplicate names and
would not produce behavior consistent with OneDrive. The same algorithm changes
the terminal key component on S3/B2.

`replace` is guarded against races:

- Google updates the one resolved file ID.
- OneDrive uses the existing item ID and an `If-Match` eTag when available.
- S3 uses `If-Match` against the resolved ETag.
- B2 uploads a new file version under the same name; the previous version
  remains recoverable under B2 version semantics.
- a precondition failure is reported instead of overwriting a file that changed
  after preparation.

B2 does not expose the same atomic create-if-absent operation by filename as S3.
Its `skip`, `fail`, and `rename` checks are therefore read-before-write and can
race with another external writer. Cloudstic never deletes the competing or
previous version, reports this limitation in verbose/debug diagnostics, and
documents that callers needing strict single-writer semantics must serialize
B2 restores externally.

No mode deletes unrelated destination entries.

### 10. Preserve verification before remote visibility

The current engine calculates each plaintext content hash while writing the
output. A direct stream into a single-request remote upload could commit an
object before a trailing hash mismatch is known.

`dir` format stages each reconstructed file in a mode-`0600` local temporary
file:

1. Reconstruct the complete plaintext from repository objects.
2. Calculate and compare the content hash unless `-no-verify` is set.
3. Rewind the staging file.
4. Upload it to an uncommitted resumable session or a single provider request.
5. Remove the staging file on success, failure, or cancellation.

Known-bad content is never uploaded. Staging also gives retry logic a seekable
source and prevents a transient network error from downloading and decrypting
the repository content again.

`zip` format stages the complete ZIP artifact:

1. Build and close the ZIP in a mode-`0600` temporary file.
2. Verify each entry while building it.
3. If any entry fails, do not upload the archive.
4. Rewind and upload the finalized ZIP through `BlobTarget`.
5. Remove the staging file on every exit.

This is intentionally stricter than the existing local ZIP behavior, where a
failed entry cannot be retracted from an already open archive. A remote ZIP is
not visible until the complete verified artifact is ready.

`-no-verify` skips only the hash comparison. Files are still staged because
retry and known-length uploads require a rewindable stream.

Staging bounds memory but consumes local disk. `dir` uses at most the
tree-writer concurrency worth of file staging; `zip` can require temporary space
for the full archive. An insufficient temporary volume is actionable and never
causes a fallback to an unsafe direct upload.

### 11. Use resumable, retry-safe destination uploads

Every remote `TreeTarget` supports concurrent files but enforces a
destination-specific semaphore. The repository read concurrency and destination
upload concurrency are independent limits. A `BlobTarget` receives one
finalized artifact and therefore has no per-entry concurrency.

Google Drive behavior:

- Create folders with the Google folder MIME type and `supportsAllDrives=true`.
- Use resumable media uploads for files over the provider's small-upload
  threshold and when retryability is preferable.
- Upload chunks in valid 256 KiB multiples.
- Request pre-generated file IDs for creates so an indeterminate retry cannot
  create a duplicate.
- Use `files.update` by resolved ID for `-conflict replace`.

OneDrive behavior:

- Create folders through the drive-item children API.
- Use simple content upload only within its supported small-file limit.
- Use `createUploadSession` for larger files.
- Upload fragments in valid 320 KiB multiples and resume from the provider's
  reported next offset after transient failures.
- Pass the explicit conflict behavior and use item IDs for replacement.

S3 behavior:

- Use streaming or multipart upload from the seekable staging file.
- Abort incomplete multipart uploads on failure or cancellation.
- Set `Content-Type`, restored-mtime metadata, and directory-marker metadata.
- Never route restored artifacts through `CompressedStore`, `EncryptedStore`,
  `PackStore`, or any other Cloudstic repository decorator.

B2 behavior:

- Use the native large-file upload flow when a staged file exceeds the
  single-upload threshold.
- Cancel unfinished large-file uploads on failure or cancellation.
- Set content type and B2 file-info fields for restored metadata.
- Never route restored artifacts through the repository decorator chain.

All remote destinations:

- honor context cancellation promptly
- retry rate limits, connection failures, and documented transient `5xx`
  responses with the shared retry policy
- honor `Retry-After`
- never retry authentication, authorization, invalid-name, quota, or
  deterministic conflict errors as if they were transient
- keep resumable-session URLs, upload IDs, and signed request URLs out of
  normal, verbose, debug, and JSON output

Upload-session state lives only for the current process in v1. If the process
stops, the next restore starts a new session. Already committed files are
handled by `-conflict`, so remote `auto` resolving to `skip` gives the overall
operation safe coarse-grained resumption.

### 12. Define portable metadata behavior

Expanded-tree targets restore:

- the canonical first-parent hierarchy
- empty directories
- exact stored file bytes
- stored names
- modification time where the destination accepts it
- a best-effort MIME type

MIME selection follows this order:

1. RFC 0003 `exportMimeType` for an exported Google-native file
2. a non-Google-native stored `mimeType`
3. content detection from the staged file and filename
4. `application/octet-stream`

Local `dir` keeps its current POSIX metadata behavior. Remote targets do not
apply POSIX mode, uid, gid, birth time, xattrs, or file flags. They increment
the warning count once per unsupported metadata class, not once per file, so a
large local-filesystem snapshot remains readable.

S3/B2 object metadata uses a small namespaced portable set:

```text
cloudstic-entry-type
cloudstic-mtime
cloudstic-original-name
```

`cloudstic-original-name` is present only when a path segment required escaping
for an object key. Object metadata is advisory; the object body remains the
restored artifact.

Google-native files are restored as the exported binary already stored in the
snapshot:

| Backed-up object | Restored cloud object |
|------------------|-----------------------|
| Google Doc | `.docx` binary |
| Google Sheet | `.xlsx` binary |
| Google Slides | `.pptx` binary |
| Other exported native type | The RFC 0003 export format |

Cloudstic does not upload with a Google Workspace conversion MIME type in v1.
Automatic reverse conversion would be lossy, would differ by destination
provider, and could make the restored bytes differ from the verified bytes.

Provider object IDs stored as `FileID`, source parent IDs, owners, download
URLs, revision IDs, and account identity describe the backed-up source. They
must never be applied as destination IDs or permissions.

### 13. Existing restore options keep their meanings

| Input | Behavior |
|-------|----------|
| snapshot positional | Same latest, full hash, or unambiguous prefix resolution |
| `-format` | Choose ZIP or expanded tree independently of destination |
| `-path file` | Include only that exact file; `dir` creates required ancestors |
| `-path dir/` | Include only that subtree |
| `-dry-run` | Read remote destination state, but perform no creation or upload |
| `-no-verify` | Skip plaintext hash comparison; do not change destination conflict checks |
| `-verbose` | Log each created, reused, skipped, replaced, renamed, or failed entry |
| `-quiet` | Suppress progress while retaining the final summary |
| `-json` | Emit one structured restore result; transport logs remain off stdout |

A remote dry run performs read-only destination resolution so its conflict and
skip counts are useful. In `zip`, it checks the one final file/object conflict.
In `dir`, it may simulate missing containers after the longest existing prefix.
It cannot prove that a future write will pass quota, retention,
conditional-access, or race checks, and the summary says so. Local dry-run
keeps its current no-output behavior.

As today, restore takes a shared repository lock, including during dry-run.
Destination writes do not stamp or otherwise mutate the repository
format.

### 14. Extend results and exit behavior

Additive fields on `RestoreResult`:

```go
type RestoreResult struct {
    // Existing fields remain.
    Destination         string
    Format              string
    ArtifactDisposition string
    FilesSkipped        int
    FilesReplaced       int
    FilesRenamed        int
    DirsReused          int
}
```

For `dir`, `FilesWritten` counts newly created destination files/objects and
`BytesWritten` counts their successfully written bodies. For `zip`,
`FilesWritten` continues to count entries and `BytesWritten` is the finalized
ZIP artifact size. `ArtifactDisposition` reports `created`, `replaced`,
`renamed`, or `skipped` for ZIP; a skipped ZIP is not reconstructed and has
zero written counts. The human summary identifies the destination URI and
format and separates created, replaced, renamed, skipped, failed, and warning
counts.

Target initialization failures are fatal before entry processing. Independent
file failures follow the current recovery-oriented restore model: report the
entry, continue other files, and return a result containing errors. A failed
directory blocks its descendants so they cannot be uploaded into an incorrect
parent. Any result with `Errors > 0` produces a non-zero CLI exit.

Expanded remote restore is not transactional. A failure can leave created
folders, markers, and successfully committed files. The final error message
explicitly recommends rerunning the same command; the default conflict policy
for expanded remote output skips those committed files. A remote ZIP is
committed only after the complete artifact is finalized and verified.

## Provider API Basis

The design relies on provider behavior documented by:

- [Google Drive file creation](https://developers.google.com/workspace/drive/api/reference/rest/v3/files/create)
- [Google Drive resumable uploads](https://developers.google.com/workspace/drive/api/guides/manage-uploads)
- [Microsoft Graph small-file upload](https://learn.microsoft.com/en-us/graph/api/driveitem-put-content?view=graph-rest-1.0)
- [Microsoft Graph upload sessions](https://learn.microsoft.com/en-us/graph/api/driveitem-createuploadsession?view=graph-rest-1.0)
- [Amazon S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html)
- [Amazon S3 multipart-upload cleanup](https://docs.aws.amazon.com/AmazonS3/latest/userguide/abort-mpu.html)
- [Backblaze B2 large files](https://www.backblaze.com/docs/cloud-storage-large-files)
- [Backblaze B2 file information](https://www.backblaze.com/docs/cloud-storage-file-information)

Implementation must recheck numeric upload thresholds and required permissions
against the provider documentation rather than copying them into constants
from this RFC. Those limits are external policy and can change independently of
Cloudstic.

## Security Considerations

### Write-capable OAuth credentials

Read-write Drive credentials are materially more powerful than backup-only
credentials. Scope escalation is explicit, visible in the consent flow, and
validated before mutation. Help output and docs recommend separate named auth
entries when operators want least-privileged backup automation and an
occasionally used recovery identity.

Tokens, service-account JSON, authorization headers, upload-session URLs, and
secret references remain subject to RFC 0016 redaction and storage rules.

### Destination path safety

Provider names are treated as opaque path segments. The engine never accepts a
snapshot name as a URL fragment without provider-appropriate encoding, and it
never uses source provider IDs as destination parent IDs.

The destination root is resolved before writes. A failed or ambiguous lookup
never falls back to the drive root, because that would scatter recovered files
outside the requested folder.

### Repository and artifact destination separation

An S3/B2 destination is raw recovered output, not another Cloudstic repository.
The destination client is constructed directly over the provider SDK and never
inherits the repository's compression, encryption, packing, cache, or index
layers.

Preflight refuses a destination that overlaps the input repository when that
can be established from resolved configuration:

- local paths are compared after absolute-path and symlink resolution
- S3 compares endpoint, bucket, and normalized prefix
- B2 compares bucket identity and normalized prefix

Both ancestor and descendant overlaps are refused. This prevents an expanded
restore from overwriting `config`, `keys/`, `index/`, or content-addressed
objects, and avoids mixing ordinary recovery artifacts into a repository
namespace. A different prefix in the same bucket is allowed only when neither
prefix contains the other.

Raw S3/B2 artifacts are not encrypted by Cloudstic. Operators who need
destination encryption use provider-side encryption or choose an encrypted
archive format in a future RFC.

### Replacement safety

Replacement is opt-in. It requires one unambiguous item of the expected type and
uses provider object IDs plus conditional writes where available. Cloudstic
does not recursively delete a conflicting directory and does not broaden
`replace` into a destination mirror.

### Temporary plaintext

Staging creates short-lived plaintext on the local machine. Files use mode
`0600`, names do not contain the original path, handles are closed before
upload, and cleanup runs on all exits. Documentation calls out the disk-space
and plaintext implications. A future encrypted staging implementation can
replace this without changing destination semantics.

## Repository Compatibility

This feature is a repository read path plus writes to an external destination.
It does not change any object stored under `chunk/`, `content/`, `filemeta/`,
`node/`, `snapshot/`, `index/`, `keys/`, or `config`.

Therefore:

- no repository format version bump is required
- no legacy repository fixture is required
- old repositories remain restorable to the new destinations
- repositories read by a remote restore remain readable by older binaries
- no migration or opportunistic rewrite occurs

Additive `RestoreResult` JSON fields and local profile token updates are outside
the repository compatibility contract.

## Implementation Plan

1. Add one destination parser for local shortcuts/URIs, Google Drive, OneDrive,
   S3, and B2 while reusing the established provider-specific URI helpers.
2. Resolve format independently after destination parsing, preserving `.zip`
   inference.
3. Extract Google/OneDrive auth construction so read sources and write
   destinations request explicit capability scopes.
4. Add `-access` to `auth login` and scope validation with actionable errors.
5. Add output-prefixed S3/B2 credential flags and secret resolution.
6. Add `pkg/destination` with `BlobTarget`, `TreeTarget`, exported entries, and
   test doubles.
7. Add local, Google Drive, and OneDrive destination transports.
8. Add raw S3 and B2 transports with multipart/large-file cleanup, metadata,
   key escaping, and repository-overlap guards.
9. Extend the restore plan with opaque path segments and a destination
   preparation phase.
10. Add per-file tree staging and whole-artifact ZIP staging in
    `internal/engine`.
11. Add `Client.RestoreTo` and keep `Restore`/`RestoreToDir` as compatible
    wrappers.
12. Extend `cmd_restore.go`, `cloudsticClient`, `stubClient`, help/completion
    generation, summaries, and JSON results.
13. Update `docs/user-guide.md`, `docs/sources.md` or a new destinations
    document, and the root README examples.

## Testing Strategy

### Unit tests

- Destination parsing for bare local paths, `local:`, `local://`, Windows drive
  letters, default-drive paths, named drives, S3/B2 buckets, prefixes, quoting,
  roots, and rejected `*-changes` targets.
- The full format/destination matrix, including explicit format overriding
  suffix inference.
- Auth precedence and provider mismatch behavior matching backup.
- Output S3/B2 credential precedence remains independent from repository
  credentials.
- Repository/destination overlap rejection for local, S3, and B2.
- Missing write-scope errors without any mutation.
- Complete-plan name validation before folder creation.
- Opaque segment handling for provider-valid names that contain local path
  punctuation.
- Reversible S3/B2 key escaping and empty-directory markers.
- Google duplicate-folder ambiguity.
- Every conflict mode for files, directories, type mismatches, and races.
- ZIP conflict policy applies before artifact construction.
- Google-native export MIME and filename behavior.
- Unsupported POSIX metadata warning deduplication.
- Hash mismatch proves that no destination upload method was called.
- Per-file and whole-ZIP temporary permissions and cleanup on success, error,
  and cancellation.
- Directory failure blocks descendants.
- Result counters and non-zero CLI exit when entries fail.

### Provider adapter tests

Use `httptest` servers and injected endpoints/HTTP clients to verify:

- exact Google Shared Drive query parameters and `supportsAllDrives`
- pre-generated Google IDs across retries
- resumable status probing and offsets
- OneDrive 320 KiB fragment alignment
- S3 conditional create/replace and multipart abort
- B2 large-file finish/cancel, SHA-1 validation, and file-info metadata
- `Retry-After`, transient retry, permanent failure, and cancellation behavior
- conditional replacement and conflict races
- upload-session URLs, upload IDs, and signed URLs are redacted from logs

### Command tests

- `stubClient` tests for target selection and every existing restore option.
- Golden help regeneration and review for destination/auth/conflict flags.
- Testscript coverage for positional/flag reordering, stdout/stderr separation,
  JSON output, usage failures, and partial-error exit status.
- Existing ZIP and directory restore suites remain unchanged and passing.

### Live tests

Optional live tests, gated by explicit credentials and never part of the
default hermetic suite, cover:

- My Drive and a Google Shared Drive
- personal/default OneDrive and a named drive
- an S3 test bucket and a Backblaze B2 test bucket
- one ZIP and one expanded tree on every destination
- empty directories, small files, multipart files, Unicode names, and mtimes
- interrupted large upload followed by a successful rerun
- read-only credential refusal

Live tests create one uniquely named root, verify its contents through provider
APIs, and remove only that exact root during cleanup.

## Alternatives Considered

### Add separate provider-specific restore commands

Rejected. Snapshot selection, path filtering, verification, locking, result
formatting, and progress are restore behavior. Separate commands would duplicate
that surface and drift.

### Treat providers as `-format` values

Rejected. `gdrive`, `onedrive`, `s3`, and `b2` answer where output goes; `zip`
and `dir` answer how it is packaged. Combining the axes would make ZIP-to-Drive
and ZIP-to-S3 impossible or require compound values such as `gdrive-zip`.

### Add a new `-destination` flag

Rejected. RFC 0006 deliberately established `-output` plus `-format` as the
extensible restore-output model. `-output` can carry a destination URI without
another mutually exclusive path flag.

### Reuse `-source`

Rejected. In `restore`, the source is the Cloudstic repository snapshot.
Calling a write target a source would invert established vocabulary and make
future source-to-destination operations ambiguous.

### Stream repository bytes directly into provider uploads

Rejected for v1. A single-request upload can become visible before the final
content-hash comparison, and reliable retries need a rewindable input. Secure
temporary staging is slower than a pure stream but preserves the existing
integrity promise.

### Reuse `store.ObjectStore` for S3/B2 artifacts

Rejected. The current interface accepts whole `[]byte` values, which is
unsuitable for very large recovered files, and its normal construction path
adds repository encryption, compression, packing, and cache decorators. Raw
artifact destinations need seekable streaming/multipart upload and must never
inherit repository encoding.

### Convert exported Office files back into Google-native documents

Rejected for v1. Conversion can be lossy, changes the verified representation,
and has no OneDrive-equivalent semantic guarantee. The stored export is the
recovery artifact.

### Default to provider-side rename

Rejected. Google allows duplicate sibling names while OneDrive has explicit
rename behavior. Cloudstic needs one predictable cross-provider policy, and the
safe remote/tree resolution of `auto` is `skip`.

## Open Questions

- Should encrypted staging be implemented before general availability, or is
  mode-`0600` temporary plaintext acceptable with explicit documentation?
- Should a later version persist resumable upload checkpoints for very large
  disaster-recovery runs?
- Should SFTP become the next `BlobTarget`/`TreeTarget`, using the same
  destination URI and conflict model?
- Should `tar` or `tar.zst` be the next artifact format now that format and
  destination are independent?
- Should Cloudstic add an encrypted archive format for raw S3/B2 recovery
  artifacts?
- Should a future opt-in mode reverse-convert RFC 0003 exports into
  Google-native objects while retaining the exported binary as a sibling?
