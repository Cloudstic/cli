# RFC 0012: Interactive TUI Mode

- **Status:** Draft
- **Date:** 2026-03-15
- **Affects:** `cmd/cloudstic`, `client.go`, `internal/tui`, docs

## Abstract

This RFC proposes an interactive terminal UI (TUI) mode for Cloudstic focused on
operator workflows: viewing backup status, inspecting stores, and triggering
manual actions.

The TUI is explicitly scoped as an interactive control surface, not a background
scheduler. Daemon/scheduling behavior is handled by a separate follow-up RFC.

## Context

Cloudstic currently provides a strong command-line interface and profile-driven
configuration (`profile`, `store`, `auth`), but multi-profile operations and
health visibility require repeated command usage and manual interpretation.

Common user goals now include:

- seeing which profiles/sources are configured
- checking when each profile last backed up
- triggering a backup quickly
- understanding store health (credentials/connectivity/encryption unlock)

The existing codebase already exposes key primitives through `cloudstic.Client`
and profile configuration models, which makes a TUI feasible without rewriting
core backup logic.

## Goals

- Add a first-party interactive TUI mode to Cloudstic.
- Provide a dashboard for profiles, stores, and auth entries.
- Show last backup metadata (latest snapshot time and source context).
- Allow manual init/backup/check actions from the UI.
- Show live progress and actionable error states.
- Reuse library APIs and existing command internals where that materially reduces duplication.

## Non-goals

- No persistent background scheduler in this RFC.
- No daemon/agent lifecycle management in this RFC.
- No mandatory editing UI for all profile/store/auth fields in v1.
- No replacement of the existing non-interactive CLI workflows.

## Proposal

### 1. Add `cloudstic tui` command

Introduce a new top-level command:

```bash
cloudstic tui
```

This launches the interactive terminal UI.

### 2. Build TUI on top of existing APIs and command internals

TUI behavior should call existing APIs directly:

- load profiles from `profiles.yaml`
- open stores via existing profile/store resolution logic
- use client list/check/backup operations for data and actions
- use a TUI-specific reporter implementation for live progress

The TUI should avoid shelling out to the `cloudstic` binary or re-parsing
`os.Args`, but it may reuse internal command helpers where those helpers already
encapsulate the correct validation and setup behavior. In the current slice,
the TUI uses a small `internal/app` orchestration service backed by CLI-side
adapters for init/backup/check execution.

### 3. Keep the package boundary small and honest

The current implementation uses:

- `internal/app`: TUI orchestration service and backend interface
- `internal/tui`: TUI view model derivation plus rendering/layout
- `cmd/cloudstic`: terminal session lifecycle, input handling, and CLI-backed
  backend adapter

We intentionally keep this service layer narrow. Earlier sketches for
`internal/status` were too thin and were collapsed into `internal/tui`.

### 4. TUI v1 feature scope

- Dashboard list of profiles with:
  - source type/path
  - store reference
  - auth reference (if any)
  - last backup time/status
- Derived readiness state for each profile, including repository-not-initialized
  classification and “never backed up” status
- Manual actions:
  - run init for selected profile store when needed
  - run backup for selected profile
  - run check for the selected profile repository
- Live progress panel for current action.

### 5. Technology choice

Use a lightweight custom terminal renderer and input loop first.

Rationale:

- lower implementation overhead for a narrow operator dashboard
- keeps startup, testing, and portability simple
- avoids introducing a framework before the interaction model stabilizes

This RFC does not rule out adopting Bubble Tea later, but the current
implementation is intentionally framework-free.

## UX Principles

- Keep actions explicit: no hidden destructive operations.
- Show status as stale/fresh when derived from background probes.
- Keep keyboard-first navigation and clear shortcuts.
- Preserve simple fallback: all TUI actions map to existing CLI capabilities.

## Architecture Notes

- Action execution uses `context.Context` for cancellation.
- Progress events come from a custom reporter implementation that feeds TUI
  state updates.
- Health probing should run in background goroutines with bounded concurrency
  and visible loading/error states.
- The current implementation uses a custom alternate-screen session with raw
  terminal input, resize handling, and a split-pane renderer.

## Testing Strategy

- Unit tests for `internal/tui` dashboard derivation and rendering logic.
- Unit tests for `cmd/cloudstic` TUI session and action helpers.
- Smoke integration test for `cloudstic tui --help` and non-interactive launch
  guardrails.

## Rollout Plan

1. Add command scaffold and minimal screen shell.
2. Implement dashboard read path (profiles + latest snapshot info).
3. Add manual init/backup/check actions with live progress.
4. Add richer store health probes and clearer error classification.
5. Polish UX, docs, and examples.

## Relationship to Daemon/Scheduling

TUI and daemon are distinct concerns.

- This RFC defines interactive mode only.
- Scheduling/background execution/OS notifications are deferred to follow-up
  RFC 0013.

Any future shared service layer introduced here should be reusable by both TUI
and a future daemon, but it should be justified by real reuse rather than added
preemptively.

## Open Questions

- Should `cloudstic tui` support a read-only mode for diagnostics?
- Should manual action history be persisted locally in v1 or deferred?
- How much inline config editing should be included in v1 vs later versions?
- Should the TUI service eventually become a broader application service once
  there is a second real caller beyond the TUI?
