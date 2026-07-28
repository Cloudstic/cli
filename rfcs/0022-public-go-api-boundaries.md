# RFC 0022: Public Go API Boundaries

- **Status:** Partially implemented — §1–§7 landed; §8 outstanding
- **Date:** 2026-07-28
- **Affects:** `client.go`, `pkg/source`, `pkg/store`, `pkg/crypto`, `pkg/config`,
  `pkg/open`, `internal/logger`, `internal/storelayer`, `cmd/cloudstic`, `docs/`

## Abstract

This RFC makes it possible to *implement* a custom `Source` or `ObjectStore`
from a separate Go module, to *name and construct* every `Client` result type,
and to *use* the library the way `cmd/cloudstic` uses it — all without
introducing a breaking API or a `/v2` module.

Five problems block this today. The first two are about *implementing* the
contracts and were addressed by §1–§4a. The last three were found while
reviewing that work: one is the same dependency-bundling problem in a package
the original survey cleared on the wrong criterion, and two are about
*consuming* the library rather than extending it.

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
3. `pkg/crypto` has the same bundling problem, and it was missed because the
   original survey asked the wrong question of it (see Context). One file,
   `pkg/crypto/kms.go`, holds both the `KMSClient` *interface* and the AWS SDK
   client that implements it, so `pkg/crypto`, `pkg/keychain`,
   `pkg/secretref/backends`, and the root package all carry the AWS SDK
   whether or not the consumer uses KMS.
4. Turning user-facing configuration into live objects — a `scheme:` URI into
   a store, a profile entry into credentials, a credential set into a
   keychain, all of it into a `Client` — lives entirely in `package main`.
   `pkg/profile` parses and validates the YAML but stops there. A consumer can
   read a profiles file and do nothing with it; to act on one they must
   re-derive the URI grammar, the secret-reference precedence, the auth-ref
   provider rules, and the keychain ordering, each an opportunity to disagree
   silently with the CLI.
5. Debug logging is a mutable package-level `io.Writer` in `internal/logger`,
   set as a side effect of `cmd/cloudstic`'s `withDebugStore`. Twelve non-test
   files log through it, including the public `pkg/store` and
   `pkg/secretref/backends`. No expression available to an external consumer
   turns any of it on.

All five are fixable inside the current `v1` module: (1) by extending the
type-alias re-export pattern `client.go` already uses (`RepoConfig =
core.RepoConfig`, `SecretRefError = secretref.Error`, `ErrRepoLocked =
engine.ErrRepoLocked`) to cover every internal type that appears in a public
signature, (2) and (3) by splitting each of `pkg/source`, `pkg/store`, and
`pkg/crypto` into a small contract package plus one subpackage per built-in
implementation, (4) by moving configuration resolution and construction out of
`package main` into `pkg/config` and `pkg/open`, and (5) by making the debug
sink an injected value rather than a global. None of these changes the
repository format.

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
  **This is true but was the wrong question to stop at.** It tested for
  internal-type leaks — problem (1) — and the bundling test of problem (2) was
  only ever run against `pkg/source` and `pkg/store`. Running it against every
  public package (`go list -deps`, counting `aws-sdk-go`,
  `google.golang.org/api`, and `golang.org/x/oauth2` packages) shows
  `pkg/crypto` has exactly the bundling problem the RFC was written to fix:

  | package | total deps | vendor SDK deps |
  | --- | ---: | ---: |
  | `.` (root) | 322 | 57 |
  | `pkg/crypto` | 285 | 57 |
  | `pkg/keychain` | 288 | 57 |
  | `pkg/secretref/backends` | 293 | 57 |
  | `pkg/store` | 62 | 0 |
  | `pkg/source` | 60 | 0 |
  | `pkg/profile` | 73 | 0 |
  | `pkg/secretref` | 68 | 0 |

  One non-test file is responsible: `pkg/crypto/kms.go`. Everything else in
  `pkg/crypto` is AES-GCM, HKDF, and BIP39, which need no SDK. `pkg/keychain`
  inherits it by importing `pkg/crypto`, `pkg/secretref/backends` by importing
  `pkg/keychain`, and the root package by importing all three. See §6.
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
- Importing the repository client does not pull in a cloud SDK the consumer
  does not use.
- A consumer can go from a profiles file to a completed backup using only
  public packages, with the same URI grammar, secret-reference precedence, and
  keychain ordering the CLI applies — without reimplementing any of it.
- Debug output from the client, engine, and store layers is reachable from
  outside the module, per-client rather than per-process.
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
- Adopting `log/slog`. §8 makes the debug sink injectable while preserving its
  current `[component] message` format exactly — a purely structural change.
  Replacing that format with structured logging is a behavioral change with
  its own migration and its own golden-file churn; it deserves a separate RFC,
  and §8 is a precondition for it rather than a substitute.
- Moving flag parsing, help generation, or command dispatch out of
  `cmd/cloudstic`. §7 moves *resolution and construction*; deciding what the
  user typed stays a CLI concern (see §7's precedence note).
- A `pkg/` home for the TUI (`internal/tui`, `internal/app`) or the onboarding
  wizard (`internal/workstation`). They are CLI surfaces, correctly internal.

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

### 6. Split the AWS KMS client out of `pkg/crypto`

Apply §3–§4's contract/implementation split to `pkg/crypto`, the package the
original survey cleared on the wrong criterion:

```text
pkg/crypto/                  # AES-256-GCM, HKDF, Argon2, BIP39,
                             # KMSClient interface (kmsclient.go)
pkg/crypto/kms/              # Client, New, Option,
                             # WithRegion/WithEndpoint/WithConfig
pkg/keychain/kms/            # WithARN
```

The split is unusually clean because the seam already exists in the file.
`crypto.KMSClient` (`pkg/crypto/kms.go:13`) is a pure interface over
`Encrypt`/`Decrypt`; `AWSKMSClient` is the concrete type, and
`WithKMSConfig(aws.Config)` is the only exported symbol whose *signature*
names an SDK type. Interfaces stay, implementation moves. Symbols are renamed
to drop the now-redundant prefix on the way out, per §3's resolved decision:
`kms.New`, `kms.Client`, `kms.Option`, `kms.WithRegion`.

`pkg/keychain` needs a third package to reach the target. `WithKMSClient` takes
an already-built `crypto.KMSClient` and is SDK-free, but `WithKMSARN`
constructs one on demand and is the sole reason `pkg/keychain` links the AWS
SDK. It moves to `pkg/keychain/kms.WithARN`. It has no callers anywhere in the
tree — deleting it was considered and rejected, since moving preserves the
capability at the same cost.

Measured after the change (`go list -deps`, same method as the Context table):

| package | before | after | SDK before | SDK after |
| --- | ---: | ---: | ---: | ---: |
| `.` (root) | 322 | 168 | 57 | 0 |
| `pkg/crypto` | 285 | 115 | 57 | 0 |
| `pkg/keychain` | 288 | 122 | 57 | 0 |
| `pkg/secretref/backends` | 293 | 129 | 57 | 0 |

The AWS SDK is now reachable only through `pkg/crypto/kms` and
`pkg/keychain/kms`. The root package's `type KMSClient = crypto.KMSClient`
alias (`aliases.go:50`) is unaffected — it aliases the interface, so root keeps
naming KMS without importing the SDK.

This stage is fully independent of §7 and §8 and can land first.

### 7. The configuration-resolution boundary

Everything between "what the user configured" and "a live object" is currently
in `package main`: `applyProfileStore` and `resolveProfileStoreValue`
(`config.go`, `profile_secret.go`), `parseStoreURI`/`parseSourceURI`
(`storeuri.go`), `newObjectStore`/`openStore` (`storebuild.go`),
`buildKeychain`/`buildKMSClient` (`keychain.go`), `openClient`
(`clientbuild.go`), and `initSource` (`cmd_backup.go`). `cmd/cloudstic`'s own
`config.go` header already states the design — resolution is explicit and
non-mutating, and construction consumes resolved values and never sees
`globalFlags`. The remaining problem is only that both halves live in `main`.

Move them into two packages, split on dependency weight:

```text
pkg/config/                  # Store, Source, Unlock, Client value types;
                             # URI parsing; profile -> config resolution.
                             # Imports pkg/profile + pkg/secretref only.
pkg/open/                    # Store(), Source(), Keychain(), Client().
                             # Imports the backends, providers, and KMS.
```

Four decisions this encodes, and why:

- **Two packages, not one.** `pkg/config` stays at roughly `pkg/profile`'s
  weight (~73 deps) because it resolves configuration without connecting
  anything; `pkg/open` carries the S3, Google, and KMS SDKs because
  constructing a backend requires them. Collapsing them puts every SDK behind
  "I want to read a profile and see which store it names" — the exact cost §3
  and §4 were written to remove. This is a judgment call about a consumer that
  resolves without connecting (a control plane, a validator, `store verify`);
  the split is cheap enough to make now and expensive to retrofit.
- **`pkg/config` imports `pkg/profile`, never the reverse.** Config is the
  general concept and a profile is one source of it, so the conversion is
  `config.FromProfileStore(profile.Store, *secretref.Resolver)`. There is no
  cycle in either direction, but only one direction keeps `pkg/profile` the
  pure YAML data package it is today.
- **Value structs, not functional options.** §3–§4's constructors take
  functional options and should keep them. Configuration is different: it
  arrives from YAML and flags, and has to be inspectable, comparable, and
  round-trippable. Options cannot be unmarshalled or diffed. Structs for the
  data, options for the behavior knobs on `NewClient`.
- **The secret resolver is a parameter, not a default.**
  `cmd/cloudstic`'s `profileSecretResolver` is a package-level var bound to
  `backends.NewDefaultResolver()`. Passing a `*secretref.Resolver` in is what
  keeps `pkg/config` off the 293-dep `backends` package, makes resolution
  testable without a real keychain, and lets a consumer register Vault —
  which `pkg/secretref/backends.Default()` was already designed to allow.

**Precedence stays in the CLI — but only the part that is actually
CLI-specific.** `applyProfileStore` takes a `provided func(string) bool` so an
explicit flag beats a profile value, and that callback is meaningless outside a
flag parser. What it turned out to be worth separating is narrower than "the
precedence": deciding *whether the user typed a flag* is a flag-parser concept
and stays in `cmd/cloudstic`, while the fold that decision drives is shared, so
an external caller applies the same rules rather than a second reading of them.
`pkg/config` exposes both — `FromProfileStore` for a caller with no overrides,
and `ApplyProfileStore(…, overridden func(flag string) bool)` for one that has
some — and `cmd/cloudstic`'s `applyProfileStore` becomes four lines over the
latter.

The two groups of fields behave differently and both behaviours are
load-bearing, so the move preserves them exactly rather than regularizing
them. Location and KMS settings are taken only when the profile names one;
credentials are taken even when the profile is silent, *clearing* what the
caller had. The second is what makes selecting a profile override an ambient
credential from the environment — a profile is an explicit choice of which
store to talk to, so reaching it with an identity inherited from the
environment would be worse than failing to reach it. Paired tests now fail if
the two rules are ever collapsed into one.

**Two zero-value defects must be fixed before these structs are published, not
after.** `clientConfig.packfile` is `true`-by-default in meaning but `false` in
its zero value, and `clientbuild.go:36` passes it to `WithPackfile`
unconditionally — overriding `NewClient`'s own `enablePackfile: true` default.
The codebase compensates twice, in opposite directions (`config.go:121`
`!g.disablePackfile`, `config.go:207` `clientConfig{packfile: true}`).
`s3.region` has the same shape, compensated once at `config.go:208`. Exported
as-is, a consumer writing `config.Client{Store: …}` silently gets packfile
disabled and no region — a repository written with a different physical layout,
with no error at any layer. The field becomes `DisablePackfile bool` so the zero
value is the safe default (matching the `-disable-packfile` flag it comes from),
and the region default resolves inside `open.Store` rather than at one of two
call sites.

Both landed ahead of the move, as `disablePackfile` and `s3Region`
(`storebuild.go`) while the types were still unexported — so the behavioral
change was reviewable on its own, before any code changed packages. Moving the
region default was caught by an existing test asserting the pre-filled value,
which is the correct outcome: the config now carries `""` and the guarantee is
asserted where the backend receives it.

**`open.Client` is a convenience over public parts, not a funnel.** A composition
root in a library is a fair thing to object to. The answer is that
`open.Store`, `open.Source`, and `open.Keychain` are independently useful and
independently exported, and `NewClient(ctx, base, opts...)` keeps taking a raw
`store.ObjectStore` exactly as it does today. `open.Client` is the shortcut
`cmd/cloudstic` happens to want; a consumer who wants to wire it differently
still can.

### 8. Debug logging becomes injectable

`internal/logger.Writer` is a mutable package-level `io.Writer`, set by
`cmd/cloudstic`'s `withDebugStore` as a side effect (the concern already
recorded as open question 3). It is read from goroutines throughout a
concurrent backup, and written once at startup — benign in the CLI, because
nothing sets it after work begins.

Exporting a setter for it would not be. It would make the global writable at
any time from any goroutine, turning a benign startup-ordering property into a
racy public API, and it would still leave two `Client`s in one process unable
to have different debug settings — which is the first thing a library consumer
hits. So the global goes away rather than being published:

- `logger.New(component, color)` already returns a `*Logger`. Give it a writer
  field instead of reading the package var, and have each construction site
  take its writer from its owner.
- `NewClient` gains `WithLogger(io.Writer)`, threading through
  `internal/engine`, `internal/hamt`, and `internal/storelayer`.
- Source constructors gain `WithLogger` in their existing option sets, which
  also covers `internal/sourceoauth`.
- `store.NewDebugStore(inner, w)` already takes a writer and needs nothing.
- `pkg/secretref` resolvers gain a logger option.
- `logger.Writer` stays as a fallback while call sites migrate, then is deleted.

The migration is per-component rather than all at once, which the fallback is
what makes possible. Converted so far: the client itself, `internal/storelayer`
(via `WithPackLogger`), `internal/hamt` (via a variadic `TreeOption`, so the
six trees that never log keep their call sites), and `internal/engine`'s
backup and init managers. Still on the fallback, and the reason the global
cannot be deleted yet:

- `internal/engine/snapshots.go` logs from *free functions*
  (`LoadSnapshotCatalog`, `AppendSnapshotCatalog`,
  `RemoveFromSnapshotCatalog`) rather than from a manager, so there is no
  receiver to carry a sink. Giving them one means changing three exported
  signatures and their callers — a different shape of change from the rest,
  and worth its own commit.
- `internal/sourceoauth` and `pkg/secretref/backends` are constructed
  independently of a client, so a client-supplied sink cannot reach them. They
  need an option on the source constructors and the resolver respectively.

`InitRepo` is a package-level function with no client, so its single debug
line also stays on the fallback.

**The injected value is an `io.Writer`, not a `*slog.Logger`.** The current
output is colored `[component] message` human debug text, and preserving it
byte-for-byte makes this stage purely structural — no golden-file churn, no
behavioral review. Structured logging is the separate proposal named in
Non-goals.

This supersedes §4a's rejection of a `WithDebug` client option, which does not
apply here: that rejection was about *`DebugStore` wrapping* — the
`*ui.SafeLogWriter` it produces feeds the reporter, which is passed *into*
`NewClient`, and `init`/`key` operate on a raw store with no Client at all. A
logger sink has neither property. `DebugStore` construction stays exactly where
§4a put it.

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
- **External importers of `pkg/crypto`'s KMS implementation**
  (`crypto.AWSKMSClient`, `crypto.NewAWSKMSClient`, `crypto.KMSClientOption`,
  `crypto.WithKMSRegion`/`WithKMSEndpoint`/`WithKMSConfig`): these move to
  `pkg/crypto/kms` under §6. Accepted on the same grounds as §3–§4, and with
  the same mitigation — the interfaces every consumer actually names
  (`KMSClient`, `KMSEncrypter`, `KMSDecrypter`) do not move, and the root
  package's `KMSClient` alias is unchanged.
- **Nothing in this RFC has shipped.** Its implementation commits post-date
  `v1.17.0`, so §6–§8 revise work no release has exposed. The §3–§4 breakage
  assessment stands as written; §6–§8 add no breakage beyond the
  `pkg/crypto/kms` move, since §7's packages are new and §8's global is
  internal.
- **`internal/logger.Writer`** is internal, so its removal in §8 is invisible
  outside the module. Debug *output* is unchanged byte-for-byte; only the way
  the sink is supplied changes.
- **`cmd/cloudstic` behavior** is unchanged throughout §6–§8. `storebuild.go`,
  `clientbuild.go`, `keychain.go`, `storeuri.go`, and `config.go` become thin
  adapters over `pkg/config` and `pkg/open`; the golden help files, testscript
  suites, and e2e tests must pass untouched, which is the primary evidence
  that §7 preserved behavior.

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

Stages 5–8 add three requirements:

- **Characterization tests before the §7 move, not after.** Grepping the blast
  radius against `cmd/cloudstic/*_test.go` shows the unlock path has no direct
  unit tests at all: `buildKeychain`, `buildKMSClient`, `parsePlatformKey`,
  `openClient`, `openStore`, `newReporter`, and `resolveProfileStoreValue` are
  referenced by zero test files. `buildKeychain` is the one that matters most —
  it fixes credential precedence (KMS, then platform key, then password, then
  recovery key, then interactive prompt), an ordering that exists today only as
  code and becomes a public promise the moment it moves. Pin it with tests in
  `package main` first, then move the tested code.
- **A dependency-weight sweep, not a hand-listed few.** The original
  `TestExternalContractsPullNoVendorSDK` asserted the property of exactly two
  packages, `pkg/source` and `pkg/store` — so it held everywhere anyone had
  thought to look, and `pkg/crypto` bundled the AWS SDK through three stages of
  the RFC written to prevent it. It is replaced by
  `TestPublicPackagesPullNoVendorSDK`, which enumerates every public package
  from `go list . ./pkg/...` and fails on any SDK dependency, with the
  SDK-bearing set (`pkg/crypto/kms`, `pkg/keychain/kms`, `pkg/source/gdrive`,
  `pkg/source/onedrive`, `pkg/store/s3`, and later `pkg/open`) named in an
  explicit allowlist so joining it is a reviewed decision. A companion test
  fails on a stale allowlist entry, since one left behind after a rename
  silently exempts whatever takes that import path next. Enumerating means a
  package added tomorrow is covered the day it is created.
- **The external fixture must consume, not only implement.**
  `internal/apicheck/testdata/externalmod` proves `Source`, `ObjectStore`, and
  a secret backend can be implemented from outside. It does not prove the
  library can be *used* from outside, which is problems (4) and (5). Extend it
  to load a profiles file, resolve it through `pkg/config`, open a client
  through `pkg/open`, run a backup with a `WithLogger` sink attached, and
  assert on the debug output — importing no `internal/` package. That test
  failing is the definition of a regression in this RFC's goal.

## Rollout plan

1. Alias sweep (§1) + API-boundary regression test (§2). Small, additive,
   immediately unblocks naming every `Client` result type. **Done.**
2. Split `pkg/source` into contract + provider subpackages (§3), update
   `cmd/cloudstic` wiring, shell-completion generators, and tests. **Done.**
3. Split `pkg/store` the same way (§4), update `storebuild.go` and tests.
   **Done.**
4. Documentation (§5) and the external-module fixture (Testing strategy).
   **Done.**
5. Split the AWS KMS client out of `pkg/crypto` (§6), and add the
   dependency-weight sweep that would have caught it. **Done.**
6. Characterization tests over the §7 blast radius, in `package main`, before
   anything moves. Then fix the `packfile` and `s3.region` zero values as a
   separate, behavioral commit. **Done.**
7. Export the resolved config types' *fields* in place, still in
   `package main`; then move the types to `pkg/config` behind
   `type clientConfig = config.Client` aliases; then the URI parsers; then
   profile resolution with the resolver injected. **Done.**

   The first of those was not in the original plan, which assumed the aliases
   made the move call-site-neutral on their own. They do not: an alias makes
   the type *name* spellable from `package main` (93 mentions), but it cannot
   make unexported fields reachable from another package, and field accesses
   are 100 sites across 8 files. Exporting the fields first — a pure rename,
   one package, verified by the compiler and applied with `gopls rename` so
   that fields named `key`, `region`, `password` and `profile` cannot collide
   with unrelated identifiers spelled the same way — makes the move that
   follows genuinely call-site-neutral.
8. Add `pkg/open`; reduce `storebuild.go`, `clientbuild.go`, and `keychain.go`
   to adapters. **Done.**
9. Logger injection (§8), per component: the logger infrastructure and the
   client, store, hamt and engine-manager call sites first; then the
   free-function and independently-constructed ones, after which the global
   can be deleted.
10. Extend the external fixture to consume the library end to end.

Steps 1–3 made external custom sources and stores possible, which was the
original goal. Steps 5–10 are what make the library usable by the consumer who
does *not* want to extend it, and each remains independently valuable.

Step 5 is independent of everything after it and is the largest single
improvement per line changed — it should not wait on the rest. Steps 6–8 are
strictly ordered: the tests gate the move, and per this repo's refactoring
practice the behavioral zero-value fix must not share a commit with the
structural move. Step 7's type aliases are what keep each commit small enough
to review — but only once its field-export commit has run first, since until
then the aliases cover the type names and nothing else.

## Open questions

1. ~~Should the external-module fixture live permanently in-tree…~~
   **Resolved:** in-tree, at `internal/apicheck/testdata/externalmod`, enforced
   on every change. Stage 10 extends it from implementing the contracts to
   consuming the library.
2. Does the API-boundary test need an allowlist mechanism for cases where an
   internal type is deliberately, temporarily exposed during a staged rollout,
   or should any leak simply fail the test until aliased?
3. ~~`withDebugStore` mutates the global `logger.Writer`…~~ **Resolved:** it is
   not a standalone cleanup after all. The same global is what makes debug
   output unreachable from outside the module (problem 5), so it is now §8 of
   this RFC rather than a separate track.
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
