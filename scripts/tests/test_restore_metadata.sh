#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."

TMP_DIR=$(mktemp -d)
PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  ✓ $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  ✗ $1"; }
check() { if eval "$2"; then pass "$1"; else fail "$1"; fi }

cleanup() {
  echo ""
  if [ "$FAIL" -eq 0 ]; then
    echo "All $PASS checks passed."
    rm -rf "$TMP_DIR"
  else
    echo "$FAIL check(s) FAILED ($PASS passed). Temp dir preserved: $TMP_DIR"
    exit 1
  fi
}
trap cleanup EXIT

CLI="go run ./cmd/cloudstic"
STORE_FLAGS="-store local:$TMP_DIR/repo"
SOURCE_FLAGS="-source local:$TMP_DIR/src"
RESTORE_DIR="$TMP_DIR/restore"

strip_ansi() { perl -pe 's/\e\[[0-9;]*m//g'; }

set_test_xattr() {
  python3 - "$1" "$2" "$3" <<'PY'
import os, sys
path, name, value = sys.argv[1:4]
os.setxattr(path, name.encode(), value.encode())
PY
}

get_test_xattr() {
  python3 - "$1" "$2" <<'PY'
import os, sys
path, name = sys.argv[1:3]
try:
    print(os.getxattr(path, name.encode()).decode())
except OSError:
    sys.exit(1)
PY
}

mode_of() {
  python3 - "$1" <<'PY'
import os, stat, sys
print(oct(stat.S_IMODE(os.lstat(sys.argv[1]).st_mode))[2:])
PY
}

echo "Testing restore metadata behavior in $TMP_DIR"
echo ""

mkdir -p "$TMP_DIR/src/subdir"
printf 'hello metadata\n' > "$TMP_DIR/src/subdir/file.txt"
chmod 750 "$TMP_DIR/src/subdir"
chmod 640 "$TMP_DIR/src/subdir/file.txt"

XATTR_NAME="user.cloudstic.test"
if [ "$(uname -s)" = "Darwin" ]; then
  XATTR_NAME="com.cloudstic.test"
fi

DIR_XATTR_OK=1
FILE_XATTR_OK=1
if ! set_test_xattr "$TMP_DIR/src/subdir" "$XATTR_NAME" "dir-xattr" 2>/dev/null; then
  DIR_XATTR_OK=0
  echo "Note: directory xattrs unavailable on this filesystem; directory xattr checks will be skipped"
fi
if ! set_test_xattr "$TMP_DIR/src/subdir/file.txt" "$XATTR_NAME" "file-xattr" 2>/dev/null; then
  FILE_XATTR_OK=0
  echo "Note: file xattrs unavailable on this filesystem; xattr checks will be skipped"
fi

echo "=== Step 1: Init & Backup ==="
$CLI init $STORE_FLAGS --no-encryption 2>&1 | tail -1
$CLI backup $STORE_FLAGS $SOURCE_FLAGS 2>&1 | strip_ansi | tail -3

echo ""
echo "=== Step 2: Restore to directory ==="
RESTORE_OUTPUT=$($CLI restore $STORE_FLAGS --output "$RESTORE_DIR" 2>&1 | strip_ansi)
echo "$RESTORE_OUTPUT" | tail -5

check "restored file content" "grep -q 'hello metadata' '$RESTORE_DIR/subdir/file.txt'"
check "restored file mode" "[ '$(mode_of "$RESTORE_DIR/subdir/file.txt")' = '640' ]"
check "restored dir mode" "[ '$(mode_of "$RESTORE_DIR/subdir")' = '750' ]"
if [ "$FILE_XATTR_OK" -eq 1 ]; then
  check "restored file xattr" "[ '$(get_test_xattr "$RESTORE_DIR/subdir/file.txt" "$XATTR_NAME")' = 'file-xattr' ]"
fi
if [ "$DIR_XATTR_OK" -eq 1 ]; then
  check "restored dir xattr" "[ '$(get_test_xattr "$RESTORE_DIR/subdir" "$XATTR_NAME")' = 'dir-xattr' ]"
fi
check "restore summary has no errors" "! echo '$RESTORE_OUTPUT' | grep -q 'Errors:'"

echo ""
echo "=== Step 3: Rerun restore into existing directory ==="
SECOND_OUTPUT=$($CLI restore $STORE_FLAGS --output "$RESTORE_DIR" 2>&1 | strip_ansi)
echo "$SECOND_OUTPUT" | tail -5

check "rerun preserves existing file content" "grep -q 'hello metadata' '$RESTORE_DIR/subdir/file.txt'"
check "rerun reports warnings for skipped entries" "echo '$SECOND_OUTPUT' | grep -q 'Warnings:'"
check "rerun still has no errors" "! echo '$SECOND_OUTPUT' | grep -q 'Errors:'"

echo ""
