# Cairn build convention: FTS5 requires the sqlite_fts5 build tag on
# mattn/go-sqlite3 (see CLAUDE.md library table). ALWAYS build/test via
# these targets or pass -tags sqlite_fts5 manually.
GOTAGS := -tags sqlite_fts5

.PHONY: build test test-short vet all

all: vet test build

build:
	go build $(GOTAGS) ./...
	go build $(GOTAGS) -o bin/cairn ./cmd/cairn

test:
	go test $(GOTAGS) ./...

test-short:
	go test $(GOTAGS) -short ./...

vet:
	go vet $(GOTAGS) ./...
