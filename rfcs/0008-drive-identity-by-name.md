# RFC 0008: Drive Identity by Name

- **Status:** Implemented
- **Date:** 2026-03-14

## Abstract

This RFC proposes the removal of the `-drive-id` command-line flag for Google Drive backups in favor of specifying the Shared Drive name directly in the source URI (e.g., `gdrive://<Drive Name>`). This change also aligns OneDrive sources to use the same URI convention for selecting specific drives.

## Context

Previously, users had to supply a `-drive-id` flag alongside their source definition to target a Google Drive Shared Drive (e.g., `cloudstic backup -source gdrive:/folder -drive-id <ID>`).

This approach suffered from several usability issues:

1. **Opaque IDs:** Shared Drive IDs are long, abstract strings that users must manually locate in the web browser URL.
2. **Inconsistency:** The `--root-folder` option was recently removed in RFC 0007 in favor of path-based URI configuration (`gdrive:/folder`). The `-drive-id` flag remained an outlier.
3. **No OneDrive Equivalent:** For OneDrive, there was no intuitive way to specify alternate drives via the CLI in a consistent manner.

## Proposal

### 1. URI Syntax Enhancement

We extend the `-source` flag to accept a Drive Name as the host component of the URI for cloud sources.

- `gdrive://Company Data/Finance` (Backs up the `/Finance` folder within the "Company Data" Shared Drive)
- `onedrive-changes://Personal/Photos` (Backs up the `/Photos` folder within the "Personal" OneDrive drive)
- `gdrive:/Documents` (If no host/drive name is provided, it continues to default to "My Drive")

### 2. Deprecation of `-drive-id`

The `-drive-id` CLI flag is completely removed from the `backup` command and autocomplete scripts. Internally, the client library options (`WithDriveID` for Google Drive) are retained, but the CLI relies entirely on the newly introduced `WithDriveName` options.

### 3. API Translation

The provided Drive Name is resolved to an internal ID dynamically at initialization:

- **Google Drive**:
  - The CLI first attempts a direct `Drives.Get` lookup in case the provided string is actually a valid Drive ID.
  - If that fails, it performs a `Drives.List` query with a filter (`name = 'Drive Name'`) to resolve the ID.
- **OneDrive**:
  - The CLI attempts a direct lookup using `https://graph.microsoft.com/v1.0/drives/<Drive Name>`.
  - If that fails, it fetches all available drives (`/me/drives`) and matches the provided name locally to extract the Drive ID.

## Trade-offs

- **Name Ambiguity**: Unlike IDs, Drive names are not inherently unique. If multiple drives have the identical name, the tool will throw an ambiguity error, forcing the user to rename the drive or (if calling the library programmatically) use the ID directly.
- **Additional API Calls**: Resolving the drive name requires an extra API call during the initialization phase of the backup. Given this only occurs once per run, the performance impact is negligible.
