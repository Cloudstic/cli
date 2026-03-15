# RFC 0010: Backup Profiles (YAML Presets)

- **Status:** Implemented
- **Date:** 2026-03-14
- **Affects:** `cmd/cloudstic/backup`, new `cmd/cloudstic/profile`, docs

## Abstract

This RFC introduces backup profiles: reusable YAML presets for backup jobs.

Users can define sources, exclusions, tags, and storage references once, then run:

- a single profile (`cloudstic backup -profile <name>`)
- all profiles (`cloudstic backup -all-profiles`)

The design separates reusable storage definitions from backup profiles and adds
an initial profile-creation command.

It also introduces reusable cloud auth entries so multiple Google/OneDrive
profiles can use independent token files without manual copy/paste.

## Context

`cloudstic backup` currently requires many flags for repeated runs. This is
error-prone in daily use and hard to maintain in scripts.

Typical pain points:

- repeating long source/store/auth flag sets
- managing multiple backup jobs targeting the same store
- keeping local shell scripts in sync with source changes

## Goals

- Define backup jobs in one YAML file.
- Allow running one profile or all profiles from the CLI.
- Reuse shared store definitions across multiple profiles.
- Keep existing flag-based workflows unchanged.

## Non-goals

- No scheduler/orchestrator in this RFC.
- No breaking changes to existing `backup` flags.
- No mandatory migration for existing users.

## Proposal

### 1. Profile file and location

Default path:

- `<config-dir>/profiles.yaml`

where `<config-dir>` is resolved by existing `internal/paths.ConfigDir()`.

Optional override:

- `-profiles-file <path>` on profile-related commands.

### 2. YAML schema

```yaml
version: 1

stores:
  home-s3:
    uri: s3:my-bucket/cloudstic
    s3_region: us-east-1
    s3_profile: prod
    s3_endpoint: https://s3.us-east-1.amazonaws.com
    # Prefer env references for secrets
    s3_access_key_env: AWS_ACCESS_KEY_ID
    s3_secret_key_env: AWS_SECRET_ACCESS_KEY

  local-disk:
    uri: local:/Volumes/BackupStore

auth:
  google-work:
    provider: google
    google_token_file: ~/.config/cloudstic/tokens/google-work.json

  microsoft-personal:
    provider: onedrive
    onedrive_client_id: <client-id>
    onedrive_token_file: ~/.config/cloudstic/tokens/ms-personal.json

profiles:
  photos:
    source: local:/Volumes/Photos
    store: home-s3
    tags: [photos, daily]
    excludes:
      - "*.tmp"
      - ".DS_Store"

  work-drive:
    source: gdrive-changes://Company Data/Engineering
    store: local-disk
    auth_ref: google-work
    skip_native_files: true
```

### 3. Why `stores` is a top-level section

`stores` are reusable infrastructure definitions. Profiles are backup jobs.

Benefits of reference-by-name:

- avoids copy/paste of store settings
- makes rotation of endpoints/credentials easier
- keeps backup job intent separate from backend wiring

### 3b. Why `auth` is a top-level section

`auth` defines reusable cloud OAuth contexts (account/session-level settings),
separate from backup job definitions.

Benefits:

- lets multiple profiles use different Google/OneDrive accounts cleanly
- supports per-account token files instead of one implicit global token
- keeps account credentials separate from source path and retention intent

### 4. CLI behavior

#### Profile command

- `cloudstic profile list`
- `cloudstic profile show <name>`
- `cloudstic profile new`

`profile new` requires existing `auth_ref` values; it does not implicitly create
auth entries.

`profile list` prints all configured stores, auth entries, and profiles.
`profile show` prints one profile with resolved store/auth references.
`profile new` creates or updates one profile entry and can optionally create or
update a referenced store entry.

#### Backup command extensions

- `cloudstic backup -profile <name>`
- `cloudstic backup -all-profiles`
- `cloudstic backup -profile <name> -profiles-file <path>`
- `cloudstic backup -source ... -auth-ref <name>`

When backing up a cloud source without `-auth-ref`, Cloudstic attaches the run
to a provider default auth entry and persists it in `profiles.yaml`:

- Google: `google-default`
- OneDrive: `onedrive-default`

This makes discovered auth state visible in CLI (`auth list` / `profile list`)
without requiring profile mode.

#### Auth command

- `cloudstic auth new`
- `cloudstic auth list`
- `cloudstic auth show <name>`
- `cloudstic auth login -name <name>`

`auth login` triggers provider OAuth flow (if needed) and writes/refreshes token
at the auth entry's configured token file.

When token file flags are omitted on `auth new`, Cloudstic derives a default
path from auth name under `<config-dir>/tokens/`.

Rules:

- `-profile` and `-all-profiles` are mutually exclusive.
- Profile mode is opt-in; existing `-source ...` usage continues unchanged.
- For `-all-profiles`, profiles are run sequentially in stable name order.
- In `-all-profiles`, the command continues after individual failures and
  returns non-zero if any profile failed.

#### Profile creation command

Introduce:

- `cloudstic profile new`

Initial scope:

- create/update one profile entry
- optionally create/update referenced store entry
- require referenced auth entry to already exist when `auth_ref` is set

Example:

```bash
cloudstic profile new \
  -name photos \
  -source local:/Volumes/Photos \
  -store-ref home-s3 \
  -store s3:my-bucket/cloudstic

cloudstic profile new \
  -name work-drive \
  -source gdrive-changes:/Engineering \
  -auth-ref google-work
```

```bash
cloudstic auth new \
  -name google-work \
  -provider google \
  -google-token-file ~/.config/cloudstic/tokens/google-work.json
```

### 5. Flag/profile precedence

When profile mode is used, value precedence is:

1. Explicit CLI flags
2. Referenced `auth` entry values (`auth_ref`)
3. Profile file values (legacy per-profile auth fields)
4. Existing env defaults

This preserves script-level overrides while keeping profiles ergonomic.

### 6. Architecture: engine logic via client

Profile file parsing and serialization live in `internal/engine` and are
exposed through package-level client functions:

- `cloudstic.LoadProfilesFile(path)`
- `cloudstic.SaveProfilesFile(path, cfg)`

CLI commands call these APIs instead of parsing YAML directly in
`cmd/cloudstic`, so profile semantics are reusable for future non-CLI surfaces.

Auth token acquisition itself remains in source/provider codepaths and is reused
by `backup` and `auth login`.

### 7. Secrets and secure storage

#### Short term (this RFC)

- Support environment-variable references in YAML (`*_env` fields).
- Avoid storing plaintext secrets directly in `profiles.yaml`.
- OAuth token files are still created by the existing source auth flows on first
  backup (interactive login if needed). `auth` entries define which token path
  to use, not a new token minting workflow.

#### Why not TPM-only right now

TPM/Secure Enclave usage is platform-specific and better introduced behind a
cross-platform abstraction. This RFC focuses on profile UX and compatibility.

#### Next step (follow-up RFC)

Add a secure secret backend abstraction for profile fields, for example:

- macOS Keychain
- Windows Credential Manager / DPAPI
- Linux Secret Service / KWallet
- optional TPM-backed provider where available

Potential syntax (future):

```yaml
password_secret: keychain://cloudstic/repo/main
```

## Compatibility and migration

- Fully backward compatible: profiles are additive.
- No impact on users who only use flags.
- Existing automation can migrate incrementally profile-by-profile.

## Error handling

- Unknown profile/store reference: clear error with available names.
- Unknown `auth_ref`: clear error with available names.
- `auth_ref` provider mismatch (e.g. OneDrive auth for gdrive source): fail.
- Cycles are impossible in current schema (single store ref).
- Invalid YAML schema: include file path + line when possible.
- `-all-profiles`: print per-profile result summary + final aggregate status.

## Documentation updates (planned)

- `docs/user-guide.md`: add profile setup and examples.
- `docs/sources.md`: mention profile-driven execution path.
- shell completion: add new flags and `profile` command.

## Open questions

- Should we add profile inheritance (`extends`) now or later? (proposed: later)
- Should interactive prompting be globally toggleable (`--no-input`) across all profile/auth commands?
- Should `-all-profiles` support include/exclude filters by tag? (later)
