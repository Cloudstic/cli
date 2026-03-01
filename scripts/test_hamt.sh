#!/bin/bash
set -e

cd "$(dirname "$0")/.."

export CLOUDSTIC_PASSWORD=test
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

CLI="go run cmd/cloudstic/main.go"
STORE_FLAGS="-store local -store-path $TMP_DIR/repo"
SOURCE_FLAGS="-source local -source-path $TMP_DIR/data"

# Strip ANSI color codes, then extract the 64-char hex hash from "Snapshot <hash> saved"
strip_ansi() { sed 's/\x1b\[[0-9;]*m//g'; }
extract_hash() { strip_ansi | grep -oE 'Snapshot [a-f0-9]{64}' | head -1 | awk '{print $2}'; }

echo "Testing HAMT behavior in $TMP_DIR"
echo ""

mkdir -p "$TMP_DIR/data" "$TMP_DIR/repo"

$CLI init $STORE_FLAGS --no-encryption 2>&1 | tail -1

# ---------------------------------------------------------
# Test 1: Write staging discards intermediate nodes
# ---------------------------------------------------------
echo ""
echo "=== Test 1: Write Staging & Intermediate Discarding ==="

for i in $(seq 1 50); do
  echo "content-$i" > "$TMP_DIR/data/file_$i.txt"
done

OUTPUT=$($CLI backup $STORE_FLAGS $SOURCE_FLAGS --debug 2>&1)
SNAP1=$(echo "$OUTPUT" | extract_hash)

FLUSH_LINE=$(echo "$OUTPUT" | grep "Flushing HAMT:")
echo "  $FLUSH_LINE"

REACHABLE=$(echo "$FLUSH_LINE" | grep -oE '[0-9]+ reachable' | grep -oE '[0-9]+')
DISCARDED=$(echo "$FLUSH_LINE" | grep -oE '[0-9]+ intermediate' | grep -oE '[0-9]+')

check "reachable nodes flushed (got $REACHABLE)" "[ '$REACHABLE' -gt 0 ]"
check "intermediate nodes discarded (got $DISCARDED)" "[ '$DISCARDED' -gt 0 ]"

WRITE_LINE=$(echo "$OUTPUT" | grep "writeParallel:" || true)
echo "  $WRITE_LINE"
WRITTEN=$(echo "$WRITE_LINE" | grep -oE 'writing [0-9]+' | grep -oE '[0-9]+' || echo "0")
check "writeParallel wrote nodes (got $WRITTEN)" "[ '$WRITTEN' -gt 0 ]"

# writeParallel only calls Put (never Exists). Verify via HAMT debug output.
HAMT_EXISTS=$(echo "$OUTPUT" | grep "\[hamt\]" | grep -ci "exists" || true)
check "HAMT issued 0 Exists calls (got $HAMT_EXISTS)" "[ '$HAMT_EXISTS' -eq 0 ]"

# ---------------------------------------------------------
# Test 2: Structural sharing — diff shows only 2 changes
# ---------------------------------------------------------
echo ""
echo "=== Test 2: Structural Sharing ==="

echo "changed-1" > "$TMP_DIR/data/file_1.txt"
echo "changed-2" > "$TMP_DIR/data/file_2.txt"

OUTPUT2=$($CLI backup $STORE_FLAGS $SOURCE_FLAGS --debug 2>&1)
SNAP2=$(echo "$OUTPUT2" | extract_hash)

FLUSH_LINE2=$(echo "$OUTPUT2" | grep "Flushing HAMT:")
echo "  $FLUSH_LINE2"

DIFF_OUTPUT=$($CLI diff $STORE_FLAGS "$SNAP1" "$SNAP2" 2>&1)
DIFF_MODIFIED=$(echo "$DIFF_OUTPUT" | grep -c "^M " || true)
echo "  diff $SNAP1 vs $SNAP2: $DIFF_MODIFIED modified files"
echo "$DIFF_OUTPUT" | grep "^[ADM] " | head -5 || true

check "diff shows exactly 2 modified files (got $DIFF_MODIFIED)" "[ '$DIFF_MODIFIED' -eq 2 ]"

# ---------------------------------------------------------
# Test 3: Read cache — persistent reads during HAMT walk
# ---------------------------------------------------------
echo ""
echo "=== Test 3: Read Cache ==="

OUTPUT_LS=$($CLI ls $STORE_FLAGS --debug 2>&1)
PERSISTENT_GETS=$(echo "$OUTPUT_LS" | grep -c "fetched from persistent" || true)
CACHE_HITS=$(echo "$OUTPUT_LS" | grep -c "hit read cache" || true)

echo "  ls: persistent_fetches=$PERSISTENT_GETS cache_hits=$CACHE_HITS"

check "ls fetched nodes from persistent (got $PERSISTENT_GETS)" "[ '$PERSISTENT_GETS' -gt 0 ]"

# ---------------------------------------------------------
# Test 4: Data integrity — ls shows all 50 files
# ---------------------------------------------------------
echo ""
echo "=== Test 4: Data Integrity ==="

LS_OUTPUT=$($CLI ls $STORE_FLAGS 2>&1)
FILE_COUNT=$(echo "$LS_OUTPUT" | grep -c "file_" || true)
check "ls shows all 50 files (got $FILE_COUNT)" "[ '$FILE_COUNT' -eq 50 ]"

# ---------------------------------------------------------
# Test 5: Prune preserves HAMT integrity
# ---------------------------------------------------------
echo ""
echo "=== Test 5: Prune + HAMT Integrity ==="

echo "third-change" > "$TMP_DIR/data/file_1.txt"
$CLI backup $STORE_FLAGS $SOURCE_FLAGS 2>&1 > /dev/null

$CLI forget $STORE_FLAGS --keep-last 1 2>&1 > /dev/null
PRUNE_OUTPUT=$($CLI prune $STORE_FLAGS --debug 2>&1)

echo "  $(echo "$PRUNE_OUTPUT" | grep -i "repack" | head -3 || true)"

POST_PRUNE_LS=$($CLI ls $STORE_FLAGS 2>&1)
POST_PRUNE_COUNT=$(echo "$POST_PRUNE_LS" | grep -c "file_" || true)
check "ls after prune shows all 50 files (got $POST_PRUNE_COUNT)" "[ '$POST_PRUNE_COUNT' -eq 50 ]"

# ---------------------------------------------------------
# Test 6: Restore integrity after all operations
# ---------------------------------------------------------
echo ""
echo "=== Test 6: Restore Integrity ==="

$CLI restore $STORE_FLAGS -output "$TMP_DIR/restored.zip" 2>&1 > /dev/null
RESTORED_COUNT=$(unzip -l "$TMP_DIR/restored.zip" 2>/dev/null | grep -c "file_" || true)
check "restore ZIP contains all 50 files (got $RESTORED_COUNT)" "[ '$RESTORED_COUNT' -eq 50 ]"

echo ""
