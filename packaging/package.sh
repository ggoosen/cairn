#!/usr/bin/env sh
# D6 — package one already-built `cairn` binary into a release tarball.
#
#   packaging/package.sh --binary bin/cairn --os linux --arch amd64 \
#                        --version 0.1.0 --outdir dist
#
# Deliberately does NOT build. Cairn is cgo (SQLite/FTS5) and cross-compiling
# cgo honestly means a cross toolchain per target; the release workflow
# sidesteps the whole question by building each artifact on a NATIVE runner and
# then calling this. Keeping packaging separate from building is also what lets
# the whole path be rehearsed locally on one machine.
#
# The tarball carries the licence (PolyForm 1.0.0 has a Required Notice, so it
# travels with every copy), the README, and the embed bootstrap script — a brew
# user has no checkout to run scripts/ from.
set -eu

BIN=""; OS=""; ARCH=""; VERSION=""; OUTDIR="dist"
while [ $# -gt 0 ]; do
  case "$1" in
    --binary) BIN="$2"; shift 2 ;;
    --os) OS="$2"; shift 2 ;;
    --arch) ARCH="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --outdir) OUTDIR="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
for req in BIN OS ARCH VERSION; do
  eval "v=\$$req"
  [ -n "$v" ] || { echo "--$(echo "$req" | tr '[:upper:]' '[:lower:]') is required" >&2; exit 2; }
done
[ -x "$BIN" ] || { echo "not an executable binary: $BIN" >&2; exit 2; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$OUTDIR"
OUTDIR="$(cd "$OUTDIR" && pwd)"

NAME="cairn_${VERSION}_${OS}_${ARCH}"
STAGE="$(mktemp -d)/$NAME"
mkdir -p "$STAGE"

cp "$BIN" "$STAGE/cairn"
chmod 0755 "$STAGE/cairn"
cp "$ROOT/LICENSE" "$ROOT/README.md" "$STAGE/"
cp "$ROOT/scripts/cairn-embed-bootstrap.sh" "$STAGE/"

# Flat tarball (cairn at the root, no version-named parent directory): that is
# what the Homebrew formula's `bin.install "cairn"` expects, and it means a
# plain `tar xzf` puts a runnable binary in front of someone who is not using
# brew at all.
#
# COPYFILE_DISABLE stops bsdtar on macOS from smuggling AppleDouble `._`
# companions into the archive. The fixed mtime and `gzip -n` mean packaging the
# SAME binary twice yields byte-identical tarballs, so an operator can re-run
# this and compare hashes. That is a property of the PACKAGING step only — the
# Go build itself is not claimed to be reproducible.
find "$STAGE" -exec touch -t 200001010000.00 {} + 2>/dev/null || true
( cd "$STAGE" && COPYFILE_DISABLE=1 tar cf - . | gzip -n > "$OUTDIR/$NAME.tar.gz" )
rm -rf "$(dirname "$STAGE")"

# One checksums.txt for the whole release, appended to per artifact. `shasum -a
# 256` exists on macOS and Linux; `sha256sum` does not exist on macOS.
( cd "$OUTDIR" && shasum -a 256 "$NAME.tar.gz" >> checksums.txt )

echo "packaged $OUTDIR/$NAME.tar.gz"
