# Cloudstic CLI User Guide

Cloudstic is a content-addressable backup tool that creates encrypted, deduplicated snapshots of your files — from local directories, SFTP servers, Google Drive, or OneDrive — and stores them locally, on Amazon S3, Backblaze B2, or a remote SFTP server.

> Full documentation (with guides, examples, and API reference) is available at **[docs.cloudstic.com](https://docs.cloudstic.com)**.

## Table of Contents

- [Quick Start](#quick-start)
- [Installation](#installation)
- [Concepts](#concepts)
- [Configuration](#configuration)
- [Commands](#commands)
  - [init](#init)
  - [backup](#backup)
  - [auth](#auth)
  - [profile](#profile)
  - [store](#store)
  - [restore](#restore)
  - [list](#list)
  - [ls](#ls)
  - [diff](#diff)
  - [forget](#forget)
  - [prune](#prune)
  - [break-lock](#break-lock)
  - [check](#check)
  - [key list](#key-list)
  - [key add-recovery](#key-add-recovery)
  - [key passwd](#key-passwd)
  - [cat](#cat)
  - [completion](#completion)
- [Shell Completions](#shell-completions)
- [Sources](#sources)
  - [Local Directory](#local-directory)
  - [SFTP](#sftp-source)
  - [Google Drive](#google-drive)
  - [Google Drive (Incremental)](#google-drive-incremental)
  - [OneDrive](#onedrive)
  - [OneDrive (Incremental)](#onedrive-incremental)
- [Storage Backends](#storage-backends)
  - [Local](#local-storage)
  - [Amazon S3](#amazon-s3)
  - [Backblaze B2](#backblaze-b2)
  - [SFTP](#sftp-storage)
- [Encryption](#encryption)
- [Retention Policies](#retention-policies)
- [Environment Variables](#environment-variables)

---

## Quick Start

```bash
# 1. Initialize an encrypted repository (prompts for password interactively)
cloudstic init

# 2. Back up a local directory (prompts for password)
cloudstic backup -source local:~/Documents

# 3. List snapshots
cloudstic list

# 4. Restore the latest snapshot
cloudstic restore
```

When running in a terminal, Cloudstic prompts for the repository password if no credential is provided via flags or environment variables. For non-interactive use (scripts, cron), pass the password explicitly:

```bash
cloudstic init -password "my secret passphrase"
cloudstic backup -source local:~/Documents -password "my secret passphrase"
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

### Curl installer (macOS / Linux)

```bash
# Install latest
curl -fsSL https://raw.githubusercontent.com/Cloudstic/cli/main/scripts/install.sh | sh

# Install a specific version
curl -fsSL https://raw.githubusercontent.com/Cloudstic/cli/main/scripts/install.sh | sh -s -- --version v1.2.3

# Install to a user-writable directory
curl -fsSL https://raw.githubusercontent.com/Cloudstic/cli/main/scripts/install.sh | sh -s -- --install-dir "$HOME/.local/bin"

# Install with shell completion (auto-detect shell)
curl -fsSL https://raw.githubusercontent.com/Cloudstic/cli/main/scripts/install.sh | sh -s -- --with-completion

# Install completion for a specific shell
curl -fsSL https://raw.githubusercontent.com/Cloudstic/cli/main/scripts/install.sh | sh -s -- --with-completion --shell zsh

# Skip checksum verification (not recommended)
curl -fsSL https://raw.githubusercontent.com/Cloudstic/cli/main/scripts/install.sh | sh -s -- --no-verify
```

The installer verifies release checksums by default. `--no-verify` is available
for constrained/debug environments but is not recommended.
Completion files are written to user directories (for example `~/.zfunc` for zsh,
`~/.config/fish/completions` for fish, and `~/.local/share/bash-completion/completions` for bash).

### Pre-built binaries

Download the latest release for your platform from the [GitHub Releases](https://github.com/cloudstic/cli/releases) page. Binaries are available for macOS (Intel & Apple Silicon), Linux (amd64 & arm64), and Windows.

> Prefer the curl installer above when possible; it verifies checksums automatically.

```bash
# Example: macOS Apple Silicon
VERSION=$(curl -fsSL https://api.github.com/repos/cloudstic/cli/releases/latest | awk -F '"' '/tag_name/{gsub(/^v/,"",$4); print $4; exit}')
ASSET="cloudstic_${VERSION}_darwin_arm64.tar.gz"
curl -fsSL "https://github.com/cloudstic/cli/releases/latest/download/${ASSET}" -o "${ASSET}"
curl -fsSL https://github.com/cloudstic/cli/releases/latest/download/checksums.txt -o checksums.txt
grep " ${ASSET}$" checksums.txt | shasum -a 256 -c -
tar -xzf "${ASSET}"
mv cloudstic /usr/local/bin/
```

### Install with Go

```bash
go install github.com/cloudstic/cli/cmd/cloudstic@latest
```

### Build from source

Requires Go 1.26+:

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

**Source** — Where files are read from during backup: a local directory, an SFTP server, Google Drive, or OneDrive.

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
export CLOUDSTIC_STORE=s3:my-backup-bucket
export CLOUDSTIC_PASSWORD="my secret passphrase"
export AWS_ACCESS_KEY_ID=your-access-key
export AWS_SECRET_ACCESS_KEY=your-secret-key

# Now commands are much shorter:
cloudstic backup -source local:~/Documents
cloudstic list
cloudstic restore
```

See [Environment Variables](#environment-variables) for the full list.

---

## Commands

### Global flags

These flags apply to all commands:

| Flag | Description |
|------|-------------|
| `-verbose` | Log detailed file-level operations (files scanned, written, deleted) |
| `-quiet` | Suppress progress bars (keeps final summary output) |
| `-json` | Write the command result as JSON to stdout |
| `-debug` | Log every store request (network calls, timing, sizes) |
| `-disable-packfile` | Disable bundling small objects into 8MB packs (packfile is on by default) — env: `CLOUDSTIC_DISABLE_PACKFILE=1` |

`-verbose` and `-quiet` are mutually exclusive. If both are set, `-quiet` takes precedence.

`-json` is available on the operational commands that return structured results, including `init`, `backup`, `restore`, `list`, `ls`, `diff`, `forget`, `prune`, `break-lock`, `check`, `key list`, `key add-recovery`, `key passwd`, and `cat`. When `-json` is set, Cloudstic suppresses progress output and writes a single JSON document to stdout instead of the usual human-readable summary.

### init

Initialize a new repository. Encryption is **required by default**.

```bash
# Interactive — prompts for password (recommended for personal use)
cloudstic init

# Interactive with a recovery key (strongly recommended)
cloudstic init -add-recovery-key

# Non-interactive — password provided via flag
cloudstic init -password "my secret passphrase"

# Non-interactive with a recovery key
cloudstic init -password "my secret passphrase" -add-recovery-key

# Platform key encryption (for automation)
cloudstic init -encryption-key <64-hex-chars>

# Both password and platform key (dual access)
cloudstic init -password "passphrase" -encryption-key <hex>

# Unencrypted (must be explicit — not recommended)
cloudstic init -no-encryption
```

When no encryption credential is provided and stdin is a terminal, `init` prompts for a new password with confirmation. In non-interactive environments (piped input, cron jobs), you must pass `-password`, `-encryption-key`, or `-no-encryption` explicitly.

If you are using a platform key or KMS but also want to protect the repository with a password, use `-prompt` to trigger an interactive password prompt alongside other credentials:

```bash
cloudstic init -encryption-key <hex> -prompt
```

**Flags:**

| Flag | Description |
|------|-------------|
| `-password <value>` | Password for password-based encryption (non-interactive) |
| `-prompt` | Prompt for password interactively (use alongside `-encryption-key` or `-kms-key-arn` to add a password layer) |
| `-encryption-key` | Platform key (64 hex chars = 32 bytes) |
| `-add-recovery-key` | Generate a 24-word recovery key during init |
| `-no-encryption` | Create an unencrypted repository (not recommended) |
| `-adopt-slots` | Adopt existing key slots (and add new credentials to them) |

When `-add-recovery-key` is used, a 24-word seed phrase is displayed **once**. Write it down and store it safely — it's your last resort if you lose your password.

---

### backup

Create a new snapshot from a source.

```bash
# Back up a local directory
cloudstic backup -source local:~/Documents

# Back up Google Drive (My Drive)
cloudstic backup -source gdrive

# Back up a specific Google Drive shared drive and folder
cloudstic backup -source "gdrive://Company Data/path/to/folder"

# Back up with tags
cloudstic backup -source local:~/Documents -tag daily -tag important

# Verbose output (shows individual files)
cloudstic backup -source local:~/Documents -verbose

# Dry run — see what would change without writing to the store
cloudstic backup -source local:~/Documents -dry-run
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-source` | `gdrive` | Source type: `local:<path>`, `sftp://[user@]host[:port]/<path>`, `gdrive[://<Drive Name>][/<path>]`, `gdrive-changes[://<Drive Name>][/<path>]`, `onedrive[://<Drive Name>][/<path>]`, `onedrive-changes[://<Drive Name>][/<path>]` |
| `-profile` | | Run backup using one named profile from `profiles.yaml` |
| `-all-profiles` | `false` | Run backup for all enabled profiles from `profiles.yaml` |
| `-auth-ref` | | Use one named auth entry from `profiles.yaml` for cloud source credentials |
| `-profiles-file` | `<config-dir>/profiles.yaml` | Override profile YAML location (also `CLOUDSTIC_PROFILES_FILE`) |
| `-tag` | | Tag to apply to the snapshot (repeatable) |
| `-exclude` | | Exclude pattern using gitignore syntax (repeatable) |
| `-exclude-file` | | Path to file containing exclude patterns, one per line |
| `-ignore-empty-snapshot` | `false` | Skip creating a new snapshot when the resulting tree is identical to the previous one |
| `-volume-uuid` | | Override volume UUID for local source (enables cross-machine incremental backup for portable drives) |
| `-skip-mode` | | Skip POSIX metadata collection (mode, uid, gid, btime, flags) |
| `-skip-flags` | | Skip file flags collection |
| `-skip-xattrs` | | Skip extended attribute collection |
| `-xattr-namespaces` | | Comma-separated xattr namespace prefixes to collect (e.g. `user.,com.apple.`) |
| `-dry-run` | `false` | Scan source and report changes without writing to the store |

`-profile` and `-all-profiles` are mutually exclusive.

`-auth-ref` can be used with direct `backup -source ...` runs (outside profile
mode) to reuse a cloud auth entry from `profiles.yaml`.

If you run a cloud backup without `-auth-ref`, Cloudstic automatically records a
provider default auth entry in `profiles.yaml` so it is discoverable later:

- Google sources -> `google-default`
- OneDrive sources -> `onedrive-default`

The `gdrive-changes` and `onedrive-changes` source types use their respective change/delta APIs for faster incremental backups after the first full backup.

When `-ignore-empty-snapshot` is enabled, Cloudstic still scans the source and reports stats, but it does not write a new snapshot if the resulting tree is unchanged. For changes-based cloud sources, this also means an unchanged run does not persist a fresh change token, so the next run may revisit the same empty delta window.

Cloudstic tracks source lineage using stable source identities internally (container identity + root location identity), not just display labels. For cloud sources, this uses stable drive/folder IDs so incremental continuity is preserved across folder renames or moves.

> **Locking:** `backup` acquires a **shared lock** on the repository at the start of the run (skipped for `-dry-run`). Multiple backups can run concurrently. The lock is released when the command exits. If the repository is exclusively locked by a `prune` run, `backup` will fail immediately with an error message. Use `break-lock` if a lock is stale.

#### Exclude patterns

You can exclude files and directories from the backup using gitignore-style patterns. This works with all source types — local, SFTP, Google Drive, and OneDrive. This is essential for skipping development directories that contain `.git/`, `node_modules/`, build artifacts, etc.

```bash
# Exclude specific directories and file types
cloudstic backup -source local:~/project \
  -exclude ".git/" -exclude "node_modules/" -exclude "*.tmp" -exclude "*.log"

# Works with cloud sources too
cloudstic backup -source gdrive-changes -exclude "node_modules/" -exclude "*.tmp"

# Load patterns from a file
cloudstic backup -source local:~/project -exclude-file ~/project/.backupignore

# Combine both
cloudstic backup -source local:~/project \
  -exclude "build/" -exclude-file .backupignore
```

**Supported pattern syntax:**

| Pattern | Meaning |
|---------|--------|
| `*.tmp` | Exclude all `.tmp` files in any directory |
| `.git/` | Exclude the `.git` directory (trailing `/` = directories only) |
| `node_modules/` | Exclude `node_modules` directories anywhere in the tree |
| `**/*.log` | Exclude all `.log` files at any depth |
| `build/output` | Exclude `build/output` anchored at root (patterns with `/` are anchored) |
| `!important.log` | Re-include `important.log` even if a previous pattern excluded `*.log` |
| `# comment` | Lines starting with `#` are comments (ignored) |

**Exclude file format** (`-exclude-file`):

```
# Development artifacts
.git/
node_modules/
__pycache__/

# Build output
build/
dist/
*.o

# Temporary files
*.tmp
*.swp
*~

# But keep this one
!important.tmp
```

Patterns are evaluated in order; the last matching rule wins. This allows negation (`!`) to override earlier excludes.

For cloud sources (Google Drive, OneDrive), exclude patterns are matched against the full path of each file as it appears in the drive (e.g. `Documents/Reports/draft.docx`).

> **Automatic rescan when exclude patterns change**
>
> When using incremental sources (`gdrive-changes`, `onedrive-changes`), Cloudstic stores a hash of the active exclude patterns in each snapshot. If the patterns change between runs (added, removed, or reordered), the next backup automatically performs a full rescan instead of an incremental one. This ensures the new patterns are applied comprehensively. The full rescan also captures a fresh change token, so subsequent runs resume incremental mode from that point.
>
> No manual intervention is required — just update your `-exclude` / `-exclude-file` flags and run the backup as usual.

---

### profile

Manage backup profiles stored in YAML.

Profiles are stored by default at `<config-dir>/profiles.yaml`.

#### profile list

List configured stores, auth entries, and profiles.

```bash
cloudstic profile list

# Custom file location
cloudstic profile list -profiles-file ./profiles.yaml
```

If the profiles file does not exist yet, `profile list` exits successfully with
no output.

#### profile show

Show one profile with resolved store and auth references.

```bash
cloudstic profile show work-drive

# Custom file location
cloudstic profile show -profiles-file ./profiles.yaml work-drive
```

#### profile new

Create or update one profile entry.

```bash
# Create profile and create/update referenced store entry
cloudstic profile new \
  -name google-drive \
  -source gdrive-changes \
  -store-ref home-s3 \
  -store s3:my-bucket/cloudstic

# Reuse an existing store reference (no -store needed)
cloudstic profile new \
  -name documents \
  -source local:~/Documents \
  -store-ref home-s3

# Create auth entry first, then attach it to a profile
cloudstic auth new \
  -name google-work \
  -provider google \
  -google-token-file ~/.config/cloudstic/tokens/google-work.json

cloudstic profile new \
  -name work-drive \
  -source "gdrive-changes:/Team Folder" \
  -auth-ref google-work

# Create a profile and define store encryption with secret refs
cloudstic profile new \
  -name photos \
  -source local:~/Pictures \
  -store-ref home-s3 \
  -store s3:my-bucket/cloudstic
```

**Important:** `profile new` requires explicit `-name` and `-source`.

If these required flags are omitted and you are in an interactive terminal,
Cloudstic prompts for the missing values.

It intentionally does **not** read `CLOUDSTIC_SOURCE` for these required fields,
to avoid accidentally persisting environment-specific defaults into
`profiles.yaml`.

Use `--no-prompt` to disable all interactive prompts. Missing required fields will cause an error instead.

Use `-store-ref` by itself to reference an existing store entry.
Add `-store` with `-store-ref` to create or update that store entry in the same
command.

When `profile new` creates a new store in interactive mode, it reuses the same
encryption setup flow as `store new` (including secret reference prompts).

Use `-auth-ref` to reference reusable cloud OAuth settings under top-level
`auth:` in `profiles.yaml`.

`-auth-ref` must point to an existing auth entry.

You can also use `-auth-ref` directly with `backup` when not using `-profile`:

```bash
cloudstic backup \
  -source "gdrive-changes:/Team Folder" \
  -auth-ref google-work
```

`profile new` validates that `-auth-ref` points to an existing auth entry and
that provider matches the source type.

`-auth-ref` is only valid for cloud sources.

---

### store

Manage named store entries in `profiles.yaml`. Stores define storage backend, connection credentials, and encryption settings.

#### store list

List configured stores.

```bash
cloudstic store list
```

#### store show

Show details for a named store.

```bash
cloudstic store show prod-s3
```

#### store verify

Resolve credentials for one named store and verify connectivity/unlock behavior.

```bash
cloudstic store verify prod-s3
```

`store verify` is a configuration/access check (credentials, backend connectivity,
and encrypted repo unlock). It is different from `cloudstic check`, which verifies
repository data integrity.

#### store init

Initialize a configured store by reference from `profiles.yaml`.

```bash
cloudstic store init prod-s3

# Non-interactive (skip confirmation prompt)
cloudstic store init -yes prod-s3
```

Use this when a store was created/configured earlier but repository
initialization was skipped or failed.

#### store new

Create or update a named store entry in `profiles.yaml`. Stores define storage backend, connection credentials, and encryption settings.

```bash
cloudstic store new \
  -name prod-s3 \
  -uri s3:my-bucket/backups \
  -s3-region eu-west-1
```

Store names must start with a letter or digit and contain only letters, digits, dots, hyphens, or underscores. URIs must use a supported scheme (`local`, `s3`, `b2`, `sftp`).

In interactive mode, `store new` prompts for:

- Missing required fields (name, URI)
- For existing stores (when no override flags are passed):
  - `Store URI` with current value prefilled
  - `Keep current encryption settings? [Y/n]`
- Encryption configuration (if no encryption flags provided):
  1. Password — saves a secret reference (default: `env://CLOUDSTIC_PASSWORD`)
  2. Platform key — saves a secret reference (default: `env://CLOUDSTIC_ENCRYPTION_KEY`)
  3. AWS KMS key — saves ARN and region
  4. No encryption
- Store initialization (if the store is accessible but not yet initialized)

Prefer `*_secret` fields and secret refs in `profiles.yaml`. Supported schemes:

- `env://VAR_NAME`
- `keychain://service/account` (macOS)
- `wincred://target` (Windows)
- `secret-service://collection/item` (Linux)

Use `*_secret` fields for all secret-backed profile settings.

Examples:

```yaml
stores:
  prod:
    uri: s3:my-bucket/cloudstic
    s3_access_key_secret: env://AWS_ACCESS_KEY_ID
    s3_secret_key_secret: keychain://cloudstic/prod/s3-secret-key
    password_secret: keychain://cloudstic/prod/repo-password
```

If native secret backends are unavailable (for example headless sessions without
desktop keyring/keychain access), use `env://...` references.

Use `--no-prompt` to disable all interactive prompts (for scripts/CI).

---

### auth

Manage reusable cloud OAuth entries in `profiles.yaml`.

#### auth new

Create or update an auth entry.

```bash
# Google auth entry
cloudstic auth new \
  -name google-work \
  -provider google \
  -google-token-file ~/.config/cloudstic/tokens/google-work.json

# OneDrive auth entry
cloudstic auth new \
  -name ms-personal \
  -provider onedrive \
  -onedrive-token-file ~/.config/cloudstic/tokens/ms-personal.json
```

If token storage flags are omitted, Cloudstic defaults to a managed encrypted
reference under `config-token://<provider>/<auth-name>`.

If required flags are omitted and you are in an interactive terminal,
`auth new` prompts for missing values.

#### Secret References and Secure Storage

Cloudstic uses **Secret References** to avoid storing sensitive credentials
like OAuth tokens or service account JSON in plaintext within your
`profiles.yaml`.

When creating an auth entry, you can specify a reference instead of a raw
file path:

```bash
# Store Google token in macOS Keychain (secure)
cloudstic auth new -name google-work -provider google \
  -google-token-ref keychain://cloudstic/auth/google-work

# Store Google token in an encrypted local file (managed by Cloudstic)
cloudstic auth new -name google-work -provider google \
  -google-token-ref config-token://google/google-work

# Use a raw file (e.g. for Kubernetes mounted secrets)
cloudstic auth new -name google-work -provider google \
  -google-token-ref file:///etc/secrets/google-token.json
```

**Supported Schemes:**

| Scheme | Description | Use Case |
| :--- | :--- | :--- |
| `keychain://` | macOS Keychain blob storage | Personal macOS workstations |
| `wincred://` | Windows Credential Manager secret storage | Personal Windows workstations |
| `secret-service://` | Linux Secret Service secret storage | Linux desktops with a keyring |
| `config-token://` | Encrypted local file | Headless servers, Linux desktops |
| `file://` | Raw local file | CI/CD, Kubernetes, Docker |

For `config-token://`, Cloudstic automatically encrypts the token at rest
using a key derived from your machine ID and user ID. See the
[Encryption Design](./encryption.md#auth-material-encryption) for more details.

#### auth login

Trigger OAuth login for an auth entry and save the token in its configured
token storage.

```bash
cloudstic auth login -name google-work
```

This is useful to pre-authorize before first backup.

#### auth list / auth show

```bash
cloudstic auth list
cloudstic auth show google-work
```

`auth list` exits successfully with no output when the profiles file does not
exist yet.

---

### restore

Restore a snapshot either as a ZIP archive (`-format zip`) or directly to a
directory (`-format dir`). By default, the format is inferred from `-output`.
If `-output` ends with `.zip`, Cloudstic uses ZIP format; otherwise it restores
to a directory.

In directory mode, Cloudstic creates parent directories automatically and, on
macOS and Linux, replays captured metadata on a best-effort basis (for example
mode bits, ownership, modification times, xattrs, and file flags where the
destination filesystem supports them). Existing files are skipped with warnings
instead of being overwritten, so rerunning the same restore into the same output
directory is safe.

```bash
# Restore the latest snapshot
cloudstic restore

# Restore a specific snapshot
cloudstic restore <snapshot-hash>

# Restore to a custom output path
cloudstic restore <snapshot-hash> -output ./my-backup.zip

# Restore directly to a directory
cloudstic restore <snapshot-hash> -format dir -output ./restored

# Restore a single file
cloudstic restore <snapshot-hash> -path Documents/report.pdf

# Restore a subtree (trailing slash)
cloudstic restore <snapshot-hash> -path Documents/

# Dry run — see what would be restored without writing output
cloudstic restore -dry-run
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-output` | `./restore.zip` | Output path (ZIP file for `zip`, directory for `dir`) |
| `-format` | auto | Restore format: `zip` or `dir` (auto-detected from `-output` when omitted) |
| `-path` | | Restore only the given file or subtree. Use a trailing `/` to select an entire directory (e.g. `Documents/`). Without a trailing slash, the exact file path is matched (e.g. `Documents/report.pdf`). |
| `-dry-run` | `false` | Show what would be restored without writing output |

The snapshot ID is a positional argument (defaults to latest if omitted).

> **Locking:** `restore` always acquires a **shared lock** at the start of the run (including `-dry-run`). The lock is released when the command exits.

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
| `-source` | Filter by source URI (e.g., `local:/path`, `gdrive`, `sftp://host/path`) |
| `-account` | Filter by account |
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

# Dry run — see what would be deleted without deleting
cloudstic prune -dry-run

# Verbose output — see each deleted key
cloudstic prune -verbose
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-dry-run` | `false` | Show what would be deleted without deleting |

> **Locking:** `prune` acquires an **exclusive lock** at the start of the run (skipped for `-dry-run`). While the exclusive lock is held, all `backup` and `restore` commands will fail immediately. The lock is released when `prune` exits. If `prune` crashes, the lock expires automatically after 1 minute. Use `break-lock` to remove it sooner.

---

### break-lock

Force-remove a stale repository lock left behind by a crashed or interrupted process. Only use this when you are certain no `backup`, `restore`, or `prune` is actively running against the repository.

```bash
cloudstic break-lock
```

If no lock is present, the command reports that and exits cleanly. If one or more locks are found, each is removed and its metadata is printed:

```text
Locks removed:
  Operation:  backup
  Holder:     mac-studio.local (pid 12345)
  Acquired:   2026-03-07T09:00:00Z
  Expired at: 2026-03-07T09:01:00Z
  Shared:     true
```

> **When to use `break-lock`:**
>
> - A `prune`, `backup`, or `restore` run was killed and you see "repository is locked" on the next attempt.
> - The lock TTL has already passed (locks expire automatically after 1 minute) but you don't want to wait.
> - You are certain the holder process is no longer running.
>
> Do **not** use `break-lock` if the locking operation is still in progress — removing the lock while `prune` is running can leave the repository in an inconsistent state.

---

### check

Verify the integrity of a repository by walking the full reference chain and checking that every referenced object exists, can be read, decrypts successfully, and decompresses correctly.

```bash
# Check all snapshots (structure verification)
cloudstic check

# Full byte-level verification (re-hash all chunk data)
cloudstic check -read-data

# Check a specific snapshot
cloudstic check <snapshot-hash>
cloudstic check latest

# Verbose output — log each verified object
cloudstic check -verbose
```

**What it verifies:**

1. **Reference chain** — `index/latest` → `snapshot` → HAMT nodes → `filemeta` → `content` → `chunk`
2. **Object readability** — Every referenced object exists, decrypts, and decompresses without error
3. **Data integrity** (with `-read-data`) — Re-hashes chunk data and verifies it matches the content-addressed key

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-read-data` | `false` | Re-hash all chunk data for full byte-level verification |
| `-snapshot` | (all) | Check a specific snapshot instead of all |

The snapshot can also be passed as a positional argument.

**Output:**

On success, the command prints a summary and exits with code 0:

```text
Repository check complete.
  Snapshots checked:  3
  Objects verified:   1247
  Errors found:       0

No errors found — repository is healthy.
```

If errors are found, they are listed and the command exits with code 1:

```text
Repository check complete.
  Snapshots checked:  3
  Objects verified:   1244
  Errors found:       3

  [missing] chunk/abc123...: chunk not found or unreadable: ...
  [corrupt] chunk/def456...: hash mismatch: expected def456..., got 789abc...
  [missing] content/ghi789...: content object not found or unreadable: ...
```

> **Tip:** Run `cloudstic check` periodically (e.g. via cron) to catch silent corruption early. Use `-read-data` for thorough verification at the cost of reading all data from the backend.

---

### key list

List all encryption key slots present in the repository. This lets you see which credential types are configured (password, platform, kms-platform, recovery).

```bash
cloudstic key list
```

Example output:

```text
+──────────────+─────────+──────────+
| TYPE         | LABEL   | KDF      |
+──────────────+─────────+──────────+
| password     | default | argon2id |
| recovery     | default | —        |
+──────────────+─────────+──────────+

2 key slot(s) found.
```

> **Note:** `key list` does not require the encryption key — slot metadata is stored unencrypted.

---

### key add-recovery

Generate a 24-word recovery key for an existing encrypted repository. Requires your current encryption credential to unlock the master key.

```bash
# Interactive — prompts for current password
cloudstic key add-recovery

# Non-interactive
cloudstic key add-recovery -password "my secret passphrase"

# For KMS-managed repositories
cloudstic key add-recovery -kms-key-arn arn:aws:kms:us-east-1:123:key/abc
```

The recovery key is displayed once. Write it down immediately.

> The legacy `add-recovery-key` command is still accepted but deprecated.

---

### key passwd

Change (or add) the repository password. You must provide your current credentials to unlock the master key.

```bash
# Interactive — prompts for current and new password
cloudstic key passwd

# Non-interactive
cloudstic key passwd -password "old passphrase" -new-password "new passphrase"

# Unlock with platform key, set a password
cloudstic key passwd -encryption-key <hex> -new-password "my passphrase"
```

**Flags:**

| Flag | Description |
|------|-------------|
| `-new-password` | New password (prompted interactively if not set) |

---

### cat

Display the raw JSON content of repository objects. This is useful for debugging, inspection, and understanding the internal structure of backups.

```bash
# Display repository configuration
cloudstic cat config

# Display the latest snapshot index
cloudstic cat index/latest

# Display a specific snapshot
cloudstic cat snapshot/abc123def456...

# Display multiple objects at once
cloudstic cat config index/latest

# Display a filemeta object
cloudstic cat filemeta/789abc...

# Display a HAMT node
cloudstic cat node/def456...

# Emit a machine-readable JSON result
cloudstic cat config -json

# Output raw, unformatted data for hashing
cloudstic cat -raw filemeta/789abc... | sha256sum
```

**Object key types:**

| Key pattern | Description |
|-------------|-------------|
| `config` | Repository configuration (version, encryption status, creation time) |
| `index/latest` | Pointer to the most recent snapshot |
| `index/snapshots` | Snapshot catalog (lightweight summaries for fast listing) |
| `snapshot/<hash>` | Snapshot metadata (creation time, root node, source info, tags) |
| `filemeta/<hash>` | File metadata (name, size, modification time, content hash) |
| `content/<hash>` | Content manifest (list of chunk references or inline data) |
| `node/<hash>` | HAMT tree node (internal or leaf) |
| `chunk/<hash>` | Raw file data chunk |
| `keys/<slot>` | Encryption key slot (stored unencrypted) |

**Flags:**

| Flag | Description |
|------|-------------|
| `-json` | Write a JSON array of fetched objects to stdout |
| `-raw` | Output raw, unformatted data (useful for hashing) |

The output is pretty-printed object content by default. With `-json`, Cloudstic returns a single JSON array where each item includes the object key and decoded data. Use `-raw` when you need the exact stored bytes instead of a structured JSON document.

**Examples:**

```bash
# Pretty-print repository config
cloudstic cat config

# Extract version from config using jq
cloudstic cat config --json | jq -r .version

# List all snapshots from index
cloudstic list --json | jq -r '.[] | .ref'

# Inspect a specific snapshot's metadata
cloudstic cat snapshot/abc123... | jq .

# Verify the integrity of a filemeta object
cloudstic cat -raw filemeta/abc123... | sha256sum
```

> **Note:** This command operates at the object store level and returns the raw JSON representation of repository objects. It does not reconstruct file contents — use `restore` for that.

---

### completion

Generate shell completion scripts for bash, zsh, or fish.

```bash
cloudstic completion bash
cloudstic completion zsh
cloudstic completion fish
```

See [Shell Completions](#shell-completions) below for setup instructions.

---

## Shell Completions

Cloudstic can generate tab-completion scripts for popular shells. Once set up, pressing `Tab` will complete commands, flags, and flag values (like `-store local:<path>|s3:<bucket>|b2:<bucket>|sftp://...` and `-source local:<path>|sftp://[user@]host/<path>|gdrive|...`).

### Bash

```bash
# Load for current session
source <(cloudstic completion bash)

# Load permanently (add to ~/.bashrc)
echo 'source <(cloudstic completion bash)' >> ~/.bashrc
```

> **Note:** Bash completions require the `bash-completion` package. Install it via your package manager if not already present (`brew install bash-completion` on macOS, `apt install bash-completion` on Debian/Ubuntu).

### Zsh

```zsh
# Load for current session
source <(cloudstic completion zsh)

# Load permanently (add to ~/.zshrc)
echo 'source <(cloudstic completion zsh)' >> ~/.zshrc
```

Alternatively, place the output in your `$fpath`:

```zsh
cloudstic completion zsh > "${fpath[1]}/_cloudstic"
```

> **Note:** You may need to start a new shell or run `compinit` for changes to take effect.

### Fish

```fish
# Load for current session
cloudstic completion fish | source

# Load permanently
cloudstic completion fish > ~/.config/fish/completions/cloudstic.fish
```

---

## Sources

A **source** is where Cloudstic reads files from during a backup. Each source type connects to a different storage provider and walks its file tree to detect new, changed, or deleted files.

### Source overview

| Source | `-source` flag | What it backs up | Auth |
|--------|---------------|------------------|------|
| [Local directory](#local-directory) | `local` | Files on your local filesystem | None |
| [SFTP](#sftp-source) | `sftp` | Files on a remote SFTP server | Password, SSH key, or ssh-agent |
| [Google Drive](#google-drive) | `gdrive` | Full scan of Google Drive (My Drive or Shared Drive) | Automatic (browser) |
| [Google Drive (Incremental)](#google-drive-incremental) | `gdrive-changes` | Incremental changes since last backup (recommended for Google Drive) | Automatic (browser) |
| [OneDrive](#onedrive) | `onedrive` | Full scan of Microsoft OneDrive | Automatic (browser) |
| [OneDrive (Incremental)](#onedrive-incremental) | `onedrive-changes` | Incremental changes since last backup (recommended for OneDrive) | Automatic (browser) |

All sources produce the same snapshot format. You can back up different sources into the same repository, and snapshots are tagged with source metadata so retention policies can be applied per-source.

### Local Directory

Back up files from a local filesystem path. No authentication or environment variables required. Specify the path as part of the source URI: `-source local:<path>`.

```bash
cloudstic backup -source local:/path/to/directory

# Skip common development directories
cloudstic backup -source local:~/project \
  -exclude ".git/" -exclude "node_modules/" -exclude "*.tmp"

# Use an exclude file
cloudstic backup -source local:~/project -exclude-file .backupignore
```

| Flag | Default | Description |
|------|---------|-------------|
| `-exclude` | | Exclude pattern, gitignore syntax (repeatable) |
| `-exclude-file` | | File containing exclude patterns (one per line) |
| `-volume-uuid` | | Override volume UUID (see [Portable drives](#portable-drives)) |
| `-skip-mode` | | Skip POSIX metadata collection (mode, uid, gid, btime, flags) |
| `-skip-flags` | | Skip file flags collection |
| `-skip-xattrs` | | Skip extended attribute collection |
| `-xattr-namespaces` | | Comma-separated xattr namespace prefixes to collect (e.g. `user.,com.apple.`) |

Cloudstic walks the directory recursively. Symbolic links are not followed.

**Extended file attributes:** By default, Cloudstic captures POSIX permissions (mode bits), numeric ownership (uid/gid), file creation time (btime, where supported), per-file flags, and extended attributes (xattrs). Directory restores on macOS and Linux replay this metadata on a best-effort basis where supported by the destination filesystem. Known OS-managed xattrs that are not meaningfully restorable, such as `com.apple.provenance`, are excluded automatically. To control what is captured:

```bash
# Skip all POSIX metadata (mode, uid, gid, btime, flags)
cloudstic backup -source local:/data -skip-mode

# Skip only file flags
cloudstic backup -source local:/data -skip-flags

# Skip extended attributes
cloudstic backup -source local:/data -skip-xattrs

# Collect only user.* xattrs (skip security.*, system.*, etc.)
cloudstic backup -source local:/data -xattr-namespaces "user."
```

See [Exclude patterns](#exclude-patterns) for the full pattern syntax reference.

#### Portable Drives

When backing up a portable or external drive from multiple machines, Cloudstic automatically detects the volume identity (on macOS and Linux) and uses it to find previous snapshots. This enables true incremental backups across machines — only changed files are uploaded, even when the mount point or hostname differs.

```bash
# Back up a portable drive — UUID is auto-detected
cloudstic backup -source local:/Volumes/MyDrive

# Override UUID when auto-detection fails or for custom lineage
cloudstic backup -source local:/mnt/backup -volume-uuid "A1B2C3D4-1234-5678-ABCD-EF0123456789"
```

The volume UUID can also be set via the `CLOUDSTIC_VOLUME_UUID` environment variable. When provided, the explicit UUID takes precedence over auto-detection.

File paths inside the backup are normalized to forward slashes regardless of the operating system, so a backup started on one OS can be continued incrementally on another.

Retention policies (via `forget`) automatically group all snapshots of the same volume together across machines, so a `--keep-daily 7` policy keeps 7 daily snapshots total regardless of which machine created them.

**Cross-OS compatibility:**

For modern GPT-formatted drives (the default for most drives today), Cloudstic uses the **GPT partition UUID** which is identical across all platforms — cross-OS incremental backups work automatically with no configuration needed.

For older MBR-formatted drives (some FAT32 USB sticks), the auto-detected UUID is platform-specific and will differ between operating systems. In this case, use `-volume-uuid` (or `CLOUDSTIC_VOLUME_UUID`) with a consistent UUID value of your choice.

| Platform | UUID detection | Label detection |
|----------|---------------|-----------------|
| macOS | ✓ Automatic (GPT partition UUID via `diskutil`, fallback to `getattrlist`) | ✓ Automatic |
| Linux | ✓ Automatic (GPT partition UUID via `/dev/disk/by-partuuid/`, fallback to `/dev/disk/by-uuid/`) | ✓ Automatic (`/dev/disk/by-label/`) |
| Windows | ✓ Automatic (GPT partition UUID via `DeviceIoControl`) | ✓ Automatic (`GetVolumeInformation`) |

**Directories spanning multiple drives:**

The volume UUID is determined from the backup root path only. If your backup directory contains mount points for other volumes (e.g. `/data/external` is a separate partition mounted under `/data`), those files are included in the backup but the volume identity reflects only the root path's filesystem. This is usually fine — the backup works correctly, and incremental detection still functions based on file content. However, for portable drive workflows, back up each volume separately rather than backing up a parent directory that spans multiple drives:

```bash
# Good: back up each volume independently
cloudstic backup -source local:/Volumes/MyDrive
cloudstic backup -source local:/Volumes/OtherDrive

# Avoid: backing up a parent that contains mount points from different volumes
cloudstic backup -source local:/Volumes
```

Note that symlinks to other volumes are **not followed** — only direct mount points within the tree are traversed.

### SFTP Source

Back up files from a remote SFTP server. Supports password authentication, SSH private key, and ssh-agent. Specify the server and path using a URI: `sftp://[user@]host[:port]/<path>`.

```bash
# Back up a remote directory via SFTP
cloudstic backup -source sftp://backup@myserver.com/data/documents \
  -source-sftp-key ~/.ssh/id_ed25519

# Using password auth
cloudstic backup -source sftp://backup@myserver.com/home/user/files \
  -source-sftp-password "secret"
```

| Flag | Description |
|------|-------------|
| `-source-sftp-password` | SFTP password (optional if using key auth) |
| `-source-sftp-key` | Path to SSH private key (optional if using password auth) |

If neither `-source-sftp-password` nor `-source-sftp-key` is provided, Cloudstic will fall back to your `SSH_AUTH_SOCK` agent.

SFTP backups capture file permissions (mode bits) and numeric ownership (uid/gid) via the SFTPv3 protocol. Birth time, file flags, and extended attributes are not available over SFTP.

Cloudstic walks the remote directory recursively. Mode bits and uid/gid are captured in snapshot metadata. Restore application of these fields depends on restore format support.

The `-exclude` and `-exclude-file` flags work with SFTP sources. See [Exclude patterns](#exclude-patterns) for the full pattern syntax.

### Google Drive

Full scan of a Google Drive account. On each backup, Cloudstic lists every file and folder, compares metadata against the previous snapshot, and uploads anything new or changed.

> **Note:** For routine backups, prefer [`gdrive-changes`](#google-drive-incremental) instead — it is significantly faster and makes far fewer API requests.

**When to use:** First backup of a Google Drive, or when you want a guaranteed complete rescan (e.g. after recovering from an error).

**Setup:**

No configuration is required — Cloudstic ships with built-in OAuth credentials. On first run, your default browser opens automatically for you to authorize access. The resulting token is cached in the [config directory](#config-directory).

```bash
# Back up entire My Drive
cloudstic backup -source gdrive:/

# Back up a shared drive
cloudstic backup -source "gdrive://Company Data"

# Back up only a specific folder
cloudstic backup -source "gdrive://Company Data/path/to/folder"
```

**Environment variables (optional overrides):**

| Variable | Description |
|----------|-------------|
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to your own Google OAuth credentials JSON file (overrides built-in credentials) |
| `GOOGLE_CREDENTIALS_JSON` | Inline Google credentials JSON string — OAuth client or service account (useful in CI/CD where mounting files is inconvenient) |
| `GOOGLE_TOKEN_FILE` | Override token cache path (flag: `-google-token-file`) |
| `-google-token-ref` | (Flag only) Secret reference to Google OAuth token (e.g., `config-token://google/default`) |
| `-google-credentials-ref` | (Flag only) Secret reference to service account credentials JSON |

**Using your own credentials or a service account:**

Cloudstic ships with built-in OAuth credentials, but you can use your own OAuth client or a Google service account instead.

Credentials can be provided in three ways (in priority order):

1. **Inline JSON** via flag or env var (highest priority, ideal for CI/CD):

   ```bash
   # Via environment variable
   export GOOGLE_CREDENTIALS_JSON='{"type":"service_account", ...}'
   cloudstic backup -source gdrive-changes

   # Via flag
   cloudstic backup -source gdrive-changes \
     -google-credentials-json '{"type":"service_account", ...}'
   ```

2. **Secret reference** (for profiles using secret managers):

   ```bash
   cloudstic backup -source gdrive -google-credentials-ref keychain://cloudstic/google-creds
   ```

3. **File path**:

   ```bash
   export GOOGLE_APPLICATION_CREDENTIALS=~/service-account.json
   cloudstic backup -source gdrive-changes
   ```

Both OAuth client JSON (authorized-user flow) and service-account JSON are auto-detected — Cloudstic tries the OAuth user flow first, then falls back to service-account auth.

### Google Drive (Incremental)

**This is the recommended way to back up Google Drive.** Uses the Google Drive Changes API to fetch only files that changed since the last backup, rather than listing every file on the drive. This dramatically reduces both backup duration and the number of API requests — a drive with 100,000 files but 50 daily changes only needs to process those 50 files instead of listing all 100,000.

**When to use:** All routine Google Drive backups. The first run performs a full scan automatically, so there is no need to start with `gdrive`.

**How it works:** The first run behaves like a full `gdrive` backup and records a change token. Subsequent runs fetch only the changes since that token, making backups much faster for drives with thousands of files but few daily modifications.

```bash
# First run: full scan + saves change token
cloudstic backup -source gdrive-changes

# Subsequent runs: only fetches changes since last token
cloudstic backup -source gdrive-changes
```

Uses the same authentication and flags as [Google Drive](#google-drive). No setup required — just run the command and authorize in the browser.

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
| `ONEDRIVE_TOKEN_FILE` | Override token cache path (flag: `-onedrive-token-file`) |
| `-onedrive-token-ref` | (Flag only) Secret reference to OneDrive OAuth token (e.g., `config-token://onedrive/default`) |

### OneDrive (Incremental)

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
cloudstic forget -keep-daily 7 -source local:~/Documents -prune
```

---

## Storage Backends

### Local Storage

Store backups on the local filesystem. This is the default.

```bash
# Uses default path ./backup_store
cloudstic init -password "passphrase"

# Custom path
cloudstic init -store local:/mnt/external/backups -password "passphrase"
```

### Backblaze B2

Store backups in a Backblaze B2 bucket. Requires B2 application keys.

```bash
export B2_KEY_ID=your-key-id
export B2_APP_KEY=your-app-key

cloudstic init -store b2:my-bucket-name -password "passphrase"
cloudstic backup -store b2:my-bucket-name -source local:~/Documents
```

Use a prefix to namespace objects within a bucket:

```bash
cloudstic init -store b2:my-bucket/laptop/ -password "passphrase"
```

**Environment variables:**

| Variable | Description |
|----------|-------------|
| `B2_KEY_ID` | Backblaze B2 application key ID |
| `B2_APP_KEY` | Backblaze B2 application key |

### Amazon S3

Store backups in an Amazon S3 bucket (or S3-compatible endpoints like Cloudflare R2, MinIO, or Wasabi). Requires credentials and a bucket path.

Cloudstic uses the standard AWS SDK for Go, meaning it automatically loads credentials from your environment. You can use explicit credentials (`AWS_ACCESS_KEY_ID`), or if you already have the AWS CLI configured, you can omit them and Cloudstic will seamlessly use your `~/.aws/credentials` profile.

```bash
# Using explicit environment variables
export AWS_ACCESS_KEY_ID=your-access-key
export AWS_SECRET_ACCESS_KEY=your-secret-key
cloudstic init -store s3:my-bucket-name -password "passphrase"

# Using an existing AWS CLI profile (e.g., from ~/.aws/credentials)
export AWS_PROFILE=my-profile
cloudstic backup -store s3:my-bucket-name -source local:~/Documents
```

If using an alternative S3 provider, you must specific the custom endpoint URL. Keep in mind you may also need to modify the `-s3-region` (defaults to `us-east-1`):

```bash
cloudstic init -store s3:my-bucket -s3-endpoint https://<account_id>.r2.cloudflarestorage.com -s3-region auto -password "passphrase"
```

Use a prefix to namespace objects within a bucket:

```bash
cloudstic init -store s3:my-bucket/laptop/ -password "passphrase"
```

If you rely on shared AWS config profiles, you can pin one explicitly:

```bash
cloudstic backup -store s3:my-bucket -s3-profile my-profile -source local:~/Documents
```

**Environment variables:**

| Variable | Description |
|----------|-------------|
| `AWS_ACCESS_KEY_ID` | S3 access key ID |
| `AWS_SECRET_ACCESS_KEY` | S3 secret access key |
| `AWS_PROFILE` | Shared AWS config profile name (also `CLOUDSTIC_S3_PROFILE`) |
| `CLOUDSTIC_S3_ENDPOINT` | Custom endpoint URL (for R2, MinIO, etc.) |
| `CLOUDSTIC_S3_REGION` | S3 Region |

### SFTP Storage

Store backups on a remote SFTP server. Supports password authentication, SSH private key, and ssh-agent.

```bash
# Initialize a repository on an SFTP server
cloudstic init -store sftp://backup@myserver.com/backups/cloudstic \
  -store-sftp-key ~/.ssh/id_ed25519 \
  -password "passphrase"

# Back up to the SFTP store
cloudstic backup -store sftp://backup@myserver.com/backups/cloudstic \
  -store-sftp-key ~/.ssh/id_ed25519 \
  -source local:~/Documents
```

The path component of the URI (`/backups/cloudstic` in the example above) is the remote directory where backup objects will be stored. It will be created if it doesn't exist.

**Flags:**

| Flag | Description |
|------|-------------|
| `-store-sftp-password` | SFTP password for the store (optional if using key auth) |
| `-store-sftp-key` | Path to SSH private key for the store (optional if using password auth) |

**Environment variables:**

| Variable | Description |
| :--- | :--- |
| `CLOUDSTIC_STORE_SFTP_PASSWORD` | SFTP password for the store |
| `CLOUDSTIC_STORE_SFTP_KEY` | Path to SSH private key for the store |

---

## Encryption

Encryption is **required by default**. All backup data is encrypted with AES-256-GCM before being written to storage.

### Interactive password prompt

When running in a terminal, Cloudstic prompts for the repository password **only if no other credential is provided** via flags (`-password`, `-encryption-key`, `-recovery-key`, `-kms-key-arn`) or environment variables (`CLOUDSTIC_PASSWORD`, etc.).

To explicitly request an interactive password prompt alongside a platform key or KMS key, use the `-prompt` flag:

```bash
cloudstic backup -encryption-key <hex> -prompt  # decrypt with key + password layer
```

This applies to all commands that access an encrypted repository — `backup`, `restore`, `list`, `ls`, `diff`, `check`, `cat`, `key passwd`, `key add-recovery`, and `init`.

For `init`, the prompt asks for a new password with confirmation. For all other commands, it asks for the existing repository password.

In non-interactive environments (piped input, cron, CI), you must provide credentials explicitly or the command will fail with an error.

### How it works

1. A random **master key** is generated during `cloudstic init`
2. The master key is wrapped into one or more **key slots**, each protected by a different credential
3. Every object written to the repository is encrypted with the master key
4. Key slot metadata is stored unencrypted (under a `keys/` prefix) so you can unlock the repo

### Key slot types

| Slot type | Credential | Use case |
| :--- | :--- | :--- |
| `password` | `--password` | Day-to-day personal use |
| `platform` | `--encryption-key` | Automation, CI/CD, platform integration (legacy) |
| `kms-platform` | `--kms-key-arn` | HSM-backed platform integration via AWS KMS (also supports `--kms-region` and `--kms-endpoint`) |
| `recovery` | `--recovery-key` | Emergency access when password is lost |

### Recommended setup

```bash
# Initialize with password + recovery key
cloudstic init -password "strong passphrase" -add-recovery-key

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
# Interactive — prompts for current password
cloudstic key add-recovery

# Non-interactive
cloudstic key add-recovery -password "my passphrase"

# For KMS-managed repositories
cloudstic key add-recovery -kms-key-arn arn:aws:kms:us-east-1:123:key/abc
```

### Changing your password

```bash
# Interactive — prompts for current and new password
cloudstic key passwd

# Non-interactive
cloudstic key passwd -password "old passphrase" -new-password "new passphrase"
```

---

## Retention Policies

Use `forget` with retention flags to automatically expire old snapshots while keeping a defined history.

### Example: keep 7 daily + 4 weekly + 12 monthly

```bash
cloudstic forget -keep-daily 7 -keep-weekly 4 -keep-monthly 12 -prune
```

### How it works (Retention)

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
| :--- | :--- | :--- |
| `CLOUDSTIC_STORE` | `-store` | Storage backend URI: `local:<path>`, `s3:<bucket>[/<prefix>]`, `b2:<bucket>[/<prefix>]`, `sftp://[user@]host[:port]/<path>` |
| `CLOUDSTIC_S3_ENDPOINT` | `-s3-endpoint` | S3 compatible endpoint (for MinIO, R2, etc.) |
| `CLOUDSTIC_S3_REGION` | `-s3-region` | S3 Region |
| `CLOUDSTIC_S3_PROFILE` | `-s3-profile` | AWS shared config profile for S3 auth |
| `AWS_ACCESS_KEY_ID` | `-s3-access-key` | S3 Access Key ID |
| `AWS_SECRET_ACCESS_KEY` | `-s3-secret-key` | S3 Secret Access Key |
| `CLOUDSTIC_STORE_SFTP_PASSWORD` | `-store-sftp-password` | SFTP password for the store |
| `CLOUDSTIC_STORE_SFTP_KEY` | `-store-sftp-key` | Path to SSH private key for the store |
| `CLOUDSTIC_SOURCE` | `-source` | Source URI: `local:<path>`, `sftp://[user@]host[:port]/<path>`, `gdrive[://<Drive Name>][/<path>]`, `gdrive-changes[://<Drive Name>][/<path>]`, `onedrive[://<Drive Name>][/<path>]`, `onedrive-changes[://<Drive Name>][/<path>]` |
| `CLOUDSTIC_SOURCE_SFTP_PASSWORD` | `-source-sftp-password` | SFTP password for the source |
| `CLOUDSTIC_SOURCE_SFTP_KEY` | `-source-sftp-key` | Path to SSH private key for the source |
| `CLOUDSTIC_ENCRYPTION_KEY` | `-encryption-key` | Platform key (hex) |
| `CLOUDSTIC_PASSWORD` | `-password` | Encryption password |
| `CLOUDSTIC_RECOVERY_KEY` | `-recovery-key` | Recovery seed phrase |
| `CLOUDSTIC_KMS_KEY_ARN` | `-kms-key-arn` | AWS KMS key ARN for kms-platform slots |
| `CLOUDSTIC_KMS_REGION` | `-kms-region` | AWS KMS region |
| `CLOUDSTIC_KMS_ENDPOINT` | `-kms-endpoint` | Custom AWS KMS endpoint URL |
| `CLOUDSTIC_PROFILES_FILE` | `-profiles-file` | Path to profiles YAML file |
| `CLOUDSTIC_CONFIG_DIR` | — | Override config directory path |
| `GOOGLE_APPLICATION_CREDENTIALS` | `-google-credentials` | Path to your own Google OAuth credentials file (optional, overrides built-in) |
| `GOOGLE_CREDENTIALS_JSON` | `-google-credentials-json` | Inline Google credentials JSON (OAuth client or service account) |
| `GOOGLE_TOKEN_FILE` | `-google-token-file` | Override Google OAuth token path |
| `ONEDRIVE_CLIENT_ID` | — | Microsoft app client ID (optional, overrides built-in) |
| `ONEDRIVE_TOKEN_FILE` | — | Override OneDrive token path |
| `B2_KEY_ID` | — | Backblaze B2 key ID |
| `B2_APP_KEY` | — | Backblaze B2 application key |
