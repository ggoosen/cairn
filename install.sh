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
# Requirements today: Git + Go 1.25+ — or any Go 1.21+ with GOTOOLCHAIN=auto
# (the default), which fetches the pinned toolchain automatically. Cairn uses
# cgo/FTS5, so this path needs a C toolchain; the zero-dependency path is the
# Homebrew tap (`brew tap ggoosen/cairn && brew install cairn`), which installs
# a prebuilt binary and goes live with the first tagged release — see D6 in
# build/BUILD-PLAN.md. Override paths with CAIRN_PREFIX /
# CAIRN_REPO / CAIRN_REF. On a machine with disk encryption OFF (FileVault
# disabled), set CAIRN_ALLOW_UNENCRYPTED=1 to proceed anyway.
set -eu

REPO_URL="${CAIRN_REPO:-https://github.com/ggoosen/cairn}"
RAW_URL="${CAIRN_RAW_URL:-https://raw.githubusercontent.com/ggoosen/cairn/master/install.sh}"
REF="${CAIRN_REF:-master}"
PREFIX="${CAIRN_PREFIX:-$HOME/.local}"

say()  { printf '\033[1m%s\033[0m\n' "$*"; }
warn() { printf '\033[33m%s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31merror: %s\033[0m\n' "$*" >&2; exit 1; }

# --- prerequisites ----------------------------------------------------------
command -v go  >/dev/null 2>&1 || die "Go 1.25+ is required (https://go.dev/dl). Cairn builds from source for now."
command -v git >/dev/null 2>&1 || die "git is required (at build time AND at runtime: cairn shells out to \`git merge-file\`)."

# Go must be new enough to either build directly (1.25+) or fetch the pinned
# toolchain itself (1.21+ with GOTOOLCHAIN=auto). Below that, the build fails
# deep in the compiler with an unhelpful message — catch it here instead.
GO_MINOR="$(go env GOVERSION 2>/dev/null | sed -n 's/^go1\.\([0-9]*\).*/\1/p')"
if [ -n "$GO_MINOR" ] && [ "$GO_MINOR" -lt 21 ]; then
  die "Go $(go env GOVERSION) is too old. Install Go 1.25+ (https://go.dev/dl), or
       any Go 1.21+ with GOTOOLCHAIN=auto so it can fetch the pinned toolchain."
fi

# cgo (SQLite/FTS5) needs a working C toolchain.
if [ "$(go env CGO_ENABLED 2>/dev/null)" = "0" ]; then
  die "CGO_ENABLED=0, but Cairn needs cgo for SQLite/FTS5. Unset it and make sure a
       C toolchain is installed (macOS: xcode-select --install)."
fi
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
  if ! git clone --depth 1 --branch "$REF" "$REPO_URL" "$SRC" >/dev/null 2>&1; then
    git clone "$REPO_URL" "$SRC" >/dev/null 2>&1 || die "clone failed."
    # honor CAIRN_REF even when the shallow --branch clone couldn't (e.g. a SHA)
    git -C "$SRC" checkout "$REF" >/dev/null 2>&1 || warn "could not check out '$REF'; building the default branch."
  fi
fi

# --- PATH hint (printed BEFORE the build so it's visible even on failure) ----
case ":$PATH:" in
  *":$PREFIX/bin:"*) : ;;
  *) warn "Note: $PREFIX/bin is not on your PATH. After install, add it:"
     warn "  echo 'export PATH=\"$PREFIX/bin:\$PATH\"' >> ~/.zshrc && exec zsh" ;;
esac

# --- build + install + setup -------------------------------------------------
cd "$SRC"
say "Building + installing to $PREFIX/bin (no sudo) and running setup ..."
if ! make deploy DEPLOY_PREFIX="$PREFIX"; then
  die "setup did not finish — read the message above. If this machine has disk
       encryption OFF (FileVault disabled), re-run this installer with
       CAIRN_ALLOW_UNENCRYPTED=1 set, e.g.:
         curl -fsSL $RAW_URL | CAIRN_ALLOW_UNENCRYPTED=1 sh
       (from a checkout: CAIRN_ALLOW_UNENCRYPTED=1 ./install.sh)"
fi

say "Done. Restart Claude Desktop / Claude Code to load the MCP tools, then: cairn digest --view operator --budget 1500"
