#!/usr/bin/env bash
#
# Shared measurement mechanics for the benchmark scripts.
#
# What lives here is deliberately limited to *how* a step is measured and
# reported — timing, peak RSS, repository growth, the table. The datasets do
# not, and that separation is the point: compare.sh's dataset exists to be fair
# across four tools, cloudstic.sh's exists to stress this one. Sharing them
# would mean a change made for cross-tool fairness silently moved the numbers
# CI tracks over time, which is the coupling the two scripts were split to
# avoid.
#
# Source this after setting REPO_ROOT. It expects bash 3.2 — still what macOS
# and the runners ship.

# BENCH_REPO_DIR (local) or BENCH_S3_PREFIX (s3://bucket/prefix/) enable
# repository-growth tracking. A script sets whichever applies before its first
# run_bench.
BENCH_REPO_DIR=""
BENCH_S3_PREFIX=""

# CURRENT_TOOL labels rows in the CSV. compare.sh reassigns it per tool;
# cloudstic.sh leaves it alone.
CURRENT_TOOL="cloudstic"

# Scratch directory for restore targets. Restore is measured on a fresh, empty
# directory every time, and the result is discarded before the next step so a
# partially-restored tree can never make a later step look faster.
RESTORE_DIR=$(mktemp -d -t benchmark-restore-XXXXXX)

# bench_init <csv-path>
#
# Builds the reporting binary and starts a fresh CSV. benchreport owns the
# schema so the harnesses cannot drift into two different formats.
bench_init() {
    BENCH_CSV=$1
    go build -o /tmp/benchreport "$REPO_ROOT/internal/cmd/benchreport"
    BENCHREPORT_BIN="/tmp/benchreport"
    mkdir -p "$(dirname "$BENCH_CSV")"
    rm -f "$BENCH_CSV"
    export BENCH_CSV BENCHREPORT_BIN
}

fresh_restore_target() {
    rm -rf "${RESTORE_DIR:?}"/*
    echo "$RESTORE_DIR/out"
}

format_size_kb() {
    local kb=$1
    if [ "$kb" -lt 1024 ]; then
        echo "${kb} KB"
    elif [ "$kb" -lt 1048576 ]; then
        echo "$(echo "scale=1; $kb / 1024" | bc) MB"
    else
        echo "$(echo "scale=2; $kb / 1048576" | bc) GB"
    fi
}

get_repo_size_kb() {
    if [ -n "$BENCH_REPO_DIR" ] && [ -d "$BENCH_REPO_DIR" ]; then
        du -sk "$BENCH_REPO_DIR" | awk '{print $1}'
    elif [ -n "$BENCH_S3_PREFIX" ]; then
        local bytes
        bytes=$(aws s3 ls "$BENCH_S3_PREFIX" --summarize --recursive 2>/dev/null \
            | grep "Total Size" | awk '{print $3}')
        echo $(( ${bytes:-0} / 1024 ))
    else
        echo 0
    fi
}

# run_bench <step name> <command...>
#
# A failing step is reported and the run continues. That is deliberate and is
# why this does not use `benchreport run`, which exits: compare.sh measures four
# tools, and one that is missing or broken has to leave a gap rather than
# abandon the comparison.
run_bench() {
    local step_name="$1"
    shift
    local stdout_file stderr_file
    stdout_file=$(mktemp)
    stderr_file=$(mktemp)

    local size_before
    size_before=$(get_repo_size_kb)

    if /usr/bin/time -l "$@" > "$stdout_file" 2> "$stderr_file"; then
        local real_time mem_bytes mem_mb
        real_time=$(grep "real" "$stderr_file" | awk '{print $1}')
        mem_bytes=$(grep "maximum resident set size" "$stderr_file" | awk '{print $1}')
        mem_mb=$(echo "scale=2; $mem_bytes / 1024 / 1024" | bc)

        local repo_added="-"
        local size_after
        size_after=$(get_repo_size_kb)
        if [ "$size_before" -gt 0 ] || [ "$size_after" -gt 0 ]; then
            local delta_kb=$((size_after - size_before))
            repo_added=$(format_size_kb $delta_kb)
        fi

        printf "| %-30s | %10s s | %10.2f MB | %12s |\n" "$step_name" "$real_time" "$mem_mb" "$repo_added"

        "$BENCHREPORT_BIN" row -out "$BENCH_CSV" -tool "$CURRENT_TOOL" \
            -op "$step_name" -seconds "$real_time" -peak-mb "$mem_mb" \
            -repo-delta "$repo_added" || true

        if [ -n "${DEBUG_FLAG:-}" ]; then
            echo "  [debug] $step_name stdout:" >&2
            cat "$stdout_file" >&2
        fi
    else
        echo "" >&2
        echo "ERROR: '$step_name' failed" >&2
        echo "Command: $*" >&2
        echo "--- stdout ---" >&2
        cat "$stdout_file" >&2
        echo "--- stderr ---" >&2
        # Strip time(1)'s own statistics so only the command's stderr shows.
        grep -v -E '^\s+[0-9].*( real | user | sys |maximum resident|page reclaims|page faults|voluntary context|involuntary context|swaps|block input|block output|messages sent|messages received|signals received|instructions retired|cycles elapsed|peak memory)' "$stderr_file" >&2 || true
        echo "---" >&2
    fi
    rm -f "$stdout_file" "$stderr_file"
}

print_table_header() {
    printf "| %-30s | %12s | %13s | %12s |\n" "Operation" "Time" "Peak Mem" "Repo Added"
    echo "|--------------------------------|--------------|---------------|--------------|"
}

print_repo_size() {
    local size
    if [ -n "$BENCH_REPO_DIR" ] && [ -d "$BENCH_REPO_DIR" ]; then
        size=$(du -sh "$BENCH_REPO_DIR" | cut -f1 | xargs)
    elif [ -n "$BENCH_S3_PREFIX" ]; then
        local bytes
        bytes=$(aws s3 ls "$BENCH_S3_PREFIX" --summarize --recursive 2>/dev/null \
            | grep "Total Size" | awk '{print $3}')
        size=$(format_size_kb $(( ${bytes:-0} / 1024 )))
    else
        size="-"
    fi
    printf "| %-30s | %12s | %13s | %12s |\n" "Final Repo Size" "$size" "-" "-"
}
