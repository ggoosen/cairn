# Cairn P0 — Build Progress

Maintained per CLAUDE.md. One milestone at a time; a milestone is done only
when all BUILD-PLAN acceptance criteria pass.

## Milestone status

| Milestone | Status | Notes |
|---|---|---|
| M0 — Scaffold, config, identity, encryption check | **done** (2026-07-11) | all acceptance criteria pass; see below |
| M1 — Event log core | **done** (2026-07-11) | crash matrix rows 1–8 green; benchmark recorded |
| M2 — Object store + text classes | **done** (2026-07-11) | object crash rows green; content_expired typed; policy tested |
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

## M1 — Event log core

**Status: DONE.** All acceptance criteria pass.

Tasks completed (one commit each):
- [x] **Fault-injecting fs wrapper** (`internal/fsx`): FS/File interface over
      os calls; production `OS` impl; `MemFS` with a real durability model
      (file data durable at fsync; namespace entries durable at parent dir
      fsync; power-cycle drops unsynced state; SIGKILL keeps page cache),
      crash-at-op-N / fail-at-op-N (ENOSPC, EIO, short write) injection,
      `Clone()` for crash-point enumeration; `WriteFileAtomic` = the durable
      publish primitive (temp → fsync → rename → dir-fsync) reused by seal
      sidecars now, objects (M2) and receipts (M4) later.
- [x] **Log core** (`internal/log`): durable `Append` (frame → write →
      fdatasync → dir-fsync-on-create; caller acks only after return, chain
      position validated); crash-safe seal via atomic sidecar
      `seg_N.seal.json` {first_seq, last_seq, event_count, root_hash =
      BLAKE3 over ordered raw event-id bytes} — the .seg file never mutates,
      sidecar existence IS the sealed state; recovery (`Open`): scan in
      order, verify every record (hash + signature + chain contiguity),
      truncate ONLY a trailing invalid frame of the open segment, complete
      an interrupted threshold seal, next_sequence derived from the log;
      `Doctor` (VerifyOnly, never repairs); `ChainVerifier` in identity
      learns keys from genesis/device.add, marks device.revoke.
- [x] **Interior vs trailing distinction:** after a frame error, recovery
      resyncs forward by magic; any later valid frame ⇒ interior corruption
      ⇒ hard error (never silently drop acked events). Frame lengths capped
      by `MaxRecordBytes` (16 MiB) so corrupt length fields error instead of
      allocating.
- [x] **Crash matrix (M1 slice, TESTING.md §1 rows 1–8 + row 11):** crash
      before EVERY mutating fs op of the send pipeline (object atomic write →
      append → ack → seq-cache update), × SIGKILL × power-sim; after-ack row
      7 both modes; ENOSPC/EIO/short-write at every op with no-false-ack +
      clean-recovery + successful-retry assertions; seal crash matrix (open
      segment valid OR exactly one valid sealed segment; recovery completes
      pending seals); multi-segment recovery + tampered seal header detection.
- [x] **Seq-state reconcile** (`identity.ReconcileSeqState`): cache behind →
      silent rebuild; cache ahead → log wins + warning; garbled/missing →
      rebuilt (TESTING.md seq-cache rows).
- [x] **`cairn doctor` CLI** with startup encryption check; CLI-level test:
      clean report on fresh cairn, PROBLEM + nonzero exit on a bit-flipped
      segment.

### Acceptance criteria → evidence
1. *Full M1 slice of the crash matrix green* — `TestCrashMatrixRows1to8`,
   `TestCrashAfterAck`, `TestInjectedWriteFailures`, `TestSealCrashMatrix`
   (~250 enumerated crash/fault scenarios), all green in CI.
2. *10k-event append+recover benchmark recorded* — dev machine (M-series
   mac, APFS, FileVault on), real fs, full durability ordering
   (F_FULLFSYNC per append):
   - append 10k events: **39.0 s total, 3.90 ms/event** (fsync-bound —
     consistent with the <200 ms P95 ack gate; single append IS the
     send-ack path)
   - cold recovery of 10k events with full hash+signature verification:
     **631 ms total, 63 µs/event**; exactly one sealed segment at the 10k
     threshold, seal header verified.
3. *Doctor detects a deliberately corrupted frame and a broken chain* —
   `TestDoctorDetectsCorruptFrameWithoutRepairing` (and never mutates),
   `TestBrokenChainDetected` (hash-valid frame, wrong previous_origin_
   event_id → recovery rejects, doctor names the chain break), CLI-level
   `TestDoctorCleanThenDetectsCorruption`.

### Deviations
- **`internal/fsx` added to the CLAUDE.md layout** — the TESTING.md-mandated
  fault wrapper is cross-cutting (log, object store, outbox, views), so it
  lives beside those packages rather than inside `internal/log`.
- **Seal header representation:** rulings §3.7 specify the header fields but
  not the encoding; implemented as an atomic JSON sidecar
  (`seg_N.seal.json`) so the .seg file stays byte-immutable from creation
  and sealing is a single atomic publish (crash-safe by construction).
- **`WithSealThresholds` test hook** on log construction; canonical 64 MiB /
  10k values remain the config defaults and are exercised for real by the
  10k benchmark.
- **`MaxRecordBytes` (16 MiB) added to constants** — frame-read sanity bound.
- Two bugs found BY the harness during development (working as intended):
  interior CRC corruption was initially treated as truncatable trailing
  state (fixed with forward resync), and a stale seal-sidecar temp blocked
  seal retry after crash (fixed in WriteFileAtomic).

### Author rulings needed
- None new in M1. (M0's root-key storage ruling still open.)

### Test results (2026-07-11)
`go test ./...` green across all packages (config, event, fsx, identity,
log, cmd/cairn); `go vet` clean. Crash matrix ~250 scenarios green.
Benchmark numbers above from a real-fs run of `TestAppendRecover10k`.

## M2 — Object store + text classes

**Status: DONE.** All acceptance criteria pass.

Tasks completed:
- [x] **Content-addressed store** (`internal/object`): BLAKE3 hex over raw
      uncompressed bytes; `objects/<first2>/<rest>`; `Put` = the
      temp→fsync→rename→dir-fsync atomic publish (fsx primitive); idempotent
      dedup on identical content; **verify-on-collision**: existing object
      with different bytes at the address is a hard error and is never
      touched; `Get` re-verifies content against the address (corrupt bytes
      never served as valid); benign rename-race tolerance.
- [x] **Text-class policy** (`ApplyPolicy`): >1 MiB canonical/eager →
      auto-downgrade to ephemeral; configurable per-message canonical
      ceiling (only meaningful below the fixed 1 MiB threshold; portable
      config field); daily canonical-byte ceiling via persisted
      `UsageTracker` (.cairn/canonical-usage.json, derived state, UTC-day
      rollover); operator override keeps the declared class (flag is wired
      to the CLI in M4; outbox callers never get it); downgrade is always a
      visible PolicyDecision with reason — never a rejection, no review
      queue (rulings §5).
- [x] **Ephemeral TTL + typed content_expired**: `HousekeepEphemeral(refs)`
      deletes objects whose EVERY reference is ephemeral and past the 7-day
      TTL (any canonical/eager or unexpired ref keeps it); `Fetch` returns
      the typed `*ExpiredError` (content_expired) for TTL-explained absence
      and plain ErrNotFound otherwise (doctor-reportable).
- [x] **Object-before-event wired into the M1 append path**: the crash-matrix
      send pipeline now uses the real `object.Store.Put` before `log.Append`;
      recoverAndCheck verifies acked events' objects via `store.Get`
      (content-verified), so crash rows 1–8 exercise the real store.

### Acceptance criteria → evidence
1. *Crash rows for object writes green* — `TestPutCrashMatrix` (crash before
   every mutating op of Put × SIGKILL × power-sim: address holds complete
   object or nothing; retry succeeds) plus `TestCrashMatrixRows1to8` /
   `TestInjectedWriteFailures` re-running the full pipeline against the
   real store.
2. *Expired ephemeral fetch returns content_expired while its event replays
   fine* — `TestExpiredEphemeralEventReplaysFine` (end to end: publish
   ephemeral → TTL passes → housekeeping deletes object → log recovery
   replays the event, doctor clean, fetch returns typed
   `*object.ExpiredError`); store-level `TestHousekeepingAndContentExpired`
   (incl. mixed-ref retention and idempotent housekeeping).
3. *Downgrade policy covered by tests* — `TestApplyPolicyDowngrades`
   (canonical/eager >1 MiB → ephemeral; ephemeral is the floor; override
   honored; small bodies untouched; unknown class rejected),
   `TestApplyPolicyPerMessageCeiling`, `TestDailyCanonicalCeiling`
   (persistence + UTC rollover + override accounting).

### Deviations
- **Housekeeping "loop"**: BUILD-PLAN says loop; the resident daemon that
  hosts it arrives in M4. M2 delivers the complete `HousekeepEphemeral`
  operation (idempotent, ref-driven); M4's housekeeping goroutine calls it
  with refs from the M3 projection. Recorded so M4 doesn't forget the wiring.
- **Ref-driven retention**: object files carry no metadata (immutable,
  shareable across events with different classes); retention decisions take
  the refs list (class + created_at per referencing event) as input —
  supplied by the projection from M3 on, by tests directly today. An object
  is kept if ANY reference is non-ephemeral or unexpired.

### Author rulings needed
- None new in M2.

### Test results (2026-07-11)
`go test ./...` green (incl. object package: 9 tests, crash matrix rows for
Put, policy suite, housekeeping/expiry integration). `go vet` clean.

## Resume-cold notes
- Next milestone: **M3 — SQLite projection + FTS + reindex**. DDL is in
  build/sql/projection.sql (remember: `source_refs` table was added post-M0
  as an M9 ingest hook — project message.publish `source_ref` when present).
  Replay hooks into `log.Open`'s OnRecord callback. Checkpoint row commits
  in the SAME transaction as the projection (rulings §6). mattn/go-sqlite3
  with FTS5 build tag; contentless FTS keyed by revision_id; retraction =
  flag; observed-remove link/pin tables; reindex --lexical side-build +
  atomic swap; --semantic stub. Object refs for housekeeping come from the
  projection (`object.Ref` shape already defined).
- `// RULING-NEEDED:` markers in code: one (root-key storage, M0).
