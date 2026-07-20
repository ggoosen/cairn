#!/usr/bin/env sh
# Cairn one-shot installer — from source, user-owned, no sudo.
#
#   curl -fsSL https://raw.githubusercontent.com/ggoosen/cairn/master/install.sh | sh
#   # or, from a checkout:  ./install.sh
#
# It builds the binary, installs it to ~/.local/bin, and runs `cairn setup`
# (creates the mesh, installs the daemon service, wires up MCP clients). Because
# the daemon service is registered at the installed binary path, everything
# stays user-owned — no /usr/local, no root.
#
# Requirements today: Git + Go 1.23+ (Cairn uses cgo/FTS5, so a prebuilt
# signed/notarized binary + Homebrew tap is the planned zero-dependency path;
# until then this builds from source). Override paths with CAIRN_PREFIX /
# CAIRN_REPO / CAIRN_REF.
set -eu

REPO_URL="${CAIRN_REPO:-https://github.com/ggoosen/cairn}"
REF="${CAIRN_REF:-master}"
PREFIX="${CAIRN_PREFIX:-$HOME/.local}"

say()  { printf '\033[1m%s\033[0m\n' "$*"; }
warn() { printf '\033[33m%s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31merror: %s\033[0m\n' "$*" >&2; exit 1; }

# --- prerequisites ----------------------------------------------------------
command -v go  >/dev/null 2>&1 || die "Go 1.23+ is required (https://go.dev/dl). Cairn builds from source for now."
command -v git >/dev/null 2>&1 || die "git is required."
case "$(uname -s)" in
  Darwin|Linux) : ;;
  *) die "unsupported OS $(uname -s) — macOS (primary) and Linux (best-effort) only." ;;
esac

# --- locate or fetch the source ---------------------------------------------
# If run from inside a Cairn checkout, build that; otherwise clone a fresh copy.
SRC=""
if [ -f "Makefile" ] && [ -f "go.mod" ] && grep -q "ggoosen/cairn" go.mod 2>/dev/null; then
  SRC="$(pwd)"
  say "Using the current checkout: $SRC"
else
  SRC="$(mktemp -d)/cairn"
  say "Cloning $REPO_URL ($REF) ..."
  git clone --depth 1 --branch "$REF" "$REPO_URL" "$SRC" >/dev/null 2>&1 \
    || git clone "$REPO_URL" "$SRC" >/dev/null 2>&1 \
    || die "clone failed."
fi

# --- build + install + setup -------------------------------------------------
cd "$SRC"
say "Building + installing to $PREFIX/bin (no sudo) and running setup ..."
make deploy DEPLOY_PREFIX="$PREFIX"

# --- PATH hint ---------------------------------------------------------------
case ":$PATH:" in
  *":$PREFIX/bin:"*) : ;;
  *) warn "Add $PREFIX/bin to your PATH:  echo 'export PATH=\"$PREFIX/bin:\$PATH\"' >> ~/.zshrc && exec zsh" ;;
esac

say "Done. Restart Claude Desktop / Claude Code to load the MCP tools, then: cairn digest --view operator --budget 1500"
