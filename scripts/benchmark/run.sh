#!/usr/bin/env bash

set -e

SOURCE=${1:-local}    # Data source: local or gdrive
STORE=${2:-local}     # Storage backend: local or s3
TOOL=${3:-all}        # Tool: cloudstic, restic, borg, duplicacy, all
S3_BUCKET=${S3_BUCKET:-cloudstic-benchmark-734836384094-us-east-1}
AWS_REGION=${AWS_REGION:-us-east-1}
RCLONE_REMOTE=${RCLONE_REMOTE:-gdrive}

if [ "$SOURCE" != "local" ] && [ "$SOURCE" != "gdrive" ]; then
    echo "Usage: $0 [local|gdrive] [local|s3] [cloudstic|restic|borg|duplicacy|all] [--debug]"
    exit 1
fi

if [ "$STORE" != "local" ] && [ "$STORE" != "s3" ]; then
    echo "Usage: $0 [local|gdrive] [local|s3] [cloudstic|restic|borg|duplicacy|all] [--debug]"
    exit 1
fi

if [ "$TOOL" != "cloudstic" ] && [ "$TOOL" != "restic" ] && [ "$TOOL" != "borg" ] && [ "$TOOL" != "duplicacy" ] && [ "$TOOL" != "all" ]; then
    echo "Usage: $0 [local|gdrive] [local|s3] [cloudstic|restic|borg|duplicacy|all] [--debug]"
    exit 1
fi

if [ "$SOURCE" == "gdrive" ]; then
    if [ "$TOOL" != "cloudstic" ] && [ "$TOOL" != "all" ]; then
        if ! command -v rclone &> /dev/null; then
            echo "Error: rclone is required for non-cloudstic tools with gdrive source."
            exit 1
        fi
    fi
fi

DEBUG_FLAG=""
if [ "$4" == "--debug" ]; then
    DEBUG_FLAG="-debug"
    echo "Debug logging enabled for Cloudstic"
fi

# Export AWS credentials as env vars for tools that need them (Restic, aws CLI).
# This resolves SSO, profiles, credential_process, etc. into explicit keys.
if [ "$STORE" == "s3" ]; then
    eval "$(aws configure export-credentials --format env 2>/dev/null)" || true
fi

echo "=== Cloudstic Benchmark ==="
echo "Source: $SOURCE"
echo "Store: $STORE"
if [ "$STORE" == "s3" ]; then
    echo "S3 Bucket: $S3_BUCKET"
fi
if [ "$SOURCE" == "gdrive" ]; then
    echo "rclone Remote: $RCLONE_REMOTE"
fi
echo ""

# Ensure we have the latest cloudstic binary
echo "Building cloudstic binary..."
go build -o /tmp/cloudstic ./cmd/cloudstic
export CLOUDSTIC_BIN="/tmp/cloudstic"

# Create temp dirs
DATA_TEMPLATE=$(mktemp -d -t benchmark-template-XXXXXX)
DATA_DIR=$(mktemp -d -t benchmark-data-XXXXXX)
REPO_DIR=$(mktemp -d -t benchmark-repos-XXXXXX)
GDRIVE_MOUNT=""      # Set when rclone mount is active
RCLONE_PID=""       # PID of background rclone mount process
GDRIVE_CACHE_DIR="" # Per-tool VFS cache dir (fresh each mount to avoid cross-tool contamination)

# Ensure cleanup on exit
cleanup() {
    # Unmount rclone if active
    if [ -n "$GDRIVE_MOUNT" ] && mount | grep -q "$GDRIVE_MOUNT"; then
        echo "Unmounting rclone..."
        umount "$GDRIVE_MOUNT" 2>/dev/null || fusermount -u "$GDRIVE_MOUNT" 2>/dev/null || true
        rm -rf "$GDRIVE_MOUNT"
    fi
    # Kill background rclone process if still running
    if [ -n "$RCLONE_PID" ] && kill -0 "$RCLONE_PID" 2>/dev/null; then
        kill "$RCLONE_PID" 2>/dev/null || true
    fi
    # Remove per-tool VFS cache
    if [ -n "$GDRIVE_CACHE_DIR" ]; then
        rm -rf "$GDRIVE_CACHE_DIR"
    fi
    
    echo "Cleaning up temp directories..."
    rm -rf "$DATA_TEMPLATE"
    rm -rf "$DATA_DIR"
    rm -rf "$REPO_DIR"
    
    if [ "$STORE" == "s3" ]; then
        echo "Cleaning up S3 benchmark prefixes..."
        aws s3 rm "s3://$S3_BUCKET/cloudstic/" --recursive >/dev/null 2>&1 || true
        aws s3 rm "s3://$S3_BUCKET/restic/" --recursive >/dev/null 2>&1 || true
        aws s3 rm "s3://$S3_BUCKET/duplicacy/" --recursive >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

# Tears down any existing rclone mount and wipes its cache.
teardown_gdrive_mount() {
    if [ -n "$GDRIVE_MOUNT" ] && mount | grep -q "$GDRIVE_MOUNT"; then
        umount "$GDRIVE_MOUNT" 2>/dev/null || fusermount -u "$GDRIVE_MOUNT" 2>/dev/null || true
        rm -rf "$GDRIVE_MOUNT"
        GDRIVE_MOUNT=""
    fi
    if [ -n "$RCLONE_PID" ] && kill -0 "$RCLONE_PID" 2>/dev/null; then
        kill "$RCLONE_PID" 2>/dev/null || true
        wait "$RCLONE_PID" 2>/dev/null || true
        RCLONE_PID=""
    fi
    if [ -n "$GDRIVE_CACHE_DIR" ]; then
        rm -rf "$GDRIVE_CACHE_DIR"
        GDRIVE_CACHE_DIR=""
    fi
}

# Always creates a fresh mount with an empty, isolated cache dir.
# Each tool gets its own cache so there is no cross-tool warm-cache contamination.
# Returns 1 (non-fatal) if the remote is not configured or FUSE is unavailable.
setup_gdrive_mount() {
    # Always start clean so each tool benchmarks from an empty cache
    teardown_gdrive_mount
    # Validate the rclone remote exists before attempting to mount
    if ! rclone listremotes 2>/dev/null | grep -q "^${RCLONE_REMOTE}:$"; then
        echo "Error: rclone remote '${RCLONE_REMOTE}' not configured." >&2
        echo "Run 'rclone config' to add it, or set RCLONE_REMOTE to use a different remote name." >&2
        return 1
    fi
    # On macOS, rclone mount requires a FUSE implementation
    if [[ "$(uname)" == "Darwin" ]]; then
        if [ ! -d "/Library/Filesystems/macfuse.fs" ] && [ ! -d "/Library/Filesystems/fuse-t.fs" ]; then
            echo "Error: FUSE not found. rclone mount requires macFUSE or FUSE-T on macOS." >&2
            echo "  macFUSE: brew install --cask macfuse" >&2
            echo "  FUSE-T:  brew install macos-fuse-t/homebrew-macos-fuse-t/fuse-t" >&2
            return 1
        fi
    fi
    GDRIVE_MOUNT=$(mktemp -d -t benchmark-gdrive-XXXXXX)
    GDRIVE_CACHE_DIR=$(mktemp -d -t benchmark-gdrive-cache-XXXXXX)
    echo "Mounting Google Drive ($RCLONE_REMOTE:) at $GDRIVE_MOUNT (fresh cache)..."
    local rclone_log
    rclone_log=$(mktemp)
    rclone mount "$RCLONE_REMOTE:" "$GDRIVE_MOUNT" \
        --read-only \
        --vfs-cache-mode full \
        --cache-dir "$GDRIVE_CACHE_DIR" 2>"$rclone_log" &
    RCLONE_PID=$!
    for i in $(seq 1 30); do
        if mount | grep -q "$GDRIVE_MOUNT"; then
            echo "rclone mount ready."
            rm -f "$rclone_log"
            return 0
        fi
        # Bail early if rclone already exited with an error
        if ! kill -0 "$RCLONE_PID" 2>/dev/null; then
            echo "Error: rclone mount process exited unexpectedly." >&2
            cat "$rclone_log" >&2
            rm -f "$rclone_log"
            return 1
        fi
        sleep 1
    done
    echo "Error: rclone mount timed out after 30s." >&2
    kill "$RCLONE_PID" 2>/dev/null || true
    rm -f "$rclone_log"
    return 1
}

# Generate synthetic dataset for local source
if [ "$SOURCE" == "local" ]; then
    echo "Generating base dataset at $DATA_TEMPLATE..."
    # Large files (random + compressible)
    dd if=/dev/urandom of="$DATA_TEMPLATE/random.dat" bs=1m count=500 status=none
    dd if=/dev/zero of="$DATA_TEMPLATE/zero.dat" bs=1m count=500 status=none
    # Medium files: 50 x 1MB random
    mkdir -p "$DATA_TEMPLATE/docs"
    for i in $(seq 1 50); do
        dd if=/dev/urandom of="$DATA_TEMPLATE/docs/doc_$i.bin" bs=1m count=1 status=none
    done
    # Small files: 100 x ~1KB text
    mkdir -p "$DATA_TEMPLATE/config"
    for i in $(seq 1 100); do
        head -c 1024 /dev/urandom | base64 > "$DATA_TEMPLATE/config/setting_$i.cfg"
    done

    DATA_SIZE=$(du -sh "$DATA_TEMPLATE" | cut -f1 | xargs)
    echo "Dataset generated. Size: $DATA_SIZE"
    echo ""
fi

# Reset DATA_DIR to a clean copy of the template
reset_data_dir() {
    rm -rf "$DATA_DIR"/*
    cp -a "$DATA_TEMPLATE"/ "$DATA_DIR/"
}

# Format KB into human-readable size
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

# Set BENCH_REPO_DIR (local) or BENCH_S3_PREFIX (s3://bucket/prefix/) to enable repo size tracking.
BENCH_REPO_DIR=""
BENCH_S3_PREFIX=""

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

run_bench() {
    local step_name="$1"
    shift
    local stdout_file=$(mktemp)
    local stderr_file=$(mktemp)
    
    local size_before
    size_before=$(get_repo_size_kb)
    
    if /usr/bin/time -l "$@" > "$stdout_file" 2> "$stderr_file"; then
        local real_time=$(grep "real" "$stderr_file" | awk '{print $1}')
        local mem_bytes=$(grep "maximum resident set size" "$stderr_file" | awk '{print $1}')
        local mem_mb=$(echo "scale=2; $mem_bytes / 1024 / 1024" | bc)
        
        local repo_added="-"
        local size_after
        size_after=$(get_repo_size_kb)
        if [ "$size_before" -gt 0 ] || [ "$size_after" -gt 0 ]; then
            local delta_kb=$((size_after - size_before))
            repo_added=$(format_size_kb $delta_kb)
        fi
        
        printf "| %-30s | %10s s | %10.2f MB | %12s |\n" "$step_name" "$real_time" "$mem_mb" "$repo_added"
        
        if [ -n "$DEBUG_FLAG" ]; then
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
        # Filter out /usr/bin/time statistics to show only command stderr
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

PASSWORD="benchmark-password-123"
export CLOUDSTIC_ENCRYPTION_PASSWORD="$PASSWORD"
export RESTIC_PASSWORD="$PASSWORD"
export BORG_PASSPHRASE="$PASSWORD"
export DUPLICACY_PASSWORD="$PASSWORD"
export DUPLICACY_DEFAULT_PASSWORD="$PASSWORD"

# ---------------------------------------------------------------------------
# Cloudstic
# ---------------------------------------------------------------------------
benchmark_cloudstic() {
    echo "### Cloudstic"
    print_table_header
    
    local repo="$REPO_DIR/cloudstic"
    
    # Store setup
    local store_flags
    if [ "$STORE" == "s3" ]; then
        BENCH_REPO_DIR=""
        BENCH_S3_PREFIX="s3://$S3_BUCKET/cloudstic/"
        store_flags="-store s3:$S3_BUCKET/cloudstic/ -encryption-password $PASSWORD"
        $CLOUDSTIC_BIN init $store_flags >/dev/null || true
    else
        BENCH_REPO_DIR="$repo"
        BENCH_S3_PREFIX=""
        store_flags="-store local:$repo"
        $CLOUDSTIC_BIN init $store_flags >/dev/null
    fi
    
    # Source setup
    local source_flags
    if [ "$SOURCE" == "gdrive" ]; then
        source_flags="-source gdrive-changes"
    else
        reset_data_dir
        source_flags="-source local:$DATA_DIR"
    fi
    
    run_bench "Initial Backup" $CLOUDSTIC_BIN backup $store_flags $source_flags -quiet $DEBUG_FLAG
    run_bench "Incremental (No Changes)" $CLOUDSTIC_BIN backup $store_flags $source_flags -quiet $DEBUG_FLAG
    
    if [ "$SOURCE" == "local" ]; then
        echo "modified" >> "$DATA_DIR/config/setting_1.cfg"
        run_bench "Incremental (1 File Changed)" $CLOUDSTIC_BIN backup $store_flags $source_flags -quiet $DEBUG_FLAG
        
        mkdir -p "$DATA_DIR/new_data"
        dd if=/dev/urandom of="$DATA_DIR/new_data/extra1.dat" bs=1m count=100 status=none
        dd if=/dev/urandom of="$DATA_DIR/new_data/extra2.dat" bs=1m count=100 status=none
        run_bench "Add 200MB New Data" $CLOUDSTIC_BIN backup $store_flags $source_flags -quiet $DEBUG_FLAG
        
        cp -r "$DATA_DIR/docs" "$DATA_DIR/docs_copy"
        cp -r "$DATA_DIR/config" "$DATA_DIR/config_copy"
        cp "$DATA_DIR/random.dat" "$DATA_DIR/random_copy.dat"
        cp "$DATA_DIR/zero.dat" "$DATA_DIR/zero_copy.dat"
        run_bench "Deduplicated Backup" $CLOUDSTIC_BIN backup $store_flags $source_flags -quiet $DEBUG_FLAG
    fi
    
    print_repo_size
    echo ""
}

# ---------------------------------------------------------------------------
# Restic
# ---------------------------------------------------------------------------
benchmark_restic() {
    echo "### Restic"
    if ! command -v restic &> /dev/null; then
        echo "Restic not found, skipping."
        echo ""
        return
    fi

    print_table_header
    
    local repo="$REPO_DIR/restic"
    
    # Store setup
    local repo_arg
    if [ "$STORE" == "s3" ]; then
        BENCH_REPO_DIR=""
        BENCH_S3_PREFIX="s3://$S3_BUCKET/restic/"
        repo_arg="s3:s3.$AWS_REGION.amazonaws.com/$S3_BUCKET/restic"
        restic init -r "$repo_arg" >/dev/null 2>&1 || true
    else
        BENCH_REPO_DIR="$repo"
        BENCH_S3_PREFIX=""
        repo_arg="$repo"
        restic init -r "$repo_arg" >/dev/null 2>&1
    fi
    
    # Source setup
    local src
    if [ "$SOURCE" == "gdrive" ]; then
        if ! setup_gdrive_mount; then
            echo "Skipping Restic: rclone gdrive mount unavailable."
            echo ""
            return
        fi
        src="$GDRIVE_MOUNT"
    else
        reset_data_dir
        src="$DATA_DIR"
    fi
    
    run_bench "Initial Backup" restic backup -r "$repo_arg" "$src"
    if [ "$SOURCE" == "gdrive" ]; then setup_gdrive_mount || return; src="$GDRIVE_MOUNT"; fi
    run_bench "Incremental (No Changes)" restic backup -r "$repo_arg" "$src"
    
    if [ "$SOURCE" == "local" ]; then
        echo "modified" >> "$DATA_DIR/config/setting_2.cfg"
        run_bench "Incremental (1 File Changed)" restic backup -r "$repo_arg" "$src"
        
        mkdir -p "$DATA_DIR/new_data"
        dd if=/dev/urandom of="$DATA_DIR/new_data/extra1.dat" bs=1m count=100 status=none
        dd if=/dev/urandom of="$DATA_DIR/new_data/extra2.dat" bs=1m count=100 status=none
        run_bench "Add 200MB New Data" restic backup -r "$repo_arg" "$src"
        
        cp -r "$DATA_DIR/docs" "$DATA_DIR/docs_copy"
        cp -r "$DATA_DIR/config" "$DATA_DIR/config_copy"
        cp "$DATA_DIR/random.dat" "$DATA_DIR/random_copy.dat"
        cp "$DATA_DIR/zero.dat" "$DATA_DIR/zero_copy.dat"
        run_bench "Deduplicated Backup" restic backup -r "$repo_arg" "$src"
    fi
    
    print_repo_size
    echo ""
}

# ---------------------------------------------------------------------------
# Borg
# ---------------------------------------------------------------------------
benchmark_borg() {
    echo "### Borg"
    if ! command -v borg &> /dev/null; then
        echo "Borg not found, skipping."
        echo ""
        return
    fi

    if [ "$STORE" == "s3" ]; then
        echo "Skipping Borg for S3 store (not natively supported)."
        echo ""
        return
    fi
    
    print_table_header

    local repo="$REPO_DIR/borg"
    BENCH_REPO_DIR="$repo"
    BENCH_S3_PREFIX=""
    borg init -e repokey "$repo" >/dev/null 2>&1
    export BORG_UNKNOWN_UNENCRYPTED_REPO_ACCESS_IS_OK=yes
    
    # Source setup
    local src
    if [ "$SOURCE" == "gdrive" ]; then
        if ! setup_gdrive_mount; then
            echo "Skipping Borg: rclone gdrive mount unavailable."
            echo ""
            return
        fi
        src="$GDRIVE_MOUNT"
    else
        reset_data_dir
        src="$DATA_DIR"
    fi
    
    run_bench "Initial Backup" borg create "$repo::initial" "$src"
    if [ "$SOURCE" == "gdrive" ]; then setup_gdrive_mount || return; src="$GDRIVE_MOUNT"; fi
    run_bench "Incremental (No Changes)" borg create "$repo::inc1" "$src"
    
    if [ "$SOURCE" == "local" ]; then
        echo "modified" >> "$DATA_DIR/config/setting_3.cfg"
        run_bench "Incremental (1 File Changed)" borg create "$repo::inc2" "$src"
        
        mkdir -p "$DATA_DIR/new_data"
        dd if=/dev/urandom of="$DATA_DIR/new_data/extra1.dat" bs=1m count=100 status=none
        dd if=/dev/urandom of="$DATA_DIR/new_data/extra2.dat" bs=1m count=100 status=none
        run_bench "Add 200MB New Data" borg create "$repo::inc3" "$src"
        
        cp -r "$DATA_DIR/docs" "$DATA_DIR/docs_copy"
        cp -r "$DATA_DIR/config" "$DATA_DIR/config_copy"
        cp "$DATA_DIR/random.dat" "$DATA_DIR/random_copy.dat"
        cp "$DATA_DIR/zero.dat" "$DATA_DIR/zero_copy.dat"
        run_bench "Deduplicated Backup" borg create "$repo::inc4" "$src"
    fi
    
    print_repo_size
    echo ""
}

# ---------------------------------------------------------------------------
# Duplicacy
# ---------------------------------------------------------------------------
benchmark_duplicacy() {
    echo "### Duplicacy"
    if ! command -v duplicacy &> /dev/null; then
        echo "Duplicacy not found, skipping."
        echo ""
        return
    fi

    if [ "$STORE" == "s3" ]; then
        echo "Skipping Duplicacy for S3 store (needs specific credential setup)."
        echo ""
        return
    fi
    
    print_table_header

    local repo="$REPO_DIR/duplicacy"
    BENCH_REPO_DIR="$repo"
    BENCH_S3_PREFIX=""
    mkdir -p "$repo"
    
    # Duplicacy must be run from the source directory (no -repository flag on backup).
    local src
    if [ "$SOURCE" == "gdrive" ]; then
        if ! setup_gdrive_mount; then
            echo "Skipping Duplicacy: rclone gdrive mount unavailable."
            echo ""
            return
        fi
        src="$GDRIVE_MOUNT"
    else
        reset_data_dir
        src="$DATA_DIR"
    fi
    
    local init_output
    if ! init_output=$(bash -c "cd '$src' && duplicacy init -e bench '$repo'" 2>&1); then
        echo "ERROR: duplicacy init failed:" >&2
        echo "$init_output" >&2
        echo ""
        return
    fi
    
    run_bench "Initial Backup" bash -c "cd '$src' && duplicacy backup"
    if [ "$SOURCE" == "gdrive" ]; then setup_gdrive_mount || return; src="$GDRIVE_MOUNT"; fi
    run_bench "Incremental (No Changes)" bash -c "cd '$src' && duplicacy backup"
    
    if [ "$SOURCE" == "local" ]; then
        echo "modified" >> "$DATA_DIR/config/setting_4.cfg"
        run_bench "Incremental (1 File Changed)" bash -c "cd '$src' && duplicacy backup"
        
        mkdir -p "$DATA_DIR/new_data"
        dd if=/dev/urandom of="$DATA_DIR/new_data/extra1.dat" bs=1m count=100 status=none
        dd if=/dev/urandom of="$DATA_DIR/new_data/extra2.dat" bs=1m count=100 status=none
        run_bench "Add 200MB New Data" bash -c "cd '$src' && duplicacy backup"
        
        cp -r "$DATA_DIR/docs" "$DATA_DIR/docs_copy"
        cp -r "$DATA_DIR/config" "$DATA_DIR/config_copy"
        cp "$DATA_DIR/random.dat" "$DATA_DIR/random_copy.dat"
        cp "$DATA_DIR/zero.dat" "$DATA_DIR/zero_copy.dat"
        run_bench "Deduplicated Backup" bash -c "cd '$src' && duplicacy backup"
    fi
    
    print_repo_size
    echo ""
}

# ---------------------------------------------------------------------------
# Run benchmarks
# ---------------------------------------------------------------------------
if [ "$TOOL" == "all" ] || [ "$TOOL" == "cloudstic" ]; then
    benchmark_cloudstic
fi

if [ "$TOOL" == "all" ] || [ "$TOOL" == "restic" ]; then
    benchmark_restic
fi

if [ "$TOOL" == "all" ] || [ "$TOOL" == "borg" ]; then
    benchmark_borg
fi

if [ "$TOOL" == "all" ] || [ "$TOOL" == "duplicacy" ]; then
    benchmark_duplicacy
fi

echo "Done."

if [ "$DEBUG_FLAG" == "-debug" ]; then
    echo ""
    echo "Debug logs saved to /tmp/cloudstic-debug-*.log"
fi
