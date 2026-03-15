# RFC 0011: Profile Credential Storage Backends

- **Status:** Draft
- **Date:** 2026-03-15
- **Affects:** `internal/engine/profiles`, `cmd/cloudstic/{backup,store,profile,auth}`, docs

## Abstract

RFC 0010 added profile-driven backups and env-var indirection for secrets, but it
left native secure credential storage as a follow-up. This RFC proposes a
cross-platform credential reference abstraction for profile secrets.

The design keeps existing `*_env` fields fully compatible while introducing a
new `*_secret` reference model that can resolve secrets from:

- environment variables
- macOS Keychain
- Windows Credential Manager / DPAPI-backed entries
- Linux Secret Service (best effort)

The primary objective is to improve secret-at-rest protection for interactive
users while preserving automation and headless workflows.

## Context

Current profile secret handling (post RFC 0010):

- `profiles.yaml` stores secret env var names, not secret values.
- Runtime resolves secret values with `os.Getenv(...)`.
- OAuth access/refresh tokens are stored in files under the config dir with
  restrictive permissions.

This is a good baseline, but gaps remain:

- users still manage long-lived secrets through shell env setup
- env vars are process-visible and easy to leak via shell history/scripts
- native OS secret stores are not leveraged

## Goals

- Add a stable, cross-platform secret reference model for profile fields.
- Support native credential stores where available.
- Keep current env-var-based workflows working unchanged.
- Fail safely in headless/missing-native-backend environments.
- Ensure CLI output never prints resolved secret values.

## Non-goals

- No breaking removal of existing `*_env` fields in this RFC.
- No mandatory migration of existing `profiles.yaml` files.
- No full redesign of repository key slot encryption (`pkg/keychain`).
- No immediate migration of OAuth token JSON blobs to native stores.

## Proposal

### 1. Add `*_secret` fields to profile store schema

Extend `ProfileStore` with secret reference fields parallel to existing secret
or env fields.

Initial set:

- `password_secret`
- `encryption_key_secret`
- `recovery_key_secret`
- `s3_access_key_secret`
- `s3_secret_key_secret`
- `store_sftp_password_secret`
- `store_sftp_key_secret`

Example:

```yaml
version: 1

stores:
  prod:
    uri: s3:my-bucket/cloudstic
    s3_region: us-east-1
    s3_profile: prod

    # New preferred model
    s3_secret_key_secret: keychain://cloudstic/store/prod/s3-secret-key
    password_secret: keychain://cloudstic/store/prod/repo-password

    # Backward-compatible legacy fields still supported
    s3_access_key_env: AWS_ACCESS_KEY_ID
    s3_secret_key_env: AWS_SECRET_ACCESS_KEY
```

### 2. Secret reference URI format

Introduce a parseable reference syntax:

`<scheme>://<path>`

Phase 1 schemes:

- `env://VAR_NAME`
- `keychain://service/account` (macOS)
- `wincred://target` (Windows)
- `secret-service://collection/item` (Linux)

Notes:

- URI values are references only; they are not secret material.
- Validation happens at parse time (format) and resolve time (backend access).

### 3. Resolver abstraction

Add a small internal abstraction used by profile resolution:

```go
type SecretResolver interface {
    Resolve(ctx context.Context, ref string) (string, error)
}
```

Implementation is composable by scheme and should return:

- descriptive, actionable errors
- typed errors for "backend unavailable" vs "secret not found"

### 4. Resolution precedence

For each credential field, resolution precedence in profile mode becomes:

1. explicit CLI flag (existing behavior)
2. `*_secret` field (new)
3. direct plaintext field, where currently supported (existing)
4. `*_env` field (existing)
5. existing env defaults from global flags (existing)

Rationale:

- explicit operator intent still wins
- new secure references are preferred over legacy env indirection
- backward compatibility remains intact

### 5. CLI behavior and UX

- `store show` and `profile show` display secret references, never resolved
  values.
- Validation errors include which field failed and why, without printing
  secret values.
- `store new` interactive mode may offer to save secrets to native backends and
  write the resulting `*_secret` reference.

Potential follow-up commands (not required in initial implementation):

- `cloudstic secret set`
- `cloudstic secret get` (masked/default-redacted output)
- `cloudstic secret delete`

## Platform behavior

### macOS

- Backend: Keychain.
- Expected strong interactive UX and secure at-rest storage.
- If Keychain is unavailable/locked in non-interactive contexts, return a clear
  error and suggest `env://` fallback.

### Windows

- Backend: Credential Manager (implementation may use DPAPI-backed storage).
- Scope should default to current user context.
- Errors in service-account/scheduled-task contexts should clearly recommend
  fallback patterns.

### Linux

- Backend: Secret Service.
- Availability is environment-dependent (desktop session, DBus, keyring
  daemon).
- Must be treated as best effort; fallback to `env://` is first-class.

## Backward compatibility and migration

- Existing profiles continue to work with no changes.
- `*_env` fields remain supported.
- Users can migrate field-by-field by replacing `*_env` entries with
  `*_secret` references.

Example migration:

```yaml
# Before
password_env: CLOUDSTIC_PASSWORD

# After
password_secret: keychain://cloudstic/store/prod/repo-password
```

## Security considerations

- `profiles.yaml` must continue to avoid raw secret values by default.
- Logs and command output must never include resolved secret values.
- Error messages should name fields and references, but not secret content.
- Native backend implementations should avoid caching plaintext in long-lived
  global state.

## Testing strategy

- Unit tests for URI parsing and precedence merging.
- Unit tests for resolver routing by scheme.
- Mock-backed tests for failure classes (`not found`, `backend unavailable`,
  `permission denied`).
- Platform integration tests should be opt-in and skipped when backend is not
  available.

## Rollout plan

1. Add schema fields and resolver abstraction with `env://` support.
2. Wire resolver into backup/store profile resolution paths.
3. Add native providers incrementally per OS.
4. Update docs and examples to prefer `*_secret` references.
5. Consider deprecation warnings for `*_env` in a later RFC after adoption data.

## Open questions

- Should Linux support KWallet in this RFC or a later follow-up?
- Should secret reference schemes be provider-specific (`keychain://`) or
  normalized under `native://` with OS-specific dispatch?
- Should OAuth token files get a separate `token_secret` model later, or remain
  file-based with stronger path management?
