# RFC 0007: Cloud Subdirectory Backup

- **Status:** Adopted
- **Date:** 2026-03-14

## Abstract

This RFC proposes native support for backing up subdirectories of cloud sources (Google Drive, OneDrive) by extending the source URI syntax and removing the confusing `-root-folder` CLI flag.

## Context

Previously, users had to use the `-source gdrive` flag in combination with a separate `-root-folder <folder-id>` flag to scope backups to a specific folder. This design had several flaws:

1. **Discoverability**: Folder IDs are abstract, opaque strings (e.g. `1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgVwQA`) that are hard to find and manage.
2. **Inconsistency**: Local sources support paths in the URI (`local:/path/to/folder`), but cloud sources required a distinct CLI argument.
3. **Display Issues**: The `cloudstic list` command displayed the raw folder ID instead of a human-readable path.

## Proposal

### 1. URI Syntax Enhancement

Extend the `-source` flag to accept an optional path component for cloud sources:

- `gdrive:/Projects/Code`
- `gdrive-changes:/Important`
- `onedrive:/Documents/Finance`
- `onedrive-changes:/Photos`

The CLI will parse this URI into the `scheme` and `path` components automatically.

### 2. Deprecation of `-root-folder`

Remove the `-root-folder` flag from the CLI to eliminate redundancy and confusion. The underlying client library option (`WithRootFolderID`) will be retained for programmatic use where providing a raw ID might be more efficient.

### 3. API Translation

The provided string path will be translated dynamically during initialization:

- **Google Drive**: The string path will be resolved layer-by-layer using `Files.List` queries into a canonical Drive Folder ID.
- **OneDrive**: Microsoft Graph API natively supports path-based addressing (`/me/drive/root:/path/to/folder`), so translation is trivial.

### 4. Implementation Details

- **Full Scan (`Walk`)**: The source iterators will only fetch or yield descendants of the resolved root path.
- **Incrementals (`WalkChanges`)**: Changes returned by delta endpoints that fall outside the configured tree will be filtered out internally by resolving parent chains up to the root path.
- **Display**: `SourceInfo.Path` will store the human-readable path (e.g., `/Projects/Code`) instead of the opaque folder ID, improving the output of the `cloudstic list` command.

## Trade-offs

- **Google Drive API Load**: Resolving a string path layer-by-layer requires additional API calls (one per path segment). This is mitigated by only doing it once during initialization.
- **Path Changes**: If a user renames a folder in Google Drive or OneDrive but does not update their backup script, the backup will fail because the string path no longer resolves. This is standard behavior for file systems (like local or SFTP backups), but different from ID-based addressing which tracks the directory regardless of renames.
