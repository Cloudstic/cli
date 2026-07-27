# RFC 0019: Repository File Search

- **Status:** Implemented
- **Date:** 2026-07-26
- **Affects:** `cmd/cloudstic`, `client.go`, `internal/engine`, `internal/hamt`, `internal/core`, docs
- **Related:** [RFC 0001](./0001-hamt-evolution.md), [RFC 0002](./0002-affinity-model.md), [RFC 0009](./0009-unified-source-identity.md), [RFC 0015](./0015-filemeta-path-normalization.md)

## Abstract

This RFC proposes a `find` command for locating a file inside a Cloudstic
repository without knowing which snapshot holds it.

Today a user who wants to recover one file must guess a snapshot, run `ls`
against it, and repeat until the file appears. That is a linear scan performed
by a human. `find` turns it into one query: name it, and get back every version
of it Cloudstic holds, with the snapshots each version lives in and the exact
`restore` invocation that would bring it back.

The interesting part of this feature is not the matching — it is the result
model. A repository holds the same file many times over: repeated unchanged
across daily snapshots, present in several copies within one snapshot, reachable
by more than one path, and renamed while remaining the same file. A naive
implementation returns one row per (snapshot × path) pair and buries the answer
under thousands of duplicates. Most of this document is about not doing that.

## Context

Cloudstic can already enumerate what a repository holds, but only one snapshot
at a time:

- `list` enumerates snapshots from the `index/snapshots` catalog.
- `ls <snapshot>` walks one snapshot's HAMT and renders its whole file tree
  (`internal/engine/ls_snapshot.go`).
- `restore -path <p>` restores a known path out of a known snapshot.
- `diff <a> <b>` structurally compares two snapshot roots.

There is no way to ask a question that spans snapshots. Every existing read
command takes a snapshot as input; `find` is the first that takes one as output.

Three properties of the storage model shape the design:

- **Snapshots share structure.** The HAMT is persistent: an unchanged subtree
  keeps its node ref across snapshots, and an unchanged file keeps its
  `filemeta/<hash>` ref. Scanning N snapshots is therefore nowhere near N times
  the cost of scanning one — if the implementation exploits sharing rather than
  re-walking.
- **Paths are not stored.** Per RFC 0015, `FileMeta.Paths` is no longer
  persisted. A path exists only as the reconstruction of a `Parents` chain plus
  `Name`. Any path-shaped query therefore costs more than a name-shaped one, and
  the design has to say where that cost lands.
- **`Parents` holds raw source FileIDs**, not `filemeta/` refs — despite the
  stale comment on `core.FileMeta.Parents`, which `backup_scan.go:44` and
  `restore.go:704` both contradict. Resolving a parent means a HAMT lookup by
  key, and the HAMT's `LookupByKey` is O(N) because the routing key
  (`AffinityKey(parentID, fileID)`, RFC 0002) cannot be reconstructed from the
  key alone. Path reconstruction has to be designed around this, not assumed
  free.

## Goals

- Answer "where is this file?" across a repository in one command.
- Support querying by name, path pattern, content hash, source file ID, and
  simple metadata predicates.
- Return a result model that collapses the four kinds of duplication
  (across snapshots, across paths, across parents, across renames) into
  something a human can read and a script can consume.
- Make searching every snapshot the sensible default, by making it cost close to
  searching one.
- Add no new on-disk structure, so the feature carries no repository
  compatibility risk.
- Expose the feature through both the CLI and the public `Client` API.

## Non-goals

- **No full-text content search.** Matching inside file contents would mean
  fetching, decrypting, and decompressing every chunk in the repository. That is
  a different feature with a different cost model; `find` matches metadata only.
- **No repository-side search index.** See "Alternatives considered".
- No server-side or backend push-down (no S3 Select, no SQL over `HybridStore`).
- No fuzzy or ranked matching. Matches are exact predicates, not scores.
- No restore-from-find. `find` prints the command; it does not run it.
- No change to what a backup writes.

## Proposal

### 1. Name the command `find`

The command is `cloudstic find`.

`find` is the right word: the operation locates entries in a tree by name and
metadata predicates, which is precisely what `find(1)` does, and the flag
vocabulary (`-name`, `-path`, `-type`, `-size`, `-newer`) can be borrowed
wholesale. It also matches user expectation set by `restic find`.

The alternatives are worse. `search` implies content search, which is an
explicit non-goal, and would make the eventual absence of `-i "TODO"` feel like
a missing feature rather than a scope boundary. `query` implies a query
language, which this is not. `locate` implies a prebuilt index, which this
deliberately does not have. `grep` would be actively misleading.

### 2. Query surface

A query is a conjunction of predicates. All given predicates must match.

**Positional pattern** — the common case:

```bash
cloudstic find "*.pdf"
cloudstic find "Documents/**/report.pdf"
```

A pattern containing no `/` matches against the entry's **basename**. A pattern
containing `/` matches against the entry's full reconstructed **path**, relative
to the source root and normalized per RFC 0015. This split is what makes the
common case cheap; see §7.

**Explicit predicates:**

| Flag | Matches |
|------|---------|
| `-name <glob>` | basename, same as a `/`-free positional |
| `-path <glob>` | full normalized path |
| `-regex <re>` | full path, RE2 |
| `-i` | case-insensitive matching for the above |
| `-id <file-id>` | raw source FileID (the HAMT key) |
| `-content-hash <sha256>` | `FileMeta.ContentHash` — "where else is this exact content?" |
| `-ref filemeta/<hash>` | one exact metadata object |
| `-type f\|d` | file or folder |
| `-size +10M`, `-size -1k` | size comparison, `find(1)` suffix syntax |
| `-newer <time>`, `-older <time>` | `Mtime`, RFC3339 or a duration like `7d` |

`-id`, `-content-hash`, and `-ref` are exact-equality predicates on values the
repository stores verbatim, so they are the cheapest queries available and the
ones worth reaching for when a user knows what they are looking for. `-id` in
particular is the "show me this file's whole history" query: FileID is stable
across renames and moves within a source, so it follows the file rather than the
path.

### 3. Snapshot scope

By default `find` searches **every snapshot in the repository**. This is the
right default only because §7 makes it affordable; if it were not, the default
would have to be `latest` and the feature would be much less useful, because the
file a user is hunting for is usually one that is *no longer* in the latest
snapshot.

Scope is narrowed with the selectors `list` and `forget` already use, so the
vocabulary is shared rather than reinvented:

```bash
cloudstic find "*.key" -snapshot 410b18a2 -snapshot 4e5d5487
cloudstic find "*.key" -source local:./Documents
cloudstic find "*.key" -tag nightly
cloudstic find "*.key" -latest 10
cloudstic find "*.key" -since 2026-01-01 -until 2026-06-30
```

`-snapshot` is repeatable and accepts `latest`. The remaining selectors filter
the snapshot catalog before the scan begins.

### 4. Result model

This is the core of the design. A repository duplicates a file along four
independent axes, and the result model has to collapse each one differently.

The unit of a result is **not** a snapshot entry. It is a **file**, identified by
its source FileID, carrying the list of **versions** of that file, each version
carrying the **snapshots** it appears in.

```go
type FindResult struct {
    Query             FindQuery
    SnapshotsSearched int
    EntriesScanned    int
    Matches           []FileMatch
    Truncated         bool
}

type FileMatch struct {
    FileID   string        // HAMT key; stable across renames within a source
    Source   *core.SourceInfo
    Type     core.FileType
    Versions []FileVersion // newest first
}

type FileVersion struct {
    Ref         string   // "filemeta/<hash>" — the immutable identity of this version
    Name        string
    Paths       []string // reconstructed; more than one only for multi-parent entries
    ContentHash string
    Size        int64
    Mtime       int64
    Mode        uint32
    Snapshots   []SnapshotRef // every snapshot containing exactly this version
    FirstSeen   string        // ISO8601, earliest containing snapshot
    LastSeen    string        // ISO8601, latest containing snapshot
}
```

Each axis of duplication resolves as follows.

**Axis 1 — the same file, unchanged, across many snapshots.** A file backed up
nightly and never edited has one `filemeta/` ref shared by all thirty snapshots.
It produces **one** `FileVersion` whose `Snapshots` has thirty entries. It does
not produce thirty results. This is not a display convenience: the refs are
literally equal, so treating them as one row is the accurate reading of what the
repository contains.

**Axis 2 — the same file, edited, across snapshots.** Each edit produces a new
`filemeta/` ref, so the file yields one `FileMatch` with several `FileVersion`
entries, newest first, each with its own snapshot range. This is exactly the
view a user recovering a file wants: "which version do I want?" is the actual
question, and version-with-date-range is the answer to it.

**Axis 3 — several copies of the same content inside one snapshot.** Two
distinct files with identical bytes (`~/a/report.pdf` and `~/backup/report.pdf`)
have different FileIDs, different names or parents, and therefore different
`filemeta/` refs — but the same `ContentHash`. They are **different files** and
are reported as **separate** `FileMatch` entries. Collapsing them would be
wrong: restoring one is not restoring the other.

The "how many copies of this content do I store?" question is real but
different, so it gets its own view rather than distorting the default one:

```bash
cloudstic find -content-hash 9f86d081... -by-content
```

`-by-content` regroups the same underlying matches by `ContentHash` instead of
by FileID, which is how a user finds duplicates. It changes grouping only, never
which entries matched.

**Axis 4 — one file reachable by several paths.** `core.FileMeta.Parents` is a
list, and for Google Drive it genuinely can hold more than one entry: a single
Drive file lives in two folders at once. Hard links on a local source raise the
same question. This is why `FileVersion.Paths` is a slice and not a string.

There is an honest limitation here to state plainly: `fileMetaPath()` in
`internal/engine/filemeta_paths.go` follows `Parents[0]` only, so the existing
codebase cannot currently produce the second path. This RFC proposes `find`
resolve **all** parents and report every path, and that `fileMetaPath` grow a
multi-path variant rather than `find` carrying a private copy of path
reconstruction. If that turns out to be more invasive than expected, the
fallback is to report `Paths[0]` and set an explicit `multi_parent` flag on the
version — surfacing the ambiguity rather than silently picking a branch. What is
*not* acceptable is emitting one path with no indication that others exist.

**Grouping key fallback.** Grouping is by FileID because FileID is the HAMT key
and therefore always present. Where a source's FileID is not stable across runs,
grouping degrades to "one match per version", which is noisier but never wrong.
`find` does not attempt to re-identify files heuristically.

### 5. Renames

Because grouping is by FileID and `Name` lives on the version, a renamed file
appears as one `FileMatch` whose versions have different names — a rename
history, for free, as a consequence of the grouping choice rather than a
feature bolted on.

One consequence worth stating: a rename means the file **matches a name query
in some snapshots and not others**. `find "old-name.txt"` matches versions
named `old-name.txt` and reports them; it does not silently pull in the
post-rename versions of the same FileID. The match set is defined by the
predicate; the grouping only decides how matches are presented. Pulling in
sibling versions would make the result set depend on grouping, which is how a
search tool becomes unpredictable. `-id` is the query for "everything about
this file regardless of name".

### 6. Matching semantics

- Patterns match against paths normalized per RFC 0015: forward slashes,
  relative to the source root, no leading `./`.
- Glob syntax is `path.Match` per segment plus `**` for "zero or more path
  segments". `path.Match` alone cannot express `**`, so a small matcher is
  needed; it should live where a future exclude-pattern implementation can share
  it rather than inside the find engine.
- `-i` lowercases both sides using simple Unicode case folding. Note this is
  *matching* case-insensitivity only — it says nothing about the case
  sensitivity of the original filesystem.
- `-regex` uses Go's RE2 against the full path. Unanchored, like `grep`.
- Multiple predicates are ANDed. There is no OR and no grouping syntax; users
  wanting a union run two queries. Adding boolean structure is how a search flag
  set turns into a query language, which is a non-goal.

### 7. Execution model

The naive implementation — for each snapshot, `Walk` the HAMT, `Get` every
`filemeta/`, evaluate — costs Σ(entries) object reads and makes searching all
snapshots untenable on a remote backend. The proposal is a delta scan that
exploits the persistence of the HAMT.

**Group snapshots by source lineage.** Diffing two snapshots of unrelated
sources is meaningless and costs two full walks. Snapshots are grouped by source
identity (the same grouping `forget` policies use, RFC 0009) and ordered by
`Seq` within each group.

**Within a lineage, walk once and diff thereafter:**

1. Full `Tree.Walk` of the oldest selected root. Evaluate the predicate against
   every entry.
2. For each subsequent pair, `Tree.Diff(prev.Root, cur.Root)`. The HAMT diff
   descends only where node refs differ, so an unchanged subtree costs one
   pointer comparison. Evaluate the predicate against added and modified values
   only.
3. Attribute matches to snapshots by tracking, per **matched** ref, the snapshot
   at which it entered the tree and the one at which the diff shows it leaving.

The cost of searching an entire lineage is therefore approximately *one full
walk plus the sum of the inter-snapshot deltas* — that is, roughly the cost of
one `ls` plus the repository's actual churn, rather than the cost of one `ls`
per snapshot.

**Memoize on ref, not on entry.** `filemeta/` objects are content-addressed and
immutable, so a ref that has already been fetched and evaluated never needs
re-evaluating — including across lineages, which is what makes an identical file
backed up from two machines cost one evaluation. The memo is a set of refs; it
should store a truncated hash rather than the full string to keep a million-file
repository in the tens of megabytes.

**Track state only for matches.** The scan does not maintain a live key→ref map
of the whole tree; at a million files that map is the dominant memory cost for
no benefit. Snapshot attribution needs only the matched refs, and the diff
supplies both the entry and the exit event for each.

**Resolve paths lazily, from a directory index.** Path reconstruction needs
FileID→FileMeta for each ancestor, and the HAMT's `LookupByKey` is O(N). But
directories are a small fraction of entries, so the initial full walk builds a
FileID→FileMeta index **of folders only**, kept current across the lineage from
the same diffs. Path resolution for a match then costs O(depth) map lookups and
zero additional object reads.

**Two-stage path matching.** A path pattern still needs a path *before* the
predicate can be evaluated, which would force resolution for every entry. The
matcher therefore splits a path pattern at its last segment: the basename
component is evaluated from the `FileMeta` alone, and the directory component is
verified only for entries that survived it. `Documents/**/*.pdf` prefilters on
`*.pdf` and resolves paths for PDFs only. A pattern whose final segment is `**`
has no cheap prefilter and falls back to resolving every entry's path; this is
correct but slow, and `find` should say so on stderr rather than appearing to
hang.

**Escape hatch.** `-no-delta` forces the naive per-snapshot walk. It exists so
that a suspected delta-scan bug can be confirmed against a straightforward
implementation, and so the two can be compared in tests.

**Bounding output.** `-max-results` (default 1000) caps accumulated matches and
sets `Truncated`. Scanning continues to completion so counters stay accurate.

### 8. Client API

Following the repo's functional-options convention:

```go
func (c *Client) Find(ctx context.Context, opts ...FindOption) (*FindResult, error)
```

```go
cloudstic.WithFindName(pattern string)
cloudstic.WithFindPath(pattern string)
cloudstic.WithFindRegex(expr string)
cloudstic.WithFindIgnoreCase()
cloudstic.WithFindFileID(id string)
cloudstic.WithFindContentHash(hash string)
cloudstic.WithFindRef(ref string)
cloudstic.WithFindType(t core.FileType)
cloudstic.WithFindSize(cmp SizeCompare)
cloudstic.WithFindModifiedRange(after, before time.Time)
cloudstic.WithFindSnapshots(refs ...string)
cloudstic.WithFindSource(uri string)
cloudstic.WithFindTags(tags ...string)
cloudstic.WithFindLatest(n int)
cloudstic.WithFindMaxResults(n int)
cloudstic.WithFindGroupByContent()
cloudstic.WithFindNoDelta()
cloudstic.WithFindVerbose()
```

Implementation is a `FindManager` in `internal/engine/find.go` with a
`Run(ctx, opts...)` method, matching `ListManager` / `LsSnapshotManager`. It
reuses `hamt.Tree` and shares a single `NodeStore` cache across all snapshot
roots via `hamt.NewTreeWithNodes` — the sharing that makes the delta scan pay
off is exactly what that constructor exists for.

`find` is a pure read path: no lock, no write, no `UpgradeRepoFormat` stamp.

### 9. CLI shape and output

Wiring follows AGENTS.md exactly: `cmd/cloudstic/cmd_find.go` with
`declareFindArgs(g *globalFlags) (*findArgs, commandInput)` and
`runFind(r *runner, ctx, a *findArgs) int`, declared with
`leaf("find", …, repoCommandGroups, …)` and registered in `commandRegistry()`.
Completion, `-h` output, and the `COMMANDS` listing are all generated from that
declaration.

Default output is a table, one row per version:

```text
$ cloudstic find "*.kdbx"

PATH                          SIZE     MODIFIED           VERSIONS  SNAPSHOTS
Documents/vault.kdbx          4.2 MiB  2026-07-21 09:14   3         28
  v1 filemeta/9f86d081  4.2 MiB  2026-07-21 09:14   snapshots 22..28  (latest)
  v2 filemeta/2c624232  4.1 MiB  2026-06-30 18:02   snapshots  9..21
  v3 filemeta/e3b0c442  3.9 MiB  2026-05-02 11:47   snapshots  1..8

1 file, 3 versions across 28 snapshots (searched 31 snapshots in 1.8s)

Restore the newest version:
  cloudstic restore -path Documents/vault.kdbx <snapshot-28-ref> -o ./out
```

The trailing hint is deliberate. The reason a user runs `find` is almost always
that they intend to run `restore` next, and the snapshot ref and path they need
are both already on screen but tedious to assemble by hand.

`-json` emits `FindResult` verbatim. Two rules for the JSON shape: `Xattrs`
values are omitted (they are file contents by another name, and nobody greps for
them), and the schema is the same whether or not `-by-content` is set — only the
grouping differs.

Exit status is `0` for a successful search with no matches, consistent with
`list` on an empty repository and with `find(1)`. Scripts distinguish the cases
by reading `matches` from `-json`.

### 10. TUI

Out of scope for v1. A find pane is a natural later addition to the dashboard
(RFC 0012) and should be built on `app.TUIService` over `Client.Find` like every
other TUI action, with no engine logic in `internal/tui`.

## Alternatives considered

**A repository-side search index** (`index/find`, or a name trie written at
backup time) would make queries O(matches) instead of O(churn). It is rejected
for v1 on compatibility grounds. A new index object is a new on-disk structure,
which means a format-gate decision, a fixture, and — most seriously — a
structure that `prune` must maintain or invalidate correctly. `docs/compatibility.md`
is explicit that "cannot decode" must never be read as "empty"; a stale or
partially-written search index that silently returns no matches is a data-loss
shaped bug wearing a search feature's clothing. The delta scan gets acceptable
performance with zero compatibility surface, which is the right trade for a
first version.

**A client-side cache** under `paths.ConfigDir()`, memoizing evaluated refs
between runs, gets a large share of the index's benefit with none of its risk: a
corrupt or stale local cache is discardable, and it cannot mislead `prune`. This
is the natural follow-up if the delta scan proves too slow in practice, and it
should be measured before any repository-side structure is considered.

**Searching only the latest snapshot by default** was rejected because it
inverts the feature's purpose. A user who knows the file is in the latest
snapshot can already find it with `ls`; `find` earns its place precisely when the
file was deleted months ago.

## Compatibility

`find` reads `index/snapshots`, `snapshot/`, `node/`, and `filemeta/` objects
that every released version already writes. It defines no new object type, no
new key prefix, and no new field.

- **No repository format bump.** `core.RepoFormatVersion` and
  `MaxSupportedRepoFormat` are unchanged. Nothing about a repository differs
  after a `find`.
- **No fixture required.** There is no new on-disk layout to commit a baseline
  for. `find` must nonetheless work against every existing legacy fixture, which
  `e2e/feature_legacy_repo_test.go` gives for free once `find` is added to the
  commands it exercises.
- **Legacy `Paths` are honored.** Snapshots predating RFC 0015 persist
  `FileMeta.Paths`; path resolution must prefer a stored path when present, as
  `fileMetaPath()` already does, and fall back to the parent chain otherwise.
- **Older builds are unaffected**, having never been asked to read anything new.

The one change to shared code is the multi-path resolution in §4. It must stay
additive: existing single-path callers (`restore`, `diff`, `ls`) keep their
current behavior, and the new variant is opt-in.

While in that file, fix the stale comment on `core.FileMeta.Parents` — it claims
the field holds `filemeta/<sha256>` refs when it holds raw source FileIDs. That
is a comment-only change, but it is the sort of wrong comment that produces a
real bug in the next person to write path-resolution code.

## Security considerations

- `find` requires an unlocked repository. `filemeta/` objects are encrypted, so
  there is no metadata-only access path and no new exposure: anyone who can run
  `find` can already run `ls` and `restore`.
- Filenames are sensitive even when contents are not. Verbose and debug output
  must not log matched paths at a level that ends up in shared logs by default.
- `-json` omits `Xattrs` values (see §9), which can hold arbitrary application
  data including, on macOS, quarantine and provenance records.
- `-regex` accepts user input into RE2. RE2 has no catastrophic backtracking, so
  a pathological pattern costs time linear in input rather than exponential —
  but the pattern is still compiled once and applied to every candidate, and a
  compile error must be reported before the scan starts, not during it.

## Testing strategy

Unit tests (`internal/engine/find_test.go`, against `mock_test.go` stores), one
per axis of the result model — these are the cases the design exists to handle,
so they are the cases that must fail loudly if it regresses:

- one unchanged file across many snapshots collapses to one version
- an edited file yields ordered versions with correct snapshot ranges
- two identical-content files at different paths stay separate matches, and
  `-by-content` groups them
- a multi-parent entry reports every path
- a renamed FileID yields one match with differing version names, and a name
  query matches only the versions bearing that name
- a file deleted and later re-added yields non-contiguous snapshot ranges
- a legacy `FileMeta` carrying `Paths` resolves from the stored path

Equivalence test: for a fixture repository, `-no-delta` and the delta scan return
identical `FindResult` values. This is the single most valuable test in the set,
because the delta scan is the part of this design most likely to be subtly wrong.

Matcher unit tests for `**`, segment globs, case folding, and the basename/path
split, including the two-stage prefilter agreeing with direct full-path matching.

Golden tests for `printFindResult` and for `-h` output
(`testdata/help_find.golden`, `testdata/usage_root.golden`).

A testscript case under `cmd/cloudstic/testdata/scripts/` covering a local-store
find over a two-snapshot repository: match, no-match exit status, `-json` shape,
and stdout/stderr separation.

Benchmark on a synthetic repository (10k files × 50 snapshots) asserting the
delta scan's object-read count stays within a small multiple of a single walk —
a regression here is silent otherwise, since the feature still returns correct
results while quietly costing fifty times more.

## Rollout plan

1. **Path resolution** — multi-path variant of `fileMetaPath`, folder-index
   builder, `Parents` comment fix. No user-visible change.
2. **Matcher** — `**`-capable glob, placed for later reuse by excludes.
3. **Engine** — `FindManager` with the naive per-snapshot walk only, plus the
   full result model. Correct and complete, just not yet fast.
4. **Delta scan** — added behind the existing `-no-delta` fallback, with the
   equivalence test as its gate.
5. **CLI and API** — `Client.Find`, `cmd_find.go`, completion, goldens,
   `docs/user-guide.md`.
6. **TUI pane** — separate RFC or issue.

Steps 3 and 4 in that order matter: shipping the optimization first would leave
nothing to check it against.

## Resolved questions

The open questions this RFC shipped with, and how implementation settled them.

1. **Multi-parent paths, fully or flagged?** *Resolved: fully.*
   `fileMetaPaths` (`internal/engine/filemeta_paths.go`) walks every parent
   chain and returns one path per chain, in `Parents` order, so the existing
   single-path `fileMetaPath` is exactly its first element and every current
   caller is unaffected. The expansion is bounded — 50 levels of depth, 32
   resolved paths — because a multi-parent chain multiplies rather than adds.
   The `multi_parent` flag was not needed.

2. **Should `-max-results` truncate or stream?** *Resolved: truncate, at 1000.*
   The cap bounds *distinct files*, never versions, so a truncated result never
   shows a file with part of its history missing. What implementation exposed is
   that "which files survive" is inherently a sample: the delta scan and the
   `-no-delta` walk encounter entries in different orders, so they can keep
   different subsets. Each is deterministic on its own — the delta scan's final
   flush is sorted precisely so a repeated query gives a repeated answer — and
   `Truncated` is reported rather than left implicit. The equivalence test
   compares match identity only for untruncated queries.

3. **Is `-since`/`-until` filtering snapshots or files?** *Resolved as
   proposed:* `-since`/`-until` select snapshots by creation time,
   `-newer`/`-older` select files by `Mtime`. Help text and the user guide both
   say so, and `TestBuildFindOpts_SnapshotAndFileTimeSelectorsStaySeparate` pins
   it.

4. **Should `find` report a version's *deletion*?** *Deferred.* Nothing in the
   result model precludes it — the delta scan already closes a run at the
   snapshot where a ref left the tree — but it is additive and no test needed
   it, so v1 does not report it.

5. **Cross-source grouping.** *Resolved: separate, as proposed.* Two machines
   produce different FileIDs and stay separate matches; `-by-content` unifies
   them on request, and the summary line names the grouping in force so the two
   modes cannot be confused.

Implementation also surfaced that §9's illustrative restore hint was not
literally runnable: `<snapshot-28-ref>` stood in for the snapshot ref, and the
actual shape matters. `restore` (like `ls` and `diff`) resolves its snapshot
argument with a direct object-store `Get` by key, not a prefix search the way
`find`'s own `-snapshot` selector does — so a truncated hash, the short form
used for on-screen display, fails with "not found" the moment it is pasted.
The hint must print the full hash. It prints the *bare* hash rather than a
`snapshot/`-prefixed ref: `restore` accepts either — the prefix check is a
no-op when already present — but the bare form is what every other place a
snapshot ID is entered uses (`restore`, `ls`, `diff`, and `list`'s own
SNAPSHOT HASH column), so it is the one worth being consistent with. This was
caught by the e2e test that actually executes the printed hint
(`TestCLI_Feature_FindLocatesDeletedFileAndPrintsAWorkingRestore`) rather than
only asserting on its text.

Implementation also surfaced a case §4 did not anticipate, worth recording
because it looks like a bug until you see why it is not: **renaming an ancestor
folder changes a file's path without changing the file's own metadata object.**
Parents are FileIDs, not refs, so the descendant's `filemeta/` ref is untouched.
A `FileVersion` is therefore keyed by (ref, paths) rather than by ref alone, and
one ref can legitimately appear as two versions at different paths. The delta
scan re-resolves active runs' paths whenever the folder index moves, which is
what keeps a path query from crediting a file to snapshots where it was
somewhere else.
