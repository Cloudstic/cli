# RFC 0012: Interactive TUI Mode

- **Status:** Draft
- **Date:** 2026-03-15
- **Affects:** `cmd/cloudstic`, `client.go`, new `internal/{app,status,tui}` packages, docs

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
- Allow manual backup/check actions from the UI.
- Show live progress and actionable error states.
- Reuse library APIs (client/engine) rather than shelling out to CLI commands.

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

### 2. Build TUI on top of client/library APIs

TUI behavior should call existing APIs directly:

- load profiles from `profiles.yaml`
- open stores via existing profile/store resolution logic
- use client list/check/backup operations for data and actions
- use a TUI-specific reporter implementation for live progress

The `cmd/cloudstic` command handlers should not be used as a backend for the
TUI runtime (avoid re-parsing `os.Args`, prompt coupling, and stdout scraping).

### 3. Add a small application service layer

Add internal packages to keep concerns separated:

- `internal/app`: orchestration facade for profile-driven actions
- `internal/status`: derived view models (profile card, store health, last run)
- `internal/tui`: Bubble Tea models/views/update loop

This layer should be designed to support a later daemon-backed mode without
requiring a TUI rewrite.

### 4. TUI v1 feature scope

- Dashboard list of profiles with:
  - source type/path
  - store reference
  - auth reference (if any)
  - last backup time/status
- Store health panel with:
  - credentials resolvable
  - backend reachable
  - repository initialized
  - encrypted repo unlock validity
- Manual actions:
  - run backup for selected profile
  - run check for selected profile/store
- Live progress panel for current action.

### 5. Technology choice

Use Charm stack for TUI implementation:

- `github.com/charmbracelet/bubbletea`
- `github.com/charmbracelet/bubbles`
- `github.com/charmbracelet/lipgloss`

Rationale:

- idiomatic event-driven model for async operations
- good list/table/progress primitives
- strong ecosystem for terminal app ergonomics

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

## Testing Strategy

- Unit tests for `internal/status` derivation logic.
- Unit tests for `internal/app` orchestration with mocked client/store behavior.
- TUI model tests for key state transitions (load, run, error, cancel).
- Smoke integration test for `cloudstic tui --help` and non-interactive launch
  guardrails.

## Rollout Plan

1. Add command scaffold and minimal screen shell.
2. Implement dashboard read path (profiles + latest snapshot info).
3. Add manual backup/check actions with live progress.
4. Add store health probes and clearer error classification.
5. Polish UX, docs, and examples.

## Relationship to Daemon/Scheduling

TUI and daemon are distinct concerns.

- This RFC defines interactive mode only.
- Scheduling/background execution/OS notifications are deferred to follow-up
  RFC 0013.

The service/status layer introduced here should be reusable by both TUI and a
future daemon.

## Open Questions

- Should `cloudstic tui` support a read-only mode for diagnostics?
- Should manual action history be persisted locally in v1 or deferred?
- How much inline config editing should be included in v1 vs later versions?
