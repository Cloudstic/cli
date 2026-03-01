# Benchmark Results

Dataset: 1.0 GB (500MB random, 500MB highly compressible zero data, 10,000 small files)
Target Type: Local File System (`local`)

| Metric | Cloudstic | Restic | Borg |
| :--- | :--- | :--- | :--- |
| **Initial Backup Time / Peak Mem** | 5.83s / 319.32 MB | 1.82s / 346.81 MB | 2.49s / 172.65 MB |
| **Incremental (No Changes)** | 0.85s / 165.78 MB | 1.13s / 100.85 MB | 0.81s / 80.60 MB |
| **Incremental (1 File Changed)** | 0.93s / 180.42 MB | 1.14s / 115.73 MB | 0.79s / 81.54 MB |
| **Final Repository Size** | 585 MB | 503 MB | 506 MB |

Note: Cloudstic currently consumes slightly more actual disk space for repositories compared to Restic and Borg (roughly 80MB on the 10,000 files benchmark). This is an architectural side-effect of its "1-to-1 object mapping" design that intentionally avoids Packfiles. In packfile-based systems, small files and their metadata are tightly batched and compressed together into monolithic blobs. In Cloudstic, every file gets an individual `filemeta` entry encrypted separately. When storing 10,000 small files, this overhead accumulates (about ~39MB for `filemeta` objects and their encryption padding). While resulting in slightly higher size, this guarantees pristine file-level object deduplication and drastically simplified remote synching capabilities natively on S3.
