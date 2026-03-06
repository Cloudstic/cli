# Benchmark Results

## Running the Benchmark

```bash
# Usage: ./scripts/benchmark/run.sh [SOURCE] [STORE] [TOOL] [--debug]
#   SOURCE: local (default) or gdrive
#   STORE:  local (default) or s3
#   TOOL:   cloudstic, restic, borg, duplicacy, or all (default)

# Local source -> local store (default)
./scripts/benchmark/run.sh
./scripts/benchmark/run.sh local local all
./scripts/benchmark/run.sh local local cloudstic

# Local source -> S3 store
./scripts/benchmark/run.sh local s3 all

# Google Drive source -> local store
./scripts/benchmark/run.sh gdrive local all

# Google Drive source -> S3 store
./scripts/benchmark/run.sh gdrive s3 cloudstic

# Enable debug logging for Cloudstic
./scripts/benchmark/run.sh local local cloudstic --debug
```

Requirements:

- Go toolchain (to build Cloudstic)
- `restic`, `borg`, and/or `duplicacy` installed for their respective benchmarks (skipped if not found)
- AWS credentials for `s3` store
- For `gdrive` source: Cloudstic's Google token at `~/.config/cloudstic/google_token.json`, plus `rclone` configured with a `gdrive` remote for non-cloudstic tools (configurable via `RCLONE_REMOTE` env var)
- ~2 GB free disk space for the test dataset and repository copies

## Results

Dataset: ~1.05 GB (500MB random, 500MB compressible zeros, 50 x 1MB docs, 100 x ~1KB config files, 152 files total)

## Local File System

_Format: time / peak RAM / +repo written_

| Metric | Cloudstic | Restic | Borg | Duplicacy |
| :--- | :--- | :--- | :--- | :--- |
| **Initial Backup** | 0.61s / 259 MB / +551 MB | 1.80s / 314 MB / +550 MB | 1.69s / 139 MB / +555 MB | 3.44s / 284 MB / +553 MB |
| **Incremental (No Changes)** | 0.05s / 96 MB / +4 KB | 0.77s / 72 MB / +12 KB | 0.67s / 72 MB / +16 KB | 0.04s / 44 MB / +4 KB |
| **Incremental (1 File Changed)** | 0.05s / 96 MB / +8 KB | 0.78s / 78 MB / +36 KB | 0.67s / 72 MB / +24 KB | 0.24s / 79 MB / +36 KB |
| **Add 200MB New Data** | 0.14s / 152 MB / +200 MB | 1.07s / 283 MB / +200 MB | 0.57s / 136 MB / +200 MB | 0.93s / 195 MB / +201 MB |
| **Deduplicated Backup** | 0.60s / 235 MB / +88 KB | 1.57s / 104 MB / +36 KB | 1.24s / 81 MB / +56 KB | 3.31s / 130 MB / +1.8 MB |
| **Final Repository Size** | 752 MB | 750 MB | 755 MB | 755 MB |

## AWS S3 (us-east-1)

_Format: time / peak RAM / +repo written_

| Metric | Cloudstic | Restic |
| :--- | :--- | :--- |
| **Initial Backup** | 17.46s / 318 MB / +550 MB | 16.29s / 489 MB / +550 MB |
| **Incremental (No Changes)** | 3.76s / 108 MB / +0 | 2.52s / 76 MB / +2 KB |
| **Incremental (1 File Changed)** | 4.10s / 108 MB / +5 KB | 2.59s / 82 MB / +22 KB |
| **Add 200MB New Data** | 8.98s / 409 MB / +200 MB | 7.45s / 458 MB / +200 MB |
| **Deduplicated Backup** | 5.14s / 360 MB / +84 KB | 3.36s / 104 MB / +25 KB |
| **Final Repository Size** | 750.4 MB | 750.3 MB |

> **Note on architecture differences:** Cloudstic defaults to a hybrid `MicroPackStore` approach. It intelligently bundles small metadata objects (filemeta, nodes) into up to tightly-packed 8MB chunks to minimize S3 `PUT` requests, while passing all large files through as native encrypted objects. This yields the best of both worlds: lightning-fast S3 API performance comparable to packfile-based tools, while preserving native S3 lifecycle rules and fine-grained partial downloads for large media files.

## Google Drive -> Local Store

Dataset: ~40 MB (personal Google Drive, 152 files). Smaller real-world dataset compared to the synthetic local benchmark.

_Format: time / peak RAM / +repo written_

| Metric | Cloudstic | Restic | Borg |
| :--- | :--- | :--- | :--- |
| **Initial Backup** | 6.08s / 127 MB / +39 MB | 11.14s / 201 MB / +39 MB | 15.06s / 113 MB / +40 MB |
| **Incremental (No Changes)** | 0.56s / 95 MB / +4 KB | 14.70s / 82 MB / +16 KB | 25.49s / 72 MB / +16 KB |
| **Final Repository Size** | 39 MB | 39 MB | 41 MB |

**Duplicacy:** skipped. Its init process writes a `.duplicacy` metadata directory into the source directory, requiring write access to the data being backed up. This makes it incompatible with any read-only source (FUSE mounts, network shares, mounted drives), which is an unusual design choice for a backup tool.

> **Methodology:** Each benchmark step remounts rclone with a fresh, empty VFS cache (no carry-over between steps). This reflects a cold-start environment with no local copy of the source data, which is Cloudstic's normal operating mode: it uses the Google Drive API natively and needs no local state. Tools with a persistent rclone cache would be faster on incremental steps, but at the cost of local storage and state.

> **macOS setup:** Running rclone FUSE mounts on macOS required installing macFUSE and booting into Recovery Mode to disable SIP before the kernel extension could load. Cloudstic needs no FUSE, no rclone, and no system configuration - just a Google OAuth token.
