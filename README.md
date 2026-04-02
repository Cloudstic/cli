# Cloudstic CLI

[![CI](https://github.com/Cloudstic/cli/actions/workflows/ci.yml/badge.svg)](https://github.com/Cloudstic/cli/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/Cloudstic/cli/branch/main/graph/badge.svg)](https://codecov.io/gh/Cloudstic/cli)
![Release](https://img.shields.io/github/v/release/Cloudstic/cli)
![License](https://img.shields.io/github/license/Cloudstic/cli)
![Go Version](https://img.shields.io/github/go-mod/go-version/Cloudstic/cli)

Content-addressable, encrypted backup tool for Google Drive, OneDrive, and local files.

## Features

- **Encrypted by default:** AES-256-GCM encryption with password, platform key, or recovery key slots
- **Content-addressable storage:** Deduplication across sources; identical files stored only once
- **Incremental backups:** Only changed files are stored
- **Multiple sources:** Google Drive, Google Drive Changes API, OneDrive, local directories
- **Multiple backends:** Local filesystem, Amazon S3 (and compatibles like R2, MinIO), or Backblaze B2
- **Retention policies:** Keep-last, hourly, daily, weekly, monthly, yearly
- **Portable drive awareness:** Automatically identifies USB drives and external disks by GPT partition UUID — back up the same drive from any machine or mount point, across macOS, Linux, and Windows
- **Point-in-time restore:** Restore any snapshot, any file, any time

## Supported Sources

| Source | Flag | Description |
| :--- | :--- | :--- |
| Local directory | `-source local` | Back up any local folder (auto-detects portable drives) |
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

# Curl installer (macOS / Linux)
curl -fsSL https://raw.githubusercontent.com/Cloudstic/cli/main/scripts/install.sh | sh

# Install with shell completion
curl -fsSL https://raw.githubusercontent.com/Cloudstic/cli/main/scripts/install.sh | sh -s -- --with-completion
```

Or download a binary from [Releases](https://github.com/cloudstic/cli/releases). See the [User Guide](docs/user-guide.md#installation) for all options.

## Quick Start

```bash
# Initialize an encrypted repository (prompts for password interactively)
cloudstic init

# Back up a local directory (prompts for password if not set via flag or env)
cloudstic backup -source local:~/Documents

# Back up Google Drive (opens browser for auth on first run)
cloudstic backup -source gdrive-changes

# Back up a USB drive (auto-detected by partition UUID)
cloudstic backup -source local:/Volumes/MyUSB

# List snapshots
cloudstic list

# Restore latest snapshot to a zip file
cloudstic restore

# Preview what a backup would do (dry run)
cloudstic backup -source local:~/Documents -dry-run

# Discover local source candidates and portable drives
cloudstic source discover -portable-only

# Preview a workstation onboarding plan
cloudstic setup workstation -dry-run
```

## Profiles

Save your backup configuration once and reuse it:

```bash
# Create a store (interactive — prompts for encryption setup)
cloudstic store new -name my-s3 -uri s3:my-bucket/backups -s3-region us-east-1

# Create a profile
cloudstic profile new -name documents -source local:~/Documents -store-ref my-s3

# Now backups are one command
cloudstic backup -profile documents

# Or back up all profiles at once
cloudstic backup -all-profiles
```

See the [User Guide — Profiles](docs/user-guide.md#profile) for details.

When running interactively, Cloudstic prompts for the repository password if no credential is provided via flags or environment variables. For non-interactive use (scripts, cron), pass `-password` or set `CLOUDSTIC_PASSWORD`:

```bash
cloudstic init -password "my passphrase"
cloudstic backup -source local:~/Documents -password "my passphrase"
```

## Portable Drive Backup

Cloudstic automatically detects when a source path is on a portable drive (USB stick, external SSD, SD card). It identifies the drive by its **GPT partition UUID** and stores paths relative to the volume root, so backups are consistent regardless of where the drive is mounted or which OS you use.

```bash
# On macOS — drive mounts at /Volumes/MyUSB
cloudstic backup -source local:/Volumes/MyUSB

# On Linux — same drive mounts at /mnt/usb
cloudstic backup -source local:/mnt/usb

# Both produce identical snapshots with the same source identity
cloudstic list
```

This works automatically on GPT-formatted drives (exFAT, APFS, ext4, NTFS). For older MBR-formatted drives, pass `-volume-uuid` explicitly. See the [User Guide](docs/user-guide.md#portable-drive-backup) for details.

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
- [Contributing](docs/contributing.md): testing, profiling, debugging

## Cloud Service

Don't want to manage infrastructure? [Cloudstic Cloud](https://cloudstic.com) handles scheduling, storage, and retention automatically. Same engine, zero ops.

## License

MIT
