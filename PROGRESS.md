# Cairn P0 — Build Progress

Maintained per CLAUDE.md. One milestone at a time; a milestone is done only
when all BUILD-PLAN acceptance criteria pass.

## Milestone status

| Milestone | Status | Notes |
|---|---|---|
| M0 — Scaffold, config, identity, encryption check | **done** (2026-07-11) | all acceptance criteria pass; see below |
| M1 — Event log core | not started | frame encode/decode + initial-segment write already exist (pulled forward, see deviations) |
| M2 — Object store + text classes | not started | |
| M3 — SQLite projection + FTS + reindex | not started | |
| M4 — Daemon, CLI, outbox, receipts | not started | |
| M5 — Exports, 3-way merge, conflicts | not started | |
| M6 — Embeddings, ranking, digest, why-ranked | not started | |
| M7 — Telemetry, gates harness, full fault matrix | not started | |
| M8 — Dogfood package | not started | |

## M0 — Scaffold, config, identity, encryption check

**Status: DONE.** All acceptance criteria pass (verified on the dev machine,
FileVault on, real `fdesetup` path — not just the injected fake).

Tasks completed (one commit each):
- [x] **Rename mesh → cairn** across CLAUDE.md, README, build/ files.
      Protocol identifiers renamed: domain separator `cairn-event-v1`,
      envelope field `cairn_id`, genesis type `cairn.genesis`, quote prefix
      `> [CAIRN] `, schema `$id` `cairn/p0-events/v1`. docs/ retain the
      historical "mesh" naming (note added at top of build/ARCHITECTURE.md).
- [x] Repo layout per CLAUDE.md; Go module `github.com/ggoosen/cairn`
      (go 1.26); cobra CLI skeleton; placeholder doc.go for later-milestone
      packages.
- [x] `internal/config/constants.go` — every tunable, commented with its
      ruling/spec source (frame format, seal thresholds, text-class limits,
      both P0 ranking profiles, half-lives, RRF k, budgets, gates, reserve,
      FTS tokenizer, paths/permissions).
- [x] Versioned TOML config: portable `cairn.toml` + device-local
      `config-device.toml`; strict unknown-key rejection; 0600 on device
      config; `--dir` / `$CAIRN_DIR` / `~/cairn` resolution;
      device state at `~/Library/Application Support/cairn/<cairn_id>/device`
      (macOS) or `$XDG_DATA_HOME/cairn/<cairn_id>/device` (Linux).
- [x] Event core: envelope (spec §4.1), RFC 8785 canonical JSON via
      `gowebpki/jcs`, no-floats enforcement with test vectors,
      signing_bytes/record_bytes split (rulings §1), event_id =
      BLAKE3("cairn-event-v1" || signing_bytes),
      signature = Ed25519(cairn_id || event_id), unknown payload fields
      preserved through verification, key IDs = BLAKE3 of raw pubkey bytes.
- [x] Identity: Ed25519 keygen; root-signed device certs (canonical-JSON
      cert signing per schema); genesis build + full verification (envelope
      sig by certified device key, event_id, root sig on cert, cross-field
      consistency); `cairn init` ceremony; restore-without-identity
      detection refuses writes under the old origin.
- [x] Encrypted-volume check: macOS `fdesetup status` (with boot-container
      device guard — external volumes report unknown); Linux best-effort
      findmnt+lsblk dm-crypt walk; **unknown fails closed**;
      `--allow-unencrypted` persists device-local and warns on every start.
- [x] CLI: `cairn init [--allow-unencrypted] [--display-name]`,
      `cairn identity show` (re-verifies genesis on every invocation).

### Acceptance criteria → evidence
1. *`cairn init` on a FileVault Mac produces a valid genesis event
   (verifiable)* — real run on dev machine (FileVault On, real fdesetup):
   genesis event `6d3d6e7b…be5c` created and re-verified by
   `cairn identity show` (envelope signature, event_id hash, root-signed
   cert). Covered in CI by `TestInitAndIdentityShow`,
   `TestInitializeProducesVerifiableGenesis`.
2. *Portable/device-local separation correct (no key material under the
   cairn dir)* — portable dir contains only `cairn.toml`, `events/`,
   `objects/`, `exports/`, `views/`, `.cairn/`; both keys live device-local
   with 0600. `TestNoKeyMaterialUnderPortableDir` walks the portable tree
   asserting no key files/material.
3. *Unencrypted volume refuses start without the flag* —
   `TestInitRefusesUnencryptedWithoutFlag`, `TestInitRefusesUnknownVolume`
   (fail closed on indeterminate), `TestAllowUnencryptedPersistsAndWarns`
   (override persisted device-local, warns on init AND on every later start).

### Deviations
- **Frame writer pulled forward from M1** (`internal/log/frame.go`): frame
  encode/decode (magic `CRNF` v1, u64-LE length, CRC32C) and
  `WriteInitialSegment` with write→fsync→dir-fsync-chain ordering, so genesis
  persists in the final on-disk format instead of a throwaway one. M1 still
  owns: general append path, durability-ordering integration with objects,
  recovery/trailing-frame truncation, sealing, doctor, fault-injection fs
  wrapper, crash matrix rows 1–8.
- **`CAIRN_FAKE_VOLUME_STATUS` env hook** in `SystemVolumeChecker`: the
  process-boundary fault-injection point for the TESTING.md volume-state
  rows (encrypted/unencrypted/indeterminate). Documented test-only.
- **UUID note:** `github.com/google/uuid` v1.6.0 `NewV7()` used per CLAUDE.md.

### Author rulings needed
- **Root key storage (P0):** spec §3.1 places the root key in offline
  operator recovery material, but P0 `device migrate` (M4) must root-sign
  `device.revoke` offline. Conservative interpretation implemented: root key
  stored in device-local state (0600, never portable), and `cairn init`
  prints an explicit "back up the root key to offline storage" instruction.
  Marked in code at `internal/identity/initialize.go` (root-key save).
  Ruling wanted: should P0 require an interactive export-then-delete
  ceremony instead?

### Test results (2026-07-11)
`go test ./...` — all green:
config, event, identity, log, cmd/cairn (26 tests total, incl. CLI-level
acceptance tests). `go vet ./...` clean. Real-machine acceptance run
recorded above.

## Resume-cold notes
- Next milestone: **M1 — Event log core**. Start with the fault-injecting
  fs wrapper (TESTING.md harness requirement) — it is reused by every later
  milestone. Then general append + recovery on top of the existing frame
  code, crash tests written WITH the code (rows 1–8 × SIGKILL × power-sim).
- `// RULING-NEEDED:` markers in code: one (root-key storage, see above).
