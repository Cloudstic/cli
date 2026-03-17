# RFC 0014: Workstation Onboarding and Profile Scaffolding

- **Status:** Draft
- **Date:** 2026-03-15
- **Affects:** `cmd/cloudstic`, `internal/paths`, `internal/engine/profiles`, docs

## Abstract

This RFC proposes a guided onboarding workflow for backing up a workstation.
The workflow helps users create practical profile sets quickly, with OS-aware
folder suggestions, portable-drive discovery, and a review-first coverage plan
for the current machine.

The goal is to make first-time setup reliable and obvious without forcing users
to manually craft each profile/store/auth entry.

This RFC is explicitly workstation-first: local folders and portable/external
drives are the primary onboarding target in v1. Cloud roots may be integrated
later, but they are not the center of the initial experience.

## Context

Cloudstic has strong primitives (`profile`, `store`, `auth`) but first-time
users still face setup complexity:

- deciding what to back up
- discovering external/portable drives
- creating multiple related profiles with consistent names/tags

Users commonly want a "set me up for this machine" flow that creates sensible
defaults and can be reviewed before writing configuration.

The differentiator is not merely generating YAML faster. The onboarding flow
should help users reason about workstation coverage: what is protected, what is
intentionally skipped, and which portable drives can keep stable backup lineage
across mount-point changes and machine changes.

## Goals

- Add a guided CLI onboarding path for workstation backup setup.
- Suggest common folders based on the current OS.
- Discover available portable drives and offer profile creation for each.
- Help users review workstation coverage before config is written.
- Reuse existing store/auth/profile model and commands.
- Keep generated configuration explicit, reviewable, and editable.

## Non-goals

- No automatic backup start without user confirmation.
- No hidden writes of secrets or credentials.
- No replacement of existing `profile new` or `store new` commands.
- No daemon/scheduling implementation in this RFC (covered by RFC 0013).
- No cloud-root onboarding flow in v1 beyond reusing an already configured
  store.
- No automatic inclusion of caches, temp directories, or other low-signal
  system data by default.

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
- optimized for local workstation coverage in v1

### 1b. Introduce an explicit onboarding plan model

`setup workstation` should build an in-memory plan before any configuration is
written.

The plan contains:

- candidate workstation folders grouped by category
- discovered portable drives
- proposed profile drafts
- references to existing or newly proposed store entries
- pending collision decisions (`create`, `update`, `rename`, `skip`)
- a coverage summary for final review

Nothing is persisted until the user confirms the final plan.

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
- whether the source is considered portable for lineage purposes

`source discover` should support both human-readable output and JSON output so
the same discovery logic can later power TUI and automation paths.

### 3. OS-aware profile suggestions

`setup workstation` should suggest common folders per platform.

Examples:

- macOS: `~/Documents`, `~/Desktop`, `~/Pictures`
- Linux: XDG user dirs where available (`Documents`, `Pictures`, `Videos`)
- Windows: common user folders (Documents, Desktop, Pictures)

Rules:

- include only directories that exist
- group suggestions by intent, not just by path
- allow selecting/deselecting before generation
- offer custom additional paths

Suggested v1 categories:

- core documents
- desktop/workspace files
- media libraries
- developer projects
- portable/external drives

Portable drives should be shown separately from built-in workstation folders so
users can make an intentional decision about larger or intermittently connected
data sources.

The setup flow should also identify common low-signal paths that are excluded by
default or at least flagged as poor backup targets, such as caches, temp files,
dependency/build artifacts, and OS metadata noise.

### 4. Store/auth integration behavior

Setup flow uses existing references:

- If no store exists, prompt to create one (reusing `store new` patterns).
- For local folders and portable drives, auth is not required.

In v1, workstation onboarding should focus on selecting or creating the backup
store. Cloud source auth flows remain available through existing commands but
are out of scope for the initial workstation setup path.

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

The review should also show workstation coverage status:

- `protected now`: selected folders and drives that will produce profiles
- `skipped intentionally`: suggestions the user deselected
- `not available now`: portable drives seen in previous discovery logic but not
  currently mounted (future-friendly wording; may be empty in v1)
- `warnings`: paths that appear noisy, redundant, or risky to back up by default

User confirms once to persist all generated entries.

The write path should apply the reviewed plan as a batch so the user sees one
coherent onboarding decision rather than a series of independent writes.

### 7. Dry-run semantics

`setup workstation --dry-run` must be side-effect free.

In particular it must not:

- create the config directory
- create token directories
- trigger auth login flows
- resolve or print secret values
- mutate `profiles.yaml`

Preview output should be deterministic enough for integration testing.

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
- Workstation review output should avoid exposing more path detail than the user
  already selected when secrets or private mount naming conventions are involved.

## Testing strategy

- Unit tests for OS folder suggestion logic.
- Unit tests for portable-drive discovery formatting and filtering.
- Unit tests for profile name generation and collision handling.
- Integration tests for `setup workstation --dry-run` deterministic output.

## Rollout plan

1. Add `source discover` with machine-readable and human-readable output.
2. Add `setup workstation --dry-run` with profile generation preview.
3. Add interactive confirmations and write path.
4. Add workstation coverage review and collision handling polish.
5. Integrate with optional scheduling suggestions once RFC 0013 lands.

## Relationship to other RFCs

- RFC 0012 (TUI mode): TUI can call the same onboarding/scaffolding logic.
- RFC 0013 (daemon/scheduling): setup can optionally attach schedule defaults
  after scheduling fields are implemented.

## Open questions

- Should generated names use concise defaults first, adding host prefixes only
  when needed to disambiguate?
- Should exclude-default bundles be always on for workstation onboarding, or
  merely suggested and reviewable?
- Should future workstation onboarding detect likely developer workspaces (for
  example `~/src`, `~/code`, `~/Projects`) with platform-specific heuristics?
