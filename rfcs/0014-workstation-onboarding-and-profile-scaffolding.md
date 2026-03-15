# RFC 0014: Workstation Onboarding and Profile Scaffolding

- **Status:** Draft
- **Date:** 2026-03-15
- **Affects:** `cmd/cloudstic`, `internal/paths`, `internal/engine/profiles`, docs

## Abstract

This RFC proposes a guided onboarding workflow for backing up a workstation.
The workflow helps users create practical profile sets quickly, with OS-aware
folder suggestions and portable-drive discovery.

The goal is to make first-time setup reliable and obvious without forcing users
to manually craft each profile/store/auth entry.

## Context

Cloudstic has strong primitives (`profile`, `store`, `auth`) but first-time
users still face setup complexity:

- deciding what to back up
- discovering external/portable drives
- creating multiple related profiles with consistent names/tags

Users commonly want a "set me up for this machine" flow that creates sensible
defaults and can be reviewed before writing configuration.

## Goals

- Add a guided CLI onboarding path for workstation backup setup.
- Suggest common folders based on the current OS.
- Discover available portable drives and offer profile creation for each.
- Reuse existing store/auth/profile model and commands.
- Keep generated configuration explicit, reviewable, and editable.

## Non-goals

- No automatic backup start without user confirmation.
- No hidden writes of secrets or credentials.
- No replacement of existing `profile new` or `store new` commands.
- No daemon/scheduling implementation in this RFC (covered by RFC 0013).

## Proposal

### 1. Add onboarding command

Introduce:

```bash
cloudstic setup workstation
```

Behavior:

- interactive by default
- supports `--dry-run` to preview generated profiles/stores
- supports `--yes` for non-interactive acceptance of defaults

### 2. Add portable-drive discovery command

Introduce:

```bash
cloudstic source discover
```

Optional filter:

```bash
cloudstic source discover --portable-only
```

Output includes enough metadata for onboarding decisions:

- source URI candidate (for `local:`)
- display name / mount point
- portable identity hints (volume UUID/label)

### 3. OS-aware profile suggestions

`setup workstation` should suggest common folders per platform.

Examples:

- macOS: `~/Documents`, `~/Desktop`, `~/Pictures`
- Linux: XDG user dirs where available (`Documents`, `Pictures`, `Videos`)
- Windows: common user folders (Documents, Desktop, Pictures)

Rules:

- include only directories that exist
- allow selecting/deselecting before generation
- offer custom additional paths

### 4. Store/auth integration behavior

Setup flow uses existing references:

- If no store exists, prompt to create one (reusing `store new` patterns).
- If cloud source selected and auth missing, prompt to create/select auth entry.
- For local folders and portable drives, auth is not required.

### 5. Naming and tagging conventions

Generated profiles should follow predictable names and tags:

- name: `<hostname>-<folder-key>` or `<folder-key>` when unique
- tags include baseline context (for example `workstation`, `portable`)

On collisions:

- prompt to overwrite, rename, or skip

### 6. Review-first UX

Before saving, show a summary table:

- profile name
- source URI
- store ref
- auth ref (if any)
- tags

User confirms once to persist all generated entries.

## Command examples

```bash
# Interactive onboarding
cloudstic setup workstation

# Preview only (no writes)
cloudstic setup workstation --dry-run

# Discover drives and mounts
cloudstic source discover --portable-only
```

## Compatibility and migration

- Existing configs remain valid and unchanged unless user confirms setup writes.
- Onboarding is additive; manual profile management continues to work as-is.
- Generated profiles can be modified with existing `profile show/new` commands.

## Security considerations

- Setup flow must never print resolved secret values.
- Store/auth creation reuses existing secure secret-reference flow.
- `--dry-run` should redact sensitive fields in preview output.

## Testing strategy

- Unit tests for OS folder suggestion logic.
- Unit tests for portable-drive discovery formatting and filtering.
- Unit tests for profile name generation and collision handling.
- Integration tests for `setup workstation --dry-run` deterministic output.

## Rollout plan

1. Add `source discover` with machine-readable and human-readable output.
2. Add `setup workstation --dry-run` with profile generation preview.
3. Add interactive confirmations and write path.
4. Integrate with optional scheduling suggestions once RFC 0013 lands.

## Relationship to other RFCs

- RFC 0012 (TUI mode): TUI can call the same onboarding/scaffolding logic.
- RFC 0013 (daemon/scheduling): setup can optionally attach schedule defaults
  after scheduling fields are implemented.

## Open questions

- Should generated names default to host-prefixed or concise names?
- Should onboarding include an optional "exclude defaults" profile template
  per OS (cache/build artifacts/system temp)?
- Should `source discover` support JSON output in v1 for automation use?
