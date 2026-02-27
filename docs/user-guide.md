# Cloudstic CLI User Guide

Cloudstic is a content-addressable backup tool that creates encrypted, deduplicated snapshots of your files — from local directories, Google Drive, or OneDrive — and stores them locally or on Backblaze B2.

## Table of Contents

- [Quick Start](#quick-start)
- [Installation](#installation)
- [Concepts](#concepts)
- [Configuration](#configuration)
- [Commands](#commands)
  - [init](#init)
  - [backup](#backup)
  - [restore](#restore)
  - [list](#list)
  - [ls](#ls)
  - [diff](#diff)
  - [forget](#forget)
  - [prune](#prune)
  - [add-recovery-key](#add-recovery-key)
- [Sources](#sources)
  - [Local Directory](#local-directory)
  - [Google Drive](#google-drive)
  - [Google Drive (Changes API)](#google-drive-changes-api)
  - [OneDrive](#onedrive)
  - [OneDrive (Changes API)](#onedrive-changes-api)
- [Storage Backends](#storage-backends)
  - [Local](#local-storage)
  - [Backblaze B2](#backblaze-b2)
- [Encryption](#encryption)
- [Retention Policies](#retention-policies)
- [Environment Variables](#environment-variables)

---

## Quick Start

```bash
# 1. Initialize an encrypted repository (encryption is required by default)
cloudstic init -encryption-password "my secret passphrase"

# 2. Back up a local directory
cloudstic backup -source local -source-path ~/Documents -encryption-password "my secret passphrase"

# 3. List snapshots
cloudstic list -encryption-password "my secret passphrase"

# 4. Restore the latest snapshot
cloudstic restore -encryption-password "my secret passphrase"
```

## Installation

### Homebrew (macOS / Linux)

```bash
# Install
brew install cloudstic/tap/cloudstic

# Upgrade to the latest version
brew upgrade cloudstic

# Uninstall
brew uninstall cloudstic
```

### Winget (Windows)

```powershell
# Install
winget install Cloudstic.CLI

# Upgrade to the latest version
winget upgrade Cloudstic.CLI

# Uninstall
winget uninstall Cloudstic.CLI
```

### Pre-built binaries

Download the latest release for your platform from the [GitHub Releases](https://github.com/cloudstic/cli/releases) page. Binaries are available for macOS (Intel & Apple Silicon), Linux (amd64 & arm64), and Windows.

```bash
# Example: macOS Apple Silicon
curl -L https://github.com/cloudstic/cli/releases/latest/download/cloudstic_$(curl -s https://api.github.com/repos/cloudstic/cli/releases/latest | grep tag_name | cut -d '"' -f 4 | sed 's/^v//')_darwin_arm64.tar.gz | tar xz
mv cloudstic /usr/local/bin/
```

### Install with Go

```bash
go install github.com/cloudstic/cli/cmd/cloudstic@latest
```

### Build from source

Requires Go 1.24+:

```bash
git clone https://github.com/cloudstic/cli.git
cd cli
go build -o cloudstic ./cmd/cloudstic
mv cloudstic /usr/local/bin/
```

### Verify

```bash
cloudstic version
```

## Concepts

**Repository** — A storage location (local directory or B2 bucket) that holds your backups. Created with `cloudstic init`.

**Snapshot** — A point-in-time record of all files from a source. Each backup creates a new snapshot. Snapshots are identified by a SHA-256 hash.

**Source** — Where files are read from during backup: a local directory, Google Drive, or OneDrive.

**Content-addressable storage** — Files are split into chunks and stored by their content hash. Identical chunks across files or snapshots are stored only once (deduplication).

**Key slots** — Encryption keys are wrapped in "slots", each accessible via a different credential (password, platform key, or recovery key). All slots unlock the same master key.

## Configuration

### Config directory

Cloudstic stores OAuth tokens and other state files in a platform-specific config directory:

| Platform | Default path |
|----------|-------------|
| Linux    | `~/.config/cloudstic/` |
| macOS    | `~/Library/Application Support/cloudstic/` |
| Windows  | `%AppData%\cloudstic\` |

Override with the `CLOUDSTIC_CONFIG_DIR` environment variable.

### Setting defaults with environment variables

Most flags can be set via environment variables to avoid repeating them. For example:

```bash
export CLOUDSTIC_STORE=b2
export CLOUDSTIC_STORE_PATH=my-backup-bucket
export CLOUDSTIC_ENCRYPTION_PASSWORD="my secret passphrase"
export B2_KEY_ID=your-key-id
export B2_APP_KEY=your-app-key

# Now commands are much shorter:
cloudstic backup -source local -source-path ~/Documents
cloudstic list
cloudstic restore
```

See [Environment Variables](#environment-variables) for the full list.

---

## Commands

### init

Initialize a new repository. Encryption is **required by default**.

```bash
# Password-based encryption (recommended)
cloudstic init -encryption-password "my secret passphrase"

# With a recovery key (strongly recommended for personal use)
cloudstic init -encryption-password "my secret passphrase" -recovery

# Platform key encryption (for automation)
cloudstic init -encryption-key <64-hex-chars>

# Both password and platform key (dual access)
cloudstic init -encryption-password "passphrase" -encryption-key <hex>

# Unencrypted (must be explicit — not recommended)
cloudstic init -no-encryption
```

**Flags:**

| Flag | Description |
|------|-------------|
| `-encryption-password` | Password for password-based encryption |
| `-encryption-key` | Platform key (64 hex chars = 32 bytes) |
| `-recovery` | Generate a 24-word recovery key during init |
| `-no-encryption` | Create an unencrypted repository (not recommended) |

When `-recovery` is used, a 24-word seed phrase is displayed **once**. Write it down and store it safely — it's your last resort if you lose your password.

---

### backup

Create a new snapshot from a source.

```bash
# Back up a local directory
cloudstic backup -source local -source-path ~/Documents

# Back up Google Drive (My Drive)
cloudstic backup -source gdrive

# Back up a specific Google Drive shared drive and folder
cloudstic backup -source gdrive -drive-id <shared-drive-id> -root-folder <folder-id>

# Back up with tags
cloudstic backup -source local -source-path ~/Documents -tag daily -tag important

# Verbose output (shows individual files)
cloudstic backup -source local -source-path ~/Documents -verbose
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-source` | `gdrive` | Source type: `local`, `gdrive`, `gdrive-changes`, `onedrive`, `onedrive-changes` |
| `-source-path` | `.` | Path to local source directory (when `source=local`) |
| `-drive-id` | | Shared drive ID for Google Drive (omit for My Drive) |
| `-root-folder` | | Root folder ID for Google Drive (defaults to entire drive) |
| `-tag` | | Tag to apply to the snapshot (repeatable) |
| `-verbose` | `false` | Show detailed file-level output |

The `gdrive-changes` and `onedrive-changes` source types use their respective change/delta APIs for faster incremental backups after the first full backup.

---

### restore

Restore a snapshot as a ZIP archive.

```bash
# Restore the latest snapshot
cloudstic restore

# Restore a specific snapshot
cloudstic restore <snapshot-hash>

# Restore to a custom output path
cloudstic restore <snapshot-hash> -output ./my-backup.zip
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-output` | `./restore.zip` | Output ZIP file path |

The snapshot ID is a positional argument (defaults to latest if omitted).

---

### list

List all snapshots in the repository.

```bash
cloudstic list
```

Outputs a table with sequence number, creation time, snapshot hash, source info, and tags.

---

### ls

List the file tree within a specific snapshot.

```bash
# List files in the latest snapshot
cloudstic ls

# List files in a specific snapshot
cloudstic ls <snapshot-hash>
```

---

### diff

Compare two snapshots to see what changed.

```bash
# Compare two specific snapshots
cloudstic diff <snapshot-hash-1> <snapshot-hash-2>

# Compare a snapshot against the latest
cloudstic diff <snapshot-hash> latest
```

Shows added, modified, and removed files between the two snapshots.

---

### forget

Remove snapshots from the repository. This deletes the snapshot metadata but **does not** reclaim storage — run `prune` afterwards (or use `-prune`).

```bash
# Forget a single snapshot
cloudstic forget <snapshot-hash>

# Forget and immediately prune
cloudstic forget <snapshot-hash> -prune

# Apply a retention policy
cloudstic forget -keep-last 7 -keep-daily 30 -keep-monthly 12

# Dry run — show what would be removed without actually removing
cloudstic forget -keep-last 7 -keep-daily 30 -dry-run

# Filter by tag before applying policy
cloudstic forget -keep-last 3 -tag daily

# Filter by source
cloudstic forget -keep-last 5 -source gdrive -account user@gmail.com
```

**Retention policy flags:**

| Flag | Description |
|------|-------------|
| `-keep-last N` | Keep the N most recent snapshots |
| `-keep-hourly N` | Keep N hourly snapshots |
| `-keep-daily N` | Keep N daily snapshots |
| `-keep-weekly N` | Keep N weekly snapshots |
| `-keep-monthly N` | Keep N monthly snapshots |
| `-keep-yearly N` | Keep N yearly snapshots |

**Filter flags:**

| Flag | Description |
|------|-------------|
| `-tag` | Filter by tag (repeatable) |
| `-source` | Filter by source type |
| `-account` | Filter by account |
| `-path` | Filter by path |
| `-group-by` | Group snapshots by fields (default: `source,account,path`) |

**Other flags:**

| Flag | Description |
|------|-------------|
| `-prune` | Run prune immediately after forgetting |
| `-dry-run` | Show what would be removed without acting |

---

### prune

Remove unreachable data chunks that are no longer referenced by any snapshot. Run this after `forget` to reclaim storage.

```bash
cloudstic prune
```

---

### add-recovery-key

Generate a 24-word recovery key for an existing encrypted repository. Requires your current encryption credential to unlock the master key.

```bash
cloudstic add-recovery-key -encryption-password "my secret passphrase"
```

The recovery key is displayed once. Write it down immediately.

---

## Sources

A **source** is where Cloudstic reads files from during a backup. Each source type connects to a different storage provider and walks its file tree to detect new, changed, or deleted files.

### Source overview

| Source | `-source` flag | What it backs up | Auth |
|--------|---------------|------------------|------|
| [Local directory](#local-directory) | `local` | Files on your local filesystem | None |
| [Google Drive](#google-drive) | `gdrive` | Full scan of Google Drive (My Drive or Shared Drive) | Automatic (browser) |
| [Google Drive (Changes API)](#google-drive-changes-api) | `gdrive-changes` | Incremental changes since last backup (recommended for Google Drive) | Automatic (browser) |
| [OneDrive](#onedrive) | `onedrive` | Full scan of Microsoft OneDrive | Automatic (browser) |
| [OneDrive (Changes API)](#onedrive-changes-api) | `onedrive-changes` | Incremental changes since last backup (recommended for OneDrive) | Automatic (browser) |

All sources produce the same snapshot format. You can back up different sources into the same repository, and snapshots are tagged with source metadata so retention policies can be applied per-source.

### Local Directory

Back up files from a local filesystem path. No authentication or environment variables required.

```bash
cloudstic backup -source local -source-path /path/to/directory
```

| Flag | Default | Description |
|------|---------|-------------|
| `-source-path` | `.` | Root directory to back up |

Cloudstic walks the directory recursively. Symbolic links are not followed. File permissions are not preserved — only name, size, modification time, and content are captured.

### Google Drive

Full scan of a Google Drive account. On each backup, Cloudstic lists every file and folder, compares metadata against the previous snapshot, and uploads anything new or changed.

> **Note:** For routine backups, prefer [`gdrive-changes`](#google-drive-changes-api) instead — it is significantly faster and makes far fewer API requests.

**When to use:** First backup of a Google Drive, or when you want a guaranteed complete rescan (e.g. after recovering from an error).

**Setup:**

No configuration is required — Cloudstic ships with built-in OAuth credentials. On first run, your default browser opens automatically for you to authorize access. The resulting token is cached in the [config directory](#config-directory).

```bash
# Back up entire My Drive
cloudstic backup -source gdrive

# Back up a shared drive
cloudstic backup -source gdrive -drive-id <shared-drive-id>

# Back up only a specific folder (by Google Drive folder ID)
cloudstic backup -source gdrive -root-folder <folder-id>
```

| Flag | Description |
|------|-------------|
| `-drive-id` | Shared Drive ID (omit for personal My Drive) |
| `-root-folder` | Restrict backup to a specific folder by ID |

**Environment variables (optional overrides):**

| Variable | Description |
|----------|-------------|
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to your own Google OAuth credentials JSON file (overrides built-in credentials) |
| `GOOGLE_TOKEN_FILE` | Override token cache path (default: `<config-dir>/google_token.json`) |

### Google Drive (Changes API)

**This is the recommended way to back up Google Drive.** Uses the Google Drive Changes API to fetch only files that changed since the last backup, rather than listing every file on the drive. This dramatically reduces both backup duration and the number of API requests — a drive with 100,000 files but 50 daily changes only needs to process those 50 files instead of listing all 100,000.

**When to use:** All routine Google Drive backups. The first run performs a full scan automatically, so there is no need to start with `gdrive`.

**How it works:** The first run behaves like a full `gdrive` backup and records a change token. Subsequent runs fetch only the changes since that token, making backups much faster for drives with thousands of files but few daily modifications.

```bash
# First run: full scan + saves change token
cloudstic backup -source gdrive-changes

# Subsequent runs: only fetches changes since last token
cloudstic backup -source gdrive-changes
```

Uses the same authentication and flags as [Google Drive](#google-drive) (`-drive-id`, `-root-folder`). No setup required — just run the command and authorize in the browser.

> **Tip:** You can use `-source gdrive-changes` from day one — the first run performs a full scan just like `gdrive`. Only fall back to `-source gdrive` if you need to force a complete rescan.

### OneDrive

Full scan of a Microsoft OneDrive account. Works the same way as the `gdrive` source — lists all files, compares against the previous snapshot, and uploads changes.

**Setup:**

No configuration is required — Cloudstic ships with built-in OAuth credentials. On first run, your default browser opens automatically for you to authorize access. The resulting token is cached in the [config directory](#config-directory).

```bash
cloudstic backup -source onedrive
```

If you prefer to use your own Azure app registration instead of the built-in credentials:

1. Go to the [Azure App Registrations](https://portal.azure.com/#blade/Microsoft_AAD_RegisteredApps/ApplicationsListBlade) portal
2. Register a new application with **"Accounts in any organizational directory and personal Microsoft accounts"**
3. Under **Authentication**, add platform **Mobile and desktop applications** with redirect URI `http://localhost/callback`
4. Under **Authentication > Advanced settings**, enable **Allow public client flows**
5. Under **API permissions**, add `Files.Read.All` and `User.Read` (Microsoft Graph, Delegated)

```bash
export ONEDRIVE_CLIENT_ID=your-client-id

cloudstic backup -source onedrive
```

No client secret is needed — Cloudstic uses the public client flow with PKCE.

**Environment variables (optional overrides):**

| Variable | Description |
|----------|-------------|
| `ONEDRIVE_CLIENT_ID` | Azure app client ID (overrides built-in credentials) |
| `ONEDRIVE_TOKEN_FILE` | Override token cache path (default: `<config-dir>/onedrive_token.json`) |

### OneDrive (Changes API)

**This is the recommended way to back up OneDrive.** Uses the Microsoft Graph delta API to fetch only files that changed since the last backup, rather than listing every file on the drive. This dramatically reduces both backup duration and the number of API requests.

**When to use:** All routine OneDrive backups. The first run performs a full scan automatically, so there is no need to start with `onedrive`.

**How it works:** The first run behaves like a full `onedrive` backup and records a delta token. Subsequent runs fetch only the changes since that token, making backups much faster for drives with many files but few daily modifications.

```bash
# First run: full scan + saves delta token
cloudstic backup -source onedrive-changes

# Subsequent runs: only fetches changes since last token
cloudstic backup -source onedrive-changes
```

Uses the same authentication as [OneDrive](#onedrive). No setup required — just run the command and authorize in the browser.

> **Tip:** You can use `-source onedrive-changes` from day one — the first run performs a full scan just like `onedrive`. Only fall back to `-source onedrive` if you need to force a complete rescan.

### Source metadata in snapshots

Each snapshot records which source produced it. This metadata is used by retention policies to group snapshots — for example, you can keep different retention rules for your local backups vs. your Google Drive backups:

```bash
# Keep 30 daily snapshots for Google Drive, 7 for local
cloudstic forget -keep-daily 30 -source gdrive -prune
cloudstic forget -keep-daily 7 -source local -prune
```

---

## Storage Backends

### Local Storage

Store backups on the local filesystem. This is the default.

```bash
# Uses default path ./backup_store
cloudstic init -encryption-password "passphrase"

# Custom path
cloudstic init -store local -store-path /mnt/external/backups -encryption-password "passphrase"
```

### Backblaze B2

Store backups in a Backblaze B2 bucket. Requires B2 application keys.

```bash
export B2_KEY_ID=your-key-id
export B2_APP_KEY=your-app-key

cloudstic init -store b2 -store-path my-bucket-name -encryption-password "passphrase"
cloudstic backup -store b2 -store-path my-bucket-name -source local -source-path ~/Documents
```

Use `-store-prefix` to namespace objects within a bucket:

```bash
cloudstic init -store b2 -store-path my-bucket -store-prefix "laptop/" -encryption-password "passphrase"
```

**Environment variables:**

| Variable | Description |
|----------|-------------|
| `B2_KEY_ID` | Backblaze B2 application key ID |
| `B2_APP_KEY` | Backblaze B2 application key |

---

## Encryption

Encryption is **required by default**. All backup data is encrypted with AES-256-GCM before being written to storage.

### How it works

1. A random **master key** is generated during `cloudstic init`
2. The master key is wrapped into one or more **key slots**, each protected by a different credential
3. Every object written to the repository is encrypted with the master key
4. Key slot metadata is stored unencrypted (under a `keys/` prefix) so you can unlock the repo

### Key slot types

| Slot type | Credential | Use case |
|-----------|-----------|----------|
| `password` | `--encryption-password` | Day-to-day personal use |
| `platform` | `--encryption-key` | Automation, CI/CD, platform integration |
| `recovery` | `--recovery-key` | Emergency access when password is lost |

### Recommended setup

```bash
# Initialize with password + recovery key
cloudstic init -encryption-password "strong passphrase" -recovery

# Write down the 24-word recovery phrase and store it safely!
```

### Using a recovery key

If you lose your password, provide the 24-word seed phrase:

```bash
cloudstic list -recovery-key "word1 word2 word3 ... word24"
cloudstic restore -recovery-key "word1 word2 ... word24"
```

### Adding a recovery key later

```bash
cloudstic add-recovery-key -encryption-password "my passphrase"
```

---

## Retention Policies

Use `forget` with retention flags to automatically expire old snapshots while keeping a defined history.

### Example: keep 7 daily + 4 weekly + 12 monthly

```bash
cloudstic forget -keep-daily 7 -keep-weekly 4 -keep-monthly 12 -prune
```

### How it works

- Snapshots are grouped by source, account, and path (configurable with `-group-by`)
- Within each group, the retention policy decides which to keep
- A snapshot matching multiple policies (e.g. both "daily" and "weekly") is kept with all reasons shown
- Snapshots not matching any keep rule are removed
- Add `-prune` to reclaim storage immediately, or run `cloudstic prune` separately

### Preview before acting

Always safe to preview first:

```bash
cloudstic forget -keep-daily 7 -keep-monthly 12 -dry-run
```

---

## Environment Variables

| Variable | Flag equivalent | Description |
|----------|----------------|-------------|
| `CLOUDSTIC_STORE` | `-store` | Storage backend: `local`, `b2` |
| `CLOUDSTIC_STORE_PATH` | `-store-path` | Local path or B2 bucket name |
| `CLOUDSTIC_STORE_PREFIX` | `-store-prefix` | Key prefix for B2 objects |
| `CLOUDSTIC_SOURCE` | `-source` | Source type: `local`, `gdrive`, `gdrive-changes`, `onedrive`, `onedrive-changes` |
| `CLOUDSTIC_SOURCE_PATH` | `-source-path` | Local source directory path (when `source=local`) |
| `CLOUDSTIC_DRIVE_ID` | `-drive-id` | Shared drive ID for Google Drive |
| `CLOUDSTIC_ROOT_FOLDER` | `-root-folder` | Root folder ID for Google Drive |
| `CLOUDSTIC_DATABASE_URL` | `-database-url` | PostgreSQL URL (hybrid store) |
| `CLOUDSTIC_TENANT_ID` | `-tenant-id` | Tenant ID (hybrid store) |
| `CLOUDSTIC_ENCRYPTION_KEY` | `-encryption-key` | Platform key (hex) |
| `CLOUDSTIC_ENCRYPTION_PASSWORD` | `-encryption-password` | Encryption password |
| `CLOUDSTIC_RECOVERY_KEY` | `-recovery-key` | Recovery seed phrase |
| `CLOUDSTIC_CONFIG_DIR` | — | Override config directory path |
| `GOOGLE_APPLICATION_CREDENTIALS` | — | Path to your own Google OAuth credentials file (optional, overrides built-in) |
| `GOOGLE_TOKEN_FILE` | — | Override Google OAuth token path |
| `ONEDRIVE_CLIENT_ID` | — | Microsoft app client ID (optional, overrides built-in) |
| `ONEDRIVE_TOKEN_FILE` | — | Override OneDrive token path |
| `B2_KEY_ID` | — | Backblaze B2 key ID |
| `B2_APP_KEY` | — | Backblaze B2 application key |
