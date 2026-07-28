# RFC 0022: Public Go API Boundaries

- **Status:** Implemented (stages 1–3); stage 4 outstanding
- **Date:** 2026-07-28
- **Affects:** `client.go`, `pkg/source`, `pkg/store`, `internal/storelayer`, `docs/`

## Abstract

This RFC makes it possible to implement a custom `Source` or `ObjectStore`
from a separate Go module, and to name and construct every `Client` result
type, without introducing a breaking API or a `/v2` module.

Two problems block this today:

1. `pkg/source.Source` and several `Client` result types (`FindResult`,
   `FileMatch`, `LsSnapshotResult`, `DiffResult`, ...) reference
   `internal/core` types directly in their exported signatures.
   `internal/core` cannot be imported from outside
   `github.com/cloudstic/cli`, so a consumer cannot write
   `func (s *MySource) Walk(ctx, func(core.FileMeta) error) error` and cannot
   name the type of `FileMatch.Source` or `LsSnapshotResult.RefToMeta`.
2. `pkg/source` and `pkg/store` each bundle their contract (the interface) with
   every built-in implementation in one package. Importing either package to
   get the four-method `Source` interface or the `ObjectStore` interface pulls
   in unrelated transitive dependencies: `pkg/source` drags in the Google API
   client, gRPC, protobuf, and OAuth2 stacks (100+ packages); `pkg/store` drags
   in the full AWS SDK. Both regardless of which, if any, of those providers
   the consumer actually wants.

Both are fixable inside the current `v1` module: (1) by extending the
type-alias re-export pattern `client.go` already uses (`RepoConfig =
core.RepoConfig`, `SecretRefError = secretref.Error`, `ErrRepoLocked =
engine.ErrRepoLocked`) to cover every internal type that appears in a public
signature, and (2) by splitting each of `pkg/source` and `pkg/store` into a
small contract package plus one subpackage per built-in implementation. Neither
change breaks an existing caller or the repository format.

## Context

`docs/source-guide.md` currently tells third parties that a custom source must
be contributed inside this repository under `pkg/source/`, because
`pkg/source.Source`'s methods are typed with `core.FileMeta` and
`core.SourceInfo`, and `internal/` packages cannot be imported by an external
module — the build fails with `use of internal package ... not allowed`. That
guidance is correct today but is not the outcome anyone wants: it forces every
custom source to live in-tree and go through this repo's own release cycle.

A grep-and-verify pass across the packages named in the original request found
the leak is narrower than "internal vs. `pkg/` doesn't work" and broader than
just `pkg/source`:

- `pkg/store.ObjectStore` is already 100% externally implementable — every
  method uses only `string`/`[]byte`/`int64`/`bool`/`context.Context`. No
  change needed to the interface itself.
- `pkg/crypto` and `pkg/keychain` have zero references to `internal/core` or
  `internal/engine` anywhere. Already fully self-contained and importable.
- `pkg/source.Source`/`IncrementalSource` (`pkg/source/interface.go:19-24`)
  reference exactly two `internal/core` types in their method signatures:
  `core.FileMeta` and `core.SourceInfo`.
- Beyond `pkg/source`, several `Client` result types aliased from
  `internal/engine` embed `internal/core` types that are not yet aliased at
  the root: `FileMatch.Source` is `*core.SourceInfo`, `FileMatch.Type` and
  `FileVersion.Type` are `core.FileType`, `LsSnapshotResult.Snapshot` is
  `core.Snapshot`, `LsSnapshotResult.RefToMeta` is `map[string]core.FileMeta`.
  A consumer holding a `client.FindResult` today cannot name the type of
  `FileMatch.Source` without importing `internal/core`.
- Separately, `go list -deps ./pkg/source/...` and `go list -deps
  ./pkg/store/...` confirm the dependency-bundling problem: both pull in every
  built-in provider's SDK regardless of which interface the caller actually
  wants.

This repo already has a working, non-breaking precedent for problem (1): the
recent `RepoConfig`, `SecretRefError`, and `ErrRepoLocked` exports from
`client.go` are exactly this pattern, applied to a handful of types.

## Goals

- A source implemented in a separate Go module can implement
  `pkg/source.Source` / `IncrementalSource` without importing anything under
  `internal/`.
- A store implemented in a separate Go module can implement
  `pkg/store.ObjectStore` without importing anything under `internal/`
  (already true — keep it true as the package is reorganized).
- Every exported field and return type reachable from a `Client` method is
  nameable and constructible from outside the module.
- Importing the `Source`/`ObjectStore` contract does not pull in a specific
  provider's SDK.
- No existing caller (`cmd/cloudstic`, `internal/tui`, `internal/app`, or an
  external consumer of today's `Client` API) breaks.
- No repository format change; `core.RepoFormatVersion` is untouched.

## Non-goals

- Redesigning the `Source`/`FileMeta` field shapes (e.g. renaming `FileMeta`
  to `Entry`, `Mtime int64` to `ModifiedAt time.Time`, `GetFileStream` to
  `Open`). That is a legitimate but separate API-ergonomics proposal; nothing
  here requires it, and bundling it in would force every built-in source to be
  rewritten as a prerequisite for the actual goal.
- A `/v2` Go module. The package reorganization in §3–§4 and the decorator
  hiding in §4a *are* breaking for anyone importing `pkg/source` or
  `pkg/store`'s concrete types directly, and against published `v1.17.0` tags
  that is formally a SemVer-major change. We accept it inside `v1` on the
  grounds that `pkg/source` was never externally implementable (that is the
  bug this RFC fixes, so no external implementation can exist), and
  `pkg/store`'s concrete types are documented nowhere as a stable import path.
  Stage 1 is unaffected either way — it is purely additive and already shipped.
- Changing which types `internal/engine` or `internal/hamt` use internally.
  `internal/core` remains the single source of truth for the domain model;
  only the subset already reachable from a public signature gets a root alias.
- Moving `pkg/crypto` or `pkg/keychain` — they have no leak to fix.

## Proposal

### 1. Complete the alias sweep in `client.go`

Add aliases for every `internal/core` type currently reachable, unaliased,
from an exported `Client` signature:

```go
type FileMeta = core.FileMeta
type SourceInfo = core.SourceInfo
type FileType = core.FileType
type Snapshot = core.Snapshot
```

(`FileType`'s constants `core.FileTypeFile`/`core.FileTypeFolder` get root
aliases too, following the existing `WithVerbose = engine.WithVerbose` var
pattern.) This is purely additive: no existing exported name changes shape,
only new names are added.

### 2. API-boundary regression test

Add a test, in the style of `TestGlobalFlagsHasNoConstructionMethods` and
`TestSecretEnvValuesNeverAppearInHelp`, that walks the exported API of the
root package (and of `pkg/source`, `pkg/store` after the split below) via
`go/types` or `golang.org/x/tools/go/packages` and fails if any exported
function, method, or struct field's type resolves to a package path
containing `/internal/`. This is what keeps the alias sweep complete as the
codebase evolves, rather than relying on manual review to catch the next leak.

### 3. Split `pkg/source` into contract + provider packages

```
pkg/source/                  # Source, IncrementalSource, FileChange, SourceSize,
                             # ExcludeMatcher — stdlib only, no provider deps
pkg/source/local/            # local filesystem
pkg/source/gdrive/           # Google Drive (google.golang.org/api, oauth2, gRPC)
pkg/source/onedrive/         # OneDrive
pkg/source/sftp/             # SFTP
internal/sourceoauth/        # OAuth helpers shared by gdrive + onedrive
```

`pkg/source/interface.go` keeps its current exported names (`Source`,
`IncrementalSource`, `FileChange`, `ChangeType`, `SourceSize`) unchanged — only
their package's contents shrink. `ExcludeMatcher`/`ParseExcludeFile` stay in
`pkg/source`: they import stdlib only, so they add no weight, and
`cmd/cloudstic/cmd_backup.go` already consumes them.

The OAuth helpers (`exchangeWithLocalServer`, `persistentTokenSource`, the
`defaultGoogleClientID`/`defaultOneDriveClientID` ldflags variables) are
unexported and shared by exactly two providers, so they move to
`internal/sourceoauth`.

**Provider symbols are renamed to drop the now-redundant prefix.** The
`GDrive`/`OneDrive`/`Local`/`SFTPSource` prefixes exist only because every
provider shares one namespace today; once split they stutter
(`gdrive.NewGDriveSource`). The new names are `gdrive.New`, `gdrive.Option`,
`gdrive.WithResolver`, `local.New`, `sftp.New`, and so on. Renaming during the
move rather than after is deliberate: the compiler flags every missed call
site, so there is no silent-failure mode, and deferring it would churn all
~90 call sites a second time.

> **Release hazard.** `.goreleaser.yml` injects OAuth client IDs with
> `-X github.com/cloudstic/cli/pkg/source.defaultGoogleClientID=…`. The Go
> linker **silently ignores `-X` for a symbol that does not exist**, so moving
> those variables without updating `.goreleaser.yml` in the same change ships
> release binaries with empty client IDs — cloud auth fails at runtime while
> every build and test stays green. The `-X` paths must move to
> `internal/sourceoauth` together with the variables. (The doc comment in
> `oauth_defaults.go` already names the wrong package, `pkg/store`; fix it.)

### 4. Split `pkg/store` the same way

```
pkg/store/                   # ObjectStore + capability interfaces
                             # (RangeGetter, ConcurrencyHinter, Unwrapper),
                             # QuotaStore, DebugStore
pkg/store/local/
pkg/store/s3/                 # aws-sdk-go-v2
pkg/store/b2/
pkg/store/sftp/
pkg/store/internal/keyprefix/ # key-prefix helper shared by s3 + b2
pkg/store/storetest/          # shared test doubles (MemStore, FaultStore,
                              # AssertRangeGetterConformance)
internal/storelayer/          # Compressed, Encrypted, Metered, Pack, KeyCache
```

There is no `pkg/store/hybrid`: `HybridStore` was removed from the code some
time ago (commit "remove hybrid store") but was still described in `AGENTS.md`,
which this change corrects.

`cmd/cloudstic/storebuild.go` updates its imports; `RangeGetter` conformance
(`assertRangeGetterConformance`) and the packfile tests move with their
respective backend packages.

### 4a. The decorator chain becomes internal

`CompressedStore`, `EncryptedStore`, `MeteredStore`, `PackStore`, and
`KeyCacheStore` move to `internal/storelayer`. They are repository-format
machinery, not composable building blocks, and exporting them buys nothing
while costing real safety:

- **The chain is assembled only inside the module today.** `client.go` builds
  Pack → Metered → Encrypted → Compressed; `internal/engine` adds KeyCache and
  Metered. `cmd/cloudstic` constructs only *backends* plus `DebugStore`.
- **Their composition order is a silent security invariant.** `PackStore` sits
  *below* `EncryptedStore`, so its catalog and footers never pass through
  encryption — which is exactly why it needs a separately derived HKDF key
  (`crypto.HKDFInfoPackIndexV1`, see `client.go` and `docs/encryption.md`). An
  external caller writing `store.NewPackStore(inner)` without
  `WithPackIndexKey` gets a repository whose pack index is **plaintext**,
  exposing metadata object keys, with no error and no warning. Exporting the
  decorators is what makes that footgun reachable.
- **Nobody needs them to write their own wrapper.** Implementing a quota,
  rate-limit, or metrics decorator requires the `ObjectStore` *interface*, not
  ours. The injection point already exists:
  `NewClient(ctx, base store.ObjectStore, opts...)` takes a backend — or a
  caller's wrapper around one — and layers the repository machinery on top,
  which is precisely how `cmd/cloudstic` injects `DebugStore`.

`QuotaStore` and `DebugStore` **stay public**. Unlike the chain they carry no
ordering invariant, are safe in any position, and serve as the worked examples
of wrapping `ObjectStore`. (`QuotaStore` is currently exported but unused
anywhere in the tree; it should be documented rather than dropped.)

Folding `DebugStore` into the client behind a `WithDebug` option was
considered and rejected. The `*ui.SafeLogWriter` it produces is consumed by the
*reporter*, which is passed *into* `NewClient` — so a client-created writer is
circular — and `init`/`key` operate on the raw store without ever building a
Client, so they would lose `-debug` entirely.

### 5. Documentation

Replace `docs/source-guide.md`'s "contribute under `pkg/source/`" guidance
with a complete external-module example: a minimal `go.mod`, an
implementation of `Source` using only `github.com/cloudstic/cli` (root) and
`github.com/cloudstic/cli/pkg/source` imports, and instructions for wiring it
into a caller via the `Client` API. Update `docs/storage-model.md` similarly
for `ObjectStore`.

## Compatibility

- **Repository format:** untouched. This is a Go package/API reorganization,
  not a change to what is written to a store. `core.RepoFormatVersion` is not
  raised.
- **Existing internal callers:** `cmd/cloudstic`, `internal/tui`,
  `internal/app` update their import paths for the moved provider packages;
  no behavior change.
- **Existing external consumers of `Client`:** every existing exported name on
  `Client` keeps its current meaning; the alias sweep only adds new names.
  Code compiled against today's `client.go` continues to compile unmodified.
- **External importers of `pkg/source`'s or `pkg/store`'s concrete types**
  (e.g. `store.S3Store`, `source.NewGDriveSource`, `store.EncryptedStore`):
  this is the one real source of breakage, and is accepted without a major
  version — see Non-goals. Nothing in `docs/` documents those concrete types
  as a stable import path; `pkg/source`'s provider types could never be
  wired into an external caller anyway, since `Source` itself could not be
  implemented outside the module; and the decorators were never safe to
  assemble by hand (§4a).
- **Release tooling:** `.goreleaser.yml`'s `-X` paths move with the OAuth
  variables in the same change. See the hazard note in §3 — a stale `-X` path
  fails silently.

## Testing strategy

- Compile-and-run an external module fixture (own `go.mod`, `replace`
  directive to this checkout) that implements `Source`, `IncrementalSource`,
  and `ObjectStore` importing only `github.com/cloudstic/cli` and
  `github.com/cloudstic/cli/pkg/source` / `pkg/store` — assert it builds
  without any `internal/` import and without pulling in AWS/Google SDKs
  (`go list -deps` on the fixture stays free of both).
- Run a full backup/restore cycle through the fixture's custom source and a
  local-store-backed `Client`, reusing existing hermetic e2e patterns.
- The API-boundary test (Proposal §2) runs in the standard unit test suite and
  fails CI if a future change reintroduces an internal-type leak.
- `go build ./...` and the existing hermetic e2e suite after the `pkg/source`
  / `pkg/store` split, to confirm no internal caller was missed.

## Rollout plan

1. Alias sweep (§1) + API-boundary regression test (§2). Small, additive,
   immediately unblocks naming every `Client` result type.
2. Split `pkg/source` into contract + provider subpackages (§3), update
   `cmd/cloudstic` wiring, shell-completion generators, and tests.
3. Split `pkg/store` the same way (§4), update `storebuild.go` and tests.
4. Documentation (§5) and the external-module fixture (Testing strategy).

Steps 1–2 alone already make external custom sources possible; 3 is
independently valuable for the same reason applied to stores, and can ship on
its own schedule.

## Open questions

1. Should the external-module fixture live permanently in-tree (e.g.
   `internal/e2e/fixtures/customsource/`, itself *not* importing `internal/`
   despite its own path) so CI enforces the contract on every change, or be a
   one-time manual verification?
2. Does the API-boundary test need an allowlist mechanism for cases where an
   internal type is deliberately, temporarily exposed during a staged rollout,
   or should any leak simply fail the test until aliased?
3. `withDebugStore` (`cmd/cloudstic/storebuild.go`) mutates the global
   `logger.Writer` as a side effect of a function that reads as pure. Worth
   separating, but it is a standalone cleanup rather than part of this RFC —
   track it separately.
4. **Root-package congestion.** `client.go` is ~1100 lines, but only 20 of its
   declarations are `Client` methods and 8 are locally defined types; the other
   ~118 are pass-through re-exports (60 type aliases + 58 option vars). Most of
   that is the intended cost of the facade — it is what lets `internal/engine`
   change freely — but two things are worth revisiting separately:
   - The **profiles/workstation cluster** (`ProfilesConfig`, `BackupProfile`,
     `ProfileStore`, `ProfileAuth`, `DiscoveredSource`, the six `Workstation*`
     types, `LoadProfilesFile`, `SaveProfilesFile`, `PlanWorkstationSetup`,
     `ApplyWorkstationSetupPlan`, …) contains **no `*Client` methods at all** —
     they are free functions over a `*ProfilesConfig` and a path. This is CLI
     configuration, not repository operations, and belongs in a `pkg/profile`
     package rather than the repository-client facade.
   - The re-export wall is **hand-maintained**: every new engine option needs a
     mirrored line. Inverting the dependency — defining the option types in a
     public package that `internal/engine` imports — would remove the mirroring
     entirely. That is a larger change than this RFC and deserves its own.

## Implementation notes

Details settled while implementing, that the plan above did not anticipate:

- **`KeySlotPrefix` moved to `pkg/store`.** It lived beside the encryption
  layer, but `pkg/keychain` — a public package — needs it, and it is a
  repository key-namespace fact rather than decorator machinery.
- **`httpRangeHeader` became `store.HTTPRangeHeader`.** Backends in their own
  packages still need it to implement `RangeGetter`.
- **`ErrPlaintextObject` is re-exported from `client.go`.** Moving
  `EncryptedStore` internal would otherwise have made a user-facing sentinel
  unreachable; callers need it to tell "never encrypted" from "wrong key".
- **`pkg/store/storetest` grew `MemStore` and
  `AssertRangeGetterConformance`.** The per-backend conformance suite could no
  longer live in `pkg/store` once the backends moved out. `storetest`
  redeclares the interfaces it needs instead of importing `pkg/store`, because
  `pkg/store`'s own internal tests import `storetest` — importing back would
  make the test binary import itself.
- **Some `pkg/store` tests became `package store_test`.** Tests needing a real
  backend cannot be internal to `pkg/store`, since `pkg/store/local` imports
  `pkg/store`. The one test of an unexported helper (`fmtBytes`) stays internal
  in its own file.
- **Decorator type names were kept** (`CompressedStore`, not `Compressed`).
  Unlike the provider renames in §3, these are internal, so there is no public
  commitment to get right and renaming would be churn on an already-large
  change.

## Resolved decisions

Recorded here so the rationale is not re-litigated:

- **Stay on `v1`.** No `/v2` module path; the reorganization's breakage is
  accepted per Compatibility above.
- **Provider symbols are renamed on move** (`gdrive.New`, not
  `gdrive.NewGDriveSource`) — the compiler enforces completeness, and
  deferring would churn every call site twice.
- **OAuth helpers → `internal/sourceoauth`; `ExcludeMatcher` stays in
  `pkg/source`** (stdlib-only, already consumed by `cmd_backup.go`).
- **Decorators → `internal/storelayer`; `QuotaStore` and `DebugStore` stay
  public** (§4a).
