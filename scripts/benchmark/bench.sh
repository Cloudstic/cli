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
# aging.sh, by contrast, *is* folded in (RFC 0026): set AGE_CHECKPOINTS and
# each (profile, size, backend) cell additionally ages one repository with
# AGE_CHURN files of churn per backup and measures AGE_OPS at each checkpoint.
# The axis it adds — how many backups contributed to the snapshot being read —
# is the one the pipeline matrix structurally cannot see, because its unit of
# repetition is a whole pipeline against a fresh store. Aging rows land in the
# same CSV with the packs/backups/policy columns filled and operations named
# `restore@40`, so benchreport renders each checkpoint as its own row instead
# of averaging the curve away. Request counts need a MinIO backend; on local,
# aging still reports wall time and peak RSS, which is the memory half of the
# question (RFC 0023).
#
# POLICIES reads the aged repository several ways at each checkpoint —
# `POLICIES='baseline=; probe=CLOUDSTIC_TEST_X=1'` — because aging a
# repository twice ages two *different* repositories (pack composition is not
# deterministic), and the difference between two runs is then not the change
# under test (RFC 0025 §7). Reads mutate nothing, so policies share one
# repository honestly.
#
# Usage:
#   scripts/benchmark/bench.sh
#   PROFILES="source mixed" SIZES="5000 50000" scripts/benchmark/bench.sh
#   BACKENDS="local minio" scripts/benchmark/bench.sh
#   SAMPLES=1 SIZES=2000 scripts/benchmark/bench.sh          # quick check
#   AGE_CHECKPOINTS="1 10 40 80" BACKENDS=minio SIZES=5000 scripts/benchmark/bench.sh
#   AGE_CHECKPOINTS="1 40" POLICIES='baseline=; probe=CLOUDSTIC_TEST_X=1' \
#       BACKENDS=minio SIZES=5000 scripts/benchmark/bench.sh
#
# Comparing two *builds* against one aged repository, which is how a read-path
# change should be measured — see ATTACH below:
#   KEEP_STORE=1 AGE_CHECKPOINTS=80 BACKENDS=minio SIZES=5000 \
#       BENCH_CLOUDSTIC_BIN=/tmp/a scripts/benchmark/bench.sh
#   ATTACH=1 AGE_CHECKPOINTS=80 SIZES=5000 \
#       BENCH_CLOUDSTIC_BIN=/tmp/b scripts/benchmark/bench.sh

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

# REPO_FORMAT selects the repository format the pipeline inits — empty for the
# build's default, 3 for the packless fat-leaf format (RFC 0026). An axis of
# the *repository*, not of the binary: the same build measured at both values
# is the v2-versus-v3 comparison the RFC's performance targets are scored on.
REPO_FORMAT=${REPO_FORMAT:-}

# The aging stage. Off unless checkpoints are given, so CI and a plain run
# measure exactly what they measured before the stage existed.
AGE_CHECKPOINTS=${AGE_CHECKPOINTS:-}
AGE_CHURN=${AGE_CHURN:-200}
# How many directories that churn is spread across, which is a separate
# variable from how many files it changes and the one format v3's retention
# cost is a function of — a snapshot keeps roughly
# `directories touched x leaf size` (RFC 0027 §6). 0 leaves the profile's
# natural spread. Note the two are coupled by the tree's fan-out: 200 changed
# files cannot fit in 4 directories of median fan-out 6, so a cap below
# roughly `count / fan-out` reduces the volume as well as the breadth, and
# gentree reports what it actually achieved.
AGE_CHURN_DIRS=${AGE_CHURN_DIRS:-0}

# Read-only operations measured at each checkpoint. Beyond restore and check,
# ls, find and diff are accepted: they traverse the same tree by different
# routes, and what a traversal declares to the store — and whether it then
# reads it — differs per command, so an aggregate over "the traversals" hides
# exactly the divergence worth seeing (RFC 0025).
#
# `${AGE_OPS-...}` rather than `${AGE_OPS:-...}`, so AGE_OPS="" means "no read
# operations" instead of silently meaning "the default two". Measuring only
# AGE_FINAL_OPS is a real thing to want, and the colon form would run a
# restore and a check first.
AGE_OPS=${AGE_OPS-"restore check"}

# Operations that change the repository, run once after the last checkpoint.
#
# They cannot be in AGE_OPS, and the separation is not tidiness. A backup adds
# a backup, which is the stage's independent variable; a prune deletes
# snapshots and rewrites packs. Either one at a checkpoint silently redefines
# every checkpoint after it, so the curve would be measured against a
# repository the earlier rows no longer describe.
#
# prune's request count carries a large run-to-run spread (RFC 0025 §6)
# because Repack's work depends on which objects happened to share a pack. A
# single figure from it means nothing; it is here to be sampled, not quoted.
AGE_FINAL_OPS=${AGE_FINAL_OPS:-""}

POLICIES=${POLICIES:-"baseline="}

# ATTACH=1 measures against the repository already in MinIO, taking no backups
# and generating no tree; KEEP_STORE=1 is how that repository gets left behind.
#
# POLICIES and ATTACH answer the same question for different shapes of change.
# A policy is a knob on one binary, so its variants interleave within a single
# run and compare at every checkpoint. Two *builds* cannot be — there is one
# BENCH_CLOUDSTIC_BIN — and running the whole stage twice compares them against
# two different repositories: pack composition is not deterministic, which is
# where check's 70% spread on identical code came from. Age once with
# KEEP_STORE=1, then attach each build in turn, and the layout is held fixed
# instead of resampled. Attach runs skip the pipeline matrix entirely; set
# PROFILES/SIZES to match the aged repository so the row labels stay honest,
# and the highest AGE_CHECKPOINTS value labels the backups column. AGE_DATA is
# the aged source tree's path, needed only for an attached `backup` final op
# (a KEEP_STORE run prints it).
ATTACH=${ATTACH:-0}
KEEP_STORE=${KEEP_STORE:-0}
AGE_DATA=${AGE_DATA:-""}

CLOUDSTIC_BIN=${BENCH_CLOUDSTIC_BIN:-"$REPO_ROOT/bin/cloudstic"}
GENTREE_BIN="$REPO_ROOT/bin/gentree"

case "$SAMPLES" in
    '' | *[!0-9]* | 0) echo "SAMPLES must be a positive integer, got '$SAMPLES'" >&2; exit 2 ;;
esac

# The highest checkpoint sets how many backups the aging stage takes; the rest
# are read off along the way. Validated here rather than discovered forty
# backups in.
AGE_TOTAL=0
for cp in $AGE_CHECKPOINTS; do
    case "$cp" in
        '' | *[!0-9]* | 0) echo "AGE_CHECKPOINTS must be positive integers, got '$cp'" >&2; exit 2 ;;
    esac
    [ "$cp" -gt "$AGE_TOTAL" ] && AGE_TOTAL=$cp
done

# Split POLICIES once, here, rather than juggling IFS around the run loop.
# Format is a ';'-separated list of `label=VAR=value VAR=value`, with an empty
# assignment list meaning the binary's own defaults. Validated up front for
# the same reason AGE_CHECKPOINTS is: a malformed entry should fail now, not
# forty backups in.
POLICY_LIST=()
while IFS= read -r entry; do
    entry=${entry#"${entry%%[![:space:]]*}"}
    entry=${entry%"${entry##*[![:space:]]}"}
    [ -n "$entry" ] || continue
    case "$entry" in
        *=*) ;;
        *) echo "POLICIES entry must be 'label=assignments', got '$entry'" >&2; exit 2 ;;
    esac
    POLICY_LIST+=("$entry")
done <<EOF
$(echo "$POLICIES" | tr ';' '\n')
EOF
[ "${#POLICY_LIST[@]}" -gt 0 ] || { echo "POLICIES is empty" >&2; exit 2; }

WORK=$(mktemp -d -t cloudstic-bench-XXXXXX)
MINIO_STARTED=0
cleanup() {
    # KEEP_STORE leaves the container running for a later ATTACH run, and the
    # working directory with it: the aged source tree lives there, and an
    # attached `backup` final op needs it.
    if [ "$MINIO_STARTED" = "1" ] && [ "$KEEP_STORE" != "1" ]; then
        minio_stop
    fi
    [ "$KEEP" = "1" ] || [ "$KEEP_STORE" = "1" ] || rm -rf "$WORK"
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

command -v bc >/dev/null 2>&1 || { echo "bench.sh needs bc" >&2; exit 2; }

# ---------------------------------------------------------------------------
# Measurement
# ---------------------------------------------------------------------------

# One CSV per backend when several are asked for.
#
# benchreport groups rows by operation and has no notion of a backend, so a
# single file holding both would have it average a local run against a MinIO one
# and present the result as repeated samples of the same thing. Splitting keeps
# every file something the renderer reads correctly.
#
# Defined here rather than in the run section because measure() calls it, and
# the ATTACH branch measures before the run section's setup is reached.
backend_csv() {
    if [ "$(echo "$BACKENDS" | wc -w | tr -d ' ')" -gt 1 ]; then
        echo "${OUT%.csv}-$1.csv"
    else
        echo "$OUT"
    fi
}

# The three columns only the aging stage fills. Globals rather than
# parameters so that measure()'s call sites in the pipeline stay untouched:
# the pipeline never sets them, so its rows carry them blank.
ROW_PACKS=""
ROW_BACKUPS=""
ROW_POLICY=""

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

    # A probe build may write diagnostic lines to stderr that are the whole
    # point of the run. time(1)'s own statistics share that stream, so the
    # command's stderr is captured rather than passed through, and anything
    # interesting in it would be discarded here. Echo what matches instead of
    # making every probe rediscover that.
    if [ -n "${ECHO_STDERR_MATCHING:-}" ]; then
        grep -E "$ECHO_STDERR_MATCHING" "$err" | sed 's/^/      /' || true
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
        # sent_before must be dollar-prefixed here: unlike the arithmetic
        # context above, this string is parsed by bc, not bash, and bc treats
        # a bare "sent_before" as its own uninitialized variable — silently 0
        # — rather than an error. That turned every sample after the first
        # into the cumulative sent-bytes total since the container started,
        # not that operation's own delta, since the missing "$" made the
        # subtraction a no-op.
        sent_mb=$(echo "scale=1; ($(minio_sent_bytes) - $sent_before) / 1048576" | bc)
        minio_api_counts >"$WORK/api-after"
        by_api=$(minio_api_delta "$WORK/api-before" "$WORK/api-after")
    fi

    # Repository growth, using lib.sh's get_repo_size_kb rather than a second
    # implementation — it already handles both a local directory and an S3
    # prefix, which matters now that a MinIO backend exists alongside local.
    local repo_kb_after repo_delta
    repo_kb_after=$(get_repo_size_kb)
    repo_delta=$(format_size_kb $(( repo_kb_after - repo_kb_before )))

    # repo_delta is what this operation added; stored_kb is what the repository
    # holds in total once it has run. The pipeline's rows make the second
    # redundant — it starts empty and only grows — but the aging stage's do not:
    # what a *retained snapshot* costs is the difference between two checkpoints'
    # totals, and a delta measured around a read operation is zero by
    # construction (issue #525).
    printf 'cloudstic,%s,%s,%d,%d,%s,%s,%.1f,%s,%s,%s,%s,%s,%s,%s,%s,%s\n' \
        "$backend" "$profile" "$size" "$sample" "$op" \
        "$seconds" "$(echo "scale=1; $peak_kb / 1024" | bc)" \
        "$alloc_mb" "$requests" "$sent_mb" "$by_api" "$repo_delta" \
        "$ROW_PACKS" "$ROW_BACKUPS" "$ROW_POLICY" "$repo_kb_after" \
        >>"$(backend_csv "$backend")"

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

    printf 'cloudstic,%s,%s,%d,%d,Final Repo Size,0,0,,,,,%s,,,,%s\n' \
        "$backend" "$profile" "$size" "$sample" "$stored_fmt" "$stored_kb" \
        >>"$(backend_csv "$backend")"
    printf '    %-20s %s\n' "Final Repo Size" "$stored_fmt"

    if [ "$stored_kb" -gt 0 ]; then
        local ratio
        ratio=$(echo "scale=2; $logical_kb / $stored_kb" | bc)
        printf 'cloudstic,%s,%s,%d,%d,Logical / Stored,0,0,,,,,%s / %s (%sx),,,,%s\n' \
            "$backend" "$profile" "$size" "$sample" "$logical_fmt" "$stored_fmt" "$ratio" \
            "$stored_kb" >>"$(backend_csv "$backend")"
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

    measure init   "$CLOUDSTIC_BIN" init $flags ${REPO_FORMAT:+-format "$REPO_FORMAT"} -quiet
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
# Aging (absorbed from aging.sh — RFC 0025's measurement, merged per RFC 0026)
# ---------------------------------------------------------------------------

# pack_count reports how many packfiles the repository currently holds — the
# independent variable of the aging curve, read from the store rather than
# inferred from the backup count, because whether the two stay in lockstep is
# precisely what is in question.
pack_count() {
    if [ "$backend" = minio ]; then
        aws --endpoint-url "$(minio_endpoint)" \
            s3 ls "s3://$MINIO_BUCKET/bench/packs/" --recursive 2>/dev/null \
            | grep -c "packs/" || true
    else
        # A format-v3 repository has no packs directory at all, and find
        # exits 1 when its root does not exist. Under `set -euo pipefail` that
        # aborted the whole run at the first checkpoint, which is why the aging
        # stage had never been run against v3 on a local backend. The minio
        # branch above already swallows its equivalent.
        { find "$WORK/repo/packs" -type f 2>/dev/null || true; } | wc -l | tr -d ' '
    fi
}

# snapshot_ids prints every snapshot hash, newest first.
#
# Read from -json rather than the table, whose column widths are presentation
# and have changed before. diff is the reason this exists at all: a harness
# that invokes it without the two IDs it requires fails instantly and reports
# the failure as a measurement of zero (RFC 0025 §6).
snapshot_ids() {
    local flags
    flags=$(store_flags)
    "$CLOUDSTIC_BIN" list $flags -json 2>/dev/null \
        | awk -F'"' '/"Ref"[[:space:]]*:[[:space:]]*"snapshot\// { n = split($4, a, "/"); print a[n] }'
}

# age_checkpoint_reads measures every AGE_OPS operation against the current
# repository under every policy, always reading the *latest* snapshot — the
# case that ages. An old snapshot restores at its original cost forever
# (RFC 0025 §4): the cost is how many backups contributed to the snapshot
# being read, not how many packs the repository has.
age_checkpoint_reads() {
    local backups=$1 flags target op entry label assignments suffix first last
    flags=$(store_flags)
    target="$WORK/age-restore"

    ROW_BACKUPS=$backups
    ROW_PACKS=$(pack_count)
    # Policy is the inner loop so the runs being compared sit next to each
    # other in time as well as against the same repository. Reads mutate
    # nothing, so their order carries no state.
    for op in $AGE_OPS; do
        for entry in "${POLICY_LIST[@]}"; do
            label=${entry%%=*}
            assignments=${entry#*=}
            ROW_POLICY=$label
            # benchreport groups rows by operation name, so the checkpoint —
            # and any non-baseline policy — is folded into it: restore@40,
            # restore@40:probe. Two policies sharing a name would be averaged
            # as repeat samples.
            suffix=""
            [ "$label" != baseline ] && suffix=":$label"
            case "$op" in
                restore)
                    rm -rf "$target"; mkdir -p "$target"
                    measure "restore@${backups}${suffix}" \
                        env $assignments "$CLOUDSTIC_BIN" restore $flags \
                        -output "$target" latest -quiet
                    rm -rf "$target"
                    ;;
                check)
                    measure "check@${backups}${suffix}" \
                        env $assignments "$CLOUDSTIC_BIN" check $flags -quiet
                    ;;
                ls)
                    measure "ls@${backups}${suffix}" \
                        env $assignments "$CLOUDSTIC_BIN" ls $flags latest
                    ;;
                find)
                    # A pattern nothing matches, on purpose: find's cost is
                    # the walk over every snapshot, and matches would add
                    # output formatting without adding traversal. It serves as
                    # the control that does not move, so it has to be the same
                    # walk every time.
                    measure "find@${backups}${suffix}" \
                        env $assignments "$CLOUDSTIC_BIN" find $flags -name 'no-such-file-*'
                    ;;
                diff)
                    # Oldest against latest, which is the whole-tree
                    # traversal. Adjacent snapshots differ by one churn step
                    # and would measure the churn rather than the tree.
                    first=$(snapshot_ids | tail -1)
                    last=$(snapshot_ids | head -1)
                    if [ -z "$first" ] || [ -z "$last" ] || [ "$first" = "$last" ]; then
                        # One snapshot is the normal state at checkpoint 1;
                        # skipping beats failing a sweep that is hours in.
                        echo "    diff@${backups} skipped: needs two snapshots"
                        continue
                    fi
                    measure "diff@${backups}${suffix}" \
                        env $assignments "$CLOUDSTIC_BIN" diff $flags "$first" "$last"
                    ;;
                *)
                    echo "unknown op '$op' in AGE_OPS" >&2; return 2 ;;
            esac
        done
    done
    ROW_PACKS=""; ROW_BACKUPS=""; ROW_POLICY=""
}

# age_final_ops runs the state-changing operations, once, after the curve is
# done.
age_final_ops() {
    local backups=$1 flags op label assignments suffix
    [ -n "$AGE_FINAL_OPS" ] || return 0
    flags=$(store_flags)

    # Each of these changes the repository, so unlike age_checkpoint_reads the
    # policy loop cannot be the inner one: the second policy would measure the
    # state the first left behind. Runs with more than one policy therefore
    # measure AGE_FINAL_OPS under the first, and the rest is a note rather
    # than a number.
    label=${POLICY_LIST[0]%%=*}
    assignments=${POLICY_LIST[0]#*=}
    if [ "${#POLICY_LIST[@]}" -gt 1 ]; then
        echo "    (AGE_FINAL_OPS runs under policy '$label' only; it mutates the repository)"
    fi
    suffix=""
    [ "$label" != baseline ] && suffix=":$label"

    ROW_BACKUPS=$backups
    ROW_PACKS=$(pack_count)
    ROW_POLICY=$label
    for op in $AGE_FINAL_OPS; do
        case "$op" in
            backup)
                if [ ! -d "$AGE_DATA" ]; then
                    echo "backup in AGE_FINAL_OPS needs a source tree; set AGE_DATA" >&2
                    return 2
                fi
                measure "backup@${backups}${suffix}" \
                    env $assignments \
                    "$CLOUDSTIC_BIN" backup $flags -source "local:$AGE_DATA" -quiet
                ;;
            prune)
                measure "prune@${backups}${suffix}" \
                    env $assignments "$CLOUDSTIC_BIN" prune $flags -quiet
                ;;
            *)
                echo "unknown op '$op' in AGE_FINAL_OPS" >&2; return 2 ;;
        esac
    done
    ROW_PACKS=""; ROW_BACKUPS=""; ROW_POLICY=""
}

# run_aging ages one repository for the current (profile, size, backend) cell,
# measuring AGE_OPS at every checkpoint and AGE_FINAL_OPS after the last.
#
# The tree is regenerated fresh rather than reusing the pipeline's $data,
# which the samples have already churned and grown: aging must start from the
# same deterministic tree whatever SAMPLES was.
run_aging() {
    local flags age_data backups cp
    flags=$(store_flags)
    age_data="$WORK/age-data"

    rm -rf "$age_data"
    "$GENTREE_BIN" -out "$age_data" -profile "$profile" -files "$size" \
        -seed 1 -max-bytes "$MAX_BYTES" >/dev/null
    # Where an attached run would need AGE_DATA passed in, this run knows it.
    AGE_DATA="$age_data"

    reset_store
    sample=1
    # Setup, not measurement — like the aging backups below, so its chatter
    # (which init writes on stderr as well as stdout) does not interleave with
    # the checkpoint table. Kept on failure, where it is the diagnosis.
    if ! "$CLOUDSTIC_BIN" init $flags ${REPO_FORMAT:+-format "$REPO_FORMAT"} -quiet >"$WORK/age-init.log" 2>&1; then
        echo "aging init failed" >&2
        sed 's/^/    /' "$WORK/age-init.log" >&2
        return 1
    fi

    backups=0
    while [ "$backups" -lt "$AGE_TOTAL" ]; do
        # Churn before every backup after the first, so backup N+1 has
        # something to write. The seed advances with the backup number: the
        # same files changing every round would rewrite one region of the
        # tree repeatedly and understate how far a snapshot's entries scatter.
        if [ "$backups" -gt 0 ]; then
            "$GENTREE_BIN" -churn "$age_data" -profile "$profile" \
                -seed "$(( 1000 + backups ))" -count "$AGE_CHURN" \
                ${AGE_CHURN_DIRS:+-churn-dirs "$AGE_CHURN_DIRS"} \
                -max-bytes "$MAX_BYTES" >/dev/null
        fi

        # Aging backups are the setup, not the measurement, so their summaries
        # are not recorded. Kept on failure, where they are the diagnosis.
        if ! "$CLOUDSTIC_BIN" backup $flags -source "local:$age_data" -quiet \
            >"$WORK/age-backup.log" 2>&1; then
            echo "aging backup $(( backups + 1 )) failed" >&2
            sed 's/^/    /' "$WORK/age-backup.log" >&2
            return 1
        fi
        backups=$(( backups + 1 ))

        for cp in $AGE_CHECKPOINTS; do
            [ "$cp" = "$backups" ] || continue
            echo "    aged to $backups backup(s), $(format_size_kb "$(get_repo_size_kb)") stored:"
            age_checkpoint_reads "$backups"
        done
    done

    age_final_ops "$backups"
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

# An explicit BENCH_CLOUDSTIC_BIN is used as given and never rebuilt. The
# aging stage's whole use is comparing a probe build — a constant changed, a
# policy swapped — against the baseline; rebuilding from the working tree
# would quietly replace that probe with whatever the source currently says
# and report the result as the probe's.
if [ -n "${BENCH_CLOUDSTIC_BIN:-}" ]; then
    [ -x "$CLOUDSTIC_BIN" ] || { echo "BENCH_CLOUDSTIC_BIN is not executable: $CLOUDSTIC_BIN" >&2; exit 2; }
    echo "Using prebuilt $CLOUDSTIC_BIN (not rebuilding)"
else
    echo "Building..."
    ( cd "$REPO_ROOT" && go build -o bin/cloudstic ./cmd/cloudstic )
fi
( cd "$REPO_ROOT" && go build -o bin/gentree ./internal/cmd/gentree )

# ATTACH runs must branch off before the MinIO startup loop: minio_start
# stops any existing container first, which would destroy the very repository
# being attached to. Deliberately no MINIO_STARTED=1 — this run did not start
# the container and must not take it down, or attaching a second build would
# find nothing.
if [ "$ATTACH" = "1" ]; then
    [ "$AGE_TOTAL" -gt 0 ] || { echo "ATTACH=1 needs AGE_CHECKPOINTS (its highest value labels the backups column)" >&2; exit 2; }
    curl -fs --max-time "$MINIO_CURL_TIMEOUT" "$(minio_endpoint)/minio/health/live" >/dev/null 2>&1 \
        || { echo "ATTACH=1 but nothing is answering on $(minio_endpoint); run once with KEEP_STORE=1 first" >&2; exit 1; }

    # One backend, one cell — BACKENDS is forced so backend_csv resolves to
    # OUT itself. The row labels come from the front of PROFILES/SIZES, so
    # set those to match the repository being attached to.
    BACKENDS=minio
    backend=minio
    profile=${PROFILES%% *}
    size=${SIZES%% *}
    sample=1
    BENCH_REPO_DIR=""
    BENCH_S3_PREFIX="s3://$MINIO_BUCKET/bench/"
    BENCH_S3_ENDPOINT=$(minio_endpoint)
    export AWS_ACCESS_KEY_ID="$MINIO_USER" AWS_SECRET_ACCESS_KEY="$MINIO_PASSWORD"
    export CLOUDSTIC_PASSWORD="$PASSWORD"

    mkdir -p "$(dirname "$OUT")"
    echo "tool,backend,profile,scale,sample,operation,seconds,peak_mb,alloc_mb,requests,sent_mb,by_api,repo_delta,packs,backups,policy,stored_kb" >"$OUT"

    echo "Attached to the repository already in $(minio_endpoint)"
    echo ""
    age_checkpoint_reads "$AGE_TOTAL"
    age_final_ops "$AGE_TOTAL"
    echo ""
    echo "Wrote $OUT"
    exit 0
fi

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

# Column names match what benchreport.Parse looks up — it is header-name driven
# and ignores what it does not recognise, so backend, profile, sample and the S3
# columns ride along without the renderer needing to know about them. "tool" is
# constant here and present only because the renderer labels rows with it.
for backend in $BACKENDS; do
    echo "tool,backend,profile,scale,sample,operation,seconds,peak_mb,alloc_mb,requests,sent_mb,by_api,repo_delta,packs,backups,policy,stored_kb" >"$(backend_csv "$backend")"
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
    echo "format:   ${REPO_FORMAT:-default}"
    if [ -n "$AGE_CHECKPOINTS" ]; then
        echo "age:      checkpoints=$AGE_CHECKPOINTS churn=$AGE_CHURN dirs=$AGE_CHURN_DIRS ops=$AGE_OPS final=$AGE_FINAL_OPS policies=$POLICIES keep_store=$KEEP_STORE"
    fi
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
            if [ -n "$AGE_CHECKPOINTS" ]; then
                echo "  $backend, aging to $AGE_TOTAL backups (checkpoints: $AGE_CHECKPOINTS)"
                run_aging
            fi
        done
    done
done

echo ""
for backend in $BACKENDS; do
    echo "Wrote $(backend_csv "$backend")"
done
echo "Wrote $env_file"

if [ "$MINIO_STARTED" = "1" ] && [ "$KEEP_STORE" = "1" ]; then
    echo ""
    echo "MinIO left running on $(minio_endpoint); aged source tree at $WORK/age-data"
    echo "Measure another build against this same repository with:"
    echo "  ATTACH=1 AGE_DATA=$WORK/age-data AGE_CHECKPOINTS=$AGE_TOTAL PROFILES=$profile SIZES=$size BENCH_CLOUDSTIC_BIN=<bin> $0"
    echo "Stop it with: docker rm -f $MINIO_CONTAINER"
fi
