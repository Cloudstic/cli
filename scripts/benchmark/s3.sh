#!/usr/bin/env bash
#
# What each operation costs against an object store, in requests and bytes.
#
# memory.sh measures peak RSS and allocation against a local directory. Those
# are the right numbers for a laptop and the wrong ones for a remote backend,
# where request latency sets the pace and egress is billed. The two can move in
# opposite directions: sizing the pack body cache below the working set left
# memory almost unchanged while causing 4.2 GB of re-read traffic (#458), and
# nothing in the local benchmark could see it.
#
# So this reports requests per API and bytes transferred, per operation, against
# MinIO. MinIO on loopback has none of S3's latency, which is deliberate: the
# counts are what transfer to a real backend, the timings are a sanity check.
#
# Usage:
#   scripts/benchmark/s3.sh                          # defaults
#   FILES=20000 PROFILE=mixed scripts/benchmark/s3.sh
#   OUT=/tmp/s3.csv scripts/benchmark/s3.sh
#
# Requires docker and the aws CLI.

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck source=scripts/benchmark/minio.sh
. "$REPO_ROOT/scripts/benchmark/minio.sh"

FILES=${FILES:-5000}
PROFILE=${PROFILE:-source}
MAX_BYTES=${MAX_BYTES:-$((512 * 1024 * 1024))}
OUT=${OUT:-benchmark-results/s3.csv}
KEEP=${KEEP:-0}
PASSWORD=${BENCH_REPO_PASSWORD:-benchmark-password}

CLOUDSTIC_BIN="$REPO_ROOT/bin/cloudstic"
GENTREE_BIN="$REPO_ROOT/bin/gentree"

WORK=$(mktemp -d -t cloudstic-s3-XXXXXX)

# The container outlives the shell unless it is removed explicitly, and it holds
# the repository as well, so cleanup covers both. --rm on docker run handles the
# normal path; this covers the rest.
cleanup() {
    minio_stop
    [ "$KEEP" = "1" ] || rm -rf "$WORK"
}
trap cleanup EXIT

for tool in docker aws curl; do
    command -v "$tool" >/dev/null 2>&1 || { echo "s3.sh needs $tool" >&2; exit 2; }
done

echo "Building..."
( cd "$REPO_ROOT" && go build -o bin/cloudstic ./cmd/cloudstic )
( cd "$REPO_ROOT" && go build -o bin/gentree ./scripts/benchmark/gentree )

echo "Starting MinIO..."
minio_start || exit 1

mkdir -p "$(dirname "$OUT")"
echo "operation,files,profile,seconds,requests,sent_mb,by_api" >"$OUT"

export CLOUDSTIC_PASSWORD="$PASSWORD"
STORE_FLAGS=$(minio_store_flags)

data="$WORK/data"
restore="$WORK/restore"
mkdir -p "$data" "$restore"

printf 'Generating %d files (%s)...\n' "$FILES" "$PROFILE"
"$GENTREE_BIN" -out "$data" -profile "$PROFILE" -files "$FILES" \
    -seed 1 -max-bytes "$MAX_BYTES"

# measure <operation> <command...>
#
# Requests and bytes are read as deltas around the command. They are cumulative
# counters on a container this script owns, so nothing else moves them.
measure() {
    local op=$1
    shift

    local req_before sent_before start end
    req_before=$(minio_requests)
    sent_before=$(minio_sent_bytes)
    start=$(date +%s)

    "$@" >/dev/null 2>&1 || { echo "  $op FAILED" >&2; return 1; }

    end=$(date +%s)
    local req sent api
    req=$(( $(minio_requests) - req_before ))
    sent=$(( $(minio_sent_bytes) - sent_before ))
    api=$(minio_requests_by_api | tr ' ' ';' | sed 's/;$//')

    printf '%s,%d,%s,%d,%d,%.1f,%s\n' \
        "$op" "$FILES" "$PROFILE" "$((end - start))" "$req" \
        "$(echo "scale=1; $sent / 1048576" | bc)" "$api" >>"$OUT"
    printf '  %-20s %6ds  %7d requests  %8.1f MB sent\n' \
        "$op" "$((end - start))" "$req" "$(echo "scale=1; $sent / 1048576" | bc)"
}

echo ""
echo "=== requests and bytes per operation ==="
minio_reset_bucket

measure init     "$CLOUDSTIC_BIN" init $STORE_FLAGS -quiet
measure backup   "$CLOUDSTIC_BIN" backup $STORE_FLAGS -source "local:$data" -quiet

"$GENTREE_BIN" -churn "$data" -profile "$PROFILE" -seed 2 -fraction 0.05 -max-bytes "$MAX_BYTES" >/dev/null
measure backup-incremental "$CLOUDSTIC_BIN" backup $STORE_FLAGS -source "local:$data" -quiet

measure check    "$CLOUDSTIC_BIN" check $STORE_FLAGS -quiet
measure restore  "$CLOUDSTIC_BIN" restore $STORE_FLAGS -output "$restore" latest -quiet
measure prune    "$CLOUDSTIC_BIN" prune $STORE_FLAGS -quiet

echo ""
echo "Wrote $OUT"
