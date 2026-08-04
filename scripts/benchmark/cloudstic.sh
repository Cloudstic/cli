#!/usr/bin/env bash
#
# Cloudstic on its own terms.
#
# This is what CI runs, and it is deliberately not compare.sh with a tool
# filter. The two answer different questions and the answers pull in different
# directions: compare.sh needs a dataset that is *fair across four tools* and
# can only measure what all of them expose, while this one is free to stress
# what this design actually claims — deduplication, compressibility,
# incremental cost — and to report numbers no other tool has.
#
# Keeping them apart is what lets a trend line survive. A dataset tweak made so
# borg is not unfairly penalised must not move the numbers CI has been tracking
# for months.
#
# Usage:
#   scripts/benchmark/cloudstic.sh              # local store
#   scripts/benchmark/cloudstic.sh s3           # S3 store
#   BENCH_CSV=/tmp/x.csv scripts/benchmark/cloudstic.sh

set -euo pipefail

STORE=${1:-local}
if [ "$STORE" != "local" ] && [ "$STORE" != "s3" ]; then
    echo "Usage: $0 [local|s3]" >&2
    exit 1
fi

S3_BUCKET=${S3_BUCKET:-cloudstic-benchmark-734836384094-us-east-1}
REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
PASSWORD=${BENCH_REPO_PASSWORD:-benchmark-password}

# shellcheck source=scripts/benchmark/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

WORK=$(mktemp -d -t cloudstic-bench-XXXXXX)
cleanup() {
    rm -rf "$WORK" "$RESTORE_DIR"
}
trap cleanup EXIT

# A pre-set variable in an operator's shell must not change what is measured.
# CLOUDSTIC_KMS_KEY_ARN in particular would send a local/local run to AWS KMS.
unset CLOUDSTIC_KMS_KEY_ARN CLOUDSTIC_KMS_REGION CLOUDSTIC_KMS_ENDPOINT
unset CLOUDSTIC_PROFILE CLOUDSTIC_PROFILES_FILE
unset CLOUDSTIC_STORE CLOUDSTIC_SOURCE
export CLOUDSTIC_PASSWORD="$PASSWORD"

echo "Building cloudstic..."
( cd "$REPO_ROOT" && go build -o /tmp/cloudstic ./cmd/cloudstic )
CLOUDSTIC_BIN="/tmp/cloudstic"

bench_init "${BENCH_CSV:-benchmark-results/cloudstic.csv}"

# ---------------------------------------------------------------------------
# Dataset
# ---------------------------------------------------------------------------
#
# Mixed on purpose, because the operations below stress different parts of it:
# incompressible bulk exercises chunking and upload, a compressible file
# exercises zstd, and a few thousand small files exercise the metadata path
# that dominates a real home directory.
#
# This dataset belongs to this script. compare.sh has its own, and neither may
# be changed to suit the other.

DATA="$WORK/data"
mkdir -p "$DATA/docs" "$DATA/config"

echo "Generating dataset..."
dd if=/dev/urandom of="$DATA/random.dat" bs=1m count=300 status=none
dd if=/dev/zero    of="$DATA/zero.dat"   bs=1m count=300 status=none
for i in $(seq 1 40); do
    dd if=/dev/urandom of="$DATA/docs/doc_$i.bin" bs=1m count=1 status=none
done
for (( i = 0; i < 3000; i++ )); do
    printf 'cloudstic benchmark config entry %d\n' "$i" > "$DATA/config/setting_$i.cfg"
done
echo "Dataset: $(du -sh "$DATA" | cut -f1 | xargs)"
echo ""

# ---------------------------------------------------------------------------
# Store
# ---------------------------------------------------------------------------

if [ "$STORE" = "s3" ]; then
    eval "$(aws configure export-credentials --format env 2>/dev/null)" || true
    BENCH_S3_PREFIX="s3://$S3_BUCKET/cloudstic-self/"
    STORE_FLAGS="-store s3:$S3_BUCKET/cloudstic-self/"
    aws s3 rm "$BENCH_S3_PREFIX" --recursive >/dev/null 2>&1 || true
else
    BENCH_REPO_DIR="$WORK/repo"
    mkdir -p "$BENCH_REPO_DIR"
    STORE_FLAGS="-store local:$BENCH_REPO_DIR"
fi

# shellcheck disable=SC2086 # STORE_FLAGS is intentionally word-split
$CLOUDSTIC_BIN init $STORE_FLAGS -quiet >/dev/null

SOURCE_FLAGS="-source local:$DATA"

echo "### Cloudstic ($STORE store)"
print_table_header

# shellcheck disable=SC2086
backup() { run_bench "$1" $CLOUDSTIC_BIN backup $STORE_FLAGS $SOURCE_FLAGS -quiet; }

backup "Initial Backup"
backup "Incremental (No Changes)"

echo "modified" >> "$DATA/config/setting_1.cfg"
backup "Incremental (1 File Changed)"

# A thousand changed files is the case a per-file cost would show up in, where
# a single changed file cannot.
for (( i = 0; i < 1000; i++ )); do
    printf 'cloudstic benchmark config entry %d modified\n' "$i" > "$DATA/config/setting_$i.cfg"
done
backup "Incremental (1000 Changed)"

mkdir -p "$DATA/new_data"
dd if=/dev/urandom of="$DATA/new_data/extra.dat" bs=1m count=200 status=none
backup "Add 200MB New Data"

# Copies of data already stored. Repo Added is the number that matters here:
# content addressing should make this close to free, and the column says so
# directly rather than leaving it inferred from elapsed time.
cp -r "$DATA/docs" "$DATA/docs_copy"
cp "$DATA/random.dat" "$DATA/random_copy.dat"
backup "Deduplicated Backup"

# shellcheck disable=SC2086
run_bench "Check" $CLOUDSTIC_BIN check $STORE_FLAGS -quiet
# shellcheck disable=SC2086
run_bench "Full Restore" $CLOUDSTIC_BIN restore $STORE_FLAGS \
    -output "$(fresh_restore_target)" -format dir -quiet
# Restore without the per-file content-hash check, which separates what
# verification costs from what fetching and writing cost.
# shellcheck disable=SC2086
run_bench "Full Restore (-no-verify)" $CLOUDSTIC_BIN restore $STORE_FLAGS \
    -output "$(fresh_restore_target)" -format dir -no-verify -quiet
# shellcheck disable=SC2086
run_bench "Prune" $CLOUDSTIC_BIN prune $STORE_FLAGS -quiet

print_repo_size

# The stored-to-logical ratio is this product's headline claim and no other
# tool reports it comparably, so it belongs here rather than in compare.sh.
logical_kb=$(du -sk "$DATA" | awk '{print $1}')
stored_kb=$(get_repo_size_kb)
if [ "$stored_kb" -gt 0 ]; then
    printf "| %-30s | %12s | %13s | %12s |\n" "Logical / Stored" \
        "$(format_size_kb "$logical_kb")" "$(format_size_kb "$stored_kb")" \
        "$(echo "scale=2; $logical_kb / $stored_kb" | bc)x"
fi

echo ""
echo "Done. CSV: $BENCH_CSV"
