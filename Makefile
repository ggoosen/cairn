# Cairn build convention: FTS5 requires the sqlite_fts5 build tag on
# mattn/go-sqlite3 (see CLAUDE.md library table). ALWAYS build/test via
# these targets or pass -tags sqlite_fts5 manually.
GOTAGS := -tags sqlite_fts5

.PHONY: build test test-short vet verify all

all: vet test build

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

vet:
	go vet $(GOTAGS) ./...
