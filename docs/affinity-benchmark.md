# Affinity Model Benchmark Results

Benchmark script: `scripts/benchmark/affinity.sh`
Go unit test: `internal/hamt/affinity_bench_test.go`

---

## E2E benchmark — `scripts/benchmark/affinity.sh`

Two scenarios with **50 modified files each**, applied to the same initial tree
(50 dirs × 50 files = 2 500 files), run as a second backup after the initial full backup.

| Scenario | Change pattern | New HAMT node objects (affinity) | New HAMT node objects (legacy) |
|---|---|---|---|
| A — clustered | All 50 changes in `dir_01` | **18** | 75 |
| B — scattered | 1 change in each of 50 dirs | 171 | 73 |

The metric is **new `node/*` objects** written to the local store during the second
backup. `KeyCacheStore` deduplicates writes: nodes whose content did not change are
skipped, so only genuinely new HAMT path nodes reach the underlying store.

> **Note:** The `Flushing HAMT: X reachable nodes` progress line shows **staging size**
> (the full final tree), not delta writes. Do not use it to judge incremental cost.
> The benchmark counts `node/*` entries in `index/packs` before/after the second backup.

### Cross-binary comparison

```
# Affinity binary (RFC 0002)
Scenario A — clustered (50 files in 1 dir):   18 new node objects
Scenario B — scattered (1 file in 50 dirs):  171 new node objects
Node-write reduction (A vs B): 89.5%  (153 fewer writes)

# Legacy binary (pre-RFC 0002)
Scenario A — clustered (50 files in 1 dir):   75 new node objects
Scenario B — scattered (1 file in 50 dirs):   73 new node objects
Difference: ~-2.7%  (no meaningful locality benefit)
```

Run the comparison yourself:

```bash
./scripts/benchmark/affinity.sh                          # current build
CLOUDSTIC_BIN=/path/to/old-cloudstic ./scripts/benchmark/affinity.sh
```

### Why clustered beats scattered (affinity model)

With `AffinityKey(parentID, fileID) = SHA256(parentID)[:4] + SHA256(fileID)[4:]`,
all 50 files in `dir_01` share the same routing at HAMT levels 0–2 (determined by
`SHA256("dir_01")[:4]`). They diverge only at level 3. An incremental update rewrites:

- 1 root + 3 internal path nodes (L0 → L1 → L2 → L3) shared across all 50 files
- ~14 L3 leaf nodes (one per occupied bucket at the divergence level)
- Total: **~18 new nodes**

For scattered changes (1 file per directory), each file traverses a different path
from root — 50 distinct root-to-leaf paths are dirtied. Because affinity keys cluster
same-dir files into deeper sub-trees, those cross-dir paths are also longer, so
scattered writes are higher with affinity (171) than with legacy keys (73). This is an
expected trade-off: affinity optimises the common case (changes concentrated in a
directory) at the cost of slightly worse worst-case (fully scattered changes).

### Why legacy shows no difference between A and B

`SHA256(fileID)` distributes all keys uniformly across the HAMT regardless of which
directory a file lives in. Clustered changes and scattered changes both dirty ~30 of 32
L0 buckets. There is no shared path to exploit, so both scenarios produce roughly the
same number of new node writes (~70–75).

---

## Go unit test — `TestAffinityNodeWriteReduction`

Simulates an incremental backup: build a 1 000-file tree (10 dirs × 100 files),
then update all 100 files in one directory. Only the changed files touch new HAMT paths.

```bash
go test ./internal/hamt/ -run TestAffinityNodeWriteReduction -v
```

Result:

```
Incremental update of 100 files in one directory (1000 total files, 10 dirs):
  affinity keys :   20 node writes
  legacy keys   :   68 node writes
  reduction     : 70.6%  (48 fewer writes)
```

The legacy simulation uses `AffinityKey(fileID, fileID) = SHA256(fileID)`, identical
to the old `computePathKey(fileID)` — no code changes needed.

### Go benchmark — `BenchmarkIncrementalUpdate_*`

```bash
go test ./internal/hamt/ -run=^$ -bench=BenchmarkIncrementalUpdate -benchmem -benchtime=3s
```

| Strategy | ns/op | B/op | allocs/op |
|---|---|---|---|
| Affinity | 2 171 472 | 1 254 035 | 14 363 |
| Legacy   | 3 812 540 | 1 968 168 | 15 992 |

**~1.75× faster, ~36% less memory** for a 100-file incremental update in one directory.

---

## Summary

| Metric | Legacy | Affinity | Delta |
|---|---|---|---|
| E2E: clustered 50-file update | 75 nodes | 18 nodes | **−76%** |
| E2E: scattered 50-file update | 73 nodes | 171 nodes | +134% (expected trade-off) |
| E2E: initial tree size (50×50) | 962 nodes | 906 nodes | −6% |
| Unit test: 100-file update, 1 dir | 68 nodes | 20 nodes | **−71%** |
| Unit test wall time | 3 813 µs | 2 171 µs | **−43%** |
| Unit test memory | 1 968 KB | 1 254 KB | **−36%** |

The affinity model's benefit is specifically for **incremental updates of multiple files
in the same directory** — the dominant pattern in real workups. The scattered case (one
change spread across every directory simultaneously) is a pathological pattern that
affinity does not optimise for.
