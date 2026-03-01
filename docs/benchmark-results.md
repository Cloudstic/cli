# Benchmark Results

Dataset: 1.0 GB (500MB random, 500MB highly compressible zero data, 10,000 small files)

## Local File System

| Metric | Cloudstic | Restic | Borg |
| :--- | :--- | :--- | :--- |
| **Initial Backup Time / Peak Mem** | 1.23s / 347 MB | 1.82s / 347 MB | 2.49s / 173 MB |
| **Incremental (No Changes)** | 0.90s / 175 MB | 1.13s / 101 MB | 0.81s / 81 MB |
| **Incremental (1 File Changed)** | 0.86s / 173 MB | 1.14s / 116 MB | 0.79s / 82 MB |
| **Final Repository Size** | 508 MB | 503 MB | 506 MB |

## AWS S3 (us-east-1)

| Metric | Cloudstic | Restic |
| :--- | :--- | :--- |
| **Initial Backup Time / Peak Mem** | 17.27s / 542 MB | 17.22s / 503 MB |
| **Incremental (No Changes)** | 5.33s / 181 MB | 2.55s / 97 MB |
| **Incremental (1 File Changed)** | 4.99s / 179 MB | 3.25s / 104 MB |

> **Note on architecture differences:** Cloudstic defaults to a hybrid `MicroPackStore` approach. It intelligently bundles small metadata objects (filemeta, nodes) into up to tightly-packed 8MB chunks to minimize S3 `PUT` requests, while passing all large files through as native encrypted objects. This yields the best of both worlds: lightning-fast S3 API performance comparable to packfile-based tools, while preserving native S3 lifecycle rules and fine-grained partial downloads for large media files.

