#!/bin/sh
# Adopt a standalone cairn mesh into a primary one (BUILD-PLAN D5, RULINGS R34).
#
# R34 permits either a `cairn adopt-standalone` verb or a documented script.
# This is the script, deliberately: adoption is rare, irreversible in the
# direction that matters (you cannot un-publish into the primary's log), and
# every step is one the operator should SEE. A verb would hide exactly the
# steps that need reading.
#
# ------------------------------------------------------------------------
# WHAT THIS DOES, AND WHY IT CANNOT DO ANYTHING ELSE
#
# A standalone mesh has its OWN genesis and root key. That is a different
# trust domain by design, so its origin logs cannot be merged into another
# mesh — not "are not yet", cannot: every event is signed under a root the
# primary mesh has never admitted, and admitting one would mean admitting
# them all. Adoption therefore RE-PUBLISHES the knowledge:
#
#   1. export every live message from the standalone mesh as Markdown
#      (`cairn export corpus`);
#   2. import that tree into the primary mesh through the ordinary,
#      idempotent, provenance-carrying path (`cairn ingest scan` → review →
#      `cairn ingest apply`);
#   3. verify BOTH meshes with `cairn doctor`;
#   4. retire the standalone mesh — mark it, keep it, never delete it.
#
# PRESERVED
#   - every live message's BODY, byte for byte (the content hash is what
#     makes a re-run a no-op, so nothing may be added to it);
#   - its topic path, mirrored into the primary under one repo label so an
#     adopted message never passes for a native one;
#   - provenance: source_ref records "<label>/<topic path>/<ORIGINAL message
#     id>.md", so the primary can always name where a message came from;
#   - the standalone mesh's ENTIRE log, untouched, on disk. Adoption reads
#     it; it never appends to, rewrites or deletes it, and `cairn doctor`
#     stays clean on both origins. The only things this writes into the
#     standalone directory are `exports/corpus-<timestamp>/` (a regenerated
#     view, like every other export) and `RETIRED.md`.
#
# NOT PRESERVED — read this list before running, it is the honest half
#   - event identity, message ids, signatures, origins. Re-published content
#     is NEW events in the primary's log, authored by the adopting device.
#   - revision history. Only the current head body crosses; superseded
#     revisions stay in the standalone log.
#   - original sender and timestamps. Adopted messages are sent by the
#     principal "ingest" with today's wall time; the original id survives in
#     source_ref, the original sender does not.
#   - secondary topic links. A message with several topics is filed under one
#     of them; the export manifest lists all of them so you can re-link.
#   - threads/replies, pins, priorities, subscriptions, attachments/blobs,
#     retracted messages (deliberately: a retraction is a decision), and the
#     local interaction log.
#
# NEVER DO THIS INSTEAD (the trap this procedure exists to avoid)
#   Do NOT copy the standalone mesh's `events/`, `.cairn/` or device key into
#   the primary directory, and do NOT run `cairn init --adopt` over a primary
#   mesh. Cairn treats a peer event at or beyond the head of our OWN active
#   origin as a device clone (N8): the receiving node freezes its own origin
#   and reports an equivocation naming a clone that does not exist. You would
#   be repairing a fork that never happened. Re-publish; do not transplant.
#
# ENROLLING THE MACHINE is a separate ceremony. If the standalone machine
# should also become a DEVICE of the primary mesh, run the N5 enrolment
# (DOGFOOD.md §11) — it needs the root key and is not part of this script.
# ------------------------------------------------------------------------
#
# Usage:
#   cairn-adopt-standalone.sh --standalone <dir> --primary <dir> [options]
#   cairn-adopt-standalone.sh --from-export <dir> --primary <dir> --repo <label> [options]
#
#   --standalone DIR   the standalone mesh (needs its daemon running)
#   --from-export DIR  an ALREADY exported corpus tree — the two-machine path:
#                      run `cairn export corpus` on the other machine, copy
#                      the printed directory across, then point here
#   --primary DIR      the mesh to adopt INTO (needs its daemon running)
#   --repo LABEL       provenance label and topic prefix
#                      (default: standalone-<source mesh id prefix>)
#   --yes              do not stop for manifest review (CI/tests only —
#                      reviewing the manifest is the point of the pause)
#   --cairn PATH       the cairn binary (default: cairn)
#   --manifest PATH    where to write the ingest manifest
set -eu

CAIRN=cairn
STANDALONE=
FROM_EXPORT=
PRIMARY=
REPO=
ASSUME_YES=0
MANIFEST=

die() { echo "error: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --standalone)  STANDALONE="${2:?--standalone needs a path}"; shift 2 ;;
    --from-export) FROM_EXPORT="${2:?--from-export needs a path}"; shift 2 ;;
    --primary)     PRIMARY="${2:?--primary needs a path}"; shift 2 ;;
    --repo)        REPO="${2:?--repo needs a label}"; shift 2 ;;
    --cairn)       CAIRN="${2:?--cairn needs a path}"; shift 2 ;;
    --manifest)    MANIFEST="${2:?--manifest needs a path}"; shift 2 ;;
    --yes)         ASSUME_YES=1; shift ;;
    -h|--help)     sed -n '1,80p' "$0"; exit 0 ;;
    *)             die "unknown argument $1 (see --help)" ;;
  esac
done

[ -n "$PRIMARY" ] || die "--primary is required"
[ -f "$PRIMARY/cairn.toml" ] || die "$PRIMARY is not a cairn directory"
if [ -n "$STANDALONE" ] && [ -n "$FROM_EXPORT" ]; then
  die "pass --standalone OR --from-export, not both"
fi
if [ -z "$STANDALONE" ] && [ -z "$FROM_EXPORT" ]; then
  die "pass --standalone <dir> or --from-export <dir>"
fi

# Refuse the two ways this is not an adoption at all.
if [ -n "$STANDALONE" ]; then
  [ -f "$STANDALONE/cairn.toml" ] || die "$STANDALONE is not a cairn directory"
  if [ "$(cd "$STANDALONE" && pwd -P)" = "$(cd "$PRIMARY" && pwd -P)" ]; then
    die "the standalone and primary directories are the same mesh"
  fi
  SRC_ID=$(grep '^cairn_id' "$STANDALONE/cairn.toml" | head -1 | cut -d'"' -f2)
  DST_ID=$(grep '^cairn_id' "$PRIMARY/cairn.toml" | head -1 | cut -d'"' -f2)
  if [ -n "$SRC_ID" ] && [ "$SRC_ID" = "$DST_ID" ]; then
    die "both directories are the SAME mesh ($SRC_ID) — you want 'cairn peer add' + 'cairn sync now', not adoption"
  fi
fi

echo "== cairn adopt-standalone =="
echo
echo "PRESERVED:      message bodies (verbatim), topic paths, provenance"
echo "                (source_ref names the ORIGINAL message id), and the"
echo "                standalone mesh's whole LOG — read, never appended to"
echo "                (only exports/ and RETIRED.md are written there)."
echo "NOT PRESERVED:  event identity, signatures, origins, revision history,"
echo "                original sender/timestamps, secondary topic links,"
echo "                threads, pins, priorities, subscriptions, attachments,"
echo "                retracted messages."
echo "NEVER:          do not copy events/, .cairn/ or the device key across,"
echo "                and do not 'cairn init --adopt' over the primary. That"
echo "                is read as a device clone (N8) and freezes an origin."
echo

# ---- 1. export the standalone corpus ------------------------------------
if [ -n "$STANDALONE" ]; then
  echo "1) exporting the standalone corpus (read-only on $STANDALONE):"
  OUT=$("$CAIRN" export corpus --dir "$STANDALONE") || die "export failed (is the standalone daemon running?)"
  echo "$OUT"
  FROM_EXPORT=$(echo "$OUT" | sed -n 's/.*"root": "\(.*\)".*/\1/p')
  [ -n "$FROM_EXPORT" ] || die "could not read the export root out of: $OUT"
  [ -n "$REPO" ] || REPO=$(echo "$OUT" | sed -n 's/.*"repo_label": "\(.*\)".*/\1/p')
else
  echo "1) using the corpus already exported to $FROM_EXPORT"
fi
[ -d "$FROM_EXPORT" ] || die "$FROM_EXPORT does not exist"
[ -f "$FROM_EXPORT/cairn-corpus-export.json" ] ||
  die "$FROM_EXPORT has no cairn-corpus-export.json — is it a 'cairn export corpus' tree?"
[ -n "$REPO" ] || die "--repo is required with --from-export (use the repo_label the export printed)"
echo "   corpus: $FROM_EXPORT"
echo "   label:  $REPO"
echo

# ---- 2. scan into a reviewable manifest ---------------------------------
[ -n "$MANIFEST" ] || MANIFEST="$FROM_EXPORT/cairn-ingest-manifest.json"
echo "2) planning the import into $PRIMARY (nothing is written yet):"
"$CAIRN" ingest scan "$FROM_EXPORT" --repo "$REPO" --manifest "$MANIFEST" --dir "$PRIMARY" ||
  die "ingest scan failed (is the primary daemon running?)"
echo

if [ "$ASSUME_YES" -ne 1 ]; then
  echo "   REVIEW $MANIFEST now. Entries marked 'publish' are new to the"
  echo "   primary; 'skip' means an identical body is already there (a re-run"
  echo "   of this script is a no-op, by design)."
  printf "   Apply it? [y/N] "
  read -r answer
  case "$answer" in
    y|Y|yes|YES) ;;
    *) echo "   stopped; nothing was written to $PRIMARY."; exit 1 ;;
  esac
fi

# ---- 3. apply -----------------------------------------------------------
echo "3) publishing into the primary mesh:"
"$CAIRN" ingest apply --manifest "$MANIFEST" --dir "$PRIMARY" || die "ingest apply failed"
echo

# ---- 4. verify BOTH origins --------------------------------------------
echo "4) verifying both meshes:"
echo "   primary:"
"$CAIRN" doctor --dir "$PRIMARY" || die "doctor is NOT clean on the primary mesh"
if [ -n "$STANDALONE" ]; then
  echo "   standalone:"
  "$CAIRN" doctor --dir "$STANDALONE" || die "doctor is NOT clean on the standalone mesh"
fi
echo

# ---- 5. retire ----------------------------------------------------------
if [ -n "$STANDALONE" ]; then
  RETIRED="$STANDALONE/RETIRED.md"
  {
    echo "# retired standalone mesh"
    echo
    echo "This mesh was adopted into another cairn mesh on $(date -u +%Y-%m-%dT%H:%M:%SZ)."
    echo
    echo "- adopted into: $PRIMARY"
    echo "- exported corpus: $FROM_EXPORT"
    echo "- ingest label: $REPO"
    echo
    echo "Its knowledge was RE-PUBLISHED into the primary mesh; its events were"
    echo "not and cannot be merged (different genesis and root — RULINGS.md R34)."
    echo "This directory is kept, complete and verifiable, as the record of"
    echo "where that content came from. Do not delete it, and do not resume"
    echo "writing to it: new messages here would be invisible to the primary"
    echo "mesh and would have to be adopted all over again."
  } > "$RETIRED"
  echo "5) retired: wrote $RETIRED"
  echo "   Stop the standalone daemon and leave the directory in place:"
  echo "     pkill -f 'cairn daemon --dir $STANDALONE'   # or stop its service"
  echo "   Nothing was deleted. Both origins remain verifiable."
else
  echo "5) retire the standalone mesh on its own machine: stop its daemon and"
  echo "   keep the directory. Nothing here deleted anything."
fi
echo
echo "ADOPTION COMPLETE."
