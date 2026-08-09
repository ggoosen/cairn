#!/usr/bin/env bash
# cairn-embed-bootstrap.sh — provision the real-model embedding venv for cairn.
#
# FIX-G5: parity across macOS and Linux. Without this venv cairn runs
# lexical-only (fully functional; `retrieval_mode` says so). This creates the
# pinned sentence-transformers venv the daemon auto-detects at
# <cairn-dir>/.cairn/embed-venv, so semantic search works the same on every
# node (the pre-N9 live run found NODE-B on Linux running lexical-only).
#
# Usage:
#   scripts/cairn-embed-bootstrap.sh [CAIRN_DIR]
# CAIRN_DIR defaults to ~/cairn. Requires python3 (3.9+) with venv + pip.
# After it completes, restart the daemon and run: cairn reindex --semantic
set -euo pipefail

CAIRN_DIR="${1:-$HOME/cairn}"
VENV_DIR="$CAIRN_DIR/.cairn/embed-venv"
MODEL="sentence-transformers/all-MiniLM-L6-v2"

if ! command -v python3 >/dev/null 2>&1; then
  echo "error: python3 not found on PATH (need 3.9+ with venv + pip)" >&2
  exit 1
fi

echo "cairn embed bootstrap"
echo "  cairn dir : $CAIRN_DIR"
echo "  venv      : $VENV_DIR"
echo "  model     : $MODEL"
echo "  platform  : $(uname -s) $(uname -m)"

mkdir -p "$CAIRN_DIR/.cairn"
python3 -m venv "$VENV_DIR"
# shellcheck disable=SC1091
"$VENV_DIR/bin/pip" install --upgrade pip >/dev/null
"$VENV_DIR/bin/pip" install "sentence-transformers"

# Pre-download the pinned model so first-query latency is not a surprise and
# an offline node still works. P4-G2: the cache lives INSIDE the venv
# (deterministic location) and its content hash is RECORDED — the daemon
# verifies the pin before starting the worker, so a swapped/tampered model
# degrades to lexical-only with a loud warning instead of silently
# embedding with something else.
export HF_HOME="$VENV_DIR/hf-cache"
"$VENV_DIR/bin/python3" - "$MODEL" <<'PY'
import sys
from sentence_transformers import SentenceTransformer
m = sys.argv[1]
print(f"downloading + caching {m} ...")
SentenceTransformer(m)
print("model cached.")
PY

# Record the model pin: sha256 over every regular cached file (sorted
# relpath + content). The daemon recomputes this exact walk at startup.
"$VENV_DIR/bin/python3" - "$VENV_DIR" <<'PY'
import hashlib, os, sys
venv = sys.argv[1]
cache = os.path.join(venv, "hf-cache")
h = hashlib.sha256()
paths = []
for root, dirs, files in os.walk(cache):
    dirs.sort()
    for f in sorted(files):
        p = os.path.join(root, f)
        if os.path.islink(p):
            continue
        paths.append(p)
for p in sorted(paths):
    rel = os.path.relpath(p, cache)
    h.update(rel.encode())
    h.update(b"\x00")
    with open(p, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
with open(os.path.join(venv, "model.hash"), "w") as out:
    out.write("sha256:" + h.hexdigest() + "\n")
print("model pin recorded:", "sha256:" + h.hexdigest())
PY

echo
echo "done. Restart the daemon (cairn daemon), then backfill embeddings:"
echo "  cairn reindex --semantic"
