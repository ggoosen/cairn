# CLAUDE.md — Cairn P0 Build Instructions

You are building **Cairn P0**: a local-first, crash-safe message and knowledge
daemon for AI agent sessions. The design has survived five rounds of adversarial
review. Your job is implementation, not redesign.

## Read order (do this before writing any code)

1. `build/ARCHITECTURE.md` — condensed implementation architecture (start here)
2. `docs/rulings-v0.3.1.md` — **binding build rulings; where anything conflicts, this wins**
3. `docs/spec-v0.3.md` — full specification (P0 scope is §12)
4. `build/BUILD-PLAN.md` — milestones M0–M8 with acceptance criteria
5. `build/TESTING.md` — crash/fault matrix (the zero-loss gate depends on it)
6. `build/schemas/p0-events.schema.json` — normative event payload schemas
7. `build/sql/projection.sql` — SQLite projection DDL

`docs/design-brief-v0.2-HISTORICAL.md` is background only. Never implement from it.

## Hard rules (non-negotiable, from the rulings)

- **Language: Go 1.23+.** Single binary `cairn` (daemon + CLI subcommands).
- **Precedence:** rulings-v0.3.1 > spec-v0.3 > this file > your judgment.
  If you find a genuine contradiction or gap, STOP, record it in
  `PROGRESS.md` under "Author rulings needed", pick the most conservative
  interpretation, mark the code with `// RULING-NEEDED:`, and continue.
- **Never** redesign around §14 (spec) decisions. Deferred features (maps,
  maintenance worker, subscriptions-as-events, MCP, networking) stay out of P0.
- **No floats in event payloads.** Integers and RFC 3339 UTC strings only.
- Canonical serialization: **RFC 8785 canonical JSON**. `signing_bytes`
  excludes event_id+signature; `record_bytes` includes both.
- Crypto: **Ed25519** signatures, **BLAKE3** for all content addressing,
  **CRC32C** for frame checksums only.
- IDs: **UUIDv7** for logical IDs; BLAKE3 hex for event/content IDs.
- Durability ordering (rulings §3) is sacred: objects → fsync → rename →
  dir-fsync → frame append → fdatasync → THEN ack. Never ack early.
  Never issue a receipt before event durability.
- The verified log is authoritative; sequence-state files are caches.
- Every acknowledged event must survive the crash matrix in TESTING.md.
- SQLite: WAL, synchronous=FULL, single writer, projection checkpoint
  commits in the same transaction as the projection.
- Enrichment: append+fsync+ack synchronous; FTS insert synchronous;
  embeddings on a background goroutine. Agents NEVER wait and NEVER see
  rejection after acknowledgement.
- Sealed segments and stored objects are immutable. Never overwrite an object.
- Device private keys live in device-local state, never in the portable
  cairn directory. A portable-data-only restore creates a new origin.
- Platform: macOS arm64 primary; Linux best-effort; no Windows.

## Key library choices (pinned intent; substitute only if broken, and record why)

| Concern | Choice |
|---|---|
| SQLite | `mattn/go-sqlite3` (CGO; FTS5 enabled via build tag) |
| Vector search | `sqlite-vec` Go bindings (`asg017/sqlite-vec-go-bindings`); brute-force cosine fallback if extension load fails |
| BLAKE3 | `lukechampine.com/blake3` |
| Ed25519 | stdlib `crypto/ed25519` |
| UUIDv7 | `github.com/google/uuid` (V7) |
| Canonical JSON | RFC 8785 implementation (e.g. `github.com/gowebpki/jcs`); verify against test vectors |
| CRC32C | stdlib `hash/crc32` (Castagnoli table) |
| Embeddings | ONNX Runtime via `github.com/yalue/onnxruntime_go` + bundled `all-MiniLM-L6-v2` ONNX model + its tokenizer (pin model artifact hash in config). If the ONNX binding proves unworkable on macOS arm64 within one working session, fall back to shelling out to a bundled Python venv (sentence-transformers) behind the same Go interface, record the deviation in PROGRESS.md, and keep the interface identical. |
| YAML front-matter | `gopkg.in/yaml.v3` with strict field whitelist |
| diff3 merge | shell out to `git merge-file` (pinned semantics); pure-Go fallback acceptable if behavior-matched by tests |
| CLI | `spf13/cobra` |

## Workflow

- Work **one milestone at a time** from BUILD-PLAN.md, in order. Do not start
  M(n+1) until M(n) acceptance criteria pass.
- Maintain `PROGRESS.md` at repo root: per milestone — status, deviations,
  rulings needed, test results.
- TDD where it matters: the event log (M1) and outbox (M4) get their crash
  tests written alongside the implementation, not after.
- Commit per completed task with message `M<milestone>: <task> — <summary>`.
- Run `go test ./...` and `go vet ./...` before every commit. Keep the build
  green at all times.
- All tunable constants live in ONE commented config module
  (`internal/config/constants.go`) — ranking weights, half-lives, seal
  thresholds, limits. No magic numbers in code.

## Repository layout (create in M0)

```
cairn/
├── cmd/cairn/            # main: daemon + CLI subcommands
├── internal/
│   ├── config/          # TOML config (portable + device-local) + constants.go
│   ├── identity/        # keys, certs, genesis, migrate, encrypted-volume check
│   ├── event/           # envelope, payloads, canonical JSON, signing, validation
│   ├── log/             # segments, framing, append, seal, recovery, doctor
│   ├── object/          # content-addressed store, text classes, TTL housekeeping
│   ├── projection/      # SQLite schema, replay, FTS, checkpoint, reindex
│   ├── embed/           # ONNX embedding interface + background enricher
│   ├── rank/            # RRF fusion, percentile, P0 profiles, why-ranked
│   ├── views/           # digest/exports/fetched/conflicts generation
│   ├── outbox/          # bundle ingest, receipts, idempotency
│   ├── merge/           # diff3, two-revision merge events, conflicts/
│   ├── telemetry/       # local interaction log, outcome commands, gates report
│   └── daemon/          # lifecycle, single-writer lock, IPC (unix socket JSON)
├── testdata/
├── PROGRESS.md
└── CLAUDE.md            # this file, copied into the repo
```

## Definition of done for P0

Every engineering gate in spec §11 (as amended by rulings §10) passes:
zero acknowledged-event loss across the full TESTING.md matrix; 100%
provenance on fetched results; 100% budget compliance; P95 send-ack →
lexical-digest-visible < 200 ms on the dev machine. `cairn doctor` reports
clean on a corpus that has survived the fault matrix. The operator can then
begin the 30-handoff product evaluation described in BUILD-PLAN M8.
