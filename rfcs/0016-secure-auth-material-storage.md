# RFC 0016: Secure Auth Material Storage

- **Status:** Implemented
- **Date:** 2026-03-17
- **Affects:** `cmd/cloudstic/{auth,backup,profile}`, `internal/engine/profiles`, `internal/paths`, `internal/secretref`, `pkg/source/{gdrive,onedrive}`

## Abstract

This RFC proposes a more secure storage model for OAuth tokens and related auth
material used by Cloudstic source integrations.

Today, Cloudstic stores Google and OneDrive OAuth token JSON files under the
app config directory and records those file paths in `profiles.yaml`. That is a
reasonable baseline, but it leaves long-lived refresh tokens in plaintext files
managed directly by the application.

This RFC introduces a dedicated auth material storage abstraction for mutable
credential blobs. It keeps file-based storage as a compatible fallback while
allowing native secure stores and app-managed encrypted local storage.

## Context

Cloudstic already has a secret reference abstraction from RFC 0011 for string
secrets such as passwords and API keys (implemented in `internal/secretref`).

Current OAuth handling is different:

- Google and OneDrive tokens are persisted as JSON files in the config dir.
- Profile/auth config stores token file paths, not secure references.
- OAuth tokens are mutable because refresh flows update the stored token.

This makes OAuth material a poor fit for the current `Resolve(ref) string`
model:

- tokens are structured JSON blobs, not single strings
- tokens need write/update semantics, not only read/resolve
- source implementations currently expect a filesystem path for load/save

RFC 0011 explicitly left this space open as a follow-up.

## Goals

- Improve at-rest protection for OAuth tokens and similar auth blobs.
- Reuse and unify with the existing reference-oriented UX.
- Preserve compatibility with current file-path-based configs.
- Support mutable credentials that need atomic read/write/update operations.
- Keep headless and automation workflows working with explicit file-based
  storage.
- Avoid leaking auth material in CLI output, logs, or profile display.

## Non-goals

- No breaking removal of existing `*_token_file` fields in this RFC.
- No mandatory migration of all existing auth entries.
- No provider-specific OAuth flow redesign.
- No claim that retrievable local secrets are safe under a fully compromised
  user session.

## Proposal

### 1. Unified Secret and Auth Material Abstraction

Instead of creating a completely separate system, we will extend the existing
`internal/secretref` infrastructure to support binary blobs and mutable state.

A `Backend` in `internal/secretref` may optionally implement the following
interfaces:

```go
type BlobBackend interface {
    LoadBlob(ctx context.Context, ref Ref) ([]byte, error)
}

type WritableBlobBackend interface {
    BlobBackend
    // SaveBlob must be atomic (e.g., write-to-temp-then-rename for files)
    SaveBlob(ctx context.Context, ref Ref, data []byte) error
    DeleteBlob(ctx context.Context, ref Ref) error
}
```

Rationale:

- OAuth tokens are structured JSON blobs.
- Unifying under `internal/secretref` allows reusing the same URI schemes
  (`keychain://`, `file://`) for both static strings and mutable blobs.
- The `Resolver` will route calls to the appropriate interface.

### 2. Add reference fields for token and credential storage

Extend auth/profile schema with storage references parallel to current path
fields.

Initial fields:

- `google_token_ref`
- `onedrive_token_ref`
- `google_credentials_ref` (for service account JSON)

Resolution precedence for auth material:

1. explicit CLI file path flag
2. `*_ref`
3. existing `*_file` (path-based)
4. derived default app-managed token location

### 3. Unified Reference Schemes

We will use the same scheme names as RFC 0011 to maintain a consistent UX:

- `file://<path>`: Preserves explicit file-based storage.
- `config-token://<provider>/<name>`: App-managed reference relative to config.
- `keychain://<service>/<account>`: Native secure storage for blobs.

### 4. Encrypted Local Fallback and Key Derivation

Cloudstic will support an encrypted local fallback (`config-token://`) for
environments without native keychains.

**Key Derivation Strategy:**

1. If a native store is available, a "Master Key" is generated and stored
   securely (e.g., in macOS Keychain).
2. If no native store exists, the key is derived from a combination of:
   - Machine-specific ID (e.g., `/etc/machine-id` or OS-specific equivalent).
   - User SID/UID.
   - A local salt file with restricted permissions (`0600`).
3. Encryption uses AES-256-GCM (Authenticated Encryption).

**File Permissions:**
All files created by `AuthMaterialStore` (encrypted or plaintext) MUST be
created with `0600` permissions, and their containing directories with `0700`.

### 5. Atomic Updates and Concurrency

To prevent corruption during OAuth token refreshes:

- `SaveBlob` implementations MUST be atomic.
- For `file://` and `config-token://`, this requires writing to a `.tmp` file
  on the same filesystem and using `os.Rename`.
- Cloudstic should use a lightweight advisory lock (e.g., `flock` or a lockfile)
  when updating a specific reference to prevent race conditions between
  concurrent processes using the same profile.

### 6. Update Source Implementations

Google Drive and OneDrive source setup will be refactored to use the
`secretref.Resolver` for both reading and updating tokens.

New flow:

- `source.New(...)` receives a `secretref.Resolver` and the `token_ref`.
- The source loads the initial token bytes.
- When the token is refreshed, the source calls `resolver.SaveBlob(ref, data)`.

## Example configuration

```yaml
version: 1

auth:
  google-work:
    provider: google
    # Storing service account JSON in Keychain!
    google_credentials_ref: keychain://cloudstic/auth/google-creds
    google_token_ref: config-token://google/google-work

  onedrive-personal:
    provider: onedrive
    onedrive_client_id: 11111111-2222-3333-4444-555555555555
    onedrive_token_ref: keychain://cloudstic/auth/onedrive-personal
```

## Security considerations

- **No Leaks:** CLI output and logs must never print token/blob contents.
- **Atomic Renames:** Prevents partial writes during crashes.
- **Secure Fallback:** Encrypted at rest even when not using native stores.
- **Auditability:** Errors should indicate which reference failed without
  revealing the secret.

## Backward compatibility

- `*_token_file` and `google_credentials` (path-based) remain fully supported.
- `secretref.Resolve()` continues to work for string-only backends (like `env://`).

## Testing strategy

- Unit tests for atomic save behavior.
- Integration tests for `keychain://` blob storage on supported OSs.
- Concurrency tests ensuring two processes don't corrupt the same token file.
- Permission checks verifying `0600` on created files.

## Open questions

- **Locking Scope:** Should locking be handled by the `Resolver` or the
  individual `Backend`? (Recommended: `Resolver` to ensure consistency).
- **Migration Tooling:** Should we provide a `cloudstic auth migrate` command
  to move tokens from files to keychain automatically?
