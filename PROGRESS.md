# Cairn P0 — Build Progress

Maintained per CLAUDE.md. One milestone at a time; a milestone is done only
when all BUILD-PLAN acceptance criteria pass.

## Milestone status

| Milestone | Status | Notes |
|---|---|---|
| M0 — Scaffold, config, identity, encryption check | **in progress** | rename task done; scaffold done |
| M1 — Event log core | not started | |
| M2 — Object store + text classes | not started | |
| M3 — SQLite projection + FTS + reindex | not started | |
| M4 — Daemon, CLI, outbox, receipts | not started | |
| M5 — Exports, 3-way merge, conflicts | not started | |
| M6 — Embeddings, ranking, digest, why-ranked | not started | |
| M7 — Telemetry, gates harness, full fault matrix | not started | |
| M8 — Dogfood package | not started | |

## M0 — Scaffold, config, identity, encryption check

**Status:** in progress

Task checklist:
- [x] Rename mesh → cairn (CLAUDE.md, README, build/ files; docs/ retain
      historical naming per note in build/ARCHITECTURE.md). Domain separator
      is `cairn-event-v1`; envelope field `cairn_id`; genesis `cairn.genesis`;
      quote prefix `> [CAIRN] `.
- [x] Repo layout + Go module (`github.com/ggoosen/cairn`) + cobra skeleton
- [ ] `internal/config/constants.go` — every tunable, commented
- [ ] TOML config, portable + device-local, versioned
- [ ] Ed25519 keygen, device cert, canonical event core (sign/verify)
- [ ] Encrypted-volume check (fdesetup / dm-crypt; unknown fails closed;
      `--allow-unencrypted` persisted device-local, warned every start)
- [ ] `cairn init` (genesis + initial device cert), `cairn identity show`
- [ ] Acceptance: verifiable genesis; portable/device-local separation;
      unencrypted volume refuses start without flag

### Deviations
- None yet.

### Author rulings needed
- None yet.

### Test results
- (updated per commit; run `go test ./...`)
