# Benchmark Results

Dataset: 1.0 GB (500MB random, 500MB highly compressible zero data, 10,000 small files)

## Local File System

| Metric | Cloudstic | Restic | Borg |
| :--- | :--- | :--- | :--- |
| **Initial Backup Time / Peak Mem** | 5.83s / 319 MB | 1.82s / 347 MB | 2.49s / 173 MB |
| **Incremental (No Changes)** | 0.85s / 166 MB | 1.13s / 101 MB | 0.81s / 81 MB |
| **Incremental (1 File Changed)** | 0.93s / 180 MB | 1.14s / 116 MB | 0.79s / 82 MB |
| **Final Repository Size** | 585 MB | 503 MB | 506 MB |

## AWS S3 (us-east-1)

| Metric | Cloudstic | Restic |
| :--- | :--- | :--- |
| **Initial Backup Time / Peak Mem** | 32.47s / 752 MB | 17.22s / 503 MB |
| **Incremental (No Changes)** | 9.23s / 186 MB | 2.55s / 97 MB |
| **Incremental (1 File Changed)** | 10.69s / 204 MB | 3.25s / 104 MB |

> **Note on performance differences:** Cloudstic uses a "1-to-1 object mapping" design that intentionally avoids packfiles. Every file gets an individual encrypted object. On local storage, this results in a modest overhead (~3x on initial backup) due to filesystem metadata operations. On S3, the gap narrows to ~2x on initial backup thanks to 128-concurrent uploads, but Cloudstic still issues ~20,000 individual PUT requests vs Restic's ~30 large packfile uploads. This design enables native S3-level object deduplication, simplified syncing, and file-level granularity that packfile-based tools cannot offer.

