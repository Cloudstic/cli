# AGENTS.md

This file provides guidance to agents when working with code in this repository.

## Project Overview

Cloudstic CLI is a content-addressable, encrypted backup tool written in Go. It supports multiple data sources (local filesystem, Google Drive, OneDrive, SFTP) and multiple storage backends (local, S3/R2/MinIO, Backblaze B2, SFTP, hybrid PostgreSQL+B2). Backups are deduplicated via content-addressing, compressed with zstd, and encrypted with AES-256-GCM.

## Build & Development Commands

```bash
# Build the binary
go build -o bin/cloudstic ./cmd/cloudstic

# Run all tests (unit + hermetic e2e)
go test -v -race -count=1 ./...

# Run a single test
go test -v -run TestName ./path/to/package

# Run the full check script (fmt + lint + test + coverage)
./scripts/check.sh

# Lint
golangci-lint run ./...

# Format
go fmt ./...
```

### E2E Test Modes

E2E tests in `e2e/` are controlled by `CLOUDSTIC_E2E_MODE`:

- `hermetic` (default) — local filesystem + Testcontainers (MinIO, SFTP). Requires Docker.
- `live` — real cloud vendor APIs (requires secrets).
- `all` — runs both.

Docker-based hermetic tests (MinIO store, SFTP source/store) are automatically skipped if `/var/run/docker.sock` is not available.

## Architecture

### Package Layout

- `client.go` (root) — Public `Client` API. Re-exports types from internal packages using Go type aliases. This is the library entry point for programmatic use.
- `cmd/cloudstic/` — CLI entry point (`package main`). `command.go` defines the recursive `command` tree (`leaf`/`group`) and its shared dispatcher; `commands.go` holds `commandRegistry()`, the ordered list that is the single source of truth for the command surface: `main.go`'s `runCmd()` dispatches from it, `printUsage()` renders its `COMMANDS` listing from it, and the shell-completion command lists are generated from it. Each command's `run*()` function lives in its own `cmd_*.go` file (e.g. `cmd_backup.go`, `cmd_key.go`). Subcommands: `init`, `backup`, `restore`, `list`, `ls`, `prune`, `forget`, `diff`, `break-lock`, `key` (with `list`/`add-recovery`/`passwd`), `check`, `cat`, `profile`, `auth`, `store`, `source`, `setup`, `tui`, `completion`. Uses Go's `flag` package (no cobra/viper); `reorderArgs()` in `flags.go` allows flags after positional args. The interactive terminal UI lives in `cmd_tui*.go`.
  - Commands are free functions taking the dependency container first: `run<Name>(r *runner, ctx, ...)` (and `exec<Name>(r, ctx, ...)` for testable sub-steps). The `runner` struct (`runner.go`) carries `out`/`errOut` writers and the client, so command output is capturable in tests; only I/O primitives (`fail`, `writeJSON`, `openClient`, `prompt*`) remain methods on it.
  - Presentation is separate from orchestration: `print*`/`render*` helpers are free functions taking an `io.Writer` first (`printBackupSummary(out, res)`), so result formatting cannot reach the client or command flow.
  - `cloudsticClient` (`client_iface.go`) is the interface `runner` depends on — satisfied by the real `*Client` and by `stubClient` (`stub_client_test.go`) in unit tests.
- `internal/engine/` — Business logic for each operation (backup, restore, prune, forget, diff, list). Each operation has a `*Manager` struct (e.g. `BackupManager`, `RestoreManager`) with a `Run(ctx)` method.
- `internal/core/` — Domain types: `Snapshot`, `FileMeta`, `Content`, `HAMTNode`, `RepoConfig`, `SourceInfo`. Also contains `ComputeJSONHash` which is the canonical content-addressing function.
- `internal/hamt/` — Persistent Merkle Hash Array Mapped Trie. Backed by the object store. Used to track file→filemeta mappings across snapshots. `TransactionalStore` buffers writes and flushes only reachable nodes.
- `pkg/store/` — `ObjectStore` interface and all implementations. Also contains `Source` and `IncrementalSource` interfaces for backup data sources.
- `pkg/crypto/` — AES-256-GCM encryption/decryption, HKDF key derivation, BIP39 mnemonic recovery keys.
- `internal/app/` — Orchestration layer shared by the CLI and TUI. `TUIService` sits on top of a `TUIBackend` interface (satisfied by the real client, stubbable in tests) and owns profile listing, health checks, and backup actions.
- `internal/tui/` — Interactive terminal dashboard built on Bubble Tea (`dashboard.go`, `shell.go`). The `cmd_tui*.go` files in `cmd/cloudstic/` only wire it to `internal/app`; the widget/state logic lives here.
- `internal/ui/` — Non-interactive console progress reporting and terminal helpers (used by plain CLI commands, distinct from the `internal/tui` dashboard).
- `internal/secretref/` — Resolves `scheme://path` secret references through pluggable, platform-specific backends (see Secret References below).
- `pkg/keychain/` — OS keychain integration and encryption key-slot helpers.
- `internal/paths/` — Config-directory and token-path resolution (`ConfigDir()`), plus `MachineID()`.
- `internal/logger/`, `internal/retry/`, `internal/sftp/` — Structured logging, retry/backoff helpers, and shared SFTP client used by both the SFTP source and store.

### Store Layering (Decorator Pattern)

Stores are composed as a decorator chain. The order matters:

```
CompressedStore → EncryptedStore → MeteredStore → [PackStore] → KeyCacheStore → <backend>
```

- `CompressedStore` — zstd compression on write, auto-detects zstd/gzip/raw on read.
- `EncryptedStore` — AES-256-GCM. Passes through objects under `keys/` prefix unencrypted (key slots).
- `MeteredStore` — Tracks bytes written for reporting.
- `PackStore` (optional) — Bundles small objects (<512KB) into 8MB packfiles to reduce API calls. Uses a bbolt-backed catalog.
- `KeyCacheStore` — Caches key existence in a temporary bbolt database to avoid redundant `Exists`/`List` calls against remote backends. Uses `singleflight` to deduplicate concurrent writes for the same key.
- Backend: `LocalStore`, `S3Store`, `B2Store`, `SFTPStore`, or `HybridStore` (PostgreSQL for metadata + B2 for chunks).

### Object Key Conventions

All objects are addressed by `<type>/<sha256>`:

- `chunk/<hash>` — Raw file data chunks
- `content/<hash>` — Chunk manifests (list of chunk refs, or inline data for small files)
- `filemeta/<hash>` — File metadata (name, type, parents, content hash)
- `node/<hash>` — HAMT internal/leaf nodes
- `snapshot/<hash>` — Point-in-time backup snapshots
- `index/latest` — Mutable pointer to latest snapshot
- `index/snapshots` — Snapshot catalog: lightweight summaries of all snapshots, used to avoid fetching each snapshot object individually. Self-heals via reconciliation with `LIST snapshot/` on load.
- `index/packs` — Pack catalog (when packfiles enabled)
- `keys/<slot>` — Encryption key slots (stored unencrypted)
- `config` — Repository marker (unencrypted)

### Backup Flow

1. `BackupManager` acquires a shared lock, loads the previous snapshot (if any) for its source identity.
2. Source is scanned via `Walk()` (full) or `WalkChanges()` (incremental, for gdrive-changes/onedrive-changes).
3. New/changed files are chunked (`internal/engine/chunker.go`) using FastCDC, content-addressed, and uploaded.
4. The HAMT tree is updated with new filemeta refs. `TransactionalStore` buffers all intermediate HAMT nodes and only flushes reachable ones from the final root.
5. A new `Snapshot` object is written, and `index/latest` is updated.

### Encryption Model

- On `init`, a random 32-byte master key is generated and wrapped into key slots (password-based via scrypt, platform key, KMS-wrapped platform key, or BIP39 recovery key).
- Key slots are stored under `keys/` prefix, which the `EncryptedStore` passes through unencrypted.
- An HMAC dedup key is derived from the encryption key via HKDF for content-addressing without exposing plaintext hashes.
- `kms-platform` slots use AWS KMS envelope encryption (master key wrapped by a KMS CMK). The CLI supports these via `-kms-key-arn` flag or `CLOUDSTIC_KMS_KEY_ARN` env var. See `pkg/store/kms.go`.

### HybridStore

Routes metadata objects to PostgreSQL (with RLS tenant isolation via `SET LOCAL cloudstic.tenant_id`) and chunk data to B2. Metadata is also written through to B2 for disaster recovery.

### Configuration & Profiles

- **Config directory** — resolved by `internal/paths.ConfigDir()`: `CLOUDSTIC_CONFIG_DIR` if set, else `os.UserConfigDir()/cloudstic` (e.g. `~/.config/cloudstic`), created `0700`. Holds the profiles file, OAuth tokens, etc.
- **Profiles** — a profile bundles a named source + store (+ secret refs) so users run `cloudstic backup -profile <name>` instead of re-specifying flags. Persisted via `LoadProfilesFile`/`SaveProfilesFile` (root package, implemented in `internal/engine/profiles.go`). Managed from the CLI (`cmd_profile.go`, `cmd_setup.go`) and the TUI. See RFC `rfcs/0010-backup-profiles.md`. The active profile / file come from `CLOUDSTIC_PROFILE` / `CLOUDSTIC_PROFILES_FILE`.

### Secret References

Store and source credentials can be stored as `scheme://path` references rather than inline secrets, resolved by `internal/secretref`. Each scheme is a pluggable, platform-gated backend:

- `env://VAR` — read from an environment variable (read-only).
- `keychain://…` — macOS Keychain (`*_darwin.go`).
- `secret-service://…` — Linux Secret Service / libsecret (`*_linux.go`).
- `wincred://…` — Windows Credential Manager (`*_windows.go`).
- file-backed refs for a writable, cross-platform fallback.

Backends expose `Load`/`Save`/`Delete`; unsupported operations return a typed `secretref.Error` (`KindInvalidRef`, `KindNotFound`, `KindBackendUnavailable`). Non-native backends compile to `*_stub.go` no-ops on other platforms.

## Environment Variables

| Variable | Purpose |
|----------|---------|
| `CLOUDSTIC_PASSWORD` | Password for the password-based key slot (non-interactive unlock). |
| `CLOUDSTIC_ENCRYPTION_KEY` | Raw master key (base64), bypassing key slots. |
| `CLOUDSTIC_RECOVERY_KEY` | BIP39 recovery mnemonic for unlock/recovery. |
| `CLOUDSTIC_KMS_KEY_ARN` / `CLOUDSTIC_KMS_REGION` / `CLOUDSTIC_KMS_ENDPOINT` | AWS KMS envelope-encryption config for `kms-platform` slots. |
| `CLOUDSTIC_STORE` / `CLOUDSTIC_SOURCE` | Default store / source URI when no flag is given. |
| `CLOUDSTIC_PROFILE` / `CLOUDSTIC_PROFILES_FILE` | Active backup profile and override for the profiles file path. |
| `CLOUDSTIC_CONFIG_DIR` | Override the config/state directory (default `~/.config/cloudstic`). |
| `CLOUDSTIC_{STORE,SOURCE}_SFTP_{PASSWORD,KEY,KNOWN_HOSTS,INSECURE}` | SFTP auth/host-key config for the store and source backends. |
| `CLOUDSTIC_DISABLE_PACKFILE` | Disable the `PackStore` small-object bundling layer. |
| `CLOUDSTIC_E2E_MODE` | E2E test mode: `hermetic` (default), `live`, or `all` (see below). |

`CLOUDSTIC_TEST_*` and `CLOUDSTIC_VOLUME_UUID` are test-only knobs, not user-facing.

## Documentation Map

Deep-dive docs live in `docs/` and design records in `rfcs/`:

- `docs/spec.md` — format/protocol specification.
- `docs/storage-model.md` — object model and store layering.
- `docs/encryption.md` — key hierarchy, slots, and the encryption model.
- `docs/sources.md` — supported data sources and their identity model.
- `docs/user-guide.md` — end-user command reference.
- `rfcs/NNNN-*.md` — numbered design proposals; add a new one for substantial features (see RFC 0010 for the profiles design as a template).

## Development Best Practices

### When Adding New Features

When implementing new functionality, always consider the following:

1. **Documentation** — Check if user-facing documentation needs to be updated:
   - `docs/user-guide.md` — Add command documentation with usage examples, flags, and descriptions.
   - `README.md` — Update if the feature changes the quick start or high-level overview.
   - Code comments — Document public APIs, especially in `client.go` and package interfaces.

2. **Unit Tests** — Add test coverage when it makes sense:
   - Always add tests for new public API methods (e.g., `Client.*()` methods).
   - Test both success and error cases.
   - Test integration with encryption/compression if applicable.
   - Use the existing test patterns (see `client_test.go`, `internal/engine/*_test.go`).
   - Mock stores are available in `internal/engine/mock_test.go` for testing.

3. **Client API** — For new operations, expose them via the `Client` struct:
   - CLI commands should use `Client` methods, not directly access stores.
   - This allows library users to programmatically use the functionality.
   - Follow the pattern: define types/options, add a `Client.*()` method, implement in `internal/engine/` if complex.

4. **CLI Integration** — For new commands:
   - Add a `cmd_<name>.go` file with a `run<Name>(r *runner, ctx context.Context, a *<name>Args) int` free function (plus `exec<Name>(r, ctx, ...)` for testable sub-steps — see existing `cmd_*.go` for the pattern).
   - Declare the command's complete input surface in `declare<Name>Args(g *globalFlags) (*<name>Args, commandInput)`. Put flags in `commandInput.flags` using `stringFlag`/`boolFlag`/`intFlag`/`valueFlag`, and positional arguments in `commandInput.positionals` using `requiredPositional`/`optionalPositional`/`requiredPositionals`/`remainingPositionals`. These declarations also carry environment, secret, help, and completion metadata.
   - Mark any flag that carries a credential with `asSecret()`. Environment values are resolved *after* parsing (`applyEnvDefaults`), never baked into a flag's default, so `-h` shows the built-in default and the variable *name* (`[$CLOUDSTIC_PASSWORD]`) but never a live value. `TestSecretEnvValuesNeverAppearInHelp` fails if that regresses.
   - Do **not** write a flag-set builder, parser, or finish hook. Declare the runnable command with `leaf(name, summary, groups, declare<Name>Args, run<Name>, opts...)`; the dispatcher builds and parses its flags, resolves environment values, records their provenance, validates and binds positionals, and hands `run<Name>` a ready args struct.
   - Keep semantic validation and derivation that depends on multiple values at the start of `run<Name>` (or in a focused helper called from it). Input binding belongs to the declaration; domain rules do not belong to parser callbacks. Use `withUsageOnError()` when parse failures should include the command's derived synopsis. Full `-h` output is generated automatically from the command declaration; never add a command-specific help callback.
   - Opt into only the global flag groups the command needs by passing `repoCommandGroups` (or `backupCommandGroups` for commands that read a source) to `leaf`. Groups are declared in `flags.go`; a command that never reads a source must not pull in `sourceSFTPFlagSpecs`, so its `-h` output stays relevant.
   - Declare the command in its own `cmd_<name>.go` with generic `leaf`, or `group(name, summary, children...)` for a command with subcommands (`command.go`). Groups need no dispatch code: `command.execute` handles subcommand lookup, the subcommand usage listing, and unknown-subcommand errors for every group.
   - Add the declaration to the ordered list in `commandRegistry()` (`commands.go`), which only fixes the order commands appear in `cloudstic help`. That tree drives dispatch in `runCmd()`, the `COMMANDS` listing in `printUsage()`, and the shell-completion command lists — do **not** edit `main.go` or `usage.go` for a new command.
   - Help output is derived, never hand-written: root help renders global flags from the sections in `globalHelpSections()` (`usage.go`), and per-command `-h` renders a command's own flags and positionals from its declaration. A new *global* flag must be added to a help section — `TestHelpSectionsCoverEveryGlobalFlag` fails otherwise. Root help intentionally lists only global flags; per-command flags are documented by `cloudstic <command> -h`, which `TestCommandHelpShowsEveryDeclaredFlag` guarantees is complete.
   - Help text is pinned by golden files (`testdata/usage_root.golden`, `testdata/help_*.golden`). Intentional wording changes are regenerated with `go test ./cmd/cloudstic -run 'TestRootUsageGolden|TestCommandHelpGolden' -update`; review the golden diff as part of the change.
   - Declare commands as `func <name>Command() command`, not as package-level `var`s: a command referencing `runCompletion` would otherwise form an initialization cycle through the completion generators.
   - Do **not** hand-edit `completion.go` for any command's flags or subcommands. bash, zsh, and fish entries — including grouped commands' subcommand lists and each subcommand's flags — are generated from the command tree and its `flagSpec`s, covering value completers (`withCompleter`), repeatable markers (`asRepeatable`), and positional argument values. Use `withShortUsage` when the full usage text is too long for a completion menu. Any completer you name must be defined in the zsh script and mapped in `fishValueSpec` and, for a fixed value set, in `bashCompleterWords` — `TestDeclaredCompletersExistInZsh` and `TestDeclaredCompletersHaveFishMapping` fail otherwise. `TestCompletionCoversEveryCommandFlag` and `TestCompletionOffersNoUndeclaredFlag` fail if a generator drops a category or offers a flag no command accepts.
   - Use the `reorderArgs()` helper (`flags.go`) so flags may follow positional args.
   - Write output via `r.out`/`r.errOut` (not `fmt.Print`) so it is capturable, and unit-test against `stubClient`.
   - Keep result rendering in `print*`/`render*` free functions that take an `io.Writer` first — never give presentation code access to `runner`.

5. **Error Handling** — Return descriptive errors:
   - Wrap errors with context using `fmt.Errorf("context: %w", err)`.
   - Provide actionable error messages to users.
   - Distinguish between user errors and system errors.

### Documentation Formatting

Markdown files are linted with `markdownlint` (rules from `.markdownlint.json`). Follow these conventions to avoid CI failures:

- Use `-` (dash) for unordered list items, not `*`.
- Surround fenced code blocks with blank lines (even when nested inside a list item).
- Surround unordered and ordered lists with blank lines.
- Restart ordered list numbering at `1` for each new list — do not continue numbering across headings or sections.

### Testing Guidelines

Choose the smallest test style that covers the behavior:

- Use plain unit tests for argument parsing, orchestration branches, domain
  logic, and individual error cases. Inject a `stubClient` when testing command
  flow without a real repository.
- Use golden-file tests for deterministic `print*` and `render*` presentation
  output when the exact full text is the contract. Write output to a buffer and
  compare it with `assertGolden` against `cmd/cloudstic/testdata/*.golden`.
  Regenerate intentionally changed files with
  `go test ./cmd/cloudstic -run <TestName> -update`, then review the golden diff.
- Use testscript tests for whole-command behavior that crosses the process
  boundary: flag ordering, stdout/stderr separation, exit success or failure,
  and filesystem effects. Add hermetic scripts under
  `cmd/cloudstic/testdata/scripts/`; prefer local stores and sources, and do not
  require network access, credentials, or Docker.

Do not use golden files for values that are inherently unstable, and do not
replace focused unit tests with testscript when direct assertions provide a
clearer failure.

- Run `go test -v -count=1 ./...` before committing to ensure all tests pass.
- E2E tests require Docker for Testcontainers (MinIO, SFTP). They skip gracefully if Docker is unavailable.
- Use `-race` flag during development to catch race conditions.
- Hermetic tests (default) use local filesystem + containers; no cloud credentials needed.

## Naming Conventions

One set of rules for commit, PR, and issue titles (derived from repo history).

**Commits & PRs** — use a Conventional Commit prefix: `type: imperative summary`, or `type(scope): …`. Lowercase the summary after the colon; no trailing period; keep it short (~72 chars) and specific — what changed, not a file list. PRs are squash-merged, so **the PR title becomes the commit subject** — give both the same form.

- Types mirror the label/branch vocabulary: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`, `perf`, `ci`. Dependabot's `chore(deps): bump X from A to B` is machine-generated — leave it.
- An optional scope names the area: `feat(tui):`, `fix(completion):`, `test(e2e):`. Prefer a `type(scope):` scope over an ad-hoc `TUI: …` sentence prefix, which a squash reword tends to mangle.

**Issues** — no conventional-commit prefix; the `type` lives in the label. Lead with an imperative verb (`Add …`, `Convert …`, `Separate …`, `Thread …`) or an `Area:` scanning prefix (`TUI: …`). No trailing period.

**RFCs** — a substantial feature gets a design record under `rfcs/`, with one naming shape across the doc, its tracking issue, and its PRs/commits:

- **File** — `rfcs/NNNN-kebab-slug.md`, zero-padded 4-digit number (next free number), listed in `rfcs/README.md`.
- **Doc title (H1)** — `# RFC NNNN: Title Case Name` (an optional parenthetical clarifier is fine, e.g. `(YAML Presets)`).
- **Tracking issue** — `RFC NNNN: Epic / Tracking issue for <lower-case description>`, labelled `rfc` + `tracking`.
- **Proposal PR/commit** (adds or revises the RFC doc): `rfc: <summary> (RFC NNNN)` — `rfc` is a repo-local type carried by the `rfc` label.
- **Implementation PR/commit**: a standard `type:` prefix (`feat:`, `refactor:`, …) with a trailing `(RFC NNNN)` cross-reference, e.g. `feat: unified source identity (RFC 0009)`.
- Always keep the zero-padded number and the `(RFC NNNN)` reference — avoid bare inline forms like `rfc 14`, and don't drop the reference on implementation work.

**Avoid** (all seen in history — don't repeat):

- `feat:` / `fix:` prefixes in *issue* titles — the label already carries the type (e.g. #176, #187).
- An `Area:` sentence prefix on a commit/PR where a squash reword will rewrite it — use a `type(scope):` scope instead.
- Duplicate PR titles for split or re-opened work (e.g. #233 / #234) — disambiguate with the sub-scope.
- RFC references that drop the padding or the cross-ref: `feat: rfc 14 api surface` (#200) should be `feat: … (RFC 0014)`.

## Creating GitHub Issues

Create issues with `gh issue create` against `Cloudstic/cli`. Match the existing house style (see #155, #250–#253 as references).

**Body structure** — use these four Markdown sections, in order:

```markdown
## Context
Current state and why it matters. Reference concrete files (and functions) with
backtick paths, e.g. `cmd/cloudstic/cmd_backup.go`. Cross-link related issues
with `#NNN` when relevant.

## Goal
One or two sentences on the desired end state.

## Scope
- bullet list of the concrete changes to make

## Acceptance Criteria
- bullet list of verifiable outcomes
- always end with the test and lint commands that must pass, scoped to the
  touched packages, e.g.
- `go test ./cmd/cloudstic` passes
- `golangci-lint run ./cmd/cloudstic/...` passes
```

**Labels** — apply exactly one *type* label plus one or more `area/*` labels:

- Type (pick one): `bug`, `enhancement`, `refactor`, `tech debt`, `chore`, `test`, `documentation`, `rfc`, `tracking`
- Area: `area/cli`, `area/core`, `area/tui`, `area/completion`, `area/onboarding`, `area/ci`

**Titles** — see Naming Conventions above. In short: no conventional-commit prefix (the label carries the type), lead with an imperative verb or an `Area:` scanning prefix, no trailing period.

Do not invent new labels — reuse what `gh label list` returns.

## Creating Pull Requests

Open PRs with `gh pr create` against `Cloudstic/cli`. Match the existing house style (see #214, #228, #236, #237 as references).

**Branch names** — `<type>/<kebab-slug>`, where the type mirrors the label vocabulary: `feat/`, `refactor/`, `test/`, `chore/`, `fix/`, `docs/` (e.g. `feat/tui-profile-history`, `refactor/tui-profile-modal-state`). Dependabot branches (`dependabot/...`) are machine-generated — leave them alone.

**Titles** — see Naming Conventions above. Use a Conventional Commit prefix (`type: …` or `type(scope): …`) matching the branch type; because PRs squash-merge, the title becomes the commit subject.

**Body structure** — two Markdown sections:

```markdown
## Summary
- bullet list of the concrete changes, imperative voice

<link line: `Closes #NNN`, `Part of #NNN`, or `Fixes #NNN`>

## Verification
- <exact test command run, scoped to the touched packages>
- <exact lint command run, scoped to the touched packages>
```

- Keep the `Summary` bullets high-signal — what changed and why, not a file-by-file diff.
- The link line ties the PR to its issue; omit it only for standalone work.
- Under `Verification`, paste the **exact commands you ran**, scoped to the packages you touched, using the repo's cache-env prefixes so they reproduce in CI:

```bash
env GOCACHE=/tmp/cloudstic-gocache go test -count=1 ./cmd/cloudstic ./internal/tui
env GOCACHE=/tmp/cloudstic-gocache GOLANGCI_LINT_CACHE=/tmp/cloudstic-golangci-lint golangci-lint run ./cmd/cloudstic ./internal/tui
```

Apply the same *type* + `area/*` labels described above; the type usually matches the branch prefix.
