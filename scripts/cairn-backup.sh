#!/bin/sh
# Backs up PORTABLE cairn data only (spec §3 / rulings §9): events, objects,
# exports, views, cairn.toml. Device-local identity (keys, seq cache, config)
# is deliberately EXCLUDED — restoring this backup on any machine creates a
# NEW origin (run `cairn init --adopt`). Derived state (.cairn/) is excluded
# as rebuildable.
#
# Usage: cairn-backup.sh <cairn-dir> <backup-dest-dir>
set -eu
SRC="${1:?usage: cairn-backup.sh <cairn-dir> <backup-dest-dir>}"
DST="${2:?usage: cairn-backup.sh <cairn-dir> <backup-dest-dir>}"
[ -f "$SRC/cairn.toml" ] || { echo "error: $SRC is not a cairn directory" >&2; exit 1; }
mkdir -p "$DST"
rsync -a --delete \
  --exclude '.cairn/' \
  --exclude '*.tmp' \
  "$SRC/" "$DST/"
echo "portable backup complete: $DST"
echo "note: device identity is NOT in this backup by design; a restore"
echo "on any machine requires 'cairn init --adopt' and creates a NEW origin."
