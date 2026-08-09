# Cairn build convention: FTS5 requires the sqlite_fts5 build tag on
# mattn/go-sqlite3 (see CLAUDE.md library table). ALWAYS build/test via
# these targets or pass -tags sqlite_fts5 manually.
GOTAGS := -tags sqlite_fts5

.PHONY: build test test-short test-race vet verify all install deploy fuzz

all: vet test build

# FIX-G4: install the built binary. macOS AMFI KILLS a code-signed binary that
# is overwritten in place (cost the operator a debugging cycle), so we REMOVE
# then copy — never `cp` over a running/installed binary.
PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
install: build
	@mkdir -p "$(BINDIR)" 2>/dev/null || { echo "ERROR: cannot create $(BINDIR) — retry with sudo (sudo make install) or install non-root (make install PREFIX=$$HOME/.local)"; exit 1; }
	@rm -f "$(BINDIR)/cairn" 2>/dev/null || { echo "ERROR: cannot remove old $(BINDIR)/cairn — retry with sudo (sudo make install) or non-root (make install PREFIX=$$HOME/.local)"; exit 1; }
	@cp bin/cairn "$(BINDIR)/cairn" 2>/dev/null || { echo "ERROR: cannot write $(BINDIR)/cairn — retry with sudo (sudo make install) or non-root (make install PREFIX=$$HOME/.local)"; exit 1; }
	@echo "installed cairn -> $(BINDIR)/cairn (remove-before-copy; AMFI-safe)"
	@# FIX-H7: end the stale-binary saga — a daemon running the OLD binary keeps
	@# running stale code until restarted. Detect and instruct (safer than auto-
	@# killing a daemon that may be mid-sync; launchd KeepAlive would also race).
	@# CAIRN_INSTALL_QUIET suppresses this when `make deploy` will restart it next.
	@if [ -z "$$CAIRN_INSTALL_QUIET" ] && pgrep -f "cairn daemon" >/dev/null 2>&1; then \
	  echo "NOTE: a cairn daemon is RUNNING the OLD binary — it will keep running stale code until you restart it:"; \
	  echo "      cairn daemon --install     # launchd/systemd: reloads + restarts with the new binary"; \
	  echo "      (hand-run daemon: pkill -f 'cairn daemon', then start the new one)"; \
	  echo "      until you restart, 'cairn --version' and 'cairn daemon' will warn about the mismatch."; \
	else \
	  echo "next: cairn daemon --install   # launchd (macOS) / systemd --user (Linux) — restarts + supervises"; \
	fi

# One-shot deploy for a new/updated machine: build + install to the USER prefix
# (~/.local, no sudo) + run the setup wizard (mesh, daemon service, MCP clients).
# Because `cairn setup` registers the launchd/systemd service at THIS binary path,
# deploying to ~/.local keeps everything user-owned — no /usr/local, no root.
# Override the location with `make deploy DEPLOY_PREFIX=/somewhere`.
DEPLOY_PREFIX ?= $(HOME)/.local
deploy: build
	@CAIRN_INSTALL_QUIET=1 $(MAKE) --no-print-directory install PREFIX="$(DEPLOY_PREFIX)"
	@echo "== cairn setup (mesh + daemon service + MCP clients) =="
	@# `cairn setup` prints its OWN PATH note — correct, conditional, and only
	@# when $(DEPLOY_PREFIX)/bin is actually missing from PATH. (An earlier echo
	@# here was unconditional AND expanded $$PATH into the suggestion — removed.)
	@"$(DEPLOY_PREFIX)/bin/cairn" setup

# FIX-F4: the plain (untagged) build must FAIL AT COMPILE TIME with an
# instructive error; the tagged suite must be green.
verify:
	@echo "== verifying the untagged build fails with the instructive guard =="
	@ERR=$$(mktemp); 	if go build ./... 2>"$$ERR"; then 		echo "FAIL: plain 'go build ./...' unexpectedly succeeded"; rm -f "$$ERR"; exit 1; 	elif grep -q "sqlite_fts5" "$$ERR"; then 		echo "OK: untagged build fails with the instructive message"; rm -f "$$ERR"; 	else 		echo "FAIL: untagged build failed WITHOUT the instructive message:"; cat "$$ERR"; rm -f "$$ERR"; exit 1; 	fi
	@echo "== running the tagged suite =="
	$(MAKE) vet test

build:
	go build $(GOTAGS) ./...
	go build $(GOTAGS) -o bin/cairn ./cmd/cairn

test:
	go test $(GOTAGS) ./...

test-short:
	go test $(GOTAGS) -short ./...

# The embedder, enricher goroutine, IPC connections and sync sweeps share a
# daemon: the race detector is part of the green bar, not an extra.
test-race:
	go test $(GOTAGS) -race ./...

# Short smoke of each fuzz target (CI runs this; leave -fuzztime higher for
# a real session: FUZZTIME=10m make fuzz).
FUZZTIME ?= 30s
fuzz:
	go test $(GOTAGS) -run '^$$' -fuzz FuzzCanonicalRoundTrip -fuzztime $(FUZZTIME) ./internal/event/
	go test $(GOTAGS) -run '^$$' -fuzz FuzzDecodeFrame -fuzztime $(FUZZTIME) ./internal/log/

vet:
	go vet $(GOTAGS) ./...
