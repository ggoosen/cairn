#!/bin/sh
# Restore drill (BUILD-PLAN M8 acceptance): prove that a portable-only
# restore CANNOT write under the old origin and that `cairn init --adopt`
# creates a usable new origin with the old history preserved.
#
# Usage: cairn-restore-drill.sh <backup-dir> <scratch-restore-dir> [cairn-binary]
set -eu
BACKUP="${1:?usage: cairn-restore-drill.sh <backup-dir> <restore-dir> [cairn]}"
RESTORE="${2:?usage: cairn-restore-drill.sh <backup-dir> <restore-dir> [cairn]}"
CAIRN="${3:-cairn}"
[ -e "$RESTORE" ] && { echo "error: $RESTORE already exists" >&2; exit 1; }

mkdir -p "$RESTORE"
rsync -a "$BACKUP/" "$RESTORE/"

echo "1) restored portable data must be refused without device identity:"
if "$CAIRN" identity show --dir "$RESTORE" 2>/dev/null; then
  echo "DRILL FAILED: restored data was accepted without identity" >&2
  exit 1
fi
echo "   refused, as required."

echo "2) adopting creates a NEW origin (old history archived read-only):"
"$CAIRN" init --adopt --dir "$RESTORE"

echo "3) the new origin is verifiable:"
"$CAIRN" identity show --dir "$RESTORE"
ls -d "$RESTORE"/events-preadopt-* >/dev/null
echo "DRILL PASSED: portable-only restore created a new origin; old history preserved."
