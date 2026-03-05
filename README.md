# Cloudstic CLI

Content-addressable, encrypted backup tool for Google Drive, OneDrive, and local files.

## Features

- **Encrypted by default:** AES-256-GCM encryption with password, platform key, or recovery key slots
- **Content-addressable storage:** Deduplication across sources; identical files stored only once
- **Incremental backups:** Only changed files are stored
- **Multiple sources:** Google Drive, Google Drive Changes API, OneDrive, local directories
- **Multiple backends:** Local filesystem, Amazon S3 (and compatibles like R2, MinIO), or Backblaze B2
- **Retention policies:** Keep-last, hourly, daily, weekly, monthly, yearly
- **Point-in-time restore:** Restore any snapshot, any file, any time

## Supported Sources

| Source | Flag | Description |
|--------|------|-------------|
| Local directory | `-source local` | Back up any local folder |
| Google Drive | `-source gdrive` | Full rescan of My Drive or a Shared Drive |
| Google Drive (Changes) | `-source gdrive-changes` | **Recommended.** Fast incremental backup via the Changes API |
| OneDrive | `-source onedrive` | Full scan of a Microsoft OneDrive account |
| OneDrive (Changes) | `-source onedrive-changes` | **Recommended.** Fast incremental backup via the Delta API |

Google Drive and OneDrive work out of the box. On first run, Cloudstic opens your browser for authorization and caches the token locally. See the [User Guide — Sources](docs/user-guide.md#sources) for details.

## Install

```bash
brew install cloudstic/tap/cloudstic   # macOS / Linux
winget install Cloudstic.CLI           # Windows
go install github.com/cloudstic/cli/cmd/cloudstic@latest  # with Go
```

Or download a binary from [Releases](https://github.com/cloudstic/cli/releases). See the [User Guide](docs/user-guide.md#installation) for all options.

## Quick Start

```bash
# Initialize an encrypted repository (prompts for password interactively)
cloudstic init

# Back up a local directory (prompts for password if not set via flag or env)
cloudstic backup -source local -source-path ~/Documents

# Back up Google Drive (opens browser for auth on first run)
cloudstic backup -source gdrive-changes

# List snapshots
cloudstic list

# Restore latest snapshot to a zip file
cloudstic restore

# Preview what a backup would do (dry run)
cloudstic backup -source local -source-path ~/Documents -dry-run
```

When running interactively, Cloudstic prompts for the repository password if no credential is provided via flags or environment variables. For non-interactive use (scripts, cron), pass `-encryption-password` or set `CLOUDSTIC_ENCRYPTION_PASSWORD`:

```bash
cloudstic init -encryption-password "my passphrase"
cloudstic backup -source local -source-path ~/Documents -encryption-password "my passphrase"
```

## Performance

Benchmarked against Restic, Borg, and Duplicacy on a ~1 GB dataset (local) and a real Google Drive account (~40 MB). Full methodology and numbers in [docs/benchmark-results.md](docs/benchmark-results.md).

**Local filesystem** (time / peak RAM):

| Operation | Cloudstic | Restic | Borg | Duplicacy |
| :--- | :--- | :--- | :--- | :--- |
| Initial backup | 0.61s / 259 MB | 1.80s / 314 MB | 1.69s / 139 MB | 3.44s / 284 MB |
| Incremental (no changes) | 0.05s / 96 MB | 0.77s / 72 MB | 0.67s / 72 MB | 0.04s / 44 MB |
| Add 200 MB new data | 0.14s / 152 MB | 1.07s / 283 MB | 0.57s / 136 MB | 0.93s / 195 MB |

**Google Drive** — each step run stateless (no rclone cache) to reflect real-world cold-start conditions:

| Operation | Cloudstic | Restic | Borg |
| :--- | :--- | :--- | :--- |
| Initial backup | 6.08s | 11.14s | 15.06s |
| Incremental (no changes) | **0.56s** | 14.70s | 25.49s |

Cloudstic uses the Google Drive Changes API natively — incremental backups only fetch what actually changed, no full re-scan required. Restic and Borg rely on rclone FUSE mounts and must re-download the entire dataset to detect changes.

## Documentation

Full documentation is available at **[docs.cloudstic.com](https://docs.cloudstic.com)**.

This repository also contains developer-focused reference docs:

- [User Guide](docs/user-guide.md): commands, setup, encryption, retention policies
- [Source API](docs/sources.md): source interface, implementations, and how to add a new source
- [Specification](docs/spec.md): object types, backup/restore flow, HAMT structure
- [Encryption](docs/encryption.md): key slot design, AES-256-GCM, recovery keys
- [Storage Model](docs/storage-model.md): content-addressable storage layout

## Cloud Service

Don't want to manage infrastructure? [Cloudstic Cloud](https://cloudstic.com) handles scheduling, storage, and retention automatically. Same engine, zero ops.

## License

MIT
