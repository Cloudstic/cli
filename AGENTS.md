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

E2E tests in `cmd/cloudstic/` are controlled by `CLOUDSTIC_E2E_MODE`:
- `hermetic` (default) — local filesystem + Testcontainers (MinIO, SFTP). Requires Docker.
- `live` — real cloud vendor APIs (requires secrets).
- `all` — runs both.

Docker-based hermetic tests (MinIO store, SFTP source/store) are automatically skipped if `/var/run/docker.sock` is not available.

## Architecture

### Package Layout

- `client.go` (root) — Public `Client` API. Re-exports types from internal packages using Go type aliases. This is the library entry point for programmatic use.
- `cmd/cloudstic/` — CLI entry point. Each subcommand (`init`, `backup`, `restore`, `list`, `ls`, `prune`, `forget`, `diff`, `break-lock`, `add-recovery-key`) is a `run*()` function in `main.go`. Uses Go's `flag` package (no cobra/viper).
- `internal/engine/` — Business logic for each operation (backup, restore, prune, forget, diff, list). Each operation has a `*Manager` struct (e.g. `BackupManager`, `RestoreManager`) with a `Run(ctx)` method.
- `internal/core/` — Domain types: `Snapshot`, `FileMeta`, `Content`, `HAMTNode`, `RepoConfig`, `SourceInfo`. Also contains `ComputeJSONHash` which is the canonical content-addressing function.
- `internal/hamt/` — Persistent Merkle Hash Array Mapped Trie. Backed by the object store. Used to track file→filemeta mappings across snapshots. `TransactionalStore` buffers writes and flushes only reachable nodes.
- `pkg/store/` — `ObjectStore` interface and all implementations. Also contains `Source` and `IncrementalSource` interfaces for backup data sources.
- `pkg/crypto/` — AES-256-GCM encryption/decryption, HKDF key derivation, BIP39 mnemonic recovery keys.
- `internal/ui/` — Console progress reporting and terminal helpers.

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

- On `init`, a random 32-byte master key is generated and wrapped into key slots (password-based via scrypt, platform key, or BIP39 recovery key).
- Key slots are stored under `keys/` prefix, which the `EncryptedStore` passes through unencrypted.
- An HMAC dedup key is derived from the encryption key via HKDF for content-addressing without exposing plaintext hashes.

### HybridStore

Routes metadata objects to PostgreSQL (with RLS tenant isolation via `SET LOCAL cloudstic.tenant_id`) and chunk data to B2. Metadata is also written through to B2 for disaster recovery.

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
   - Add a `run*()` function in `cmd/cloudstic/main.go`.
   - Add the command to the switch case in `runCmd()`.
   - Add command documentation to `printUsage()`.
   - Use the `reorderArgs()` helper for proper flag parsing.

5. **Error Handling** — Return descriptive errors:
   - Wrap errors with context using `fmt.Errorf("context: %w", err)`.
   - Provide actionable error messages to users.
   - Distinguish between user errors and system errors.

### Testing Guidelines

- Run `go test -v -count=1 ./...` before committing to ensure all tests pass.
- E2E tests require Docker for Testcontainers (MinIO, SFTP). They skip gracefully if Docker is unavailable.
- Use `-race` flag during development to catch race conditions.
- Hermetic tests (default) use local filesystem + containers; no cloud credentials needed.
