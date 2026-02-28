# Show HN Post — Cloudstic

## Link Strategy

> [!IMPORTANT]
> **Link to the GitHub repo** (`https://github.com/Cloudstic/cli`), not the landing page.
>
> HN readers want to see code, docs, and implementation details first. The repo has a clean README, thorough technical docs (spec, encryption design, storage model), and is MIT-licensed — all things that build credibility on HN. The cloud service at cloudstic.com is a secondary funnel; linking to it as a "Show HN" feels like a product launch, which HN moderates more strictly. The landing page also has a waitlist CTA, which can feel marketing-heavy.
>
> **Mention the cloud option naturally** in the post body — people who want hosted will find it.

---

## Title

```
Show HN: Cloudstic – Encrypted, deduplicated backups for Google Drive and OneDrive
```

> [!TIP]
> HN titles are capped at ~80 chars. This is 78. It front-loads the three things that make people click: **encrypted**, **deduplicated**, and the **Google Drive/OneDrive** hook (a pain point many HN readers share).

---

## Post Body

> [!NOTE]
> HN "Show HN" text boxes support plain text only — no markdown, no images. The body below is formatted accordingly. Keep it tight; long Show HN descriptions lose readers.

```
Hi HN,

I built Cloudstic, an open-source CLI that creates encrypted, point-in-time
backups of your Google Drive, OneDrive, and local files.

The problem: Google Drive and OneDrive are sync tools, not backup tools. If
you delete a file, it's gone everywhere. If you overwrite something, sync
propagates the mistake. There's no way to say "restore my entire Drive as
it looked on January 15th."

Cloudstic creates immutable snapshots you can always go back to. I'm a big
fan of restic (its content-addressed design inspired much of Cloudstic), but
restic only backs up from local filesystems, and its tree structure mirrors
the directory hierarchy, which doesn't map well to cloud storage where files
have IDs, multiple parents, and no single canonical path. Cloudstic uses a
Merkle-HAMT keyed by file ID instead, which handles these semantics natively.

A few things that make it different:

• Encryption by default. AES-256-GCM, with password or BIP39 recovery
  phrase. Your storage provider (local, S3, B2) sees only encrypted blobs.

• Cross-source deduplication. Files are split using FastCDC (content-defined
  chunking) and stored in a content-addressed format. If the same file
  exists on your Drive and your laptop, it's stored once.

• Merkle-HAMT for structural sharing. Each snapshot is a complete
  checkpoint (no delta replay), but unchanged subtrees share nodes
  with the previous snapshot. A 100k-file backup where 10 files changed
  uploads ~10 new HAMT nodes, not 100k.

• Crash-safe by design. All objects are immutable and append-only.
  An interrupted backup leaves zero corruption. Prune cleans up orphans.

• Fast incremental backups. Uses Google Drive's Changes API and
  OneDrive's Delta API. Only fetches what changed since the last snapshot.

It's a single Go binary, no daemon, MIT licensed. Store to local disk,
S3/R2/MinIO, Backblaze B2, or SFTP, with more backends on the way.

Quick start:

  brew install cloudstic/tap/cloudstic
  cloudstic init -encryption-password "my passphrase"
  cloudstic backup -source gdrive-changes -encryption-password "my passphrase"
  cloudstic list
  cloudstic restore

I'm also building a managed cloud version (https://cloudstic.com), currently
in early access, that will handle scheduling and retention if you don't want
to run the CLI yourself. Same engine, zero ops.

Would love feedback on the architecture, crypto design, or anything else.
The encryption design doc and storage spec are in the repo under docs/.
```

---

## Estimated character count: ~1,600 (well within HN's ~2,000 limit)

---

## Checklist Before Posting

- [ ] Post between **9–11 AM ET** on a Tuesday or Wednesday (best HN windowing)
- [ ] Upvote the post yourself once (obviously)
- [ ] Have a few concrete answers ready for likely questions:
  - *"How does this compare to restic?"* → Restic doesn't natively source from Google Drive/OneDrive APIs. You'd need rclone as a FUSE mount. Cloudstic talks to the APIs directly and uses Change tokens for incremental.
  - *"Why not just rclone?"* → rclone is a sync/transfer tool. No snapshots, no dedup, no encryption-at-rest by default, no point-in-time restore.
  - *"Why a new HAMT implementation?"* → Needed structural sharing across snapshots on flat object stores (S3, B2) that don't support symlinks or hardlinks. Git's packfile approach doesn't work well on cloud object storage.
  - *"What about Borg?"* → Borg is excellent for local/SSH backups but has no native cloud source integration and doesn't run on Windows.
- [ ] Reply to every comment within the first 2 hours — this is critical for HN traction
- [ ] Don't ask people to star the repo; HN moderators flag that
