#!/usr/bin/env bash
#
# Peak memory as a function of repository size.
#
# scripts/benchmark/run.sh already reports peak RSS, but at one dataset size,
# and a single point cannot distinguish memory that is constant from memory
# that grows with the repository. That difference is the whole question: an
# operation holding a fixed working set is fine at any scale, one holding a
# per-file structure eventually meets a repository it cannot open. Telling them
# apart needs the same operation measured at several sizes.
#
# The dataset is therefore many small files rather than run.sh's gigabyte of
# large ones: the independent variable is the number of entries, and file size
# is held down so throughput does not drown out per-entry cost.
#
# Usage:
#   scripts/benchmark/memory.sh                   # default sizes
#   SIZES="1000 5000" scripts/benchmark/memory.sh # override
#   OUT=/tmp/mem.csv scripts/benchmark/memory.sh

set -euo pipefail

SIZES=${SIZES:-"5000 20000 50000"}
OUT=${OUT:-benchmark-results/memory.csv}
KEEP=${KEEP:-0} # keep scratch dirs for inspection

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
CLOUDSTIC_BIN="$REPO_ROOT/bin/cloudstic"
BENCHREPORT="$REPO_ROOT/bin/benchreport"
PASSWORD=${BENCH_REPO_PASSWORD:-benchmark-password}

WORK=$(mktemp -d -t cloudstic-mem-XXXXXX)
cleanup() { [ "$KEEP" = "1" ] || rm -rf "$WORK"; }
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Measurement
# ---------------------------------------------------------------------------

# measure <operation> <files> <command...>
#
# Delegates to benchreport, which takes peak RSS from the wait4 rusage the
# kernel already returns rather than parsing /usr/bin/time — whose flag, label
# and unit all differ between BSD and GNU, and which is absent on a minimal
# Linux image. It appends the CSV row and echoes a console line.
#
# A failing command is fatal: a curve with a hole in it is worse than no curve.
measure() {
    local op=$1 files=$2
    shift 2
    "$BENCHREPORT" run -op "$op" -scale "$files" -out "$OUT" -quiet -- "$@"
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

# touch_fraction <root> <count> <denominator>
#
# Rewrites every Nth file so the follow-up backup has real work to do. An
# incremental over an unchanged tree measures the scan path only; changing a
# slice exercises upload and the HAMT rewrite too.
touch_fraction() {
    local root=$1 count=$2 denom=$3
    for (( i = 0; i < count; i += denom )); do
        printf 'cloudstic benchmark entry %d modified\n' "$i" > "$root/dir-$(( i / 100 ))/file-$i.txt"
    done
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

echo "Building cloudstic and benchreport..."
( cd "$REPO_ROOT" && go build -o bin/cloudstic ./cmd/cloudstic )
( cd "$REPO_ROOT" && go build -o bin/benchreport ./internal/cmd/benchreport )

# benchreport writes the header itself on first append, so a stale CSV must go
# or the sweep appends to the previous run.
mkdir -p "$(dirname "$OUT")"
rm -f "$OUT"

echo ""
echo "=== Peak memory vs repository size ==="
echo "sizes: $SIZES"
echo ""

export CLOUDSTIC_PASSWORD="$PASSWORD"

for size in $SIZES; do
    echo "--- $size files ---"
    data="$WORK/data-$size"
    repo="$WORK/repo-$size"
    restore="$WORK/restore-$size"
    mkdir -p "$data" "$repo" "$restore"

    printf '  generating %d files... ' "$size"
    generate_tree "$data" "$size"
    echo "done"

    store_flags=(-store "local:$repo")
    source_flags=(-source "local:$data")

    "$CLOUDSTIC_BIN" init "${store_flags[@]}" -quiet >/dev/null

    measure backup-initial "$size" \
        "$CLOUDSTIC_BIN" backup "${store_flags[@]}" "${source_flags[@]}" -quiet

    touch_fraction "$data" "$size" 20

    measure backup-incremental "$size" \
        "$CLOUDSTIC_BIN" backup "${store_flags[@]}" "${source_flags[@]}" -quiet

    # diff needs two explicit refs, and list reports newest first. Written
    # without mapfile or a negative array index: macOS still ships bash 3.2,
    # where both are syntax errors rather than graceful failures.
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

    # Reclaim before the next size so the scratch dir does not grow without
    # bound over the sweep; each size is independent anyway.
    rm -rf "$data" "$repo" "$restore"
    echo ""
done

echo "Wrote $OUT"
