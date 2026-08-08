#!/usr/bin/env bash
#
# Memory as a function of repository size.
#
# scripts/benchmark/cloudstic.sh already reports peak RSS, but at one dataset
# size, and a single point cannot distinguish memory that is constant from
# memory that grows with the repository. That difference is the whole question:
# an operation holding a fixed working set is fine at any scale, one holding a
# per-file structure eventually meets a repository it cannot open. Telling them
# apart needs the same operation measured at several sizes.
#
# The dataset is therefore many small files rather than cloudstic.sh's gigabyte
# of large ones: the independent variable is the number of entries, and file
# size is held down so throughput does not drown out per-entry cost.
#
# Two measurements per point, because peak RSS alone was not enough:
#
#   peak RSS   the largest amount live at once — what decides whether an
#              operation fits in the memory available.
#   allocated  cumulative bytes allocated, freed or not. Peak RSS is a
#              high-water mark and cannot see churn the collector reclaims, so
#              a change removing tens of MB of transient garbage moves it by
#              nothing measurable. PR #449 removed 36 MB of allocation from
#              CompactCatalog and shifted peak RSS by 5 MB, well inside the
#              noise. This column is the one that moved.
#
# And SAMPLES repetitions per point, reported as a median with its min–max
# band. Repeated runs of one binary on one machine gave 408/405/352 MB — a
# ±60 MB spread, wider than most of the improvements being chased. A single
# sample cannot tell a 40 MB win from a lucky run; three and a median can.
# Every operation here mutates the repository, so a sample repeats the whole
# pipeline against a fresh repository rather than re-running one command.
#
# Usage:
#   scripts/benchmark/memory.sh                     # defaults
#   SIZES="1000 5000" scripts/benchmark/memory.sh   # override sizes
#   SAMPLES=5 scripts/benchmark/memory.sh           # more repetitions
#   OUT=/tmp/mem.csv scripts/benchmark/memory.sh

set -euo pipefail

SIZES=${SIZES:-"5000 20000 50000"}
# Which workload shape to measure against. "uniform" is the original flat tree —
# constant file size, constant fan-out, nothing duplicated — kept as the default
# so numbers stay comparable with everything recorded before. The other profiles
# come from scripts/benchmark/gentree and have the statistics a real source has:
# heavy-tailed sizes and fan-out, duplicated content, and churn that clusters in
# a few directories instead of spreading evenly. See gentree/main.go.
PROFILE=${PROFILE:-uniform}
MAX_BYTES=${MAX_BYTES:-$((2 * 1024 * 1024 * 1024))}
SAMPLES=${SAMPLES:-3}
OUT=${OUT:-benchmark-results/memory.csv}
KEEP=${KEEP:-0} # keep scratch dirs for inspection

case "$SAMPLES" in
    '' | *[!0-9]* | 0)
        echo "SAMPLES must be a positive integer, got '$SAMPLES'" >&2
        exit 2
        ;;
esac

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
BENCHREPORT="$REPO_ROOT/bin/benchreport"
PASSWORD=${BENCH_REPO_PASSWORD:-benchmark-password}

# A prebuilt binary to measure instead of this checkout's.
#
# The question this harness gets asked is almost always "did that change help",
# which means measuring two commits. Building each into its own path and
# pointing the sweep at them in turn compares like with like — the alternative,
# checking out the other commit, also swaps out the harness doing the measuring.
#
# It has to be a binary built from a tree carrying -memstats (cmd/cloudstic/
# profiling.go); anything built before that flag existed will not write the
# file measure() expects and the sweep will fail on its first row.
CLOUDSTIC_BIN=${BENCH_CLOUDSTIC_BIN:-"$REPO_ROOT/bin/cloudstic"}
GENTREE_BIN="$REPO_ROOT/bin/gentree"

WORK=$(mktemp -d -t cloudstic-mem-XXXXXX)
cleanup() { [ "$KEEP" = "1" ] || rm -rf "$WORK"; }
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Measurement
# ---------------------------------------------------------------------------

# Where the measured process drops its allocation counters. Rewritten by every
# run and read back immediately, so one path serves the whole sweep.
MEMSTATS="$WORK/memstats.json"

# measure <operation> <files> <command...>
#
# Delegates to benchreport, which takes peak RSS from the wait4 rusage the
# kernel already returns rather than parsing /usr/bin/time — whose flag, label
# and unit all differ between BSD and GNU, and which is absent on a minimal
# Linux image. It appends the CSV row and echoes a console line.
#
# -memstats is appended to the measured command rather than threaded through
# its arguments: cloudstic strips that flag from os.Args before any command
# parsing, so its position does not matter. The allocation total has to come
# from inside the process — the parent can observe a high-water mark through
# rusage, but not bytes that were allocated and freed again.
#
# One row is appended per sample rather than one per point. The median is
# computed at render time, which keeps every sample in the CSV where a reader
# can see how noisy a point actually was.
#
# A failing command is fatal: a curve with a hole in it is worse than no curve.
measure() {
    local op=$1 files=$2
    shift 2
    rm -f "$MEMSTATS"
    "$BENCHREPORT" run -op "$op" -scale "$files" -out "$OUT" \
        -alloc-from "$MEMSTATS" -quiet -- "$@" -memstats "$MEMSTATS"
}

# ---------------------------------------------------------------------------
# Dataset
# ---------------------------------------------------------------------------

# generate_tree <root> <count>
#
# One folder per 100 files, so the folder count scales with the tree rather
# than every entry landing in one enormous directory. Contents are unique per
# file: identical bytes would deduplicate to a single content object and make
# the store side of the measurement unrepresentative.
#
# The loop uses only builtins — no forks — which is what keeps 50k files a
# few seconds rather than a few minutes.
generate_tree() {
    local root=$1 count=$2 per_dir=100
    local dirs=$(( (count + per_dir - 1) / per_dir ))

    for (( d = 0; d < dirs; d++ )); do
        mkdir -p "$root/dir-$d"
    done
    for (( i = 0; i < count; i++ )); do
        printf 'cloudstic benchmark entry %d\n' "$i" > "$root/dir-$(( i / per_dir ))/file-$i.txt"
    done
}

# touch_fraction <root> <count> <denominator> <generation>
#
# Rewrites every Nth file so the follow-up backup has real work to do. An
# incremental over an unchanged tree measures the scan path only; changing a
# slice exercises upload and the HAMT rewrite too.
#
# The generation is in the written bytes so that repeated samples over one tree
# stay honest. Without it the second sample would rewrite the same files with
# the same content, content-address to the objects already there, and measure
# an incremental backup that uploads nothing.
touch_fraction() {
    local root=$1 count=$2 denom=$3 gen=$4
    for (( i = 0; i < count; i += denom )); do
        printf 'cloudstic benchmark entry %d modified %d\n' "$i" "$gen" \
            > "$root/dir-$(( i / 100 ))/file-$i.txt"
    done
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

if [ -n "${BENCH_CLOUDSTIC_BIN:-}" ]; then
    echo "Measuring prebuilt $CLOUDSTIC_BIN"
    if [ ! -x "$CLOUDSTIC_BIN" ]; then
        echo "BENCH_CLOUDSTIC_BIN is not an executable file: $CLOUDSTIC_BIN" >&2
        exit 2
    fi
else
    echo "Building cloudstic..."
    ( cd "$REPO_ROOT" && go build -o bin/cloudstic ./cmd/cloudstic )
fi
echo "Building benchreport..."
( cd "$REPO_ROOT" && go build -o bin/benchreport ./internal/cmd/benchreport )
if [ "$PROFILE" != "uniform" ]; then
    ( cd "$REPO_ROOT" && go build -o bin/gentree ./scripts/benchmark/gentree )
fi

# benchreport writes the header itself on first append, so a stale CSV must go
# or the sweep appends to the previous run.
mkdir -p "$(dirname "$OUT")"
rm -f "$OUT"

echo ""
echo "=== Memory vs repository size ==="
echo "sizes:   $SIZES"
echo "profile: $PROFILE"
echo "samples: $SAMPLES per point (median reported)"
echo ""

export CLOUDSTIC_PASSWORD="$PASSWORD"

# run_pipeline <data> <size> <generation>
#
# One full sample: every operation once, over a repository built from scratch.
#
# The repository is rebuilt per sample because every operation here mutates it.
# Re-running `prune` against an already-pruned repository, or `backup-initial`
# against a populated one, measures a different operation that happens to share
# a name — so the unit of repetition has to be the pipeline, not the command.
run_pipeline() {
    local data=$1 size=$2 gen=$3
    local repo="$WORK/repo" restore="$WORK/restore"

    rm -rf "$repo" "$restore"
    mkdir -p "$repo" "$restore"

    local store_flags source_flags
    store_flags=(-store "local:$repo")
    source_flags=(-source "local:$data")

    "$CLOUDSTIC_BIN" init "${store_flags[@]}" -quiet >/dev/null

    measure backup-initial "$size" \
        "$CLOUDSTIC_BIN" backup "${store_flags[@]}" "${source_flags[@]}" -quiet

    if [ "$PROFILE" = "uniform" ]; then
        touch_fraction "$data" "$size" 20 "$gen"
    else
        # Seeded by the sample number so each sample churns differently, the way
        # successive days would, while staying reproducible run to run.
        # -max-bytes must match generation: churn scales its writes to the same
        # budget, and a mismatch writes files from a different size distribution
        # into the tree, quietly changing what the incremental measures.
        "$GENTREE_BIN" -churn "$data" -profile "$PROFILE" -seed "$gen" \
            -fraction 0.05 -max-bytes "$MAX_BYTES" >/dev/null
    fi

    measure backup-incremental "$size" \
        "$CLOUDSTIC_BIN" backup "${store_flags[@]}" "${source_flags[@]}" -quiet

    # diff needs two explicit refs, and list reports newest first. Written
    # without mapfile or a negative array index: macOS still ships bash 3.2,
    # where both are syntax errors rather than graceful failures.
    local refs newest oldest
    refs=$("$CLOUDSTIC_BIN" list "${store_flags[@]}" -json 2>/dev/null \
        | grep -o 'snapshot/[a-f0-9]\{64\}')
    newest=$(printf '%s\n' "$refs" | head -1)
    oldest=$(printf '%s\n' "$refs" | tail -1)
    measure diff "$size" \
        "$CLOUDSTIC_BIN" diff "${store_flags[@]}" "$oldest" "$newest" -quiet

    measure check "$size" \
        "$CLOUDSTIC_BIN" check "${store_flags[@]}" -quiet

    measure restore "$size" \
        "$CLOUDSTIC_BIN" restore "${store_flags[@]}" -output "$restore" latest -quiet

    measure prune "$size" \
        "$CLOUDSTIC_BIN" prune "${store_flags[@]}" -quiet

    rm -rf "$repo" "$restore"
}

for size in $SIZES; do
    data="$WORK/data-$size"
    mkdir -p "$data"

    # Generated once and reused across the samples of this size. Regenerating
    # would add dataset variation to the very noise the samples exist to
    # measure; the tree is an input, not part of what is under test.
    printf -- '--- %d files: generating (%s)... ' "$size" "$PROFILE"
    if [ "$PROFILE" = "uniform" ]; then
        generate_tree "$data" "$size"
        echo "done"
    else
        echo ""
        "$GENTREE_BIN" -out "$data" -profile "$PROFILE" -files "$size" \
            -seed 1 -max-bytes "$MAX_BYTES" -stats "$WORK/dataset-$size.json"
    fi

    for (( sample = 1; sample <= SAMPLES; sample++ )); do
        echo "  sample $sample/$SAMPLES"
        run_pipeline "$data" "$size" "$sample"
    done

    # Reclaim before the next size so the scratch dir does not grow without
    # bound over the sweep; each size is independent anyway.
    rm -rf "$data"
    echo ""
done

echo "Wrote $OUT"
