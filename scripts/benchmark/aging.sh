#!/usr/bin/env bash
#
# How a repository's read cost grows with the number of backups it has taken.
#
# bench.sh answers "what does one pipeline cost", and its unit of repetition is
# the whole pipeline against a fresh store. That deliberately cannot see the
# effect measured here, which only appears after a repository has been backed up
# many times: each backup seals its own packs, so a snapshot's tree is assembled
# from every backup that contributed an entry to it, and reading it visits all of
# them.
#
# Measured to 7 backups (RFC 0025 §4) that cost is exactly linear — +1 pack, +9
# requests and +1.1 MB per backup, independent of how much churn each backup
# carried. Two things about that were never checked, and both decide what is
# worth building next:
#
#   1. Does it stay linear? 7 backups is a week of daily use. The interesting
#      range is months, where a snapshot spans more packs than the pack body
#      cache can hold (packBodyCacheBudget is 8 packs) and the read stops
#      fitting in cache at all.
#   2. Does anything other than request count move? A term that only costs
#      requests is a bill; one that also grows peak RSS is a wall, and RFC 0023
#      exists to keep memory off the repository-size axis.
#
# The sweep is therefore over backup count rather than tree size, and it holds
# the tree fixed: the variable under test is how many backups contributed to the
# snapshot being read, not how much data there is.
#
# MinIO is required rather than optional. Request count is the measurement —
# wall time on loopback has none of the latency that makes those counts matter,
# and a local store reports no counts at all.
#
# Usage:
#   scripts/benchmark/aging.sh
#   CHECKPOINTS="1 5 10 20 40 80" scripts/benchmark/aging.sh
#   FILES=20000 CHURN=500 scripts/benchmark/aging.sh
#   OPS="restore check" scripts/benchmark/aging.sh

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck source=scripts/benchmark/minio.sh
. "$REPO_ROOT/scripts/benchmark/minio.sh"
# shellcheck source=scripts/benchmark/lib.sh
. "$REPO_ROOT/scripts/benchmark/lib.sh"

# Checkpoints are where a read is measured, expressed as a backup count. They
# are sparse and widening on purpose: the question is the shape of the curve,
# and a linear term is established by its endpoints far more cheaply than by
# every point along it. Each one costs a full restore.
CHECKPOINTS=${CHECKPOINTS:-"1 5 10 20 30 40"}

# Which operations to measure at each checkpoint.
#
# restore and check traverse the same tree but differ in one respect that this
# script exists to expose: #481 groups restore's metadata reads by pack, and
# check still reads in walk order. If the linear term is a re-contact cost the
# two should diverge; if it is a first-contact cost (RFC 0025 §1) they should
# grow in step.
OPS=${OPS:-"restore check"}

FILES=${FILES:-5000}
PROFILE=${PROFILE:-source}
CHURN=${CHURN:-200}
SEED=${SEED:-1}
MAX_BYTES=${MAX_BYTES:-$((2 * 1024 * 1024 * 1024))}
OUT=${OUT:-benchmark-results/aging.csv}
KEEP=${KEEP:-0}
PASSWORD=${BENCH_REPO_PASSWORD:-benchmark-password}

CLOUDSTIC_BIN=${BENCH_CLOUDSTIC_BIN:-"$REPO_ROOT/bin/cloudstic"}
GENTREE_BIN="$REPO_ROOT/bin/gentree"

for tool in docker aws curl bc; do
    command -v "$tool" >/dev/null 2>&1 || { echo "aging.sh needs $tool" >&2; exit 2; }
done

# The highest checkpoint sets how many backups to take; the rest are read off
# along the way. Validated here rather than discovered forty backups in.
TOTAL_BACKUPS=0
for cp in $CHECKPOINTS; do
    case "$cp" in
        '' | *[!0-9]* | 0) echo "CHECKPOINTS must be positive integers, got '$cp'" >&2; exit 2 ;;
    esac
    [ "$cp" -gt "$TOTAL_BACKUPS" ] && TOTAL_BACKUPS=$cp
done
[ "$TOTAL_BACKUPS" -gt 0 ] || { echo "CHECKPOINTS is empty" >&2; exit 2; }

WORK=$(mktemp -d -t cloudstic-aging-XXXXXX)
MINIO_STARTED=0
cleanup() {
    [ "$MINIO_STARTED" = "1" ] && minio_stop
    [ "$KEEP" = "1" ] || rm -rf "$WORK"
    rm -rf "$RESTORE_DIR"
}
trap cleanup EXIT

detect_time() {
    if /usr/bin/time -l true >/dev/null 2>&1; then echo bsd
    elif /usr/bin/time -v true >/dev/null 2>&1; then echo gnu
    else echo none
    fi
}
TIME_FLAVOUR=$(detect_time)
[ "$TIME_FLAVOUR" = none ] && { echo "need BSD or GNU time(1)" >&2; exit 2; }

data="$WORK/data"

store_flags() { minio_store_flags; }

# pack_count reports how many packfiles the repository currently holds.
#
# This is the independent variable the whole script is built around — "how many
# packs does a snapshot span" is the first factor in the cost model, and it is
# read from the store rather than inferred from the backup count, because
# whether the two stay in lockstep is precisely what is in question.
pack_count() {
    aws --endpoint-url "$(minio_endpoint)" \
        s3 ls "s3://$MINIO_BUCKET/bench/$PACK_PREFIX" --recursive 2>/dev/null \
        | grep -c "$PACK_PREFIX" || true
}
PACK_PREFIX="packs/"

# measure_op runs one command and appends a row.
#
# Deliberately its own implementation rather than bench.sh's measure(): that one
# is keyed by the pipeline matrix (profile, size, sample, backend) and writes
# benchreport's schema. The axis here is backup count, and forcing it into those
# columns would misreport it as a size sweep.
measure_op() {
    local op=$1 backups=$2
    shift 2

    local err out
    err="$WORK/stderr"; out="$WORK/stdout"

    local req_before sent_before
    req_before=$(minio_requests)
    sent_before=$(minio_sent_bytes)

    local rc=0
    if [ "$TIME_FLAVOUR" = bsd ]; then
        /usr/bin/time -l "$@" >"$out" 2>"$err" || rc=$?
    else
        /usr/bin/time -v "$@" >"$out" 2>"$err" || rc=$?
    fi
    if [ "$rc" != 0 ]; then
        echo "  $op at $backups backups FAILED (exit $rc)" >&2
        sed 's/^/    /' "$err" >&2
        return 1
    fi

    local peak_kb seconds
    if [ "$TIME_FLAVOUR" = bsd ]; then
        peak_kb=$(( $(grep "maximum resident set size" "$err" | awk '{print $1}') / 1024 ))
        seconds=$(grep -E "real" "$err" | tail -1 | awk '{print $1}')
    else
        peak_kb=$(grep "Maximum resident set size" "$err" | awk '{print $NF}')
        seconds=$(grep "Elapsed (wall clock)" "$err" | awk '{print $NF}' | awk -F: '
            { if (NF == 3) print $1 * 3600 + $2 * 60 + $3;
              else if (NF == 2) print $1 * 60 + $2;
              else print $1 }')
    fi

    # A probe build may write diagnostic lines to stderr that are the whole
    # point of the run. time(1)'s own statistics share that stream, so the
    # command's stderr is captured rather than passed through, and anything
    # interesting in it would be discarded here. Echo what matches instead of
    # making every probe rediscover that.
    if [ -n "${ECHO_STDERR_MATCHING:-}" ]; then
        grep -E "$ECHO_STDERR_MATCHING" "$err" | sed 's/^/      /' || true
    fi

    local requests sent_mb packs peak_mb
    requests=$(( $(minio_requests) - req_before ))
    # $-prefixed for bc, which reads a bare name as its own uninitialized
    # variable and silently yields 0 rather than erroring — the bug that made
    # every bench.sh sample after the first report cumulative totals.
    sent_mb=$(echo "scale=1; ($(minio_sent_bytes) - $sent_before) / 1048576" | bc)
    packs=$(pack_count)
    peak_mb=$(echo "scale=1; $peak_kb / 1024" | bc)

    printf '%d,%d,%s,%s,%s,%d,%s\n' \
        "$backups" "$packs" "$op" "$seconds" "$peak_mb" "$requests" "$sent_mb" >>"$OUT"

    printf '    %-10s %6d packs %7ss %8s MB peak %6d req %8s MB sent\n' \
        "$op" "$packs" "$seconds" "$peak_mb" "$requests" "$sent_mb"
}

# read_checkpoint measures every operation in OPS against the current
# repository, always reading the *latest* snapshot.
#
# Restoring the latest is the case that ages. An old snapshot restores at its
# original cost forever, because its entries all still sit in the packs the
# backup that wrote them sealed — measured in RFC 0025 §4, and the reason the
# cost is "how many backups contributed to this snapshot" rather than "how many
# packs the repository has".
read_checkpoint() {
    local backups=$1 flags target
    flags=$(store_flags)

    for op in $OPS; do
        case "$op" in
            restore)
                target=$(fresh_restore_target)
                rm -rf "$target"; mkdir -p "$target"
                measure_op restore "$backups" \
                    "$CLOUDSTIC_BIN" restore $flags -output "$target" latest -quiet
                rm -rf "$target"
                ;;
            check)
                measure_op check "$backups" "$CLOUDSTIC_BIN" check $flags -quiet
                ;;
            *)
                echo "unknown op '$op' in OPS" >&2; return 2 ;;
        esac
    done
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

# An explicit BENCH_CLOUDSTIC_BIN is used as given and never rebuilt.
#
# The reason is specific to this script: its whole use is comparing a probe
# build — a constant changed, a policy swapped — against the baseline over the
# same aging curve. Rebuilding from the working tree would quietly replace that
# probe with whatever the source currently says and report the result as the
# probe's, which is a wrong answer rather than a failed run.
if [ -n "${BENCH_CLOUDSTIC_BIN:-}" ]; then
    [ -x "$CLOUDSTIC_BIN" ] || { echo "BENCH_CLOUDSTIC_BIN is not executable: $CLOUDSTIC_BIN" >&2; exit 2; }
    echo "Using prebuilt $CLOUDSTIC_BIN (not rebuilding)"
else
    echo "Building cloudstic..."
    go build -o "$CLOUDSTIC_BIN" "$REPO_ROOT/cmd/cloudstic"
fi
go build -o "$GENTREE_BIN" "$REPO_ROOT/internal/cmd/gentree"

echo "Generating $PROFILE tree of $FILES files..."
"$GENTREE_BIN" -out "$data" -profile "$PROFILE" -files "$FILES" \
    -seed "$SEED" -max-bytes "$MAX_BYTES" >/dev/null

echo "Starting MinIO..."
minio_start || { echo "could not start minio" >&2; exit 1; }
MINIO_STARTED=1
minio_reset_bucket
export AWS_ACCESS_KEY_ID="$MINIO_USER" AWS_SECRET_ACCESS_KEY="$MINIO_PASSWORD"
export CLOUDSTIC_PASSWORD="$PASSWORD"

mkdir -p "$(dirname "$OUT")"
echo "backups,packs,op,seconds,peak_mb,requests,sent_mb" >"$OUT"

flags=$(store_flags)
"$CLOUDSTIC_BIN" init $flags -quiet

echo ""
echo "Aging to $TOTAL_BACKUPS backups, reading at: $CHECKPOINTS"
echo ""

backups=0
while [ "$backups" -lt "$TOTAL_BACKUPS" ]; do
    # Churn before every backup after the first, so backup N+1 has something to
    # write and seals a pack of its own. The seed advances with the backup
    # number: the same files changing every round would rewrite one region of
    # the tree repeatedly and understate how far a snapshot's entries scatter.
    if [ "$backups" -gt 0 ]; then
        "$GENTREE_BIN" -churn "$data" -profile "$PROFILE" \
            -seed "$(( SEED * 1000 + backups ))" -count "$CHURN" \
            -max-bytes "$MAX_BYTES" >/dev/null
    fi

    # Aging backups are the setup, not the measurement — only the reads at each
    # checkpoint are recorded, so their summaries would be forty screens of
    # output nobody reads. Kept on failure, where they are the diagnosis.
    if ! "$CLOUDSTIC_BIN" backup $flags -source "local:$data" -quiet >"$WORK/backup.log" 2>&1; then
        echo "backup $(( backups + 1 )) failed" >&2
        sed 's/^/    /' "$WORK/backup.log" >&2
        exit 1
    fi
    backups=$(( backups + 1 ))

    for cp in $CHECKPOINTS; do
        if [ "$cp" = "$backups" ]; then
            echo "  after $backups backup(s):"
            read_checkpoint "$backups"
        fi
    done
done

echo ""
echo "Wrote $OUT"
