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
  - [OneDrive](#onedrive)
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
cloudstic restore -encryption-password "my secret passphrase" -target ./restored
```

## Installation

Build from source (requires Go 1.24+):

```bash
cd cli
go build -o cloudstic ./cmd/cloudstic
```

Move the binary to a directory in your `$PATH`:

```bash
mv cloudstic /usr/local/bin/
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
cloudstic restore -target ./restored
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
| `-source` | `gdrive` | Source type: `local`, `gdrive`, `gdrive-changes`, `onedrive` |
| `-source-path` | `.` | Path to local source directory (when `source=local`) |
| `-drive-id` | | Shared drive ID for Google Drive (omit for My Drive) |
| `-root-folder` | | Root folder ID for Google Drive (defaults to entire drive) |
| `-tag` | | Tag to apply to the snapshot (repeatable) |
| `-verbose` | `false` | Show detailed file-level output |

The `gdrive-changes` source type uses the Google Drive Changes API for faster incremental backups after the first full backup.

---

### restore

Restore files from a snapshot.

```bash
# Restore the latest snapshot
cloudstic restore -target ./restored

# Restore a specific snapshot
cloudstic restore -target ./restored -snapshot <snapshot-hash>

# Restore using a snapshot hash prefix
cloudstic restore -target ./restored -snapshot abc123
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-target` | `./restore_out` | Directory to restore files into |
| `-snapshot` | latest | Snapshot hash or prefix (defaults to the most recent snapshot) |

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

### Local Directory

Back up files from a local filesystem path.

```bash
cloudstic backup -source local -source-path /path/to/directory
```

No additional environment variables or authentication required.

### Google Drive

Back up files from Google Drive. Requires a Google OAuth credentials file.

**Setup:**

1. Set `GOOGLE_APPLICATION_CREDENTIALS` to the path of your Google OAuth credentials JSON file
2. On first run, you'll be prompted to open a URL in your browser and paste an authorization code
3. The OAuth token is cached in the [config directory](#config-directory) as `google_token.json`

```bash
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/credentials.json

# Back up entire My Drive
cloudstic backup -source gdrive

# Back up a shared drive
cloudstic backup -source gdrive -drive-id <shared-drive-id>

# Back up a specific folder
cloudstic backup -source gdrive -root-folder <folder-id>

# Incremental backup using Changes API (faster after first full backup)
cloudstic backup -source gdrive-changes
```

**Environment variables:**

| Variable | Description |
|----------|-------------|
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to Google OAuth credentials JSON file |
| `GOOGLE_TOKEN_FILE` | Override token cache path (default: `<config-dir>/google_token.json`) |

### OneDrive

Back up files from Microsoft OneDrive. Requires a Microsoft OAuth app registration.

**Setup:**

1. Register an app in Azure Portal and obtain client ID and secret
2. Set the environment variables below
3. On first run, you'll be prompted to complete the OAuth flow

```bash
export ONEDRIVE_CLIENT_ID=your-client-id
export ONEDRIVE_CLIENT_SECRET=your-client-secret

cloudstic backup -source onedrive
```

**Environment variables:**

| Variable | Description |
|----------|-------------|
| `ONEDRIVE_CLIENT_ID` | Microsoft app client ID |
| `ONEDRIVE_CLIENT_SECRET` | Microsoft app client secret |
| `ONEDRIVE_TOKEN_FILE` | Override token cache path (default: `<config-dir>/onedrive_token.json`) |

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
cloudstic restore -recovery-key "word1 word2 ... word24" -target ./restored
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
| `CLOUDSTIC_DATABASE_URL` | `-database-url` | PostgreSQL URL (hybrid store) |
| `CLOUDSTIC_TENANT_ID` | `-tenant-id` | Tenant ID (hybrid store) |
| `CLOUDSTIC_ENCRYPTION_KEY` | `-encryption-key` | Platform key (hex) |
| `CLOUDSTIC_ENCRYPTION_PASSWORD` | `-encryption-password` | Encryption password |
| `CLOUDSTIC_RECOVERY_KEY` | `-recovery-key` | Recovery seed phrase |
| `CLOUDSTIC_CONFIG_DIR` | — | Override config directory path |
| `GOOGLE_APPLICATION_CREDENTIALS` | — | Path to Google OAuth credentials file |
| `GOOGLE_TOKEN_FILE` | — | Override Google OAuth token path |
| `ONEDRIVE_CLIENT_ID` | — | Microsoft app client ID |
| `ONEDRIVE_CLIENT_SECRET` | — | Microsoft app client secret |
| `ONEDRIVE_TOKEN_FILE` | — | Override OneDrive token path |
| `B2_KEY_ID` | — | Backblaze B2 key ID |
| `B2_APP_KEY` | — | Backblaze B2 application key |
