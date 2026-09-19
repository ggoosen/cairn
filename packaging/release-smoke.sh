#!/usr/bin/env sh
# D6 — release artifact smoke: prove a built binary is the binary we mean to ship.
#
#   packaging/release-smoke.sh <path-to-cairn-binary>
#
# This is the FIX-F4 guard carried forward to the ARTIFACT. The compile-time
# guard (internal/projection/notag_guard.go) proves the *source tree* refuses
# to build untagged; it says nothing about a downloaded tarball. The thing that
# would actually hurt a user — a released `cairn` whose bundled SQLite was
# compiled without -DSQLITE_ENABLE_FTS5, or one built with `cairn_novec` so the
# vector index is absent — is a RUNTIME property, so it is asserted at runtime,
# on the exact bytes that will be uploaded.
#
# It runs the real thing end to end: init a throwaway mesh, start the daemon,
# send a message, search for it. A search that returns the seeded document
# could not have happened without a working FTS5 virtual table, and `cairn
# status` names the vector path so a stray `cairn_novec` build is caught too.
#
# Everything is confined to a temp dir: $CAIRN_DIR for the portable side,
# $CAIRN_DEVICE_STATE_DIR for the device keys. Nothing touches ~/cairn or
# ~/.local/share/cairn. CI runners have unencrypted disks, so the encrypted-
# volume check is answered with the operator override flag (`--allow-
# unencrypted`) rather than the test-only fault injector, which is deliberately
# absent from a release build.
set -eu

BIN="${1:-}"
[ -n "$BIN" ] || { echo "usage: $0 <path-to-cairn-binary>" >&2; exit 2; }
[ -x "$BIN" ] || { echo "not executable: $BIN" >&2; exit 2; }
case "$BIN" in /*) ;; *) BIN="$PWD/$BIN" ;; esac

WORK="$(mktemp -d)"
CAIRN_DIR="$WORK/mesh"
CAIRN_DEVICE_STATE_DIR="$WORK/devstate"
export CAIRN_DIR CAIRN_DEVICE_STATE_DIR
DAEMON_PID=""

cleanup() {
  if [ -n "$DAEMON_PID" ]; then kill "$DAEMON_PID" 2>/dev/null || true; wait "$DAEMON_PID" 2>/dev/null || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

fail() { echo "SMOKE FAIL: $*" >&2; [ -f "$WORK/daemon.log" ] && { echo "--- daemon.log ---" >&2; cat "$WORK/daemon.log" >&2; }; exit 1; }

echo "== artifact smoke: $BIN"
file "$BIN" 2>/dev/null || true

# 1. It runs at all, and reports a version. On macOS this is also the first
#    point at which a Gatekeeper/codesign problem would surface.
VERSION="$("$BIN" --version 2>/dev/null)" || fail "\`cairn --version\` did not run (on macOS this can mean a broken or missing code signature)"
[ -n "$VERSION" ] || fail "\`cairn --version\` printed nothing"
echo "   version: $VERSION"

# 2. A real mesh on real disk.
"$BIN" init --allow-unencrypted >"$WORK/init.log" 2>&1 || { cat "$WORK/init.log" >&2; fail "cairn init"; }

"$BIN" daemon >"$WORK/daemon.log" 2>&1 &
DAEMON_PID=$!
i=0
while [ "$i" -lt 60 ]; do
  if "$BIN" status >/dev/null 2>&1; then break; fi
  kill -0 "$DAEMON_PID" 2>/dev/null || fail "daemon exited during startup"
  i=$((i + 1))
  sleep 1
done
[ "$i" -lt 60 ] || fail "daemon did not become answerable within 60s"

# 3. FTS5. The projection creates an FTS5 virtual table on first write; a
#    binary without FTS5 fails here rather than at some later user's desk.
"$BIN" send --topic release/smoke "the council approved the quarterly budget" >"$WORK/send.log" 2>&1 \
  || { cat "$WORK/send.log" >&2; fail "cairn send"; }

SEARCH="$("$BIN" search "council quarterly" 2>&1)" || { echo "$SEARCH" >&2; fail "cairn search"; }
echo "$SEARCH" | grep -q "the council approved the quarterly budget" \
  || { echo "$SEARCH" >&2; fail "lexical search did not return the seeded message — this artifact's SQLite has no working FTS5 (FIX-F4)"; }
echo "   FTS5: OK (lexical search returned the seeded message)"

# 4. sqlite-vec compiled IN. `cairn_novec` is a TEST configuration (Makefile
#    comment on GONOVECTAGS); shipping one would silently downgrade every user
#    to the brute-force cosine scan. `cairn status` names the live path.
STATUS="$("$BIN" status 2>&1)" || { echo "$STATUS" >&2; fail "cairn status"; }
echo "$STATUS" | grep -q "vector path:  *vec0" \
  || { echo "$STATUS" >&2; fail "vector path is not vec0 — this artifact looks like a cairn_novec build, which is a test configuration and must never be released"; }
echo "   sqlite-vec: OK (vector path is vec0)"

# 5. Doctor walks the log it just wrote: frames, chain, signatures, objects.
"$BIN" doctor >"$WORK/doctor.log" 2>&1 || { cat "$WORK/doctor.log" >&2; fail "cairn doctor on a freshly written mesh"; }
echo "   doctor: OK"

echo "SMOKE PASS: $BIN ($VERSION)"
