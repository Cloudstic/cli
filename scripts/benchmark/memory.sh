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
PASSWORD=${BENCH_REPO_PASSWORD:-benchmark-password}

WORK=$(mktemp -d -t cloudstic-mem-XXXXXX)
cleanup() { [ "$KEEP" = "1" ] || rm -rf "$WORK"; }
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Peak RSS, portably
# ---------------------------------------------------------------------------

# time(1) reports maximum resident set size in different units and under
# different labels per platform: BSD/macOS `-l` gives bytes, GNU `-v` gives
# kilobytes. Normalising here keeps the CSV in one unit.
detect_time() {
    if /usr/bin/time -l true >/dev/null 2>&1; then
        echo "bsd"
    elif /usr/bin/time -v true >/dev/null 2>&1; then
        echo "gnu"
    else
        echo "none"
    fi
}
TIME_FLAVOUR=$(detect_time)

if [ "$TIME_FLAVOUR" = "none" ]; then
    echo "Error: need BSD (/usr/bin/time -l) or GNU (/usr/bin/time -v) time." >&2
    echo "On Debian/Ubuntu: apt-get install time" >&2
    exit 1
fi

# measure <operation> <files> <command...>
#
# Appends one CSV row. A failing command is fatal: a partial run would produce
# a curve with a hole in it, which is worse than no curve at all.
measure() {
    local op=$1 files=$2
    shift 2
    local err out
    err=$(mktemp)
    out=$(mktemp)

    local seconds peak_kb peak_mb
    if [ "$TIME_FLAVOUR" = "bsd" ]; then
        /usr/bin/time -l "$@" >"$out" 2>"$err" || { cat "$err" >&2; exit 1; }
        peak_kb=$(( $(grep "maximum resident set size" "$err" | awk '{print $1}') / 1024 ))
        seconds=$(grep -E "real" "$err" | tail -1 | awk '{print $1}')
    else
        /usr/bin/time -v "$@" >"$out" 2>"$err" || { cat "$err" >&2; exit 1; }
        peak_kb=$(grep "Maximum resident set size" "$err" | awk '{print $NF}')
        # GNU prints h:mm:ss or m:ss.ss; normalise to seconds.
        seconds=$(grep "Elapsed (wall clock)" "$err" | awk '{print $NF}' | awk -F: '
            {  if (NF == 3) print $1 * 3600 + $2 * 60 + $3;
               else if (NF == 2) print $1 * 60 + $2;
               else print $1 }')
    fi
    peak_mb=$(echo "scale=1; $peak_kb / 1024" | bc)

    printf '%s,%d,%s,%s\n' "$op" "$files" "$peak_mb" "$seconds" >>"$OUT"
    printf '  %-22s %8d files  %8s MB  %8ss\n' "$op" "$files" "$peak_mb" "$seconds"

    rm -f "$err" "$out"
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

echo "Building cloudstic..."
( cd "$REPO_ROOT" && go build -o bin/cloudstic ./cmd/cloudstic )

mkdir -p "$(dirname "$OUT")"
echo "operation,files,peak_mb,seconds" >"$OUT"

echo ""
echo "=== Peak memory vs repository size ==="
echo "time(1): $TIME_FLAVOUR   sizes: $SIZES"
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
