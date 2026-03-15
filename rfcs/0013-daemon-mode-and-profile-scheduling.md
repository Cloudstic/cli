# RFC 0013: Daemon Mode and Profile Scheduling

- **Status:** Draft
- **Date:** 2026-03-15
- **Affects:** `internal/engine/profiles`, new `internal/{daemon,scheduler,notify}` packages, `cmd/cloudstic`

## Abstract

This RFC proposes a daemon mode for Cloudstic that runs scheduled jobs in the
background and emits notifications for job outcomes.

It also introduces scheduling metadata in backup profiles so users can define
job cadence in `profiles.yaml` and use the same profile model across CLI, TUI,
and daemon runtimes.

## Context

Cloudstic currently supports manual and scripted backups through CLI commands,
including profile-driven workflows. What is missing is a first-class background
runtime that can:

- execute backups/checks on a schedule
- retain run status/history
- notify users on success/failure

The interactive TUI RFC (RFC 0012) explicitly defers background scheduling to a
separate concern. This RFC defines that concern.

## Goals

- Add a daemon runtime for background execution of profile jobs.
- Add schedule fields to profile configuration.
- Support recurring backup/check jobs with clear status persistence.
- Add OS notifications for successful and failed runs.
- Keep existing manual CLI flows unchanged and compatible.

## Non-goals

- No distributed/orchestrated multi-host scheduler.
- No cloud control plane in this RFC.
- No forced migration for existing profiles without scheduling fields.

## Proposal

### 1. Add scheduling options to profile schema

Extend `BackupProfile` with optional scheduling metadata:

- `schedule` (cron expression)
- `timezone` (IANA TZ, defaults to local timezone)
- `check_schedule` (optional cron expression for integrity checks)
- `notify` (notification preference)

Proposed YAML shape:

```yaml
version: 1

profiles:
  documents:
    source: local:/Users/alice/Documents
    store: prod-s3
    tags: [daily]

    schedule: "0 */6 * * *"        # every 6 hours
    check_schedule: "0 2 * * 0"    # Sunday 02:00
    timezone: "Europe/Paris"
    notify: failures                # always | failures | none
```

Validation rules:

- schedule fields are optional.
- if present, cron syntax must parse.
- timezone must be valid when provided.
- `notify` must be one of `always`, `failures`, `none`.

### 2. Add daemon command group

Introduce daemon commands:

- `cloudstic daemon run` (foreground service loop)
- `cloudstic daemon status` (show daemon/job status)
- `cloudstic daemon trigger <profile>` (manual enqueue)

Initial mode is foreground for easier debugging and compatibility with service
managers (`launchd`, `systemd`, Task Scheduler wrappers).

### 3. Add internal scheduler and job runner

New internal packages:

- `internal/daemon`: orchestration loop, queue, worker lifecycle
- `internal/scheduler`: cron parsing and next-run planning
- `internal/notify`: OS notification abstraction

Execution behavior:

- scheduled run creates a job execution record
- daemon resolves profile/store/auth and runs backup/check via existing client
- result is persisted with timestamps, duration, and error classification

### 4. Persist daemon state

Persist local runtime state for observability and TUI integration:

- last run status per profile
- last error message (sanitized)
- next scheduled run
- recent run history (bounded)

Storage can start as a local JSON file in config dir with atomic writes.

### 5. Notification model

Notification policy is profile-scoped via `notify`.

- `always`: success + failure notifications
- `failures`: failure notifications only
- `none`: no notifications

Notification backend is abstracted by platform to avoid direct daemon coupling
to OS-specific APIs.

### 6. Notification event matrix and noise control

Initial event types:

- backup success
- backup failure
- check failure
- daemon runtime failure (fatal startup/runtime errors)

Default behavior:

- profile `notify: failures` sends only failure notifications.
- profile `notify: always` sends success and failure notifications.
- profile `notify: none` suppresses profile job notifications.
- daemon runtime failures are always emitted regardless of profile-level policy.

Noise-control rules (v1):

- dedupe repeated failures by `(profile, job_type, error_class)` within a short
  time window (for example 30 minutes).
- emit a recovery notification when a profile transitions from failure to
  success.
- avoid per-retry notification spam; notify on terminal outcome.

### 7. Notification configuration layering

Notifications use a layered policy:

1. profile-level `notify` (highest priority)
2. daemon-level default policy (if profile value is omitted)
3. built-in default (`failures`)

This allows gradual rollout while preserving per-profile tuning.

### 8. Notification abstraction

Introduce `internal/notify` with a small interface:

```go
type Event struct {
    Kind       string // backup_success, backup_failure, check_failure, daemon_failure
    Profile    string
    JobType    string // backup | check
    StartedAt  time.Time
    EndedAt    time.Time
    Duration   time.Duration
    Message    string // sanitized summary
    ErrorClass string // optional stable class for dedupe
}

type Notifier interface {
    Notify(ctx context.Context, event Event) error
}
```

The daemon should use a fan-out notifier implementation so multiple channels
can be enabled later without changing scheduler/runner logic.

### 9. Channel rollout

Channel support is incremental:

- v1: desktop OS notification channel (best effort)
- v2: webhook channel (for chat/email bridges)
- v3: additional integrations as needed

All channel failures should be non-fatal for backup execution and surfaced in
daemon status logs/state.

## Backward compatibility

- Existing profiles remain valid; schedule fields are optional.
- Manual `cloudstic backup -profile ...` behavior remains unchanged.
- Daemon mode only activates when explicit daemon command is used.

## Security considerations

- Daemon must not log raw secret material.
- Credential resolution errors must remain descriptive but sanitized.
- Local daemon state files should use restrictive permissions.

## Testing strategy

- Unit tests for schedule parsing and next-run calculations.
- Unit tests for profile schema validation of schedule fields.
- Unit tests for daemon queue and retry/backoff behavior.
- Integration tests for end-to-end scheduled run execution in hermetic mode.

## Rollout plan

1. Add profile schema fields + validation.
2. Add scheduler primitives and schedule preview command support.
3. Add `daemon run` with backup jobs only.
4. Add notifications and `check_schedule` support.
5. Add richer status output and TUI consumption path.

## Open questions

- Should cron be standard 5-field format only, or support aliases/macros?
- Should missed runs on restart be backfilled, skipped, or policy-configurable?
- Should daemon state be JSON-first or bbolt from day one?
- Should notification defaults be global or profile-only?
- Should success notifications be throttled (for very frequent schedules)?
- Should daemon-failure notifications support an out-of-band fallback channel
  when desktop notifications are unavailable?
