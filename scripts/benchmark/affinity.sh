#!/usr/bin/env bash
# scripts/benchmark/affinity.sh
#
# Demonstrates the incremental node-write reduction from the affinity model (RFC 0002).
#
# WHAT IS MEASURED
# ================
# After an initial full backup, a second backup is run with N files modified.
# The number of new HAMT node objects written to the store during the second
# backup is recorded (via counting new node/* files in the store directory).
# Because KeyCacheStore deduplicates by key, only genuinely new nodes — those
# whose content changed — reach the underlying store.
#
# Two change patterns with the SAME number of modified files are compared:
#
#   A) CLUSTERED  — all 50 modified files are in a single directory
#   B) SCATTERED  — 50 modified files spread across 50 different directories
#
# With affinity keys (RFC 0002), clustered changes share the top 3 HAMT levels,
# so the second backup writes O(depth + N_leaves) new nodes instead of O(N * depth).
# Expected: clustered << scattered for new HAMT node writes.
#
# NOTE: This benchmark uses local source (full scan on every backup). The affinity
# benefit is fully visible here because the local store deduplicates node writes via
# KeyCacheStore: unchanged HAMT paths are skipped on the second backup.
# The `Flushing HAMT` progress line shows staging size (full tree), NOT delta writes.
# This script counts actual node/* files created instead.
#
# Usage:
#   ./scripts/benchmark/affinity.sh [--debug]
#
# Cross-binary comparison:
#   CLOUDSTIC_BIN=/path/to/old-cloudstic ./scripts/benchmark/affinity.sh

set -e
cd "$(dirname "$0")/../.."

DEBUG_FLAG=""
if [ "${1}" == "--debug" ]; then
    DEBUG_FLAG="--debug"
fi

DIRS=50            # 50 directories (must be >= CHANGED for scattered scenario)
FILES_PER_DIR=50   # 50 files per directory = 2500 files total
CHANGED=50         # files to modify in the second backup

TMP_DIR=$(mktemp -d)
PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  ✓ $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  ✗ $1"; }
check() { if eval "$2"; then pass "$1"; else fail "$1"; fi }

cleanup() {
    echo ""
    if [ $FAIL -eq 0 ]; then
        echo "All $PASS checks passed."
        rm -rf "$TMP_DIR"
    else
        echo "$FAIL check(s) FAILED ($PASS passed). Temp dir preserved: $TMP_DIR"
        exit 1
    fi
}
trap cleanup EXIT

# Count node/* entries in the pack catalog of a local store directory.
# PackStore bundles small objects (including node/*) into packs/ packfiles,
# so there is no node/ directory on disk. The catalog at index/packs is a
# JSON map of object-key -> {p,o,l}, so we count keys starting with "node/".
count_nodes() {
    local catalog="$1/index/packs"
    [ -f "$catalog" ] && grep -o '"node/' "$catalog" | wc -l | tr -d ' ' || echo 0
}

# Build (or use provided) binary.
if [ -z "$CLOUDSTIC_BIN" ]; then
    echo "Building cloudstic..."
    go build -o /tmp/cloudstic-affinity-bench ./cmd/cloudstic
    CLOUDSTIC_BIN="/tmp/cloudstic-affinity-bench"
fi
CLI="$CLOUDSTIC_BIN"

echo ""
echo "=== Affinity Model Benchmark (RFC 0002) ==="
echo "Binary        : $CLOUDSTIC_BIN"
echo "Initial tree  : $DIRS dirs × $FILES_PER_DIR files = $((DIRS * FILES_PER_DIR)) files"
echo "Modified files: $CHANGED (same count for both scenarios)"
echo "Scenario A    : all $CHANGED modified files in ONE directory (clustered)"
echo "Scenario B    : 1 modified file in each of $CHANGED directories (scattered)"
if [ -n "$DEBUG_FLAG" ]; then echo "Mode          : debug"; fi
echo ""
echo "The metric is: new HAMT node/* objects written during the second backup."
echo "KeyCacheStore skips nodes already present, so only genuinely new nodes are counted."
echo ""

run_backup() {
    local store_flags="$1" source_flags="$2"
    $CLI backup $store_flags $source_flags -quiet $DEBUG_FLAG 2>&1
}

DATA="$TMP_DIR/data"
mkdir -p "$DATA"

# Create initial dataset: DIRS × FILES_PER_DIR files.
for d in $(seq 1 $DIRS); do
    dir=$(printf "dir_%02d" $d)
    mkdir -p "$DATA/$dir"
    for f in $(seq 1 $FILES_PER_DIR); do
        printf "initial dir=%02d file=%04d\n" $d $f > "$DATA/$dir/file_$(printf '%04d' $f).txt"
    done
done

# ===========================================================================
# Scenario A: CLUSTERED — modify all CHANGED files in dir_01
# ===========================================================================
echo "=== Scenario A: Clustered (all $CHANGED changes in dir_01) ==="

REPO_A="$TMP_DIR/repo_a"
mkdir -p "$REPO_A"
$CLI init -store local -store-path "$REPO_A" --no-encryption 2>&1 | tail -1

# Backup 1: full initial backup.
run_backup "-store local -store-path $REPO_A" "-source local -source-path $DATA" > /dev/null
NODES_BEFORE_A=$(count_nodes "$REPO_A")
echo "  After backup 1: $NODES_BEFORE_A node objects in store"

# Modify all CHANGED files in dir_01.
for f in $(seq 1 $CHANGED); do
    printf "updated dir=01 file=%04d\n" $f > "$DATA/dir_01/file_$(printf '%04d' $f).txt"
done

# Backup 2: incremental (full scan, but only changed nodes reach persistent store).
run_backup "-store local -store-path $REPO_A" "-source local -source-path $DATA" > /dev/null
NODES_AFTER_A=$(count_nodes "$REPO_A")
NEW_NODES_A=$((NODES_AFTER_A - NODES_BEFORE_A))

echo "  After backup 2: $NODES_AFTER_A node objects (+$NEW_NODES_A new)"
check "Scenario A produced new node writes (got $NEW_NODES_A)" "[ '${NEW_NODES_A:-0}' -gt 0 ]"

# Restore dir_01 to original for fairness (both scenarios start from the same state).
for f in $(seq 1 $FILES_PER_DIR); do
    printf "initial dir=%02d file=%04d\n" 1 $f > "$DATA/dir_01/file_$(printf '%04d' $f).txt"
done

# ===========================================================================
# Scenario B: SCATTERED — modify 1 file in each of CHANGED directories
# ===========================================================================
echo ""
echo "=== Scenario B: Scattered (1 change in each of $CHANGED dirs) ==="

REPO_B="$TMP_DIR/repo_b"
mkdir -p "$REPO_B"
$CLI init -store local -store-path "$REPO_B" --no-encryption 2>&1 | tail -1

# Backup 1: full initial backup.
run_backup "-store local -store-path $REPO_B" "-source local -source-path $DATA" > /dev/null
NODES_BEFORE_B=$(count_nodes "$REPO_B")
echo "  After backup 1: $NODES_BEFORE_B node objects in store"

# Modify 1 file in each of CHANGED directories (dirs 1 through CHANGED).
for d in $(seq 1 $CHANGED); do
    dir=$(printf "dir_%02d" $d)
    printf "updated dir=%02d file=0001\n" $d > "$DATA/$dir/file_0001.txt"
done

# Backup 2.
run_backup "-store local -store-path $REPO_B" "-source local -source-path $DATA" > /dev/null
NODES_AFTER_B=$(count_nodes "$REPO_B")
NEW_NODES_B=$((NODES_AFTER_B - NODES_BEFORE_B))

echo "  After backup 2: $NODES_AFTER_B node objects (+$NEW_NODES_B new)"
check "Scenario B produced new node writes (got $NEW_NODES_B)" "[ '${NEW_NODES_B:-0}' -gt 0 ]"

# ===========================================================================
# Summary and assertions
# ===========================================================================
echo ""
echo "=== Results ==="
printf "  %-50s %s new node objects\n" "Scenario A — clustered ($CHANGED files in 1 dir):" "$NEW_NODES_A"
printf "  %-50s %s new node objects\n" "Scenario B — scattered (1 file in $CHANGED dirs):" "$NEW_NODES_B"

if [ "${NEW_NODES_A:-0}" -gt 0 ] && [ "${NEW_NODES_B:-0}" -gt 0 ]; then
    REDUCTION=$(awk "BEGIN { printf \"%.1f\", ($NEW_NODES_B - $NEW_NODES_A) / $NEW_NODES_B * 100 }")
    printf "\n  Node-write reduction (A vs B): %s%%  (%d fewer writes)\n" \
        "$REDUCTION" "$((NEW_NODES_B - NEW_NODES_A))"
fi

echo ""
check "Clustered (A=$NEW_NODES_A) writes fewer node objects than scattered (B=$NEW_NODES_B)" \
    "[ '${NEW_NODES_A:-0}' -lt '${NEW_NODES_B:-1}' ]"

echo ""
echo "─── Cross-binary comparison ──────────────────────────────────────────"
echo "  With the pre-RFC-0002 binary, both scenarios produce similar new"
echo "  node counts (no locality — every change traverses independent paths)."
echo ""
echo "  CLOUDSTIC_BIN=/path/to/old-cloudstic $0${1:+ $1}"
echo ""
