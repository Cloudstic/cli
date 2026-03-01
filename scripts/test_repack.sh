#!/bin/bash
set -e

# Change to workspace root so we can execute 'go run' easily
cd "$(dirname "$0")/.."

export CLOUDSTIC_PASSWORD=test
TMP_DIR=$(mktemp -d)
echo "Testing Repack Strategy in $TMP_DIR"

mkdir -p "$TMP_DIR/data"
mkdir -p "$TMP_DIR/repo"

# ---------------------------------------------------------
# Test 1: Orphaned Pack (100% wasted)
# ---------------------------------------------------------
echo "=== Test 1: Full Orphan Repack ==="

# Create a bunch of small files
for i in {1..20}; do
  echo "This is some content for file number $i that will be backed up" > "$TMP_DIR/data/file_$i.txt"
done

# Init repository
go run cmd/cloudstic/main.go init -store local -store-path "$TMP_DIR/repo" --no-encryption

echo "--- Running Initial Backup ---"
go run cmd/cloudstic/main.go backup -store local -store-path "$TMP_DIR/repo" -source local -source-path "$TMP_DIR/data"

# Make changes to the files and backup again to create entirely new metadata (orphaning the old metadata)
for change in {1..3}; do
  echo "--- Running Backup $change ---"
  for i in {1..20}; do
    echo "Change $change for file $i" > "$TMP_DIR/data/file_$i.txt"
  done
  go run cmd/cloudstic/main.go backup -store local -store-path "$TMP_DIR/repo" -source local -source-path "$TMP_DIR/data"
done

echo "--- Forgetting old snapshots (keep last 1) ---"
go run cmd/cloudstic/main.go forget -store local -store-path "$TMP_DIR/repo" --keep-last 1

echo "--- Running Prune to trigger Orphan deletion ---"
go run cmd/cloudstic/main.go prune -store local -store-path "$TMP_DIR/repo" --verbose --debug 2>&1 | grep -i "repack\|wasted\|deleted"

# ---------------------------------------------------------
# Test 2: Fragmented Pack (Partially wasted)
# ---------------------------------------------------------
echo ""
echo "=== Test 2: Partial Fragment Repack ==="

mkdir -p "$TMP_DIR/data2"
mkdir -p "$TMP_DIR/repo2"

# Create files A and B
for i in {1..100}; do echo "A content $i" > "$TMP_DIR/data2/A_$i.txt"; done
for i in {1..100}; do echo "B content $i" > "$TMP_DIR/data2/B_$i.txt"; done

go run cmd/cloudstic/main.go init -store local -store-path "$TMP_DIR/repo2" --no-encryption

echo "--- Snap 1 (A + B) ---"
go run cmd/cloudstic/main.go backup -store local -store-path "$TMP_DIR/repo2" -source local -source-path "$TMP_DIR/data2"

# Now delete B files, create C files
rm "$TMP_DIR/data2"/B_*.txt
for i in {1..100}; do echo "C content $i" > "$TMP_DIR/data2/C_$i.txt"; done

echo "--- Snap 2 (A + C) ---"
go run cmd/cloudstic/main.go backup -store local -store-path "$TMP_DIR/repo2" -source local -source-path "$TMP_DIR/data2"

echo "--- Forgetting Snap 1 ---"
# we just keep-last 1 which keeps snap 2 (so B is unreachable, A is still kept, pack is fragmented)
go run cmd/cloudstic/main.go forget -store local -store-path "$TMP_DIR/repo2" --keep-last 1

echo "--- Prune (Should trigger partial repack!) ---"
go run cmd/cloudstic/main.go prune -store local -store-path "$TMP_DIR/repo2" --verbose --debug 2>&1 | grep -i "repack\|wasted\|deleted"

echo ""
echo "Done! Temp dir: $TMP_DIR"
