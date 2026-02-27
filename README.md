# Cloudstic CLI

Content-addressable, encrypted backup tool for Google Drive, OneDrive, and local files.

## Features

- **Encrypted by default** — AES-256-GCM encryption with password, platform key, or recovery key slots
- **Content-addressable storage** — Deduplication across sources; identical files stored only once
- **Incremental backups** — Only changed files are stored
- **Multiple sources** — Google Drive, Google Drive Changes API, OneDrive, local directories
- **Multiple backends** — Local filesystem or Backblaze B2
- **Retention policies** — Keep-last, hourly, daily, weekly, monthly, yearly
- **Point-in-time restore** — Restore any snapshot, any file, any time

## Supported Sources

| Source | Flag | Description |
|--------|------|-------------|
| Local directory | `-source local` | Back up any local folder |
| Google Drive | `-source gdrive` | Full rescan of My Drive or a Shared Drive |
| Google Drive (Changes) | `-source gdrive-changes` | **Recommended.** Fast incremental backup via the Changes API |
| OneDrive | `-source onedrive` | Full scan of a Microsoft OneDrive account |
| OneDrive (Changes) | `-source onedrive-changes` | **Recommended.** Fast incremental backup via the Delta API |

Google Drive and OneDrive work out of the box — no API keys or credentials setup needed. On first run, Cloudstic opens your browser for authorization and caches the token locally. See the [User Guide — Sources](docs/user-guide.md#sources) for details.

## Install

```bash
brew install cloudstic/tap/cloudstic   # macOS / Linux
winget install Cloudstic.CLI           # Windows
go install github.com/cloudstic/cli/cmd/cloudstic@latest  # with Go
```

Or download a binary from [Releases](https://github.com/cloudstic/cli/releases). See the [User Guide](docs/user-guide.md#installation) for all options.

## Quick Start

```bash

# Initialize an encrypted repository
cloudstic init -encryption-password "my passphrase"

# Back up a local directory
cloudstic backup -source local -source-path ~/Documents -encryption-password "my passphrase"

# Back up Google Drive (opens browser for auth on first run)
cloudstic backup -source gdrive-changes -encryption-password "my passphrase"

# List snapshots
cloudstic list -encryption-password "my passphrase"

# Restore latest snapshot to a zip file
cloudstic restore -encryption-password "my passphrase"
```

## Documentation

- [User Guide](docs/user-guide.md) — commands, setup, encryption, retention policies
- [Source API](docs/sources.md) — source interface, implementations, and how to add a new source
- [Specification](docs/spec.md) — object types, backup/restore flow, HAMT structure
- [Encryption](docs/encryption.md) — key slot design, AES-256-GCM, recovery keys
- [Storage Model](docs/storage-model.md) — content-addressable storage layout

## Cloud Service

Don't want to manage infrastructure? [Cloudstic Cloud](https://cloudstic.com) handles scheduling, storage, and retention automatically. Same engine, zero ops.

## License

MIT
