# AGENTS.md

This file provides guidance to agents when working with code in this repository.

## Project Overview

Cloudstic CLI is a content-addressable, encrypted backup tool written in Go. It supports multiple data sources (local filesystem, Google Drive, OneDrive, SFTP) and multiple storage backends (local, S3/R2/MinIO, Backblaze B2, SFTP). Backups are deduplicated via content-addressing, compressed with zstd, and encrypted with AES-256-GCM.

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

# One sweep: time, peak RSS and allocation across sizes (minutes, not seconds)
SIZES="5000 20000" SAMPLES=3 ./scripts/benchmark/bench.sh

# The same plus request and byte counts, against MinIO as well as local
BACKENDS="local minio" SIZES="5000" ./scripts/benchmark/bench.sh
```

### Performance measurement

Three layers, in cost order:

- **Go benchmarks** (`*_bench_test.go`) — allocation and time per operation.
  Compare runs with `benchstat`; `-benchmem` reports `B/op`, which measures
  allocation *volume* and cannot distinguish memory that is freed from memory
  that is retained.
- **`scripts/benchmark/compare.sh`** — the same binary against restic, borg and
  duplicacy. Manual, and needs those tools installed.

  CI does **not** use `compare.sh`, and the split is not cosmetic. Its dataset
  exists to be fair across four tools and may legitimately change for that
  reason — which would silently move numbers a trend line is built on. The two
  share `lib.sh` for measurement mechanics and nothing else; in particular
  their datasets are separate, which is the whole point.
- **`internal/cmd/gentree/`** — generates a backup source with the
  statistics a real one has: heavy-tailed file sizes and directory fan-out,
  duplicated content, and churn that clusters in a few directories rather than
  spreading evenly. Seeded, so a given (profile, files, seed, MAX_BYTES) is
  byte-identical run to run — a benchmark whose dataset drifts measures nothing. Profiles:
  `source` (the default), `media`, `mixed`, and `uniform` — the original flat tree
  of identical files, kept only for reproducing a historical measurement, since
  its lack of duplication and constant fan-out is what made this workstream draw
  wrong conclusions about deduplication and write amplification. Select with
  `PROFILES=source ./scripts/benchmark/bench.sh`; `MAX_BYTES` scales the size
  distribution to keep a realistic tree runnable. `-churn-dirs` sets how many
  directories a churn is spread across, independently of how many files it
  changes — the profile's own `churnDirZipfS` chooses *which* directories are
  hot rather than how many are reached, and sweeping it moved the directories
  touched by 200 changed files only from 53 to 41. `-churn-renames 0` suppresses
  the per-round directory rename, which matters because under a path-identity
  source a rename re-keys every descendant (issue #543): every retention
  measurement taken before that knob existed had one folded into it, and
  isolating it put renames at about 9% of the cost.
- **`internal/cmd/leafstat/`** — reports what a format-v3 repository's HAMT
  leaves are made of: their size distribution, how much of each is inline file
  content rather than metadata, and — via `-refs` set-differenced across
  snapshots — how much of the tree each backup rewrote. `-entries` emits one
  line per entry naming its body and size, which differenced across a
  repository's snapshots is what lets a layout be *simulated* over real churn
  before it is built: the blob-packing numbers in RFC 0026's revision come from
  that, on repositories that already existed. The other tools observe a
  repository from outside and so cannot answer why a v3 repository grows with
  retained snapshots, which is a property of individual leaves (issue #525).
  Local, unencrypted repositories only.
- **`scripts/benchmark/bench.sh`** — one pass over the pipeline collecting every
  metric it can yield: wall time and peak RSS from `time(1)`, cumulative
  allocation from the binary's `-memstats`, and — on a MinIO backend — requests
  and bytes with a per-API breakdown. The matrix is
  `PROFILES x SIZES x BACKENDS x SAMPLES`; a cell runs the pipeline once and
  fills the columns available to it. Backend stays an axis rather than a metric,
  because pointing at MinIO changes what is measured (the S3 SDK's buffers are
  part of the process) rather than adding detail to the local number.
  Peak RSS answers "does this grow with the repository", which a single
  measurement cannot: an operation holding a fixed working set is fine at any
  scale, one holding a per-entry structure eventually meets a repository it
  cannot open. Peak RSS is a high-water mark, though, and blind to churn the
  collector reclaims: PR #449 removed 36 MB of allocation from `CompactCatalog`
  and moved peak RSS by 5 MB, inside the ±60 MB run-to-run spread observed on
  one machine — allocation is the column that shows a change like that. Each
  point is `SAMPLES` repetitions (default 3) reduced to a median with its
  min–max band. Writes one CSV per backend plus a sidecar recording the machine,
  since timings are a property of the hardware as much as of the code.
  `minio.sh` holds the container lifecycle and metric scraping; docker, `aws`
  and `curl` are needed only for `BACKENDS=minio`. What the `Benchmark`
  workflow runs.

  Setting `AGE_CHECKPOINTS="1 10 40 80"` adds an **aging stage** per
  (profile, size, backend) cell (absorbing the former `aging.sh`, RFC 0026):
  one repository is aged with `AGE_CHURN` files of churn per backup —
  `AGE_CHURN_DIRS` caps how many directories that churn is spread across,
  which is a separate variable from how many files it changes and the one
  format v3's retention cost is a function of (RFC 0027 §6); the two are
  coupled by fan-out, so a cap below roughly `count / fan-out` reduces volume
  as well as breadth and `gentree` reports what it achieved — and
  `AGE_OPS` (default `restore check`; also `ls`, `find`, `diff`, which
  traverse the same tree by different routes and are counted separately for
  that reason — `find` uses a never-matching pattern so it is the same walk
  every time, `diff` compares oldest against latest so it measures the tree
  rather than one churn step) are measured at each checkpoint, always reading
  the latest snapshot. This is the one axis the pipeline matrix structurally
  cannot see — how read cost grows with the number of backups that
  contributed to a snapshot, with the tree held fixed. `AGE_FINAL_OPS`
  (`backup`, `prune`) run once after the last checkpoint, never at one: a
  mutation at a checkpoint silently redefines every checkpoint after it. Rows
  land in the same CSV with the `packs`/`backups`/`policy` columns filled and
  operations named `restore@40`. Every row also carries `stored_kb`, the
  repository's total size when it was measured: the aging backups are setup
  rather than measurements, so what they write appears in no `repo_delta`, and
  a delta taken around a read is zero however much history is being carried.
  Differencing it between checkpoints is what a *retained snapshot* costs
  (RFC 0026, issue #525), rendered as the Aging section's **Retained size**
  table. `benchreport` renders them as an **Aging**
  section — one table per (operation, policy), rows ordered by backup count,
  with the growth factor computed across the endpoints — and keeps them out of
  the per-operation tables, where a `restore@25` row would read as another
  operation measured at the sweep's tree sizes. Request counts need
  `BACKENDS=minio`; a local backend still yields the wall-time, peak-RSS and
  retained-size half of the curve — and retained size is the one aging number a
  local store measures as well as MinIO does, which makes it the cheap way to
  compare two formats' write amplification.

  The `Benchmark` workflow's MinIO job runs the aging stage by default
  (`1 10 25`, overridable via the `age_checkpoints` dispatch input, empty to
  disable) because it answers what the pipeline structurally cannot: the
  pipeline measures a *freshly created* repository, which is the best case for
  any layout that bundles objects and the least representative one. The local
  job does not — request count is the measurement and a local store reports
  none. `REPO_FORMAT=3` selects the packless fat-leaf format, so the same
  build can be measured at both formats (RFC 0026).

  Two mechanisms compare variants against **one** aged repository, because
  aging twice ages into two *different* repositories (pack composition is not
  deterministic) and the difference between runs is then not the change under
  test (RFC 0025 §7). `POLICIES` compares env-variable knobs on one binary,
  interleaved at every checkpoint —
  `POLICIES='baseline=; probe=CLOUDSTIC_TEST_X=1'`. Two *builds* cannot
  interleave, so they go through `KEEP_STORE=1` (age once, leave MinIO and
  the working tree up) followed by `ATTACH=1` per build (skip the pipeline
  and the aging, measure the checkpoint reads against the repository already
  there; the closing message of a `KEEP_STORE` run prints the exact command).
  An explicit `BENCH_CLOUDSTIC_BIN` is never rebuilt, so a probe build is not
  silently replaced by the working tree. Note also that the backup-count axis
  does not vary what `packBodyCache` bounds, which is bytes: forty backups of
  small churn keep every pack body resident, so a policy that only matters
  under eviction shows nothing along it.

## Architecture

### Package Layout

- Root package (`client.go`, `repo.go`, `aliases.go`, `backup.go`, `query.go`, `retention.go`, `restore.go`, `check.go`) — the public `Client` API and the library entry point. Split by domain rather than held in one file: `client.go` is the `Client`, its options and the store chain; `repo.go` covers init, format upgrade and key slots; `aliases.go` holds the type re-exports; the rest map to operations. Internal types reachable from an exported signature are re-exported as Go type aliases, which `TestPublicAPIHasNoUnaliasedInternalTypes` enforces, and every exported `With*` option in `internal/engine` must be mirrored here, which `TestEveryEngineOptionIsReExported` enforces (both in `internal/apicheck`).
- `cmd/cloudstic/` — CLI entry point (`package main`). `command.go` defines the recursive `command` tree (`leaf`/`group`) and its shared dispatcher; `commands.go` holds `commandRegistry()`, the ordered list that is the single source of truth for the command surface: `main.go`'s `runCmd()` dispatches from it, `printUsage()` renders its `COMMANDS` listing from it, and the shell-completion command lists are generated from it. Each command's `run*()` function lives in its own `cmd_*.go` file (e.g. `cmd_backup.go`, `cmd_key.go`). A `cmd_*.go` file holds the command surface — its args, flags, and `run*()`; workflows those commands call into live beside it under the area's own name, so `store_encryption.go` (choosing a store's encryption method and where its secrets live) and `store_health.go` (is a store reachable and initialized, including the AWS SSO re-auth path) sit next to `cmd_store.go` rather than inside it, since `profile new` and `setup` call into both. Subcommands: `init`, `backup`, `restore`, `list`, `ls`, `find`, `prune`, `forget`, `diff`, `copy`, `break-lock`, `key` (with `list`/`add-recovery`/`passwd`), `check`, `cat`, `profile`, `auth`, `store`, `source`, `setup`, `tui`, `completion`. Uses Go's `flag` package (no cobra/viper); `reorderArgs()` in `flags.go` allows flags after positional args. The interactive terminal UI lives in `cmd_tui*.go`.
  - Commands are free functions taking the dependency container first: `run<Name>(r *runner, ctx, ...)` (and `exec<Name>(r, ctx, ...)` for testable sub-steps). The `runner` struct (`runner.go`) carries `out`/`errOut` writers and the client, so command output is capturable in tests; only I/O primitives (`fail`, `writeJSON`, `openClient`, `prompt*`) remain methods on it.
  - `globalFlags` (`flags.go`) holds only parsed command-line/environment values — it has no methods that build infrastructure. `config.go` defines the resolved configuration types (`clientConfig`, `storeConfig`, `unlockConfig`, …) and the explicit, non-mutating resolution step `resolveClientConfig(g *globalFlags) (clientConfig, error)`, which folds in the selected profile's store without writing back into `g`. Construction takes those config values, never `globalFlags`, so it's unit-testable without going through flag parsing: `storebuild.go` builds the object store, `clientbuild.go` layers a store/keychain/reporter into a repository client, and `keychain.go` builds the keychain/KMS client. `TestGlobalFlagsHasNoConstructionMethods` (`config_test.go`) fails if a construction method is added back onto `globalFlags`.
  - Each command translates its flags into a resolved `pkg/config` value **once**, at the top, and everything below works from that value: `clientConfigFromFlags` for the repository and `backupConfigFromFlags` (`cmd_backup.go`) for what to back up. Both are pure translations — no I/O, no mutation of the args. Nothing downstream reads the flag struct except for the two things a resolved config deliberately does not carry: the output mode, and the store half that `r.openClient` resolves from the selected profile. Source construction and option building are *not* in this package at all — they are `open.Source` and `open.Backup`.
  - Presentation is separate from orchestration: `print*`/`render*` helpers are free functions taking an `io.Writer` first (`printBackupSummary(out, res)`), so result formatting cannot reach the client or command flow.
  - `cloudsticClient` (`client_iface.go`) is the interface `runner` depends on — satisfied by the real `*Client` and by `stubClient` (`stub_client_test.go`) in unit tests.
- `internal/engine/` — Business logic for each operation (backup, restore, prune, forget, diff, list). Each operation has a `*Manager` struct (e.g. `BackupManager`, `RestoreManager`) with a `Run(ctx)` method.
- `internal/core/` — Repository-format types: `Snapshot`, `Content`, `RepoConfig`, plus `ComputeJSONHash`, the canonical content-addressing function, and `FileMetaRef`. `FileMeta`, `SourceInfo` and `FileType` are *defined in `pkg/source`* and aliased here, so the public Source contract does not depend on an internal package while the engine keeps spelling them `core.FileMeta` (RFC 0022). It holds the types more than one package shares; a format type read and written by exactly one package belongs to that package instead (see `internal/hamt`).
- `internal/objkey/` — The compact in-memory form of an object key (`Key`, `Encode`, `DecodeDigest`) and the `Set` built on it. A key is `<namespace>/<64 hex>`: 73 bytes of text plus, as a map key, an interior pointer the garbage collector traces, all to carry 32 bytes of hash. Reducing the namespace to a byte and decoding the hash puts the key inline in the map — 67 B/entry against 132 for `map[string]bool`. Used by the structures sized by the repository rather than by the work in front of them: prune's reachable set, check's verified set, and `storelayer`'s pack catalog. It is *not* repository format — a `Key` never reaches a store — which is why it is not in `internal/core`. The encoding is **total**: a key it cannot encode is kept verbatim in a string-keyed fallback, never dropped, because `docs/compatibility.md` forbids a garbage collector from reading "cannot represent" as "not referenced". Only canonical lowercase hex decodes, so encoding is injective and two byte-distinct keys can never share a `Key`.
- `internal/hamt/` — Persistent Merkle Hash Array Mapped Trie. Backed by the object store. Used to track file→filemeta mappings across snapshots. A `Txn` (`hamt.go`) rewrites nodes in memory and writes nothing until `Commit`, which serializes only the dirty spine — so nodes superseded mid-transaction are never uploaded. It also **owns the encoding of the `node/<sha256>` objects** (`node.go`): the stored form (`storedNode`, `leafEntry`) sits next to the in-memory form and the conversion between them, unexported because no other package reads or writes a node. That encoding is still repository format — `core.ComputeJSONHash` marshals fields in declaration order, so a field, tag or `omitempty` change rewrites every root hash, which `TestRootHashGolden` pins. The package depends on `internal/core` for content-addressing (`ComputeJSONHash`, `ComputeHash`, `VerifyRef`) and nothing else: the hash function is a shared invariant across snapshot, filemeta and node objects, so injecting a different one would silently produce a differently-addressed repository.
- `pkg/store/` — The `ObjectStore` contract and its optional capability interfaces (`RangeGetter`, `BatchDeleter`, `ConcurrencyHinter`, `Unwrapper`), plus the order-independent wrappers `QuotaStore` and `DebugStore`. Depends on nothing outside the standard library, so implementing a custom backend pulls in no vendor SDK (RFC 0022). Backends live in their own subpackages: `pkg/store/{local,s3,b2,sftp}`, constructed as `local.New`, `s3.New`, … `pkg/store/storetest` holds shared test doubles (`MemStore`, `FaultStore`, `AssertRangeGetterConformance`, `AssertBatchDeleterConformance`); it deliberately redeclares the interfaces it needs rather than importing `pkg/store`, because `pkg/store`'s own internal tests import it.
- `internal/storelayer/` — The repository-format decorator chain: `CompressedStore`, `EncryptedStore`, `MeteredStore`, `PackStore` — plus `KeyCacheStore`, which is not part of that chain but wraps it (see Store Layering below). Internal on purpose: their **composition order is a correctness and security invariant** (see Store Layering below), and nothing outside the module should assemble the chain. A caller who wants to wrap a store implements `store.ObjectStore` and passes it to `NewClient`, which layers this chain on top — exactly what `cmd/cloudstic` does with `DebugStore`.
- `pkg/crypto/` — AES-256-GCM encryption/decryption, HKDF key derivation, BIP39 mnemonic recovery keys, and the `KMSClient` interface. Depends on no cloud SDK; `pkg/crypto/kms` holds the AWS implementation of that interface.
- `internal/app/` — Orchestration layer shared by the CLI and TUI. `TUIService` sits on top of a `TUIBackend` interface (satisfied by the real client, stubbable in tests) and owns profile listing, health checks, and backup actions.
- `internal/tui/` — Interactive terminal dashboard built on Bubble Tea. `dashboard.go` derives the view-model from the profiles config and store probes, `app.go` is the root `Model` (view + key handling), `summary.go` holds the pure label/badge/button derivation it renders, `styles.go` is the lipgloss theme, and `forms/` holds the `bubbles/textinput` form components. Bubble Tea owns the terminal — there is no hand-rolled renderer, input decoder, or resize handling (RFC 0012 Phase 2, issue #341). The `cmd_tui*.go` files in `cmd/cloudstic/` only wire it to `internal/app`; the widget/state logic lives here.
- `internal/ui/` — Non-interactive console progress reporting and terminal helpers (used by plain CLI commands, distinct from the `internal/tui` dashboard).
- `pkg/secretref/` — The `scheme://path` secret-reference contract: `Ref`, `Parse`, `Backend`, `BlobBackend`, `WritableBackend`, `Resolver`, `NewResolver`, `Error`. Public so a custom backend (Vault, a cloud KMS) can be registered from another module. `pkg/secretref/backends/` holds the built-ins and `Default()`, which returns a fresh map so callers extend the set rather than replace it (see Secret References below).
- `pkg/profile/` — The backup-profiles YAML format: `Config`, `Profile`, `Store`, `Auth`, `Load`, `Save`, `LoadOrEmpty`, `EnsureMaps`, `Normalize`, plus `Config.StoreFor` (which store a profile selects) and `DefaultPath`/`DefaultFilename` (where the file lives). Public because the profiles file is user-facing and worth scripting against without opening a repository.
- `pkg/config/` — The *resolved* configuration: `Client`, `Store`, `Unlock`, `Backup`, `Source`, and the URI parsers. It answers "what did the user configure", performs no I/O against a store or provider, and depends on nothing heavier than YAML — so reading and validating configuration is cheap. Zero values are the correct defaults throughout (hence `DisablePackfile`, not `Packfile`). Layering a caller's own configuration mechanism over a profile goes through `MergeProfileStore` / `MergeProfileBackup` with a `FieldSet` of typed `Field` constants naming what the caller has already decided; `StoreFields`/`BackupFields` enumerate them and `FieldsSetIn` derives a set from a filled-in `Client`. The set is typed rather than stringly-keyed because a misspelled key silently meant "not decided", and the profiles file spells `s3_access_key` where the flag is `-s3-access-key` (`Field.ProfileKey` renders the former).
- `pkg/open/` — Construction: `Store`, `Keychain`, `Client`, `Source`, `Backup`, and the one-call `FromProfile`. It answers "connect to it", and is the one public package allowed to link a provider SDK (see `sdkBearingPackages` in `internal/apicheck`) — which is why it, and not the root client, is where a profile becomes a live client. `WithDecided` layers a caller's own configuration over the profile; `WithSecretResolver`, `WithReporter`, `WithDebugWriter`, `WithLogger` and `WithBackendWrapper` carry the writers and callbacks a serializable config cannot. `Backup` returns a `BackupJob` pairing the source with its options because the two share one derived value — the snapshot's exclude hash must cover exactly the patterns the source filters on.
- `internal/workstation/` — Workstation onboarding: `Plan`, `Apply`, `Setup`, and local source discovery (`DiscoverSources`) including the platform-specific mount probing. Internal: it is a CLI wizard, not a library capability.
- `pkg/keychain/` — OS keychain integration and encryption key-slot helpers. `WithKMSClient` takes an already-built `crypto.KMSClient` and so needs no SDK; `pkg/keychain/kms.WithARN` builds one from a key ARN on demand and is separated for that reason.
- `internal/paths/` — Config-directory and token-path resolution (`ConfigDir()`), plus `MachineID()`.
- `internal/pathmatch/` — Glob matching for slash-separated paths: `path.Match` per segment plus `**` for "zero or more segments", which `path.Match` alone cannot express. It is the single glob implementation in the module: `find`'s `-name`/`-path` patterns and `pkg/source`'s `ExcludeMatcher` both compile through it, so the two cannot drift the way they had (the exclude matcher used to open-code `**` over `filepath.Match`, whose separator is platform-dependent). `pkg/source` importing it is invisible to an external `Source` implementer, who only ever names `pkg/source`. Note that `**` is a segment wildcard only when it is a whole segment — `a**b` is consecutive asterisks within one segment, per gitignore.
- `internal/logger/`, `internal/retry/`, `internal/sftp/` — Structured logging, retry/backoff helpers, and shared SFTP client used by both the SFTP source and store.

### Store Layering (Decorator Pattern)

Stores are composed as a decorator chain, assembled by the root package from
`internal/storelayer`. **The order matters, and getting it wrong is silent** —
`PackStore` sits below `EncryptedStore`, so its catalog and footers never pass
through encryption and need a separately derived key. A chain built without
`WithPackIndexKey` yields a repository whose pack index is plaintext, with no
error at any layer. That is why these types are internal and why callers inject
a backend into `NewClient` rather than composing the chain themselves (RFC 0022).

```
CompressedStore → EncryptedStore → MeteredStore → [PackStore] → <backend>
```

- `CompressedStore` — zstd compression on write, auto-detects zstd/gzip/raw on read.
- `EncryptedStore` — AES-256-GCM. Passes through objects under `keys/` prefix unencrypted (key slots).
- `MeteredStore` — Tracks bytes written for reporting.
- `PackStore` (optional) — Bundles small objects (<512KB) into 8MB packfiles to reduce API calls. Only content-addressed prefixes (`filemeta/`, `node/`, `snapshot/`, `chunk/`, `content/`) are packed; mutable keys such as `index/latest` are never bundled. Each packfile ends with a self-describing footer (`internal/storelayer/packfooter.go`, RFC 0018) listing its contents, which makes the `index/packs` JSON catalog a rebuildable cache rather than the sole source of truth: a missing catalog is healed automatically from footers before any read is served, and `RebuildCatalog` exposes the same repair explicitly. Packs predating the footer cannot be recovered that way, so a rebuild reports how many it found instead of returning a partial catalog. A catalog that is *unreadable* (as opposed to absent) fails the calling operation instead of degrading to an empty one, and `Flush` refuses to overwrite a catalog it has not first merged with the stored copy. Footer reads use the optional `RangeGetter` interface (`pkg/store/interface.go`), implemented by `local.Store`, `s3.Store`, `b2.Store`, `sftp.Store`, and forwarded by `DebugStore`, falling back to a full `Get` for any backend without it. All implementations are held to one shared contract by `storetest.AssertRangeGetterConformance`, which each backend package calls, and `TestPackStore_UsesRangedReadsForFooters` asserts the ranged path is actually taken rather than silently degrading to whole-pack transfers. The catalog is stored as append-only shards under `index/packmap/` (`internal/storelayer/packshard.go`), one per flush, so concurrent writers cannot erase each other's entries the way a single read-modify-write object allowed; readers merge every shard plus the pre-shard `index/packs` if the repository still has one. Shards cannot express a deletion, so removing an entry is durable only once `CompactCatalog` rewrites the index — which is why `prune` compacts and why a flush following a delete does too. Compaction removes only index objects the store has itself absorbed, so it cannot delete a shard written concurrently but never read. Because a shard also costs every later operation one request to read, `backup` consolidates the index when it has grown past `packIndexCompactThreshold` (`internal/engine/packindex.go`) — otherwise that cost grows with the number of backups a repository has ever taken, and only `prune` ever bounded it. It does so after releasing its shared lock and under the exclusive lock, since deleting absorbed shards can fail a concurrent reader that listed them first; `AcquireRepoLock` fails immediately when any lock is held, so concurrent backups skip it and whichever finishes alone does the work. Because `PackStore` sits below `EncryptedStore` and the index and footers never pass through it, both are sealed with a separate HKDF-derived key (`crypto.HKDFInfoPackIndexV1`, passed via `WithPackIndexKey`); plaintext indexes written before this — and those in unencrypted repositories — are still read, and are sealed on the next flush.
`KeyCacheStore` is deliberately absent from that chain: `NewClient` never builds
one. `BackupManager` wraps the *whole* assembled chain in one from above, for
the duration of a single run, so it short-circuits before compression and
encryption are reached — which is the point, since a content-addressed key it
already knows needs no write at all. See `docs/caching.md`.
- Backend: `local.Store`, `s3.Store`, `b2.Store`, or `sftp.Store`, each in its own subpackage under `pkg/store/`.

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
2. Source is scanned via `Walk()` (full) or `WalkChanges()` (incremental, for gdrive-changes/onedrive-changes). The full scan buffers the walk into batches of `entryBatch` and resolves each batch's previous refs before reading any of them, so change detection's filemeta reads are declared to the store together rather than arriving one at a time in an order unrelated to storage layout (RFC 0025). Entries are still *processed* in walk order — that order becomes the upload order, and with it the locality of newly written objects.
3. New/changed files are chunked (`internal/engine/chunker.go`) using FastCDC, content-addressed, and uploaded.
4. The HAMT tree is updated with new filemeta refs through a `hamt.Txn`, which holds every intermediate node in memory and serializes only the dirty spine reachable from the final root.
5. A new `Snapshot` object is written, and `index/latest` is updated.
6. After the shared lock is released, the pack index is consolidated if it has grown past its threshold (see `PackStore` above).

### Batched Deletion

`prune`'s sweep is the only place `BatchDeleter` (`pkg/store/interface.go`) is
used, and deletion is the only direction object stores batch: S3's
`DeleteObjects` takes up to 1,000 keys per request (MinIO and B2's
S3-compatible endpoint included), Azure Blob Batch 256, GCS 100, while none of
them offers a multi-object GET or PUT — which is why read aggregation had to
move into the data layout instead (RFC 0026, issue #518). `s3.Store` implements
it over `DeleteObjects`; `local`, `sftp` and `b2` implement it as a loop via
`store.DeleteEach`, so a caller needs no fallback branch, and a custom backend
that implements neither keeps working through `store.DeleteAll`.

Two things are load-bearing:

- **The capability is looked up on the store itself, never by unwrapping.**
  `Delete` is not a passthrough at every layer — `PackStore`'s rewrites a
  catalog rather than touching the backend — so reaching past a wrapper to the
  batch-capable backend beneath it would delete nothing and report everything.
  `CompressedStore`, `EncryptedStore`, `KeyCacheStore`, `MeteredStore`,
  `QuotaStore` and `DebugStore` each declare `DeleteAll` because forwarding is
  safe for them; `PackStore` deliberately does not, which sends
  `store.DeleteAll` down its per-key loop, and `TestPackStoreDoesNotClaimBatchDelete`
  pins that.
- **Per-key failures are never collapsed.** A `DeleteObjects` response carries
  a verdict per key, so implementations return `store.DeleteErrors` naming
  every key they could not confirm gone — a key the backend refused, and a key
  its response did not mention at all. `store.FailedDeletes` distinguishes that
  from an error with no per-key detail, where nothing may be assumed and a
  caller doing accounting must credit none of the batch.
  `MeteredStore.DeleteAllReturnSizes` is where that accounting lives: it
  reports the size of each key it confirmed deleted, and prune counts objects
  and reclaimed bytes from that map alone. A sweep that could not delete
  everything it classified as garbage deletes what it can and then fails, since
  `docs/compatibility.md` forbids a garbage collector reporting a success over
  data it could not fully act on.

### Encryption Model

- On `init`, a random 32-byte master key is generated and wrapped into key slots (password-based via scrypt, platform key, KMS-wrapped platform key, or BIP39 recovery key).
- Key slots are stored under `keys/` prefix, which the `EncryptedStore` passes through unencrypted.
- An HMAC dedup key is derived from the encryption key via HKDF for content-addressing without exposing plaintext hashes.
- `kms-platform` slots use AWS KMS envelope encryption (master key wrapped by a KMS CMK). The CLI supports these via `-kms-key-arn` flag or `CLOUDSTIC_KMS_KEY_ARN` env var. The `crypto.KMSClient` *interface* lives in `pkg/crypto` (`kmsclient.go`); the AWS implementation lives in `pkg/crypto/kms`, and the ARN-based keychain credential in `pkg/keychain/kms`. That split keeps `pkg/crypto`, `pkg/keychain`, `pkg/secretref/backends` and the root client free of the AWS SDK unless a caller actually uses KMS (RFC 0022 §6), which `TestPublicPackagesPullNoVendorSDK` (`internal/apicheck`) enforces across every public package.

### Configuration & Profiles

- **Config directory** — resolved by `internal/paths.ConfigDir(override)`: the `-config-dir` flag if given, else `CLOUDSTIC_CONFIG_DIR`, else `os.UserConfigDir()/cloudstic` (e.g. `~/.config/cloudstic`). Holds the profiles file, OAuth tokens, and the `config-token` secret backend's managed store. Resolution does **no** filesystem access — the directory is created `0700` by whoever writes into it (`paths.SaveAtomic`, `profile.Save`), so help, completion and `setup -dry-run` can ask where configuration lives without creating it.
- **Profiles** — a profile bundles a named source + store (+ secret refs) so users run `cloudstic backup -profile <name>` instead of re-specifying flags. Persisted via `profile.Load`/`profile.Save` (`pkg/profile`). Managed from the CLI (`cmd_profile.go`, `cmd_setup.go`) and the TUI. See RFC `rfcs/0010-backup-profiles.md`. The active profile / file come from `CLOUDSTIC_PROFILE` / `CLOUDSTIC_PROFILES_FILE`.
- **Resolution precedence** — `resolveClientConfig` (`config.go`) resolves a command's store configuration as: explicit CLI flag > selected profile's store field > environment variable > built-in default. A profile is a named choice the user invoked with `-profile`, so once selected it overrides ambient environment variables the same way it overrides built-in defaults — only an explicit flag beats it. `applyProfileStore` expresses this as a `config.FieldSet` built by `flagDecidedFields`, which only recognizes `originFlag`; `originEnv` values are treated the same as `originDefault` and are eligible to be overridden. The set is derived by iterating `config.StoreFields()` / `config.BackupFields()` rather than listing field names here, so a field added to the profiles format is covered without a matching edit on the CLI side. See `TestResolveClientConfig_ProfileOverridesEnvironment` (`config_test.go`).

### Flag Defaults That Depend on Other Flags

A flag whose default is computed from another flag declares it with
`withLateDefault` (`flagspec.go`) instead of passing a value to `stringFlag`.
Late defaults resolve in `applyLateDefaults`, a pass after `applyEnvDefaults`,
and only for flags still at their built-in value — so an explicit flag and an
environment value both still win, and the origin stays `originDefault` so a
profile may still override it.

`-profiles-file` is the case this exists for: its default is a path inside
`-config-dir`, unknown until parsing finishes. Computing such a default at
declaration time is wrong twice over — it reads a flag that has no value yet,
and it runs on every path that merely *describes* a command, including `-h` and
shell completion, which is how resolving the profiles path used to create the
config directory as a side effect of asking for help.

### Secret References

Store and source credentials can be stored as `scheme://path` references rather than inline secrets, resolved by `pkg/secretref`. Each scheme is a pluggable, platform-gated backend implemented in `pkg/secretref/backends`:

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
| `CLOUDSTIC_CONFIG_DIR` | Override the config/state directory (default `~/.config/cloudstic`); `-config-dir` beats it. |
| `CLOUDSTIC_{STORE,SOURCE}_SFTP_{PASSWORD,KEY,KNOWN_HOSTS,INSECURE}` | SFTP auth/host-key config for the store and source backends. |
| `CLOUDSTIC_DISABLE_PACKFILE` | Disable the `PackStore` small-object bundling layer. |
| `CLOUDSTIC_VOLUME_UUID` | Override the volume UUID for a local source (cross-machine incremental backup for portable drives). |
| `CLOUDSTIC_E2E_MODE` | E2E test mode: `hermetic` (default), `live`, or `all` (see below). |

This table is a curated subset (`CLOUDSTIC_*` vars only) for orientation. The
exhaustive, one-row-per-variable inventory — including provider-standard vars
like `AWS_ACCESS_KEY_ID` and `B2_KEY_ID` that are deliberately unprefixed — is
the "Environment Variables" table in `docs/user-guide.md`, kept complete by
`TestUserGuideDocumentsEveryEnvVar` (`cmd/cloudstic/flagspec_test.go`), which
fails if a flag's `env` binding has no matching row.

`CLOUDSTIC_TEST_*` are test-only knobs, not user-facing:
`CLOUDSTIC_TEST_LEAF_BYTES` and `CLOUDSTIC_TEST_NODE_CACHE_BYTES`
(`internal/hamt/tuning.go`) size a v3 leaf and the node cache;
`CLOUDSTIC_TEST_INLINE_BYTES` (`internal/engine/backup_upload.go`) sets the
inline threshold, so setting it to 1 chunks every body and produces a tree
whose leaves carry metadata and refs only — which is how RFC 0026's
metadata-only figures were measured rather than extrapolated.

### Documentation Drift

The user-facing docs live in a separate repository (`Cloudstic/doc`), so nothing
here breaks when a rename leaves their code samples describing an API that no
longer exists — which is how `store.NewLocalStore` and `source.NewGDriveSource`
survived two releases past the backend split, long enough that the documented
Quick Start could not compile.

`internal/apicheck/docs_test.go` closes that gap. Point `CLOUDSTIC_DOCS_DIR` at a
docs checkout and it checks every Go sample against the real API; without the
variable it skips, so the ordinary build needs no second checkout:

```bash
CLOUDSTIC_DOCS_DIR=../cloudstic-doc go test ./internal/apicheck -run TestDocs
```

Two things are checked, matching the two ways docs actually rot: a sample naming
a symbol that no longer exists, and a stated signature that no longer matches
(`Client.Find` kept its name while losing its variadic options, so every
documented call became wrong without the name changing).

It does not compile the samples. Most are statement fragments referencing
variables established in prose, and the rest are type declarations and bodiless
signatures that would either fail to compile or compile vacuously as fresh local
declarations — assembling them takes guesswork per block and fails for reasons
that are not drift. Illustrative pseudocode opts out with
`{/* apicheck:ignore <reason> */}` before the fence.

The docs repository runs this on every pull request. **A change here that renames
or removes exported API should be paired with a docs update**, since the docs
side is where the failure surfaces.

## Documentation Map

Deep-dive docs live in `docs/` and design records in `rfcs/`:

- `docs/compatibility.md` — **normative** repository compatibility contract. Read it before changing anything on disk.
- `docs/spec.md` — format/protocol specification.
- `docs/storage-model.md` — object model and store layering.
- `docs/caching.md` — every in-process cache, what it holds, and why none is redundant.
- `docs/encryption.md` — key hierarchy, slots, and the encryption model.
- `docs/sources.md` — supported data sources and their identity model.
- `docs/user-guide.md` — end-user command reference.
- `rfcs/NNNN-*.md` — numbered design proposals; add a new one for substantial features (see RFC 0010 for the profiles design as a template).

## Repository Compatibility

`docs/compatibility.md` is normative and takes precedence over convenience. The
short version, which applies to any change touching what is written to a store:

- **Backward compatibility is permanent.** A repository written by any released
  version must stay readable by every later version — `list`, `ls`, `check`,
  `cat`, `diff`, `restore`. There is no deprecation window and no migration you
  may require in order to *read*. Legacy read paths may be refactored, never
  removed.
- **Forward compatibility is not guaranteed, but failure must be safe.** Older
  builds may be unable to read newer repositories. They must never *misread*
  them. Two rules carry the weight:
  - Never treat "cannot decode" as "empty". A failed index load is an error, not
    an index with no entries — "no entries" is what makes a garbage collector
    delete a live repository.
  - Never let `prune`, `forget`, or repacking proceed on data that could not be
    fully read.
- **The version gate.** `core.RepoFormatVersion` and
  `core.MaxSupportedRepoFormat` (`internal/core/models.go`) gate every repository
  open through `LoadRepoConfig`. Raise both when a change would make a
  repository unreadable or misreadable by earlier builds; leave them alone when
  earlier builds cope, since a needless bump locks users out of their own data.
- **Upgrades are in place, opportunistic, and permanently partial.** New
  structures are written in the current format; older ones are read as they are
  and rewritten only when a write happens to pass over them. A repository stays
  a mixture of eras indefinitely, and that is the steady state — permanent
  backward compatibility means no code ever needs to know whether migration
  "finished". `config.version` is not a completion marker: it is the signal that
  tells other machines sharing the repository to upgrade. `UpgradeRepoFormat`
  stamps it after a successful `backup`, `prune`, or `forget`, and never on a
  read — a read changes nothing, and `LoadRepoConfig` runs on restore paths where
  a write would break read-only credentials.
- **Changing the on-disk format** requires, per `docs/compatibility.md`: keeping
  older layouts readable, upgrading only opportunistically, committing a fixture
  from the last release using the old format, deciding on the version gate,
  adding the baseline to the doc's table, and stating in the PR what older
  builds do when they meet the new format — **verified by running an old binary,
  not by reasoning about it**.

Enforcement lives in `e2e/feature_legacy_repo_test.go`
(`TestCLI_Feature_ReadsLegacyRepositories` runs the current build against every
committed baseline; `TestCompatibilityDocListsEveryFixture` fails when a fixture
is missing from the doc's table) and in `repo_format_test.go` for the gate.

## Development Best Practices

### When Adding New Features

When implementing new functionality, always consider the following:

1. **Documentation** — Check if user-facing documentation needs to be updated:
   - `docs/user-guide.md` — Add command documentation with usage examples, flags, and descriptions.
   - `README.md` — Update if the feature changes the quick start or high-level overview.
   - Code comments — Document public APIs, especially in the root package and package interfaces.

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

Markdown files are linted with `markdownlint-cli2` (rules from `.markdownlint-cli2.jsonc`). Follow these conventions to avoid CI failures:

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

## Commit Hygiene

Treat the staged file list as a security and release boundary. Ad-hoc build
binaries (`cs` and `cs439`, about 90 MB combined) reached main in the past; a
matching `.gitignore` rule is a backstop, not a substitute for reviewing what a
commit contains.

- Run `git status --short` before staging and account for every modified and
  untracked path. Existing changes may belong to the user or another workstream.
- Stage only explicit paths that were intentionally created or modified for the
  current task. Never use `git add .`, `git add -A`, a directory-wide add, or a
  wildcard that can sweep up files you have not reviewed.
- Never add a binary file. Do not commit compiled executables, archives,
  profiles, coverage output, scratch data, credentials, tokens, or other build
  and runtime artifacts.
- Before committing, inspect `git diff --cached --name-status` and
  `git diff --cached --stat`, then review the staged diff for every text file.
  Use `file -- <path>` when a file's type is not obvious. Every staged path must
  be known, intentional, and explainable.
- Run `git diff --cached --check`. If anything unexpected is staged, unstage it
  without deleting the working-tree file, investigate how it got there, and do
  not commit until the index contains only reviewed files.

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

Create issues with `gh issue create` against `Cloudstic/cli`. The forms under
`.github/ISSUE_TEMPLATE/` are the source of truth even when the CLI bypasses the
web template chooser. Match the existing house style (see #155, #250–#253 as
references).

Never open a public issue for a suspected vulnerability. Direct it to the
repository's private security-advisory form, as configured in
`.github/ISSUE_TEMPLATE/config.yml`.

**Bug reports** — follow `01-bug-report.yml`. Include these sections in order:

- `Context`
- `Reproduction`
- `Actual behavior`
- `Expected behavior`
- `Cloudstic version`
- `Operating system`
- `Relevant configuration` when it matters
- `Logs or additional evidence` when available, with secrets and private paths
  redacted
- `Acceptance criteria` when the regression has concrete proof conditions

**Work items and proposals** — follow `02-work-item.yml`. Use these core
Markdown sections in order:

```markdown
## Context
Current state and why it matters. Reference concrete files (and functions) with
backtick paths, e.g. `cmd/cloudstic/cmd_backup.go`. Cross-link related issues
with `#NNN` when relevant.

## Goal
One or two sentences on the desired end state.

## Scope
- bullet list of the concrete changes to make

## Repository compatibility
State how old repositories remain readable and whether the format version must
change, or write "No repository format change."

## Acceptance Criteria
- bullet list of verifiable outcomes
- always end with the test and lint commands that must pass, scoped to the
  touched packages, e.g.
- `go test ./cmd/cloudstic` passes
- `golangci-lint run ./cmd/cloudstic/...` passes
```

**Labels** — apply exactly one *type* label plus one or more `area/*` labels:

- Type (pick one): `bug`, `enhancement`, `refactor`, `tech debt`, `chore`, `test`, `documentation`, `perf`, `rfc`, `tracking`
- Area: `area/cli`, `area/core`, `area/tui`, `area/completion`, `area/onboarding`, `area/ci`

**Titles** — see Naming Conventions above. In short: no conventional-commit prefix (the label carries the type), lead with an imperative verb or an `Area:` scanning prefix, no trailing period.

Do not invent new labels — reuse what `gh label list` returns.

## Creating Pull Requests

Open PRs with `gh pr create` against `Cloudstic/cli`. Use
`.github/pull_request_template.md` as the source of truth even when constructing
the body non-interactively. Match the existing house style (see #214, #228,
and #236–#237 as references).

**Branch names** — `<type>/<kebab-slug>`, where the type mirrors the label vocabulary: `feat/`, `refactor/`, `test/`, `chore/`, `fix/`, `docs/` (e.g. `feat/tui-profile-history`, `refactor/tui-profile-modal-state`). Dependabot branches (`dependabot/...`) are machine-generated — leave them alone.

**Titles** — see Naming Conventions above. Use a Conventional Commit prefix (`type: …` or `type(scope): …`) matching the branch type; because PRs squash-merge, the title becomes the commit subject.

**Body structure** — use these Markdown sections:

```markdown
## Summary
- bullet list of the concrete changes, imperative voice

## Related issues
<`Closes #NNN`, `Part of #NNN`, `Fixes #NNN`, or `None`>

## Repository compatibility
<compatibility and version-gate decision, or `No repository format change.`>

## Verification
- <exact test command run, scoped to the touched packages>
- <exact lint command run, scoped to the touched packages>

## Documentation
<documentation changes, or `No documentation change required.`>
```

- Keep the `Summary` bullets high-signal — what changed and why, not a file-by-file diff.
- `Related issues` ties the PR to its issue; write `None` only for standalone work.
- Read `docs/compatibility.md` before changing anything written to a store. The
  compatibility section must identify the legacy read path, safe failure
  behavior for older builds, and repository-format version decision.
- Exported API changes must be paired with an update to the separate Cloudstic
  docs repository; say so in `Documentation`.
- Under `Verification`, paste the **exact commands you ran**, scoped to the packages you touched, using the repo's cache-env prefixes so they reproduce in CI:

```bash
env GOCACHE=/tmp/cloudstic-gocache go test -count=1 ./cmd/cloudstic ./internal/tui
env GOCACHE=/tmp/cloudstic-gocache GOLANGCI_LINT_CACHE=/tmp/cloudstic-golangci-lint golangci-lint run ./cmd/cloudstic ./internal/tui
```

Apply the same *type* + `area/*` labels described above; the type usually matches the branch prefix.
