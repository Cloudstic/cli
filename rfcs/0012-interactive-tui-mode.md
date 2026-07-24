# RFC 0012: Interactive TUI Mode

- **Status:** Draft (Revised)
- **Date:** 2026-03-15
- **Revised:** 2026-07-24 — reverses the Technology Choice decision; see
  [Revision history](#revision-history).
- **Affects:** `cmd/cloudstic`, `client.go`, `internal/tui`, `internal/app`, docs

## Abstract

This RFC proposes an interactive terminal UI (TUI) mode for Cloudstic focused on
operator workflows: viewing backup status, inspecting stores, and triggering
manual actions.

The TUI is explicitly scoped as an interactive control surface, not a background
scheduler. Daemon/scheduling behavior is handled by a separate follow-up RFC.

The original v1 slice shipped with a hand-rolled, framework-free terminal
engine, as decided in the original
[Technology choice](#5-technology-choice-original-2026-03-15--superseded)
section below. This revision reverses that decision, in
[Section 5, revised](#5-technology-choice-revised-2026-07-24): the v1
implementation has grown into roughly
4,300 lines of raw-mode management, byte-by-byte CSI/SGR mouse parsing,
ANSI-aware string measurement, box drawing, and modal overlays via absolute
cursor addressing — a hardened reimplementation of exactly what a real TUI
framework provides for free. [AGENTS.md](../AGENTS.md) already describes
`internal/tui` as "built on Bubble Tea"; this revision makes that true instead
of correcting the description downward.

## Revision history

- **2026-03-15** — Initial draft. Chose a custom, framework-free renderer for
  v1 (Section 5, original).
- **2026-07-24** — Reassessed after v1 shipped (issues #211–#232, 10 of 13
  sub-issues of the tracking epic #132 closed). Reverses the technology
  choice to Bubble Tea (`charmbracelet/bubbletea`, `bubbles`, `lipgloss`).
  See [Current implementation assessment](#current-implementation-assessment-2026-07-24)
  and [Technology choice, revised](#5-technology-choice-revised-2026-07-24).

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
- *(Added 2026-07-24)* Make long-running actions asynchronous and cancellable
  without freezing the rest of the UI.
- *(Added 2026-07-24)* Support a snapshot browser and selective restore flow
  from within the TUI.

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

This launches the interactive terminal UI. *(Shipped in #211.)*

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
adapters for init/backup/check execution. This layer is unaffected by the
2026-07-24 revision below — it is the part of the original design that worked.

### 3. Keep the package boundary small and honest

The current implementation uses:

- `internal/app`: TUI orchestration service and backend interface
- `internal/tui`: TUI view model derivation plus rendering/layout
- `cmd/cloudstic`: terminal session lifecycle, input handling, and CLI-backed
  backend adapter

We intentionally keep this service layer narrow. Earlier sketches for
`internal/status` were too thin and were collapsed into `internal/tui`.

As of the 2026-07-24 revision, this boundary is not honest in practice: the
profile/store/secret-ref forms (`cmd/cloudstic/cmd_tui_profile_form.go`,
`cmd_tui_store_form.go`, ~1,800 lines combined) contain full widget state
machines living in `package main`, not in `internal/tui`. See
[Revised architecture](#revised-architecture) — the Bubble Tea migration is
the natural point to make the code match this section instead of continuing
to describe an aspiration.

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

This scope shipped across ten tracking sub-issues of the epic (#132); see the
[Rollout Plan](#rollout-plan) below for the full list. Scope beyond v1 is now
tracked in [Usefulness roadmap](#usefulness-roadmap) rather than as an
open-ended extension of this list.

### 5. Technology choice (original, 2026-03-15 — superseded)

> **Superseded by [Section 5, revised](#5-technology-choice-revised-2026-07-24)
> below. Kept for the record.**

Use a lightweight custom terminal renderer and input loop first.

Rationale:

- lower implementation overhead for a narrow operator dashboard
- keeps startup, testing, and portability simple
- avoids introducing a framework before the interaction model stabilizes

This RFC does not rule out adopting Bubble Tea later, but the current
implementation is intentionally framework-free.

### 5. Technology choice, revised (2026-07-24)

The interaction model has now stabilized (13 sub-issues, 10 shipped), and the
premise of the original decision — avoid a framework before the shape of the
UI is known — no longer holds. Reassessed against what actually got built,
adopting Bubble Tea is the clear call, not a reflexive rewrite-for-its-own-sake:

- `go.mod` has no TUI framework dependency, yet `internal/tui` and the
  `cmd_tui_*.go` files already reimplement, by hand, most of what
  `bubbletea` + `bubbles` + `lipgloss` provide in hardened, cross-platform
  form: full Unicode input decoding with bracketed paste, mouse support,
  alt-screen and resize handling (including Windows — currently a 9-line
  stub, see Low findings below), diffed frame rendering, text inputs,
  viewports, spinners, progress bars, and tables.
- The Elm architecture (`Model`/`Update`/`View`, with `tea.Cmd` for side
  effects) makes asynchronous, cancellable actions the *default* shape of a
  command instead of a retrofit — directly fixing the two High-severity
  findings below (UI freezes during actions; serial, timeout-free startup
  probes).
- `AGENTS.md` already documents `internal/tui` as "built on Bubble Tea".
  Adopting it resolves a real doc/code mismatch instead of requiring a
  correction in the other direction.

See [Current implementation assessment](#current-implementation-assessment-2026-07-24)
for the finding-by-finding detail behind this call, and
[Revised architecture](#revised-architecture) for the resulting package
layout and staged rollout.

## Current implementation assessment (2026-07-24)

Assessment of the v1 TUI (`internal/tui`, `cmd/cloudstic/cmd_tui*.go`,
`internal/app/tui_service.go`) as shipped under the original technology
choice. Nearly every finding below is a hardened feature that a real TUI
framework provides for free — that single fact is the throughline.

### Keep

The view-model split is genuinely good and should survive the migration
unchanged. `internal/tui/dashboard.go` is a pure derivation — config plus
store probes in, `Dashboard`/`ProfileCard` structs out — with real health
semantics (reachability, repository state, backup freshness) and zero I/O.
`internal/app/tui_service.go` cleanly separates orchestration behind a
stubbable `TUIBackend` interface. This layer is what a framework can't hand
you for free, and it ports over almost verbatim as the Bubble Tea model's
state.

### High severity

**The UI freezes during every operation — no async, no cancel.**
`cmd/cloudstic/tui_runtime.go:273` — `handleAction` runs backup/check/init
synchronously inside the event loop via `runSuspended`. During a long backup,
nothing repaints, the user cannot navigate to another profile, and there is
no cancel key: the activity panel's running status is nearly never visible
live, because the loop only re-renders after the blocking action already
completed. Fix: run actions as async commands that stream progress messages
back into the event loop — the default shape of Bubble Tea's `tea.Cmd`
model — with an explicit cancel key wired to a `context.CancelFunc`.

**Startup blocks on serial, timeout-free store probes.**
`internal/tui/dashboard.go:189` — `BuildDashboardFromConfig` probes every
configured store one at a time, synchronously, with no timeout, before the
first frame is drawn. One unreachable SFTP store means a blank alt-screen for
the duration of a TCP connect timeout. Fix: probe concurrently with a
per-store timeout, and render the first frame immediately with "checking…"
badges that resolve as results arrive.

### Medium severity

**Form text input is ASCII-only; layout math counts runes, not display width.**
`cmd/cloudstic/cmd_tui_profile_form.go:640` — `readTUIModalInput`'s default
case only accepts bytes `0x20`–`0x7e`; accented characters and all non-Latin
input are silently dropped in every form field (profile names, paths, secret
refs). Separately, `internal/tui/shell.go`'s `visibleLen` counts Unicode code
points, not terminal display cells, so CJK or emoji content breaks box
alignment. Both are exactly the class of bug a real input decoder and a
width-aware string library (`uniseg`, used by `bubbles/textinput`) solve by
construction.

**Mouse hit-testing is a parallel reimplementation of the render layout — and
can go stale across a resize.** `internal/tui/shell.go:53` —
`LayoutDashboardWidth` re-derives pixel/cell coordinates with hardcoded
arithmetic (`y += 3`, `leftWidth + 3`) that must stay byte-for-byte in sync
with the actual render path in `dashboardLinesWidth`. Classic drift hazard:
any layout tweak to one function silently breaks click targeting in the
other, with no compiler error. Worse, in `tui_runtime.go:172` the
input-reading goroutine's layout permit channel holds a stale layout across a
terminal resize, so a click immediately after resizing can hit the wrong row.

**Full clear-and-reprint on every keystroke; no diffed rendering.**
`cmd/cloudstic/cmd_tui_activity.go:18` — every render emits `\x1b[2J\x1b[H`
(clear screen, home cursor) before redrawing the entire frame. Flickers
visibly on any non-trivial terminal and scales poorly as panes grow. A real
TUI framework diffs the previous and next frame and only writes the changed
cells.

### Low severity

**Windows has no resize handling at all.**
`cmd/cloudstic/cmd_tui_resize_windows.go` is a 9-line stub — hand-rolling
SIGWINCH-based resize detection is inherently Unix-only, so Windows users get
a TUI that never adapts to terminal size changes after launch.

**Form/modal logic lives in `package main`, contradicting Section 3 above.**
`cmd/cloudstic/cmd_tui_profile_form.go` and `cmd_tui_store_form.go` (~1,800
lines combined) contain full state machines for profile/store/secret-ref
editing, entirely outside `internal/tui`. The Bubble Tea migration is the
natural point to make the code match the documented boundary.

## Revised architecture

```text
internal/tui/
  app.go        — root tea.Model; routes messages between panes
  msgs.go       — typed messages: dashboardLoaded, probeResult,
                  actionProgress, actionDone, …
  dashboard/    — profile list pane
  detail/       — summary/history pane
  activity/     — scrollable log/progress pane (bubbles/viewport)
  forms/        — profile/store/secret-ref forms (bubbles/textinput)
  snapshots/    — snapshot browser + restore flow (new)
  styles/       — lipgloss theme: adaptive light/dark, NO_COLOR
internal/app/   — grows an async action runner: Start(ctx, profile, kind)
                  → progress channel; owns cancellation
cmd/cloudstic/cmd_tui.go — thin: parse flags, tea.NewProgram(...).Run()
```

**What moves and what's deleted:**

- The pure view-model derivation in `dashboard.go` stays essentially
  unchanged — it becomes the model's state, not its renderer.
- The forms move out of `package main` into `internal/tui/forms`, making the
  Section 3 architecture description true instead of requiring it to be
  corrected downward.
- The `Reporter` → progress plumbing becomes `program.Send(actionProgress{...})`
  from a goroutine running the backup/check call — this deletes
  `crlfWriter`, `captureTUIRunnerOutput`, and the suspend/resume-raw-mode
  dance around every action.
- Store probes become concurrent `tea.Cmd`s with per-store timeouts, fixing
  the blocking-startup finding directly.
- `tui_runtime.go`, `cmd_tui_input.go`, the modal input byte readers, and
  `shell.go`'s box-drawing engine are deleted outright, not refactored.

New dependencies: `github.com/charmbracelet/bubbletea`,
`github.com/charmbracelet/bubbles`, `github.com/charmbracelet/lipgloss`.

## UX Principles

- Keep actions explicit: no hidden destructive operations.
- Show status as stale/fresh when derived from background probes.
- Keep keyboard-first navigation and clear shortcuts.
- Preserve simple fallback: all TUI actions map to existing CLI capabilities.
- *(Added 2026-07-24)* Every long-running action must be cancellable from the
  keyboard while it runs.

## Architecture Notes

- Action execution uses `context.Context` for cancellation.
- Progress events come from a custom reporter implementation that feeds TUI
  state updates, now delivered as `tea.Msg` values via `program.Send` rather
  than captured stdout.
- Health probing runs concurrently with bounded concurrency and a per-store
  timeout, rendering "checking…" badges immediately and resolving them as
  results arrive.
- The session runs Bubble Tea's alt-screen program with mouse-cell-motion and
  bracketed-paste enabled; raw-mode, resize, and input decoding are owned by
  the framework instead of hand-rolled per platform.

## Testing Strategy

- Unit tests for `internal/tui` dashboard derivation and rendering logic.
- Unit tests for `cmd/cloudstic` TUI session and action helpers.
- Smoke integration test for `cloudstic tui --help` and non-interactive launch
  guardrails.
- *(Added 2026-07-24)* Golden-model tests via
  `charmbracelet/x/exp/teatest`, which drives a `tea.Model` headlessly and
  asserts on rendered output — a direct replacement for the current approach
  of testing `dashboardLinesWidth` string output.

## Rollout Plan

### Phase 1 — shipped (original scope)

1. Add command scaffold and minimal screen shell. *(#211)*
2. Implement dashboard read path (profiles + latest snapshot info). *(#211,
   #217)*
3. Add manual init/backup/check actions with live progress. *(#211, #213,
   #216)*
4. Add richer store health probes and clearer error classification. *(#213,
   #217)*
5. Polish UX, docs, and examples. *(#215, #219, #220, #221, #222, #232)*

### Phase 2 — Bubble Tea migration (this revision)

| Step | Scope |
| --- | --- |
| 1 | Add `bubbletea`/`bubbles`/`lipgloss` dependencies; build a root model that renders the existing view-model, read-only, alongside the current renderer (feature-flagged) to compare output. |
| 2 | Port actions (backup/check/init) to async `tea.Cmd`s with live progress and a cancel key. |
| 3 | Port the profile/store/secret-ref forms to `bubbles/textinput`-based components in `internal/tui/forms`. |
| 4 | Delete `tui_runtime.go`, `cmd_tui_input.go`, the modal input readers, and `shell.go`'s box engine. |
| 5 | Land the new capabilities in [Usefulness roadmap](#usefulness-roadmap) on the new foundation. |

The three still-open sub-issues under the tracking epic (#218 expand profile
actions, #223 store inventory view, #224 auth inventory view) should land
*after* step 1 of Phase 2 lands, directly on the Bubble Tea model, rather than
being built against the code Phase 2 step 4 deletes.

## Usefulness roadmap

Ranked by value; each assumes the Phase 2 foundation above.

1. **Snapshot browser → selective restore.** The highest-value feature, and
   the engine already has everything it needs
   (`internal/engine/ls_snapshot.go`, path-filtered restore). Drill from
   profile → history → file tree inside a snapshot → restore a selection to
   a chosen directory, with a dry-run preview screen first. Today the history
   view only shows refs the user can't act on.
2. **Live, cancellable, concurrent operations.** Watch a backup's phase and
   byte progress in the activity pane while still browsing other profiles; a
   cancel key; eventually a "backup all enabled profiles" batch mode with a
   per-profile status column.
3. **Error drill-down.** Failures currently collapse into one truncated
   status line. A scrollable full-error-plus-recent-log view turns "backup
   failed" into something actionable without dropping to a shell.
4. **Retention preview.** Surface `forget`/`prune -dry-run` as a "what would
   be deleted" screen before destructive operations run — this is exactly
   where a TUI earns its keep over a plain CLI: showing consequences before
   commitment.
5. **First-run onboarding.** Reuse the existing
   `internal/engine/workstation_setup.go` planning flow so launching
   `cloudstic tui` against an empty config walks the user into a working
   profile instead of showing "No profiles configured."

## Style

- **Fix inverted status colors.** Today warning renders cyan and error
  renders with no color at all (`shell.go:681`) — the visual hierarchy is
  backwards. With `lipgloss`, adopt consistent semantic colors for
  ready/warning/error/disabled states.
- **Respect terminal preferences.** Adaptive palettes for light/dark
  backgrounds and `NO_COLOR` support — the current hardcoded `ui.Cyan`-style
  constants (`internal/ui`) honor neither.
- **Responsive layout.** Stack the two panes vertically below roughly 80
  columns instead of clamping pane widths into unreadable slivers.
- Small robustness touches: a spinner during store probes, relative
  timestamps ("2h ago" instead of `2026-07-19 14:02`), and a persistent
  key-hint bar that updates with focus context instead of one static footer
  line.
- **Keep the current restraint.** Boxes, dim labels, minimal color — the
  aesthetic itself is good and worth preserving; the goal is the same look
  produced by a layout engine instead of by string arithmetic.

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
- *(Added 2026-07-24)* Should Phase 2 land as one large migration branch, or
  should the feature-flagged dual-renderer approach in step 1 ship and stay
  live for a period before the old renderer is deleted in step 4?
- *(Added 2026-07-24)* Do the three open sub-issues (#218, #223, #224) get
  re-scoped onto the Bubble Tea foundation now, or paused until Phase 2 step
  1 merges, to avoid throwaway work against code step 4 deletes?
