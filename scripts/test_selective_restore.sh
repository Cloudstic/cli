#!/bin/bash
set -e

cd "$(dirname "$0")/.."

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

echo "Testing Selective Restore (Google Drive source) in $TMP_DIR"

STORE="-store local -store-path $TMP_DIR/repo"

# ---------------------------------------------------------
# Step 1: Backup from Google Drive
# ---------------------------------------------------------
echo ""
echo "=== Step 1: Init & Backup from Google Drive ==="

$CLI init $STORE -no-encryption 2>&1 | tail -1

echo "  backing up Google Drive..."
$CLI backup $STORE -source gdrive 2>&1 | strip_ansi | tail -5

# Verify backup created a snapshot
LS_OUTPUT=$($CLI ls $STORE 2>&1)
TOTAL_FILES=$(echo "$LS_OUTPUT" | strip_ansi | grep -v "^$" | grep -v "Listing" | grep -v "entries listed" | wc -l | tr -d ' ')
echo "  total entries in snapshot: $TOTAL_FILES"
check "backup has at least 1 entry" "[ '$TOTAL_FILES' -ge 1 ]"

# ---------------------------------------------------------
# Step 2: Full restore (baseline)
# ---------------------------------------------------------
echo ""
echo "=== Step 2: Full Restore (baseline) ==="

$CLI restore $STORE -output "$TMP_DIR/full.zip" 2>&1 | strip_ansi | tail -3

# List zip entry names, one per line (handles paths with spaces).
zip_entries() { zipinfo -1 "$1" 2>/dev/null; }

FULL_COUNT=$(zip_entries "$TMP_DIR/full.zip" | wc -l | tr -d ' ')
echo "  entries in full restore: $FULL_COUNT"
check "full restore has entries" "[ '$FULL_COUNT' -ge 1 ]"

# Pick a folder from the zip for subtree test.
# Find the first directory entry (ends with /).
FOLDER=$(zip_entries "$TMP_DIR/full.zip" | grep '/$' | head -1)
echo "  selected folder for subtree test: ${FOLDER:-<none>}"

# Pick a file nested inside that folder for single-file test.
# Use LC_ALL=C to handle non-ASCII chars in Google Drive filenames.
if [ -n "$FOLDER" ]; then
  FILE=$(LC_ALL=C zip_entries "$TMP_DIR/full.zip" | LC_ALL=C grep "^${FOLDER}" | LC_ALL=C grep -v '/$' | head -1)
else
  FILE=$(LC_ALL=C zip_entries "$TMP_DIR/full.zip" | LC_ALL=C grep -v '/$' | head -1)
fi
echo "  selected file for single-file test: ${FILE:-<none>}"

# ---------------------------------------------------------
# Step 3: Selective restore — subtree
# ---------------------------------------------------------
if [ -n "$FOLDER" ]; then
  echo ""
  echo "=== Step 3: Selective Restore — Subtree ($FOLDER) ==="

  $CLI restore $STORE -output "$TMP_DIR/subtree.zip" -path "$FOLDER" 2>&1 | strip_ansi | tail -3
  SUBTREE_COUNT=$(zip_entries "$TMP_DIR/subtree.zip" | wc -l | tr -d ' ')
  echo "  entries in subtree restore: $SUBTREE_COUNT"
  check "subtree restore has entries" "[ '$SUBTREE_COUNT' -ge 1 ]"
  check "subtree restore is smaller than full" "[ '$SUBTREE_COUNT' -le '$FULL_COUNT' ]"

  # Verify all entries are under the selected folder.
  OUTSIDE=$(zip_entries "$TMP_DIR/subtree.zip" | grep -v "^${FOLDER}" | wc -l | tr -d ' ')
  check "no files outside subtree (got $OUTSIDE)" "[ '$OUTSIDE' -eq 0 ]"
else
  echo ""
  echo "=== Step 3: SKIPPED (no folder found in backup) ==="
fi

# ---------------------------------------------------------
# Step 4: Selective restore — single file
# ---------------------------------------------------------
if [ -n "$FILE" ]; then
  echo ""
  echo "=== Step 4: Selective Restore — Single File ($FILE) ==="

  $CLI restore $STORE -output "$TMP_DIR/single.zip" -path "$FILE" 2>&1 | strip_ansi | tail -3
  SINGLE_FILES=$(zip_entries "$TMP_DIR/single.zip" | grep -v '/$' | wc -l | tr -d ' ')
  echo "  files in single-file restore: $SINGLE_FILES"
  check "single-file restore has exactly 1 file" "[ '$SINGLE_FILES' -eq 1 ]"

  # Verify the correct file is present.
  HAS_FILE=$(LC_ALL=C zip_entries "$TMP_DIR/single.zip" | LC_ALL=C grep -cF "$FILE" || true)
  check "correct file present in zip" "[ '$HAS_FILE' -ge 1 ]"
else
  echo ""
  echo "=== Step 4: SKIPPED (no file found in backup) ==="
fi

# ---------------------------------------------------------
# Step 5: Selective restore — dry run
# ---------------------------------------------------------
echo ""
echo "=== Step 5: Selective Restore — Dry Run ==="

DRY_OUTPUT=$($CLI restore $STORE -dry-run ${FILE:+-path "$FILE"} 2>&1)
DRY_CLEAN=$(echo "$DRY_OUTPUT" | strip_ansi)
echo "$DRY_CLEAN" | tail -3

check "dry run reports files" "echo '$DRY_CLEAN' | grep -q 'Files:'"

# ---------------------------------------------------------
# Step 6: Selective restore — no match
# ---------------------------------------------------------
echo ""
echo "=== Step 6: Selective Restore — No Match ==="

$CLI restore $STORE -output "$TMP_DIR/nomatch.zip" -path "this/path/does/not/exist.txt" 2>&1 | strip_ansi | tail -3
# An empty zip (22 bytes) has no entries; zipinfo returns nothing on stdout.
if zipinfo -1 "$TMP_DIR/nomatch.zip" >/dev/null 2>&1; then
  NOMATCH_ENTRIES=$(zip_entries "$TMP_DIR/nomatch.zip" | grep -c '.' || true)
else
  NOMATCH_ENTRIES=0
fi
check "no-match restore has 0 entries" "[ '$NOMATCH_ENTRIES' -eq 0 ]"

echo ""
