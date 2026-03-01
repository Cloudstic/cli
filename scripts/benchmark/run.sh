#!/usr/bin/env bash

set -e

TARGET=${1:-local} # Default to local
TOOL=${2:-all}     # Default to all tools
S3_BUCKET=${S3_BUCKET:-cloudstic-benchmark-681494392773-us-east-1}
AWS_REGION=${AWS_REGION:-us-east-1}

if [ "$TARGET" != "local" ] && [ "$TARGET" != "s3" ]; then
    echo "Usage: $0 [local|s3] [cloudstic|restic|borg|all] [--debug]"
    exit 1
fi

if [ "$TOOL" != "cloudstic" ] && [ "$TOOL" != "restic" ] && [ "$TOOL" != "borg" ] && [ "$TOOL" != "all" ]; then
    echo "Usage: $0 [local|s3] [cloudstic|restic|borg|all] [--debug]"
    exit 1
fi

DEBUG_FLAG=""
if [ "$3" == "--debug" ]; then
    DEBUG_FLAG="-debug"
    echo "Debug logging enabled for Cloudstic"
fi

# Export temporary AWS credentials for tools that don't support SSO natively (e.g. restic)
# if [ "$TARGET" == "s3" ]; then
#     if [ -z "$AWS_ACCESS_KEY_ID" ]; then
#         echo "Exporting AWS credentials from SSO session..."
#         eval "$(aws configure export-credentials --format env 2>/dev/null)" || {
#             echo "Warning: Could not export AWS credentials. Restic S3 benchmark may fail."
#         }
#     fi
# fi

echo "=== Cloudstic Benchmark ==="
echo "Target: $TARGET"
if [ "$TARGET" == "s3" ]; then
    echo "S3 Bucket: $S3_BUCKET"
fi
echo ""

# Ensure we have the latest cloudstic binary
echo "Building cloudstic binary..."
go build -o /tmp/cloudstic ./cmd/cloudstic
export CLOUDSTIC_BIN="/tmp/cloudstic"

# Create temp dirs
DATA_DIR=$(mktemp -d -t benchmark-data-XXXXXX)
REPO_DIR=$(mktemp -d -t benchmark-repos-XXXXXX)

# Ensure cleanup on exit
cleanup() {
    echo "Cleaning up temp directories..."
    rm -rf "$DATA_DIR"
    rm -rf "$REPO_DIR"
    
    if [ "$TARGET" == "s3" ]; then
        echo "Cleaning up S3 benchmark prefixes..."
        # Use env -u to avoid SSO issues if manual credentials were set
        env -u AWS_ACCESS_KEY_ID -u AWS_SECRET_ACCESS_KEY -u AWS_SESSION_TOKEN \
            aws s3 rm "s3://$S3_BUCKET/cloudstic/" --recursive >/dev/null 2>&1 || true
        env -u AWS_ACCESS_KEY_ID -u AWS_SECRET_ACCESS_KEY -u AWS_SESSION_TOKEN \
            aws s3 rm "s3://$S3_BUCKET/restic/" --recursive >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

echo "Generating dataset at $DATA_DIR..."
# 1. 500MB random file
dd if=/dev/urandom of="$DATA_DIR/random.dat" bs=1m count=500 status=none
# 2. 500MB zero file (highly compressible)
dd if=/dev/zero of="$DATA_DIR/zero.dat" bs=1m count=500 status=none
# 3. 10,000 small files
mkdir -p "$DATA_DIR/small"
for i in {1..10000}; do
    echo "Hello World benchmark file $i" > "$DATA_DIR/small/file_$i.txt"
done

DATA_SIZE=$(du -sh "$DATA_DIR" | cut -f1 | xargs)
echo "Dataset generated. Size: $DATA_SIZE"
echo ""

# Helper to run and extract time/memory on macOS
run_bench() {
    local step_name="$1"
    shift
    local out_file=$(mktemp)
    
    # Run silently, capture /usr/bin/time -l output
    if /usr/bin/time -l "$@" > /dev/null 2> "$out_file"; then
        if [ "$DEBUG_FLAG" == "-debug" ]; then
            local safe_name=$(echo "$step_name" | tr -d ' ' | tr '[:upper:]' '[:lower:]')
            cp "$out_file" "/tmp/cloudstic-debug-${safe_name}.log"
            # We don't print anything here so we don't break the Markdown table,
            # but the user will be notified at the end.
        fi
        local real_time=$(grep "real" "$out_file" | awk '{print $1}')
        local mem_bytes=$(grep "maximum resident set size" "$out_file" | awk '{print $1}')
        local mem_mb=$(echo "scale=2; $mem_bytes / 1024 / 1024" | bc)
        printf "| %-30s | %10s s | %10.2f MB |\n" "$step_name" "$real_time" "$mem_mb"
    else
        echo "Command failed: $@"
        cat "$out_file"
    fi
    rm "$out_file"
}

PASSWORD="benchmark-password-123"
export CLOUDSTIC_ENCRYPTION_PASSWORD="$PASSWORD"
export RESTIC_PASSWORD="$PASSWORD"
export BORG_PASSPHRASE="$PASSWORD"

benchmark_cloudstic() {
    echo "### Cloudstic"
    printf "| %-30s | %12s | %13s |\n" "Operation" "Time" "Peak Mem"
    echo "|--------------------------------|--------------|---------------|"
    
    local repo="$REPO_DIR/cloudstic"
    
    if [ "$TARGET" == "local" ]; then
        $CLOUDSTIC_BIN init -store local -store-path "$repo" >/dev/null 2>&1
        run_bench "Initial Backup" $CLOUDSTIC_BIN backup -store local -store-path "$repo" -source local -source-path "$DATA_DIR" -quiet -cpuprofile /tmp/cloudstic-bench.prof $DEBUG_FLAG
        run_bench "Incremental (No Changes)" $CLOUDSTIC_BIN backup -store local -store-path "$repo" -source local -source-path "$DATA_DIR" -quiet $DEBUG_FLAG
        
        echo "modified" >> "$DATA_DIR/small/file_1.txt"
        
        run_bench "Incremental (1 File Changed)" $CLOUDSTIC_BIN backup -store local -store-path "$repo" -source local -source-path "$DATA_DIR" -quiet -memprofile /tmp/cloudstic-mem.prof $DEBUG_FLAG
        
        local repo_size=$(du -sh "$repo" | cut -f1 | xargs)
        printf "| %-30s | %12s | %13s |\n" "Final Repo Size" "$repo_size" "-"
    else
        $CLOUDSTIC_BIN init -store s3 -encryption-password "$PASSWORD" -store-path "$S3_BUCKET" -store-prefix "cloudstic/" >/dev/null 2>&1 || true
        run_bench "Initial Backup" $CLOUDSTIC_BIN backup -store s3 -encryption-password "$PASSWORD" -store-path "$S3_BUCKET" -store-prefix "cloudstic/" -source local -source-path "$DATA_DIR" -quiet -cpuprofile /tmp/cloudstic-bench-s3.prof $DEBUG_FLAG
        run_bench "Incremental (No Changes)" $CLOUDSTIC_BIN backup -store s3 -encryption-password "$PASSWORD" -store-path "$S3_BUCKET" -store-prefix "cloudstic/" -source local -source-path "$DATA_DIR" -quiet $DEBUG_FLAG
        
        echo "modified" >> "$DATA_DIR/small/file_1.txt"
        run_bench "Incremental (1 File Changed)" $CLOUDSTIC_BIN backup -store s3 -encryption-password "$PASSWORD" -store-path "$S3_BUCKET" -store-prefix "cloudstic/" -source local -source-path "$DATA_DIR" -quiet -memprofile /tmp/cloudstic-mem-s3.prof $DEBUG_FLAG
    fi
    echo ""
}

benchmark_restic() {
    echo "### Restic"
    if ! command -v restic &> /dev/null; then
        echo "Restic not found, skipping."
        echo ""
        return
    fi

    printf "| %-30s | %12s | %13s |\n" "Operation" "Time" "Peak Mem"
    echo "|--------------------------------|--------------|---------------|"
    
    local repo="$REPO_DIR/restic"
    
    if [ "$TARGET" == "local" ]; then
        restic init -r "$repo" >/dev/null 2>&1
        run_bench "Initial Backup" restic backup -r "$repo" "$DATA_DIR"
        run_bench "Incremental (No Changes)" restic backup -r "$repo" "$DATA_DIR"
        
        echo "modified" >> "$DATA_DIR/small/file_2.txt"
        run_bench "Incremental (1 File Changed)" restic backup -r "$repo" "$DATA_DIR"
        
        local repo_size=$(du -sh "$repo" | cut -f1 | xargs)
        printf "| %-30s | %12s | %13s |\n" "Final Repo Size" "$repo_size" "-"
    else
        restic init -r "s3:s3.$AWS_REGION.amazonaws.com/$S3_BUCKET/restic" >/dev/null 2>&1 || true
        run_bench "Initial Backup" restic backup -r "s3:s3.$AWS_REGION.amazonaws.com/$S3_BUCKET/restic" "$DATA_DIR"
        run_bench "Incremental (No Changes)" restic backup -r "s3:s3.$AWS_REGION.amazonaws.com/$S3_BUCKET/restic" "$DATA_DIR"
        
        echo "modified" >> "$DATA_DIR/small/file_2.txt"
        run_bench "Incremental (1 File Changed)" restic backup -r "s3:s3.$AWS_REGION.amazonaws.com/$S3_BUCKET/restic" "$DATA_DIR"
    fi
    echo ""
}

benchmark_borg() {
    echo "### Borg"
    if ! command -v borg &> /dev/null; then
        echo "Borg not found, skipping."
        echo ""
        return
    fi

    if [ "$TARGET" == "s3" ]; then
        echo "Skipping Borg for S3 target (not natively supported)."
        echo ""
        return
    fi
    
    printf "| %-30s | %12s | %13s |\n" "Operation" "Time" "Peak Mem"
    echo "|--------------------------------|--------------|---------------|"

    local repo="$REPO_DIR/borg"
    borg init -e repokey "$repo" >/dev/null 2>&1
    
    # Borg requires a unique archive name each time. We suppress warnings to capture clean output.
    export BORG_UNKNOWN_UNENCRYPTED_REPO_ACCESS_IS_OK=yes
    
    run_bench "Initial Backup" borg create "$repo::initial" "$DATA_DIR"
    run_bench "Incremental (No Changes)" borg create "$repo::inc1" "$DATA_DIR"
    
    echo "modified" >> "$DATA_DIR/small/file_3.txt"
    run_bench "Incremental (1 File Changed)" borg create "$repo::inc2" "$DATA_DIR"
    
    local repo_size=$(du -sh "$repo" | cut -f1 | xargs)
    printf "| %-30s | %12s | %13s |\n" "Final Repo Size" "$repo_size" "-"
    echo ""
}

# Run benchmarks
if [ "$TOOL" == "all" ] || [ "$TOOL" == "cloudstic" ]; then
    benchmark_cloudstic
fi

if [ "$TOOL" == "all" ] || [ "$TOOL" == "restic" ]; then
    benchmark_restic
fi

if [ "$TOOL" == "all" ] || [ "$TOOL" == "borg" ]; then
    benchmark_borg
fi

echo "Done."

if [ "$DEBUG_FLAG" == "-debug" ]; then
    echo ""
    echo "Debug logs have been saved to:"
    echo "  /tmp/cloudstic-debug-initialbackup.log"
    echo "  /tmp/cloudstic-debug-incremental(nochanges).log"
    echo "  /tmp/cloudstic-debug-incremental(1filechanged).log"
fi
