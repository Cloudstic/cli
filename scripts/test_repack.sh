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

strip_ansi() { sed 's/\x1b\[[0-9;]*m//g'; }

echo "Testing Repack Strategy in $TMP_DIR"

# ---------------------------------------------------------
# Test 1: Orphaned Pack (100% wasted)
# ---------------------------------------------------------
echo ""
echo "=== Test 1: Full Orphan Repack ==="

STORE1="-store local -store-path $TMP_DIR/repo"
SOURCE1="-source local -source-path $TMP_DIR/data"

mkdir -p "$TMP_DIR/data" "$TMP_DIR/repo"

$CLI init $STORE1 --no-encryption 2>&1 | tail -1

for i in $(seq 1 20); do
  echo "This is some content for file number $i that will be backed up" > "$TMP_DIR/data/file_$i.txt"
done

echo "  initial backup..."
$CLI backup $STORE1 $SOURCE1 2>&1 > /dev/null

for change in $(seq 1 3); do
  for i in $(seq 1 20); do
    echo "Change $change for file $i" > "$TMP_DIR/data/file_$i.txt"
  done
  echo "  backup $change..."
  $CLI backup $STORE1 $SOURCE1 2>&1 > /dev/null
done

PACKS_BEFORE=$(ls "$TMP_DIR/repo/packs/" 2>/dev/null | wc -l | tr -d ' ')
echo "  packs before prune: $PACKS_BEFORE"

$CLI forget $STORE1 --keep-last 1 2>&1 > /dev/null
PRUNE_OUTPUT=$($CLI prune $STORE1 --verbose --debug 2>&1)
PRUNE_CLEAN=$(echo "$PRUNE_OUTPUT" | strip_ansi)

ORPHANS_DELETED=$(echo "$PRUNE_CLEAN" | grep -c "deleting orphaned pack" || true)
echo "  orphaned packs deleted: $ORPHANS_DELETED"

check "at least 1 orphaned pack deleted (got $ORPHANS_DELETED)" "[ '$ORPHANS_DELETED' -ge 1 ]"

PACKS_AFTER=$(ls "$TMP_DIR/repo/packs/" 2>/dev/null | wc -l | tr -d ' ')
echo "  packs after prune: $PACKS_AFTER"
check "fewer packs after prune ($PACKS_AFTER < $PACKS_BEFORE)" "[ '$PACKS_AFTER' -lt '$PACKS_BEFORE' ]"

LS_OUTPUT=$($CLI ls $STORE1 2>&1)
FILE_COUNT=$(echo "$LS_OUTPUT" | grep -c "file_" || true)
check "ls shows all 20 files after prune (got $FILE_COUNT)" "[ '$FILE_COUNT' -eq 20 ]"

# ---------------------------------------------------------
# Test 2: Fragmented Pack (Partially wasted)
# ---------------------------------------------------------
echo ""
echo "=== Test 2: Partial Fragment Repack ==="

STORE2="-store local -store-path $TMP_DIR/repo2"
SOURCE2="-source local -source-path $TMP_DIR/data2"

mkdir -p "$TMP_DIR/data2" "$TMP_DIR/repo2"

$CLI init $STORE2 --no-encryption 2>&1 | tail -1

for i in $(seq 1 100); do echo "A content $i" > "$TMP_DIR/data2/A_$i.txt"; done
for i in $(seq 1 100); do echo "B content $i" > "$TMP_DIR/data2/B_$i.txt"; done

echo "  snap 1 (A + B)..."
$CLI backup $STORE2 $SOURCE2 2>&1 > /dev/null

rm "$TMP_DIR/data2"/B_*.txt
for i in $(seq 1 100); do echo "C content $i" > "$TMP_DIR/data2/C_$i.txt"; done

echo "  snap 2 (A + C)..."
$CLI backup $STORE2 $SOURCE2 2>&1 > /dev/null

$CLI forget $STORE2 --keep-last 1 2>&1 > /dev/null

PRUNE2_OUTPUT=$($CLI prune $STORE2 --verbose --debug 2>&1)
PRUNE2_CLEAN=$(echo "$PRUNE2_OUTPUT" | strip_ansi)

REPACKED=$(echo "$PRUNE2_CLEAN" | grep -c "repacking packs/" || true)
WASTED_LINE=$(echo "$PRUNE2_CLEAN" | grep "repacking packs/" | head -1 || true)
echo "  $WASTED_LINE"

check "at least 1 fragmented pack repacked (got $REPACKED)" "[ '$REPACKED' -ge 1 ]"

LS2_OUTPUT=$($CLI ls $STORE2 2>&1)
A_COUNT=$(echo "$LS2_OUTPUT" | grep -c "A_" || true)
B_COUNT=$(echo "$LS2_OUTPUT" | grep -c "B_" || true)
C_COUNT=$(echo "$LS2_OUTPUT" | grep -c "C_" || true)

check "ls shows 100 A files (got $A_COUNT)" "[ '$A_COUNT' -eq 100 ]"
check "ls shows 0 B files (got $B_COUNT)" "[ '$B_COUNT' -eq 0 ]"
check "ls shows 100 C files (got $C_COUNT)" "[ '$C_COUNT' -eq 100 ]"

# ---------------------------------------------------------
# Test 3: Restore integrity after repack
# ---------------------------------------------------------
echo ""
echo "=== Test 3: Restore Integrity After Repack ==="

$CLI restore $STORE2 -output "$TMP_DIR/restored.zip" 2>&1 > /dev/null
RESTORED_A=$(unzip -l "$TMP_DIR/restored.zip" 2>/dev/null | grep -c "A_" || true)
RESTORED_C=$(unzip -l "$TMP_DIR/restored.zip" 2>/dev/null | grep -c "C_" || true)
RESTORED_B=$(unzip -l "$TMP_DIR/restored.zip" 2>/dev/null | grep -c "B_" || true)

check "restore has 100 A files (got $RESTORED_A)" "[ '$RESTORED_A' -eq 100 ]"
check "restore has 100 C files (got $RESTORED_C)" "[ '$RESTORED_C' -eq 100 ]"
check "restore has 0 B files (got $RESTORED_B)" "[ '$RESTORED_B' -eq 0 ]"

echo ""
