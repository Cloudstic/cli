#!/usr/bin/env bash
#
# One pass over the pipeline, every metric it can yield.
#
# This replaces cloudstic.sh, memory.sh and s3.sh, which each ran substantially
# the same operations and collected a different subset of the numbers: timing
# and repository growth, peak RSS and allocation, requests and bytes. Nothing
# forced those into separate passes — time and peak RSS come from time(1),
# allocation from the binary's own -memstats, requests and bytes from the
# backend — so running three sweeps to get three columns was paying three times
# for one answer, and every new metric family made it worse.
#
# The matrix is profile x size x backend x sample. A cell runs the pipeline once
# and emits every column available to it; the S3 columns are blank on a local
# backend, which is the only thing that varies by cell.
#
# Backend stays an axis rather than folding into the metrics. Pointing at MinIO
# changes what is being measured, not just how — the S3 SDK's buffers and HTTP
# stack are part of the process under test — so its peak RSS is a different
# number from the local one rather than a more complete version of it.
#
# compare.sh is deliberately not folded in. It answers a different question
# against a dataset constrained to be fair across four tools, and it drives
# restic, borg and duplicacy as well as this one.
#
# Usage:
#   scripts/benchmark/bench.sh
#   PROFILES="source mixed" SIZES="5000 50000" scripts/benchmark/bench.sh
#   BACKENDS="local minio" scripts/benchmark/bench.sh
#   SAMPLES=1 SIZES=2000 scripts/benchmark/bench.sh          # quick check

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck source=scripts/benchmark/minio.sh
. "$REPO_ROOT/scripts/benchmark/minio.sh"
# shellcheck source=scripts/benchmark/lib.sh
. "$REPO_ROOT/scripts/benchmark/lib.sh"

PROFILES=${PROFILES:-source}
SIZES=${SIZES:-"5000 20000 50000"}
BACKENDS=${BACKENDS:-local}
SAMPLES=${SAMPLES:-3}
MAX_BYTES=${MAX_BYTES:-$((2 * 1024 * 1024 * 1024))}
OUT=${OUT:-benchmark-results/bench.csv}
KEEP=${KEEP:-0}
PASSWORD=${BENCH_REPO_PASSWORD:-benchmark-password}

CLOUDSTIC_BIN=${BENCH_CLOUDSTIC_BIN:-"$REPO_ROOT/bin/cloudstic"}
GENTREE_BIN="$REPO_ROOT/bin/gentree"

case "$SAMPLES" in
    '' | *[!0-9]* | 0) echo "SAMPLES must be a positive integer, got '$SAMPLES'" >&2; exit 2 ;;
esac

WORK=$(mktemp -d -t cloudstic-bench-XXXXXX)
MINIO_STARTED=0
cleanup() {
    [ "$MINIO_STARTED" = "1" ] && minio_stop
    [ "$KEEP" = "1" ] || rm -rf "$WORK"
    # RESTORE_DIR is created by sourcing lib.sh and unused here; bench.sh keeps
    # its own restore scratch space under $WORK.
    rm -rf "$RESTORE_DIR"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Peak RSS, portably
# ---------------------------------------------------------------------------

# time(1) reports maximum resident set size in different units under different
# labels per platform: BSD/macOS -l in bytes, GNU -v in kilobytes.
detect_time() {
    if /usr/bin/time -l true >/dev/null 2>&1; then echo bsd
    elif /usr/bin/time -v true >/dev/null 2>&1; then echo gnu
    else echo none
    fi
}
TIME_FLAVOUR=$(detect_time)
[ "$TIME_FLAVOUR" = none ] && { echo "need BSD or GNU time(1)" >&2; exit 2; }

for tool in bc; do
    command -v "$tool" >/dev/null 2>&1 || { echo "bench.sh needs $tool" >&2; exit 2; }
done

# ---------------------------------------------------------------------------
# Measurement
# ---------------------------------------------------------------------------

# measure <operation> <command...>
#
# Runs the command once and records every column. Peak RSS and wall time come
# from time(1), allocation from the memstats file the binary writes, and the S3
# columns from the backend's own counters when there is one.
measure() {
    local op=$1
    shift

    local err out stats
    err="$WORK/stderr"; out="$WORK/stdout"; stats="$WORK/memstats.json"
    rm -f "$stats"

    local req_before sent_before
    if [ "$backend" = minio ]; then
        req_before=$(minio_requests)
        sent_before=$(minio_sent_bytes)
        minio_api_counts >"$WORK/api-before"
    fi

    local repo_kb_before
    repo_kb_before=$(get_repo_size_kb)

    local rc=0
    if [ "$TIME_FLAVOUR" = bsd ]; then
        /usr/bin/time -l "$@" -memstats "$stats" >"$out" 2>"$err" || rc=$?
    else
        /usr/bin/time -v "$@" -memstats "$stats" >"$out" 2>"$err" || rc=$?
    fi
    if [ "$rc" != 0 ]; then
        echo "  $op FAILED (exit $rc)" >&2
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

    # Allocation is what the process asked for over its life, freed or not. Peak
    # RSS is the largest amount live at once. A change can move one and not the
    # other, which is the whole reason both are here.
    local alloc_mb=""
    if [ -f "$stats" ]; then
        alloc_mb=$(awk -F'[:,]' '/total_alloc_bytes/ { gsub(/[^0-9]/, "", $2); printf "%.1f", $2 / 1048576 }' "$stats")
    fi

    local requests="" sent_mb="" by_api=""
    if [ "$backend" = minio ]; then
        requests=$(( $(minio_requests) - req_before ))
        sent_mb=$(echo "scale=1; ($(minio_sent_bytes) - sent_before) / 1048576" | bc)
        minio_api_counts >"$WORK/api-after"
        by_api=$(minio_api_delta "$WORK/api-before" "$WORK/api-after")
    fi

    # Repository growth, using lib.sh's get_repo_size_kb rather than a second
    # implementation — it already handles both a local directory and an S3
    # prefix, which matters now that a MinIO backend exists alongside local.
    local repo_kb_after repo_delta
    repo_kb_after=$(get_repo_size_kb)
    repo_delta=$(format_size_kb $(( repo_kb_after - repo_kb_before )))

    printf 'cloudstic,%s,%s,%d,%d,%s,%s,%.1f,%s,%s,%s,%s,%s\n' \
        "$backend" "$profile" "$size" "$sample" "$op" \
        "$seconds" "$(echo "scale=1; $peak_kb / 1024" | bc)" \
        "$alloc_mb" "$requests" "$sent_mb" "$by_api" "$repo_delta" >>"$(backend_csv "$backend")"

    printf '    %-20s %7ss %8.1f MB peak' "$op" "$seconds" "$(echo "scale=1; $peak_kb / 1024" | bc)"
    [ -n "$alloc_mb" ] && printf ' %9s MB alloc' "$alloc_mb"
    [ -n "$requests" ] && printf ' %6s req %8s MB sent' "$requests" "$sent_mb"
    printf ' %10s repo' "$repo_delta"
    printf '\n'
}

# ---------------------------------------------------------------------------
# Pipeline
# ---------------------------------------------------------------------------

# store_flags prints the flags pointing at the backend for this cell.
store_flags() {
    if [ "$backend" = minio ]; then
        minio_store_flags
    else
        echo "-store local:$WORK/repo"
    fi
}

# reset_store empties the backend and points get_repo_size_kb at it, so
# repository-growth tracking (measure(), measure_summary()) sees the right
# place for whichever backend this cell is measuring against.
reset_store() {
    if [ "$backend" = minio ]; then
        minio_reset_bucket
        BENCH_REPO_DIR=""
        BENCH_S3_PREFIX="s3://$MINIO_BUCKET/bench/"
        BENCH_S3_ENDPOINT=$(minio_endpoint)
        export AWS_ACCESS_KEY_ID="$MINIO_USER" AWS_SECRET_ACCESS_KEY="$MINIO_PASSWORD"
    else
        rm -rf "$WORK/repo"; mkdir -p "$WORK/repo"
        BENCH_REPO_DIR="$WORK/repo"
        BENCH_S3_PREFIX=""
        BENCH_S3_ENDPOINT=""
    fi
}

# measure_summary appends the two rows that describe the whole pipeline rather
# than one operation in it: the repository's final size, and how that compares
# to the logical size of the tree that was backed up — the headline
# deduplication and compression number. Neither is a timed operation, so
# seconds and peak_mb are left at zero; benchreport renders them in their own
# section rather than the per-operation tables.
measure_summary() {
    local stored_kb logical_kb stored_fmt logical_fmt
    stored_kb=$(get_repo_size_kb)
    logical_kb=$(du -sk "$data" | awk '{print $1}')
    stored_fmt=$(format_size_kb "$stored_kb")
    logical_fmt=$(format_size_kb "$logical_kb")

    printf 'cloudstic,%s,%s,%d,%d,Final Repo Size,0,0,,,,,%s\n' \
        "$backend" "$profile" "$size" "$sample" "$stored_fmt" >>"$(backend_csv "$backend")"
    printf '    %-20s %s\n' "Final Repo Size" "$stored_fmt"

    if [ "$stored_kb" -gt 0 ]; then
        local ratio
        ratio=$(echo "scale=2; $logical_kb / $stored_kb" | bc)
        printf 'cloudstic,%s,%s,%d,%d,Logical / Stored,0,0,,,,,%s / %s (%sx)\n' \
            "$backend" "$profile" "$size" "$sample" "$logical_fmt" "$stored_fmt" "$ratio" \
            >>"$(backend_csv "$backend")"
        printf '    %-20s %s / %s (%sx)\n' "Logical / Stored" "$logical_fmt" "$stored_fmt" "$ratio"
    fi
}

# run_pipeline measures one sample.
#
# The unit of repetition is the whole pipeline, not the command: re-running
# prune against an already-pruned repository, or an initial backup against a
# populated one, measures a different operation that happens to share a name.
#
# The sequence mirrors what a real repository sees over time: an initial
# backup, then the incrementals that follow it (nothing changed, a single
# edit, a bounded batch of edits), then growth (new data), then the case that
# separates this design from a naive copy (content it already has).
run_pipeline() {
    local flags restore restore_no_verify
    flags=$(store_flags)
    restore="$WORK/restore"
    restore_no_verify="$WORK/restore-no-verify"

    reset_store
    rm -rf "$restore" "$restore_no_verify"
    mkdir -p "$restore" "$restore_no_verify"

    measure init   "$CLOUDSTIC_BIN" init $flags -quiet
    measure backup "$CLOUDSTIC_BIN" backup $flags -source "local:$data" -quiet

    # The cheapest possible incremental: nothing in the source changed. Shows
    # whether scanning cost is proportional to the tree or to the churn.
    measure backup-incremental-noop "$CLOUDSTIC_BIN" backup $flags -source "local:$data" -quiet

    # An exact count rather than a fraction: "1 file" and "1000 changed" have
    # to mean the same thing at every tree size, which -fraction cannot
    # express without its shape changing with the tree.
    "$GENTREE_BIN" -churn "$data" -profile "$profile" -seed "$(( sample * 100 + 1 ))" \
        -count 1 -max-bytes "$MAX_BYTES" >/dev/null
    measure backup-incremental-1 "$CLOUDSTIC_BIN" backup $flags -source "local:$data" -quiet

    "$GENTREE_BIN" -churn "$data" -profile "$profile" -seed "$(( sample * 100 + 2 ))" \
        -count 1000 -max-bytes "$MAX_BYTES" >/dev/null
    measure backup-incremental-1000 "$CLOUDSTIC_BIN" backup $flags -source "local:$data" -quiet

    # Growth rather than modification: brand new content the repository has
    # never seen, sized to a fixed budget independent of the sweep's tree size.
    #
    # -max-bytes only ever scales a tree *down* to fit a budget (see
    # fitToBudget in gentree/main.go) — it never pads a naturally smaller one
    # up to fill it, which is the right behaviour for the main dataset but
    # means the file count here has to be large enough that every profile's
    # natural size already clears 200MB before scaling kicks in. 20000 clears
    # it for the smallest-mean profile (source, ~18KB/file) with margin to
    # spare for mixed and media, whose means are larger still.
    #
    # $data is regenerated once per (profile, size) and reused across every
    # sample and backend, so this directory already exists from an earlier
    # iteration; removing it first keeps -out writing a fresh, deterministic
    # tree instead of layering another batch of names into it.
    rm -rf "$data/bench-added-200mb"
    "$GENTREE_BIN" -out "$data/bench-added-200mb" -profile "$profile" -files 20000 \
        -seed "$sample" -max-bytes $(( 200 * 1024 * 1024 )) >/dev/null
    measure backup-add-200mb "$CLOUDSTIC_BIN" backup $flags -source "local:$data" -quiet

    # Backing up content the repository already holds, end to end: the repo
    # should barely grow even though a whole directory's worth of bytes was
    # logically presented to it again.
    #
    # Removed first for the same reason as above: cp -R into an already-
    # existing directory nests a copy inside it rather than replacing it,
    # which would otherwise grow one directory deeper every sample.
    # Piping sort straight into `head -1` lets head close the pipe the moment
    # it has its first line; once the tree is big enough that sort's full
    # output does not fit in one pipe write, sort's next write gets SIGPIPE
    # and pipefail fails the whole script. Capturing the whole listing first
    # — a single command substitution waits for the pipeline to finish rather
    # than closing it early — and then taking the first line with a bash
    # parameter expansion avoids the pipe entirely for that step.
    local all_dirs dedup_src
    all_dirs=$(find "$data" -mindepth 1 -maxdepth 1 -type d | sort)
    dedup_src=${all_dirs%%$'\n'*}
    rm -rf "$data/bench-dedup-copy"
    cp -R "$dedup_src" "$data/bench-dedup-copy"
    measure backup-dedup "$CLOUDSTIC_BIN" backup $flags -source "local:$data" -quiet

    measure check   "$CLOUDSTIC_BIN" check $flags -quiet
    measure restore "$CLOUDSTIC_BIN" restore $flags -output "$restore" latest -quiet
    # Restore without the per-file content-hash check, which separates what
    # verification costs from what fetching and writing cost.
    measure restore-no-verify "$CLOUDSTIC_BIN" restore $flags \
        -output "$restore_no_verify" -no-verify latest -quiet
    measure prune "$CLOUDSTIC_BIN" prune $flags -quiet

    measure_summary
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

echo "Building..."
( cd "$REPO_ROOT" && go build -o bin/cloudstic ./cmd/cloudstic )
( cd "$REPO_ROOT" && go build -o bin/gentree ./internal/cmd/gentree )

for backend in $BACKENDS; do
    if [ "$backend" = minio ]; then
        for tool in docker aws curl; do
            command -v "$tool" >/dev/null 2>&1 || { echo "backend minio needs $tool" >&2; exit 2; }
        done
        echo "Starting MinIO..."
        minio_start || exit 1
        MINIO_STARTED=1
    fi
done

mkdir -p "$(dirname "$OUT")"

# One CSV per backend when several are asked for.
#
# benchreport groups rows by operation and has no notion of a backend, so a
# single file holding both would have it average a local run against a MinIO one
# and present the result as repeated samples of the same thing. Splitting keeps
# every file something the renderer reads correctly.
backend_csv() {
    if [ "$(echo "$BACKENDS" | wc -w | tr -d ' ')" -gt 1 ]; then
        echo "${OUT%.csv}-$1.csv"
    else
        echo "$OUT"
    fi
}

# Column names match what benchreport.Parse looks up — it is header-name driven
# and ignores what it does not recognise, so backend, profile, sample and the S3
# columns ride along without the renderer needing to know about them. "tool" is
# constant here and present only because the renderer labels rows with it.
for backend in $BACKENDS; do
    echo "tool,backend,profile,scale,sample,operation,seconds,peak_mb,alloc_mb,requests,sent_mb,by_api,repo_delta" >"$(backend_csv "$backend")"
done

export CLOUDSTIC_PASSWORD="$PASSWORD"

# Record what produced these numbers, next to them.
#
# Timings and peak RSS are properties of the machine as much as of the code, so
# a CSV without its hardware is not comparable to another CSV — and the two get
# compared anyway once they are both in a directory called benchmark-results.
# Written as a sidecar rather than a column because it is constant per run.
env_file="${OUT%.csv}-env.txt"
{
    echo "date:     $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "goos:     $(go env GOOS)"
    echo "goarch:   $(go env GOARCH)"
    echo "go:       $(go version | awk '{print $3}')"
    echo "cpus:     $(getconf _NPROCESSORS_ONLN 2>/dev/null || echo '?')"
    if [ "$(uname)" = Darwin ]; then
        echo "cpu:      $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo '?')"
        echo "memory:   $(( $(sysctl -n hw.memsize 2>/dev/null || echo 0) / 1073741824 )) GB"
    else
        echo "cpu:      $(awk -F: '/model name/ { print $2; exit }' /proc/cpuinfo 2>/dev/null | sed 's/^ *//' || echo '?')"
        echo "memory:   $(( $(awk '/MemTotal/ { print $2 }' /proc/meminfo 2>/dev/null || echo 0) / 1048576 )) GB"
    fi
    echo "commit:   $(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo '?')"
    echo "profiles: $PROFILES"
    echo "sizes:    $SIZES"
    echo "backends: $BACKENDS"
    echo "samples:  $SAMPLES"
} >"$env_file"
sed 's/^/  /' "$env_file"

echo ""
echo "profiles: $PROFILES   sizes: $SIZES   backends: $BACKENDS   samples: $SAMPLES"
echo ""

for profile in $PROFILES; do
    for size in $SIZES; do
        data="$WORK/data"
        rm -rf "$data"; mkdir -p "$data"

        # Generated once per (profile, size) and reused across samples and
        # backends. Regenerating would add dataset variation to the very noise
        # the samples exist to measure; the tree is an input, not part of what
        # is under test.
        printf -- '--- %s, %d files: generating...\n' "$profile" "$size"
        "$GENTREE_BIN" -out "$data" -profile "$profile" -files "$size" \
            -seed 1 -max-bytes "$MAX_BYTES" | sed 's/^/    /'

        for backend in $BACKENDS; do
            for (( sample = 1; sample <= SAMPLES; sample++ )); do
                echo "  $backend, sample $sample/$SAMPLES"
                run_pipeline
            done
        done
    done
done

echo ""
for backend in $BACKENDS; do
    echo "Wrote $(backend_csv "$backend")"
done
echo "Wrote $env_file"
