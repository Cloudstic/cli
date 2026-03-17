# RFC 0016: Secure Auth Material Storage

- **Status:** Draft
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
secrets such as passwords and API keys.

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
- Reuse the existing reference-oriented UX where it fits.
- Preserve compatibility with current file-path-based configs.
- Support mutable credentials that need read/write/update operations.
- Keep headless and automation workflows working with explicit file-based
  storage.
- Avoid leaking auth material in CLI output, logs, or profile display.

## Non-goals

- No breaking removal of existing `*_token_file` fields in this RFC.
- No mandatory migration of all existing auth entries.
- No redesign of RFC 0011 string secret resolution semantics.
- No provider-specific OAuth flow redesign.
- No claim that retrievable local secrets are safe under a fully compromised
  user session.

## Proposal

### 1. Introduce a separate auth material storage abstraction

Keep `secretref.Resolve(...)` focused on retrievable string secrets.

Add a separate abstraction for mutable auth blobs:

```go
type AuthMaterialStore interface {
    Load(ctx context.Context, ref string) ([]byte, error)
    Save(ctx context.Context, ref string, data []byte) error
    Delete(ctx context.Context, ref string) error
}
```

Rationale:

- OAuth tokens are read and rewritten over time.
- Some auth material is JSON and should be treated as an opaque blob.
- A blob-oriented interface avoids overloading the simpler secret resolver.

### 2. Add reference fields for token storage

Extend auth/profile schema with storage references parallel to current path
fields.

Initial fields:

- `google_token_ref`
- `onedrive_token_ref`

Potential follow-up fields if needed:

- `google_credentials_ref`

Resolution precedence for auth material becomes:

1. explicit CLI file path flag
2. `*_token_ref`
3. existing `*_token_file`
4. derived default app-managed token location

This keeps existing CLI flags and file workflows working unchanged.

### 3. Define auth material reference schemes

Initial schemes:

- `file://<absolute-or-managed-path>`
- `config-token://<provider>/<name>`
- `keychain://<service>/<account>`

Scheme intent:

- `file://` preserves explicit file-based storage.
- `config-token://` gives Cloudstic a stable app-managed reference without
  exposing raw paths in user config.
- `keychain://` enables native secure storage on supported platforms for token
  blobs.

Later platform-specific schemes may include:

- `wincred://...`
- `secret-service://...`

### 4. Separate blob refs from string secret refs conceptually

The syntax can remain URI-shaped, but the behavior should differ clearly:

- string secret refs resolve to plaintext values
- auth material refs load and save opaque bytes

This avoids confusing semantics like treating a mutable token JSON document as a
single resolved string.

Implementation may reuse parser/registration patterns from `internal/secretref`,
but the runtime contracts should remain separate.

### 5. Add an encrypted local fallback backend

Cloudstic should support a secure fallback even where native credential stores
are missing or impractical.

Preferred fallback model:

- token blob stored in app config directory
- blob encrypted at rest by Cloudstic before writing
- encryption key stored in the native secret backend when available
- file permissions remain restrictive (`0700` directories, `0600` files)

This gives a better default than plaintext token JSON files while keeping local
portability and deterministic behavior.

If native secure storage is unavailable, the encrypted local backend may derive
or provision a local app-specific key using the best available platform option,
with a documented fallback to plain file storage only when necessary.

### 6. Update source implementations to use bytes, not paths, internally

Google Drive and OneDrive source setup should stop assuming that token state is
always a file on disk.

New flow:

- auth/profile resolution chooses a token storage reference or explicit file path
- source auth loader reads token bytes via the configured backend
- OAuth refresh writes updated token bytes back via the same backend

This keeps provider-specific code focused on OAuth behavior instead of storage
mechanics.

### 7. CLI and UX behavior

`auth new` and related flows should evolve toward reference-first behavior:

- for interactive use, prefer secure app-managed storage by default
- allow explicit `-google-token-file` / `-onedrive-token-file` overrides
- display refs in `auth show` / `profile show`, never token contents

Possible UX additions:

- `cloudstic auth migrate-token-storage`
- `cloudstic auth doctor`

These are not required for the initial implementation.

## Example configuration

```yaml
version: 1

auth:
  google-work:
    provider: google
    google_credentials: /Users/alice/.config/gcloud/application_default_credentials.json
    google_token_ref: config-token://google/google-work

  onedrive-personal:
    provider: onedrive
    onedrive_client_id: 11111111-2222-3333-4444-555555555555
    onedrive_token_ref: keychain://cloudstic/auth/onedrive-personal
```

Backward-compatible legacy form remains valid:

```yaml
auth:
  google-work:
    provider: google
    google_token_file: /Users/alice/.config/cloudstic/tokens/google-work.json
```

## Security considerations

- CLI output and logs must never print token contents.
- Errors may include the failing field/ref, but not secret material.
- Blob backends should avoid long-lived plaintext caches.
- App-managed encrypted files should use authenticated encryption.
- Migration commands should delete replaced plaintext files only after verified
  successful write to the new backend.
- Documentation must clearly describe that native stores improve default posture
  but do not protect against a fully compromised local session.

## Backward compatibility and migration

- Existing `*_token_file` fields remain supported.
- Existing token files continue to work with no migration.
- New interactive flows should prefer `*_token_ref` where supported.
- An optional migration command can convert stored file paths into managed refs.

Migration example:

```yaml
# Before
google_token_file: /Users/alice/.config/cloudstic/tokens/google-work.json

# After
google_token_ref: config-token://google/google-work
```

## Alternatives considered

### 1. Reuse `secretref.Resolve(...)` unchanged

Rejected as the primary design because it only models read-only string
resolution and does not fit mutable JSON token storage.

### 2. Keep plaintext token files, but tighten path handling only

Helpful, but insufficient. Better defaults and path hygiene do not address the
core problem of long-lived refresh tokens sitting unencrypted on disk.

### 3. Store all auth blobs directly in native backends only

Too restrictive for headless, CI, and cross-platform environments where native
stores may be unavailable or unsuitable.

### 4. Encrypt local token files with a native-store-held key

This remains a strong option and is included in this RFC as the preferred
fallback for app-managed storage.

## Testing strategy

- Unit tests for auth material ref parsing and backend routing.
- Unit tests for profile/auth precedence between `*_token_ref` and
  `*_token_file`.
- Unit tests for round-trip load/save of token blobs.
- Provider tests verifying refreshed OAuth tokens persist through the selected
  backend.
- Platform-specific tests should remain opt-in where native stores are not
  always available.

## Rollout plan

1. Add schema fields and storage abstraction for auth material refs.
2. Implement `file://` and `config-token://` backends.
3. Refactor Google Drive and OneDrive token persistence to use the abstraction.
4. Add `keychain://` blob support on macOS.
5. Update interactive auth/profile flows to prefer managed secure storage.
6. Add optional migration tooling and docs.

## Relationship to other RFCs

- RFC 0011 introduced secret references for string secrets and explicitly left
  OAuth token storage as a future follow-up.
- This RFC complements RFC 0011 rather than replacing it.

## Open questions

- Should `google_credentials` service-account JSON remain path-based, gain its
  own `*_ref`, or continue to rely on external provider defaults?
- Should `config-token://` be implemented as encrypted files from the start, or
  first as an abstraction over current plaintext files with encryption added in
  the next step?
- Should native token blob storage reuse the same `keychain://` namespace as
  string secrets, or a distinct `keychain-token://` scheme?
