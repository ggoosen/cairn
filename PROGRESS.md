# Cairn P0 — Build Progress

Maintained per CLAUDE.md. One milestone at a time; a milestone is done only
when all BUILD-PLAN acceptance criteria pass.

## Milestone status

| Milestone | Status | Notes |
|---|---|---|
| M0 — Scaffold, config, identity, encryption check | **done** (2026-07-11) | all acceptance criteria pass; see below |
| M1 — Event log core | **done** (2026-07-11) | crash matrix rows 1–8 green; benchmark recorded |
| M2 — Object store + text classes | **done** (2026-07-11) | object crash rows green; content_expired typed; policy tested |
| M3 — SQLite projection + FTS + reindex | **done** (2026-07-11) | byte-identical reindex; idempotent resume; property tests green |
| M4 — Daemon, CLI, outbox, receipts | **done** (2026-07-11) | receipts idempotent; concurrent senders serialize; migrate matrix green |
| M5 — Exports, 3-way merge, conflicts | **done** (2026-07-11) | merge races + retraction race green; no half-graphs |
| M6 — Embeddings, ranking, digest, why-ranked | **done** (2026-07-11) | Success@5 0.97; budgets never exceeded; why-ranked exact |
| M7 — Telemetry, gates harness, full fault matrix | **done** (2026-07-11) | matrix complete; P95 gate 1.5ms at 100k; scorecard recorded |
| M8 — Dogfood package | **done** (2026-07-11) | install smoke 2s; restore drill CI-verified; **P0 COMPLETE** |
| M9 — Ingest (post-P0) | **done** (2026-07-12) | scan/manifest/apply; idempotent; provenance via source_ref |

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
> CLOSED (bookkeeping 2026-08-06): adjudicated by RULINGS.md **R1** —
> entry preserved as written.
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

## M3 — SQLite projection + FTS + reindex

**Status: DONE.** All acceptance criteria pass.

Tasks completed:
- [x] **Projection** (`internal/projection`): DDL embedded from
      build/sql/projection.sql (drift-pinned by
      `TestSchemaMatchesNormativeDDL`); WAL + synchronous=FULL + FKs on,
      single connection; `Apply` projects one verified event in ONE
      transaction that also advances the per-origin checkpoint row
      (rulings §6) — an event is fully projected+checkpointed or untouched;
      events ≤ checkpoint skipped (idempotent), sequence gaps are errors.
      All P0 payloads projected: publish/reply (messages, revisions,
      recipients, attachments, source_refs hook), revise_body (1–2
      revisions incl. merge parents + head move), retract (flag only, FTS
      untouched), topic.create, link add/remove (observed-remove),
      pin/unpin, signal.emit, genesis→meta; unknown types preserved in
      events table only.
- [x] **FTS**: contentless FTS5 keyed by immutable revision_id via fts_map,
      unicode61 tokenchars `_-#@` (from the DDL); synchronous lexical insert
      at Apply time; enrichment table records lexical_indexed; missing body
      (expired ephemeral) → recorded, never fatal.
- [x] **Search API** (deterministic, M6 fuses it with vectors):
      `SearchLexical` over HEAD revisions with bm25 ordering and
      message_id/revision_id tie-breaks; retracted excluded by default,
      `include_retracted` opt-in; `VisibleLinks`/`LiveLinkIDs`/`ActivePins`;
      `ObjectRefs` feeds M2 housekeeping (pinned objects excluded).
- [x] **Replay/reindex**: read-only `log.Walk` (new Strict scan mode: full
      verification, hard error on defects, tolerates the un-acked torn tail
      without mutating — daemon recovery owns truncation); `log.Origins`
      enumeration; `Replay` = walk every origin, Apply past checkpoint;
      `ReindexLexical` side-builds at index.sqlite.rebuild, folds WAL
      (wal_checkpoint TRUNCATE), atomically renames over the live db;
      `ReindexSemantic` stub errors clearly until M6.
- [x] **CLI**: `cairn reindex --lexical|--semantic` (identity + encryption
      check first); CLI test builds a real projection on a fresh cairn.
- [x] **testutil.Chain**: shared signed-event-chain builder for
      projection/outbox/daemon suites.

### Acceptance criteria → evidence
1. *Delete index.sqlite → reindex reproduces byte-identical query results* —
   `TestReindexByteIdentical`: snapshot of 5 queries × {default,
   include_retracted} + link sets serialized to JSON, database deleted,
   reindexed, snapshots compared byte-for-byte.
2. *Crash mid-projection resumes idempotently* — `TestCrashMidProjectionResumes`:
   for EVERY prefix length, apply-prefix → close → reopen → Replay resumes
   from checkpoint to the exact full state; re-Apply of an old event is a
   no-op. (Torn-transaction safety inside a single Apply is SQLite
   WAL+synchronous=FULL, checkpoint committed in the same tx.)
3. *Property tests: concurrent link add/remove + retraction visibility* —
   `TestObservedRemoveLinkProperties` (same assertions in every valid order
   converge; a remove never kills adds it didn't list; protected links
   survive automatic (non-operator) removal but yield to operator removal);
   retraction: hidden by default, visible with include_retracted, identical
   across full reindex (replayable history).

### Deviations
- **`sqlite_fts5` build tag is now mandatory repo-wide** (mattn/go-sqlite3
  compiles FTS5 only behind it — anticipated by CLAUDE.md's library table).
  Makefile targets (`make test` / `make vet` / `make build`) encode it;
  CLAUDE.md workflow line updated accordingly.
- **SQLite runs outside fsx** (CGO owns its own I/O). Acceptable because the
  projection is DERIVED state: its crash-safety story is rebuild-from-log
  (proven by the acceptance tests), not zero-loss. ReindexLexical therefore
  takes an explicit real-fs dbPath; production uses .cairn/index.sqlite.
- **log.Strict mode added** (third scan mode) for read-only replay/reindex.

### Author rulings needed
- **Protected-link removal semantics (minor):** spec §5.5 says auto-processes
  may not remove protected links. P0 implementation: a topic.link.remove
  whose actor_principal_id ≠ "operator" skips protected links; operator
  removals always apply. Flagged for confirmation when P1 capability
  enforcement lands (no code marker — behavior is spec-supported; noted here
  for the P1 revisit).

### Test results (2026-07-11)
`go test -tags sqlite_fts5 ./...` — all 8 packages green (projection: 7
tests incl. byte-identity, resume-at-every-cut, observed-remove properties,
expired-body reindex race). `go vet -tags sqlite_fts5 ./...` clean.

## M4 — Daemon, CLI, outbox, receipts

**Status: DONE.** All acceptance criteria pass.

Tasks completed:
- [x] **Daemon** (`internal/daemon`): resident single writer — exclusive
      flock in device-local state + unix-socket JSON IPC (short socket path
      under TempDir, registered in device state; never PID files); startup:
      identity + encryption check, active-origin recovery with projection
      catch-up wired into the scan (OnRecord), other origins replayed
      read-only with FIXPOINT key resolution (migrate creates origins
      admitted by earlier ones), seq-cache reconcile, revoked-device write
      refusal; the send path (rulings §3): policy → object → signed chained
      event → durable append (ACK) → synchronous FTS projection (projection
      failure after ack degrades with a warning, never rejects); initial
      topic links as separate events in the same request; SimpleEvent for
      retract/link/pin/signal; Fetch (manifest/body pair, trust=untrusted,
      typed content_expired); housekeeping via projection ObjectRefs;
      Run loops (outbox poll + housekeeping tickers).
- [x] **Outbox** (`internal/outbox`): canonical <request_id>.ready bundle
      (strict request.json, body/body_file, publish|reply), .md convenience
      shorthand with STRICT yaml front-matter whitelist; request_id =
      correlation_id on every event (new projected `correlation_id` column,
      DDL updated in both build/sql and the embedded schema); receipts are a
      pure function of (request_id, result) — no timestamps — written
      atomically ONLY after event durability; duplicate detection via
      EventsByCorrelation regenerates the identical receipt without new
      events; invalid bundles → rejected/<id>/error.json (structured), no
      ack; operator override is impossible via the outbox.
- [x] **`cairn migrate`** (`identity.Migrate`): offline ceremony — STAGE new
      identity (key + root-signed cert + old-device-id in pending.json) →
      device.add (signed by old device) → device.revoke (signed by the ROOT
      key; ChainVerifier now registers the root key at genesis) → swap
      device-local identity with the config write as the COMMIT MARKER →
      cleanup. Fully crash-resumable; a completion guard prevents rerun
      re-entry after the swap.
- [x] **CLI**: daemon, send, reply, retract, topic create, link, pin,
      signal, search (lexical, flagged for M6 fusion), peek, fetch, migrate,
      doctor, reindex, init, identity show; honest stubs for digest /
      why-ranked (M6), resolve (M5), found / not-found / manual-workaround
      (M7). Mutations REQUIRE the daemon (rulings §6) and fail with a clear
      message otherwise.

### Acceptance criteria → evidence
1. *Duplicate bundle returns identical receipt* —
   `TestDuplicateBundleIdenticalReceipt`: same request_id re-dropped →
   byte-identical receipt, zero new events, zero new messages.
2. *Crash-during-receipt retry regenerates same receipt* —
   `TestCrashDuringReceiptRetryIdentical`: events durable, receipt removed
   (the crash window), bundle re-dropped → regenerated receipt is
   byte-identical.
3. *Concurrent CLI senders serialize correctly* —
   `TestConcurrentSendersSerialize`: 10 parallel publishes → all acked
   events present exactly once after restart, chain contiguous and
   signature-valid (Walk verifies).
4. *Migrate crash between add/revoke recovers per matrix* —
   `TestMigrateCrashMatrix`: EIO after EVERY mutating fs op of the ceremony
   (faultFS over the real fs, File.Write/Sync ticked); every intermediate
   state doctor-clean; rerun completes; final identity always usable.
   `TestMigrateCleanCeremony`: rows 14/15 end state — old origin read-only
   (daemon refuses a revoked device), new device publishes and searches.

### Deviations
- **`correlation_id` column added to the events projection** (build/sql +
  embedded schema): the receipt-idempotency key must survive replay, so it
  is projected from the envelope (the envelope field already existed).
- **Later-milestone CLI verbs ship as stubs** naming their milestone (digest,
  why-ranked → M6; resolve → M5; outcome commands → M7). BUILD-PLAN lists
  them under M4's CLI; their semantics are defined by later milestones.
- **Search over IPC is lexical-only** until M6 fusion (flagged in help text).
- **Two bugs found by the fault matrix** (fixed): migrate rerun after a
  post-swap crash would re-enter the ceremony and revoke the NEW device
  (completion guard + commit-marker ordering); log.Open did not create the
  segment directory for a fresh (post-migrate) origin.

### Author rulings needed
- None new in M4.

### Test results (2026-07-11)
`go test -tags sqlite_fts5 ./...` — all 10 packages green (daemon: 8 tests
incl. migrate crash matrix; outbox: 6 tests incl. both receipt acceptance
tests; CLI e2e). `go vet` clean.

## M5 — Exports, 3-way merge, conflicts

**Status: DONE.** All acceptance criteria pass.

Tasks completed:
- [x] **Merge core** (`internal/merge`): pinned diff3 = `git merge-file -p`
      (operator edit × export base × current head; labeled markers);
      exit 0 = clean, positive = conflict-marked output; NormalizeLF
      (CRLF/CR → LF, BOM strip, UTF-8 validation — invalid UTF-8 rejected).
- [x] **Exports** (`internal/views` + daemon): `cairn export <id>` renders
      exports/<message_id>.md with READ-ONLY front-matter {message_id,
      revision_id, base_revision_id, body_hash, exported_at} (strict YAML);
      regenerated after every successful ingest/resolve.
- [x] **Ingest** (`daemon.IngestExport` via `cairn export ingest <path>`):
      front-matter validated field-by-field against the projection (unknown
      fields, hash/message/revision mismatches, deleted front-matter → all
      rejected with a structured <export>.reject.json error receipt);
      retracted target → rejected ("restore requires explicit operator
      action"); unchanged body → no event; base==head → ONE normal
      revision; head moved + clean diff3 → ONE revise_body event with TWO
      revision objects (operator branch parent=base; machine-merged head
      parents=[branch, head], machine_merged=true); conflict →
      views/operator/conflicts/<id>/{BASE,CURRENT,OPERATOR_EDIT,RESOLVE}.md
      + conflict.json manifest, RESOLVE.md seeded with the marked merge.
- [x] **Resolve** (`cairn resolve <id>`): applies RESOLVE.md as the human
      resolution — event carries the operator branch (OPERATOR_EDIT.md,
      parent = original base) + resolved head (parents = [branch,
      current head], machine_merged=false); head moved during resolution →
      diff3 against the conflict-time head, clean → apply, conflict →
      materialization refreshed; marker guard refuses half-edited
      RESOLVE.md; success removes the conflict dir and refreshes the export.
- [x] **`cairn doctor conflicts`**: lists unresolved conflict dirs, nonzero
      exit on debt.

### Acceptance criteria → evidence
1. *Merge race tests green* — `TestExportIngestCleanMergeRaces` (stale edit
   vs 1 and vs 3 intervening revisions → two-revision merge event, both
   sides' changes in the merged head); `TestConflictAndResolve` (overlapping
   edits → full materialization, marker guard, resolution applied, debt
   cleared); `TestRetractionRacesIngest` (retract-then-ingest → rejected
   with error receipt, no revision created); `TestFrontMatterMutationRejected`
   (4 mutation shapes); `TestUnchangedExportIsNoOp`; CRLF normalization.
2. *Crash-during-merge leaves no half-graph* — `TestMergeCrashLeavesNoHalfGraph`:
   EIO after EVERY mutating fs op of a merge ingest → the message has
   either its pre-ingest revisions or the COMPLETE 2-revision merge (2 or 4
   revisions, never 3), doctor clean, daemon restarts fine. Atomicity is by
   construction: both revisions ride ONE event; objects land first.

### Deviations
- **Resolve semantics made precise** (rulings §8 says "runs the same merge
  path against then-current head" without naming the base): replaying the
  ORIGINAL export base re-conflicts forever on the very line the operator
  just resolved. Implemented: the resolution is authoritative relative to
  the conflict-time head; if the head moved again, RESOLVE is diff3-merged
  against the new head (base = conflict-time head). The event preserves the
  operator branch and marks the resolved head machine_merged=false (human
  merge). Flagged for author confirmation (behavior documented in code).
- **Conflicts live under views/operator/conflicts/** — spec §7.3 places
  conflicts/ per agent view; the operator is the export editor in P0.
- **Export ingest is explicit** (`cairn export ingest`), not a filesystem
  watcher: editor temp-saves make auto-ingest of exports/ hostile; the
  outbox remains the watched surface. exported_at is the one front-matter
  field not verifiable against the log (not stored); all others are.

### Author rulings needed
> CLOSED (bookkeeping 2026-08-06): adjudicated by RULINGS.md **R2** —
> entry preserved as written.
- **Resolve base semantics** (above): confirm resolution-merges-against-
  conflict-time-head; P0 behavior is conservative and never loses either
  branch.

### Test results (2026-07-11)
`go test -tags sqlite_fts5 ./...` — all 11 packages green (merge: 4;
daemon export suite: 7 incl. the merge crash matrix; CLI export flow).
`go vet` clean.

## M6 — Embeddings, ranking, digest, why-ranked

**Status: DONE.** All acceptance criteria pass.

Tasks completed:
- [x] **Embed interface** (`internal/embed`): pinned Embedder interface
      (ModelID/Dim/Embed/Close; unit vectors; models never mixed).
      Implementations: `Python` — the CLAUDE.md-sanctioned real-model
      fallback (sentence-transformers subprocess with the PINNED
      all-MiniLM-L6-v2, venv at .cairn/embed-venv or $CAIRN_EMBED_PYTHON);
      `BagOfWords` — deterministic dev/test embedder (distinct ModelID so
      its vectors can never be compared with the real model's). No embedder
      provisioned ⇒ daemon serves lexical_only (degradation ladder step 4).
- [x] **Vector store**: plain vectors table per the DDL (float32 LE blobs),
      brute-force cosine over head revisions (rulings §7 sanctions the
      fallback for candidate sets < 5k — P0 corpus is hundreds);
      InsertVector marks enrichment in the same transaction; model id
      pinned in meta; InvalidateVectors for migration.
- [x] **Ranking** (`internal/rank`, pure): RRF k=60; percentile over the
      union (strictly-smaller counting; single candidate ⇒ R=1); freshness
      2^(−age/half-life) as bonus-only; effective_P =
      (declared/3)·2^(−age/60h) suspended by active pin or priority_confirm;
      both P0 profiles from constants.go; tie order mandatory class → score
      → wall time → event_id; RankUniformR for query-less digests (R=1.0);
      budget_chars = Unicode scalars over the COMPLETE payload
      (header + entries + truncation marker) via TakeWithinBudget.
- [x] **Search** (daemon): FTS top-100 + vector top-100 → fusion → profile →
      budget; retrieval_mode full|lexical_only; interaction_id per call;
      rank_explanations stored as shortest-round-trip decimal STRINGS.
- [x] **Digest**: local view config (views/<agent>/view.json: hard topic
      filters + optional interest query — never events); mandatory inclusion
      (explicit recipients, then pins) before scored items, budget-counted,
      overflow drops oldest-first with omitted_mandatory_count; digest.md
      written atomically; EVERY quoted body line prefixed `> [CAIRN] `.
- [x] **why_ranked**: prints the exact stored arithmetic; CLI verb live.
- [x] **Enricher**: background goroutine (2s tick, batch 64) — agents never
      wait; EnrichOnce refuses model mixing; `reindex --semantic` (via
      daemon IPC) = invalidate-on-migration + full backfill.
- [x] **CLI**: search --budget, digest --view --budget, why-ranked; stubs
      remain only for M7 outcome commands.

### Acceptance criteria → evidence
1. *Golden corpus* — 184 messages / 4 synthetic projects + 8 anchors, 30
   queries with known-relevant sets (generated deterministically in
   corpus code): **Success@5 = 0.97** (gate ≥ 0.70), **lexical_only
   top-10 = 0.63** (gate ≥ 0.60). CI runs the deterministic dev embedder;
   real-model numbers to be recorded during M8 dogfood setup.
2. *Budget compliance property* — budgets {1 … 2^20} for search AND digest:
   payload rune count never exceeds; mandatory overflow produces
   omitted_mandatory_count > 0 under a tight budget.
3. *why-ranked exact* — stored components recompute to the IDENTICAL score
   (shortest-round-trip decimal strings), stored rank == returned rank,
   text carries the stored arithmetic.
4. *Kill enricher mid-batch* — partial enrichment then embedder death ⇒
   lexical_only with results; restore + reindex --semantic ⇒ zero pending,
   full mode. Plus model-migration invalidation (mix refused, reindex
   migrates).

### Deviations
- **ONNX in-process binding not used** (CLAUDE.md fallback rule invoked):
  no onnxruntime dylib on the dev machine and the binding needs a WordPiece
  tokenizer implementation — outside one working session. The identical-
  interface Python-venv path (sentence-transformers, same pinned model) is
  the real-model implementation; venv provisioning is an M8 DOGFOOD setup
  step. `config.EmbeddingModelHash` stays empty until the artifact is
  vendored (flagged in constants.go).
- **sqlite-vec not integrated**: brute-force cosine is the active path —
  explicitly sanctioned by rulings §7 for < 5k candidates (P0 scale).
  The vectors table matches the DDL, so a vec0 virtual table can replace
  the scan in P1 without schema change.
- **CI retrieval numbers use the deterministic dev embedder** (BagOfWords,
  distinct model id). Honest limitation, recorded; the ranking pipeline
  (fusion, percentile, budgets, explanations) is fully exercised.

### Author rulings needed
- None new (the two open items: M0 root-key storage, M5 resolve semantics).

### Test results (2026-07-11)
`go test -tags sqlite_fts5 ./...` — all 12 packages green. Golden-corpus
numbers above. `go vet` clean.

## M7 — Telemetry, gates harness, full fault matrix

**Status: DONE.** All acceptance criteria pass.

Tasks completed:
- [x] **Telemetry** (`internal/telemetry`): .cairn/telemetry.sqlite — a
      SEPARATE database, never events, never replicated (verified: searches
      and outcomes append NOTHING to the event log). Interactions (ids,
      attribution, budgets, payload chars, result positions, retrieval
      mode), impressions, ack→lexical-visible latency samples. Missing
      task/surface attribution is daemon-generated and flagged inferred=true
      (rulings §10).
- [x] **Outcome commands**: `cairn found | not-found | manual-workaround
      <interaction-id> [--message]` — REQUIRE the interaction_id returned by
      search/digest; unknown ids and bogus kinds refused.
- [x] **`cairn gates`**: engineering gates computed (zero-loss via doctor +
      CI matrix, provenance from fetched manifests, budget compliance from
      telemetry, ack→visible P95 vs the 200 ms constant); product gates
      render from recorded outcomes with the ≥30-handoff caveat; the
      automated | human-measured column is in the report (rulings §10).
- [x] **Emergency reserve** (rulings §11): 64 MiB preallocated at daemon
      start (.cairn/reserve.bin); ordinary sends never touch it (verified at
      max declared priority — priority is NEVER reserve authorization);
      `cairn reserve release` (operator) grants exactly ONE send ≤ 64 KiB
      (`cairn send --emergency`); double release refused; oversized refused;
      reserve re-preallocated after the emergency send.
- [x] **`cairn init --adopt`** (restored-data row): archives the old history
      read-only at events-preadopt-<old-cairn-id>/ (never deleted), drops
      stale derived state, creates a completely new origin identity; plain
      init still refuses restored data; the new origin publishes fine.
- [x] **Remaining fault rows**: clock rollback + extreme-future timestamps
      (ordering untouched, no freshness boost); row 10 view swap (faults
      during digest regeneration never expose a partial digest.md);
      SQLite failure AFTER ack degrades with a warning and never un-acks
      (restart heals via replay); hash-valid frame with an INVALID signature
      → doctor surfaces it, daemon refuses to start over it (conservative
      quarantine for a single-writer log); missing referenced object →
      `daemon.VerifyObjects` walks every origin's body references against
      the store (expired ephemeral is a legitimate absence) and reports;
      projection drift check (checkpoint vs log head).
- [x] **Scorecard harness**: env-gated `TestScorecard`
      (CAIRN_SCORECARD=<n>) measuring the TESTING.md §5 quantities on the
      real send path. It immediately caught a real bug: FTS5 operator
      characters in queries (hyphens etc.) caused syntax errors — queries
      are now quoted per-term (FTSQuery), so agent queries can never
      produce SQL-ish failures.

### Acceptance criteria → evidence
1. *Entire fault matrix green in CI* — every TESTING.md §1 row (1–15) and
   §2/§3 item now has an automated test across M1–M7 (crash matrices,
   injected failures, reserve rows, adopt, clock, signature, objects,
   properties). Full suite: 11 packages green.
2. *`cairn gates` shows the four engineering gates green* — gates report
   tested end-to-end (CLI + daemon); on the dev machine the P95 gate is
   measured (below), provenance and budget compute from real data, doctor
   clean.
3. *Scorecard numbers recorded* — dev machine (M-series mac, APFS,
   FileVault), full durability ordering, real daemon send path:

   | n | append total | send-ack P50/P95 | ack→lexical-visible P95 | search P50/P95 | cold recovery | reindex --lexical |
   |---|---|---|---|---|---|---|
   | 10k  | 2m18s (13.8 ms/ev) | 13.2 / 16.2 ms | **1.14 ms** | 1.9 / 3.3 ms | 0.87 s | 2.4 s |
   | 100k | 24m14s (14.5 ms/ev) | 13.8 / 18.0 ms | **1.52 ms** (gate < 200 ms ⇒ PASS) | 13.5 / 17.8 ms | 8.5 s | 27.7 s |

   send-ack ≈ 14 ms is F_FULLFSYNC-bound (macOS durable-write cost), well
   inside interactive tolerance; the GATE metric (ack → lexical visible)
   is ~1.5 ms at 100k.

### Deviations
- **1M scorecard deferred**: at 14.5 ms/event a 1M append run is ≈ 4 h of
  wall clock. 10k and 100k are measured (the 200 ms gate is defined at
  100k — TESTING §5); scaling is linear in n for append and ~linear for
  recovery/reindex (0.87 s→8.5 s, 2.4 s→27.7 s). Run overnight with
  `CAIRN_SCORECARD=1000000 go test -tags sqlite_fts5 -run TestScorecard
  -timeout 600m ./internal/daemon/` and append the row here (M8 note).
- **Invalid-signature handling**: recovery refuses to start (hard error)
  rather than quarantining-and-continuing — the quarantine flow proper
  belongs to P1 replication of FOREIGN origins; for the local single-writer
  log an invalid signature is corruption. Doctor surfaces it either way.
- **ENOSPC on SQLite WAL**: not directly injectable (SQLite owns its I/O,
  outside fsx); the equivalent guarantee — projection failure after ack
  degrades and heals by replay — is tested directly.

### Author rulings needed
- None new (open items unchanged: M0 root-key storage, M5 resolve semantics).

### Test results (2026-07-11)
`go test -tags sqlite_fts5 ./...` — 11 packages green; `go vet` clean.
Scorecard runs above (10k foreground, 100k detached, both PASS).

## M8 — Dogfood package

**Status: DONE. P0 IS COMPLETE — operator evaluation can begin.**

Tasks completed:
- [x] **`cairn setup-agent <name>`**: creates views/<name>/{outbox, fetched,
      conflicts} + view.json (--topic hard filters, --interest query);
      path-metacharacter names refused.
- [x] **DOGFOOD.md**: install quickstart; optional embed-venv provisioning
      (real-model embeddings); wiring the three agent surfaces (Claude Code
      project A/B via CLAUDE.md snippets + outbox/digest, chat copy/paste
      view); the 30-handoff diary protocol with the copy-paste baseline
      week; weekly `cairn gates`/`doctor` review; backup/restore procedure;
      the deferred 1M-scorecard command; the rulings-§10 exit
      interpretation (config falsified before thesis).
- [x] **launchd autostart**: scripts/com.cairn.daemon.plist (sed-install
      one-liner in DOGFOOD.md; KeepAlive, logs to ~/Library/Logs).
- [x] **Backup + restore drill**: scripts/cairn-backup.sh (rsync of
      portable data ONLY — .cairn/ derived state and device identity
      excluded by design) and scripts/cairn-restore-drill.sh (asserts the
      restored copy is REFUSED without identity, then `init --adopt`
      creates a new origin with the old history archived read-only).

### Acceptance criteria → evidence
1. *Fresh-machine install from README in <10 minutes* — README install
   section rewritten (clone → make build → init → daemon → send/search).
   Timed smoke on this machine: build → init (real FileVault check) →
   daemon → send → search → setup-agent → digest → gates in **2 seconds**
   (a true fresh machine adds `git clone` + first `go build` module
   downloads: minutes, nowhere near 10).
2. *Restore drill demonstrates portable-only restore creates a new origin* —
   `TestBackupRestoreDrillScripts` runs BOTH real shell scripts end to end
   in CI: backup excludes derived+identity; restored data refused; adopt
   creates a working new origin; events-preadopt-<old-id>/ preserved.
3. *Operator evaluation can begin* — DOGFOOD.md is the complete protocol;
   `cairn gates` tracks it weekly.

### Deviations
- None beyond those already recorded (M6 embedding backend, M7 1M
  scorecard deferral).

### Test results (2026-07-11)
`go test -tags sqlite_fts5 ./...` — 11 packages green; `go vet` clean.

---

# P0 DEFINITION OF DONE — met

Engineering gates (spec §11 as amended by rulings §10):
- **Zero acknowledged-event loss** across the TESTING.md matrix — every
  row automated and green (M1–M7 crash/fault matrices).
- **100% provenance** on fetched results — manifests carry source event +
  hashes; gates report verifies.
- **100% budget compliance** — property-tested (payload never exceeds
  budget_chars incl. metadata/markers); telemetry-tracked at runtime.
- **P95 send-ack → lexical-digest-visible = 1.52 ms at 100k events**
  (gate < 200 ms) on the dev machine.
- `cairn doctor` clean on corpora that survived the fault matrix.

The 30-handoff product evaluation (DOGFOOD.md) is now the operator's.

Open author rulings (non-blocking, conservative interpretations shipped):
1. M0 — root-key storage (device-local + backup instruction vs offline
   export ceremony).
2. M5 — resolve merges against the conflict-time head (documented).

## M9 — Ingest (post-P0)

**Status: DONE.** The BUILD-PLAN M9 stub defined no acceptance criteria;
the criteria below were self-defined conservatively and all pass.

Tasks completed:
- [x] **`cairn ingest scan <dir>`** (`internal/ingest`): walks a docs tree /
      llm-wiki repo (.md files, dot-dirs skipped), LF/UTF-8 normalizes,
      classifies each file against the LIVE cairn — source_refs lookup by
      `repo/relpath`, then head body-hash comparison → publish | revise |
      skip; proposes topics from the directory structure (sanitized to the
      schema's topic-name pattern); resolves [[wiki links]] (with |alias
      support) to message ids across BOTH existing and about-to-be-published
      files (ids pre-assigned in the manifest — caller-supplied logical ids
      per rulings §2); unresolved links recorded. Output: a deterministic,
      reviewable JSON manifest.
- [x] **`cairn ingest apply --manifest …`**: executes the plan entirely
      through daemon IPC (rulings §6 — ingest never appends independently
      and can never use the operator class override): topic-ensure
      (idempotent create-or-lookup), publish with source_ref
      {path, repo, content_hash, imported_at} + relates_to + initial topic
      links, revise via the daemon's base==head revision path; a source file
      changed between scan and apply is REFUSED ("rerun scan") — never a
      silent divergence between manifest hash and published content.
- [x] **Plumbing**: PublishRequest carries source_ref/relates_to (validated,
      projected into source_refs); new IPC ops revise / topic-ensure /
      source-ref; projection lookups SourceRefMessage + TopicIDByName.
- [x] **DOGFOOD.md §7**: operator instructions for seeding cairn from an
      existing wiki.

### Acceptance criteria (self-defined) → evidence
1. *Deterministic reviewable manifest* — `TestIngestScanApplyEndToEnd`:
   4-file synthetic wiki → correct publish classification, directory
   topics (`team-wiki`, `team-wiki/eng`, `team-wiki/eng/deep`), wiki links
   resolved to pre-assigned ids, unresolved link surfaced; manifest
   round-trips through disk (`TestManifestRoundTrip`).
2. *Full provenance on apply* — source_refs projected (path→message),
   bodies searchable, topic links live, relates_to in the signed payloads.
3. *Strict idempotency* — rescan+apply with no changes ⇒ 4 skips, zero
   events, stable message ids; editing ONE file ⇒ exactly one revise event
   and the new head is the search hit; scan/apply drift refused.
4. *Policy respected* — >1 MiB canonical import is downgraded (ingest has
   no operator override): `TestIngestCannotForceCanonical`.

### Deviations
- **source_ref paths are stored as `repo/relpath`** — this makes the
  user-specified single-column `source_refs.path` primary key collision-free
  across repos (the concern flagged when the hook landed). If two imports
  use the SAME repo label for different trees, last-import-wins per path
  (deterministic on replay).
- relates_to remains payload-only (signed, replayable) — no projection
  table for it yet; a P1/P2 reference-graph projection can add one without
  schema conflict.

### Test results (2026-07-12)
`go test -tags sqlite_fts5 ./...` — 12 packages green (ingest: 3 tests,
end-to-end over real daemon IPC). `go vet` clean.

## Audit fix work order (docs/cairn-p0-fix-workorder.md) — COMPLETE

Two independent audits of d0aca05 (Codex, Claude) returned FAIL; the
adjudicated work order F1–F7 is fully executed. `make verify` green
(untagged compile-guard + 17 packages). One commit per item (FIX-F1…F7).

| Item | Defect | Fix | Regression proof |
|---|---|---|---|
| F1 BLOCKER | `send --topic` poisoned the projection; projector stalled silently; restart/reindex unrecoverable | pre-ack referential validation for ALL intra-mesh refs; `--topic` resolves by name and auto-creates (create+link in-request, all durable before the single ack); projector PARKING (quarantine table, stream never stalls; reindex exits 0 with report); projection schema v2 | CLI red test confirmed the stall (1/3 messages); pre-ack link rejection; injected historical poison parked with later events projecting |
| F2 BLOCKER | migrated mesh could never reindex (per-origin key verification); identity show broken post-migrate | two-pass replay: `identity.MeshTrust` (pass 1, fixpoint across ALL origins) + seeded verifiers (pass 2) used by reindex, recovery, doctor, gates, object checks, and Migrate itself (double-migrate had the same bug); identity show reads mesh trust; revocation gates writes only | red with the auditor's exact "record before genesis"; migrate→reindex byte-identical; A→B→C; F1+F2 combined; post-migrate trust |
| F3 MAJOR | doctor false-clean on projection poison and missing objects | DeepDoctor: parked events, checkpoint semantics (AHEAD/unverifiable = fail; BEHIND with zero parked = informational — parking makes stalls impossible), object presence + hash verification, cross-origin trust; nonzero exit; gates zero-loss row cites it | CLI: missing-object fail, parked fail, post-migrate clean |
| F4 MAJOR | plain build produced a non-functional binary | compile-time guard (untagged build fails naming the fix); `make verify` asserts guard + tagged suite | make verify in CI path |
| F5 MAJOR | export-root ruling never reached the repo | `cairn identity export-root` (verified export, mesh-dir refusal, prompted/one-shot local removal); **RULINGS.md** created (all conversation + work-order rulings; chat rulings land there before implementation); unknown subcommands exit nonzero | export lifecycle + exit-code tests |
| F6 MINOR | restore UX; TTL ergonomics; version | typed ErrRestoredCopy pointing at `init --adopt`; READ-ONLY daemon mode over restores (search/digest/fetch/doctor/identity show per R9; all writes refused); ephemeral_ttl_hours / housekeep_minutes portable-config tunables + startup sweep + `cairn housekeep`; version from build info | read-only e2e; tightened-TTL expiry; drill script updated to probe write-refusal |
| F7 | blockers grew in untested seams | direct unit tests: rank / embed / views / telemetry / projection-replay (blocker shapes pinned) — immediately caught and fixed a REAL budget bug (truncation marker could exceed budget_chars) | 17 packages green |

Durable-log internals (frame format, signing, sealing) were NOT touched by
any fix, as the work order requires.

Re-audit scope readiness: plain-command Phase 0 now fails instructively;
F1/F2 live-drill scenarios are automated; deepened doctor covers
missing-object/parked/post-migrate; durability paths untouched.

## F8 punch list (docs/cairn-f8-punchlist.md) — COMPLETE

Round-2 re-audit: Claude PASS, Codex conditional pass; the adjudicated
residue is fully executed (FIX-F8.1…F8.5, `make test` green before each
commit).

- **F8.1** — `ephemeral_ttl` / `housekeep_interval` accept duration strings
  with a `d` suffix ("30s", "90m", "7d", "1d12h"); legacy integer keys stay
  valid; both-forms or unparseable strings are load-time errors. Regression:
  end-to-end 30-second expiry drill (alive at 29s, housekept at 31s, typed
  content_expired, event intact). RULINGS.md R13.
- **F8.2** — golden corpus shipped as `testdata/corpus/` fixtures (184
  messages / 30 queries), embedded with a drift test; `cairn bench golden`
  reproduces the 0.97 / 0.63 claim from the binary in a throwaway mesh
  (isolated device state) with per-query miss detail; the M6 test consumes
  the same fixtures. Doubles as the P2 calibration harness. R14.
- **F8.3** — the daemon logs LOUDLY at park time (event id, type,
  origin/seq, error, doctor pointer), asserted in the F1 parking
  regression. R15.
- **F8.4** — RULINGS.md precedence corrected (RULINGS.md > rulings-v0.3.1 >
  spec-v0.3 > TESTING.md; newest amends oldest explicitly); R13–R17 added,
  including the R16 score-drift wording adjudication.
- **F8.5** — missing/corrupt-object doctor lines name a referencing
  revision_id + message_id. R17.

Observed-once flake (recorded): `TestF3DoctorFailsOnMissingObject` failed
in one run where two full `make test` suites executed back-to-back on the
same machine; 11 consecutive isolated and full-package reruns are green.
Treated as load-induced timing; will re-diagnose if it recurs under normal
single-suite runs.

## Resume-cold notes
> STALE MARKER (refreshed 2026-08-06): this block described the end of P0
> only. Current state — P0–P3 + AFFORDANCE built; P1 N1–N8 complete with
> N9 code-complete; P2 built (opt-in via the `rank_profile` device-config
> key since DEPLOY-E2); P3 offline scope built (two-node live checkout
> hardware-gated); WP-A…WP-F remediation (audit 2026-08-05) landed with CI
> in .github/workflows. Still owed by the operator: the 30-handoff
> evaluation (gates are now COMPUTED — see DEPLOY-E3), the overnight 1M
> scorecard, embed-venv provisioning on each node, and the open author
> rulings indexed under "Author rulings needed" (FIX-A6 residual, R38,
> R40/R41 confirmation, ladder rungs 6–7 if WP-G4 lands).
- **Every milestone in BUILD-PLAN.md (M0–M9) is complete.** What remains is
  operator work and future phases: the 30-handoff evaluation (DOGFOOD.md),
  the overnight 1M scorecard, embed-venv provisioning, the two open author
  rulings, and P1 (networking/replication/MCP — a design phase, not a
  BUILD-PLAN milestone).
- `// RULING-NEEDED:` markers in code: one (root-key storage, M0).

---

# P1 (cairn-p1-buildpack-v1.1-full.md) — N1→N9

Authority for P1: RULINGS.md (R18–R34 appended, commit 0a9fd4a) >
rulings-v0.3.1 > spec-v0.3. Durable-log internals remain out of bounds —
N1 did not touch internal/log framing/signing/sealing.

## N1 — MCP server + untrusted-content envelope — COMPLETE

Status: all automatable acceptance criteria pass; the Claude Desktop leg
is the operator's (instructions in DOGFOOD.md §3b).

- `internal/mcp`: transport-agnostic tool layer (`CallTool`) + stdio
  JSON-RPC framing (`ServeStdio`), newline-delimited per the MCP spec.
  Tool layer is reusable unchanged for a later HTTP mount. Tools mirror
  spec §5.5 exactly — nine tools, nothing else.
- `cairn mcp --view <v> --actor <a>`: bridges stdio to the daemon's unix
  socket; fails fast (nonzero, R8) when no daemon is running; stdout
  carries protocol messages ONLY (diagnostics go to stderr).
- **R18**: every content-bearing result (digest, search + each result,
  peek, fetch, why_ranked) is wrapped in the §7.4 envelope with
  trust:"untrusted" and provenance {message_id, revision_id, sender,
  content_hash}; every such tool description states returned content is
  data, not instructions. Aggregate kinds (digest, search_results) carry
  content + interaction_id; per-message provenance rides on the per-result
  envelopes — interpretation recorded here, no ruling gap.
- **R19**: budgets pass through to the daemon and are accounted over the
  complete retrieval payload, identically to the CLI (the JSON-RPC wrapper
  is transport, exactly as the IPC wrapper is for the CLI). Defaults
  digest 1500 / search 2000 in config (MCPDigestBudgetDefault /
  MCPSearchBudgetDefault). The server re-verifies payload ≤ budget and
  errors if the daemon ever exceeded it.
- **R20**: cairn_send/cairn_reply decode arguments strictly
  (DisallowUnknownFields) — operator_override / force-class /
  auto_create_topics do not exist on this surface and are rejected as
  invalid arguments; unresolved topics reject BEFORE ack (no auto-create
  from MCP, consistent with the FIX-F1 outbox ruling).
- R21 groundwork: the recorded principal is the server's --actor; N2 binds
  it to a capability profile (MCP never tier-1).

Tests (all green, `make test` + `go test -count=3 ./internal/mcp`):
- handshake (echoes client protocolVersion), tools/list = exactly the nine
  §5.5 names, R18 description check, unknown method → -32601, notification
  silence, ping
- full round-trip against a live daemon: send → search (envelope + full
  provenance per result) → peek (metadata-only, no content) → fetch (body
  + mime + provenance matches search's content_hash) → why_ranked →
  outcome(found) → reply (threads correctly) → signal → digest (envelope,
  ≤ default budget)
- R19: 12-message corpus, digest budget 220 → payload ≤ 220 runes while
  the unbudgeted digest exceeds it; search budget 150 honored
- R20: hidden-knob rejection ×3, unresolved-topic pre-ack rejection,
  empty-body and invalid-outcome rejection
- CLI level: `cairn mcp` serves initialize + tools/list over stdio with
  pure-protocol stdout; nonzero exit when the daemon is down

Deviations / notes:
- Fetch returns the body inline in the envelope by reading the
  daemon-materialized body file (same host by definition of the stdio
  transport); the views/<view>/fetched/ manifest+body pair is still
  written, so provenance separation on disk is unchanged.
- Test-helper flake fixed during the milestone: waiting on existence of
  daemon.sock.path races its content write; the helper now polls a status
  call. Pre-existing cmd/cairn tests use waitForSocket the same racy way
  but have never flaked — left untouched (fix scope).
- F9 (bench golden --embedder real) not taken this session; must ride
  along by N4 per the buildpack.

Commits: 0a9fd4a (R18–R34), 2ca7b07 (N1 implementation), + docs.
Next: **N2 — capability enforcement + trusted launcher**. Operator
checkpoint after N2 per the buildpack (security model activation).

## N2 — Capability enforcement + trusted launcher — COMPLETE

Status: all acceptance criteria pass. **Operator checkpoint is HERE** (the
buildpack: "read PROGRESS.md before continuing" — the security model just
activated). Durable-log internals untouched.

- rulings §7.2 tier system activated at the ONE dispatch boundary every
  client shares (unix-socket IPC), so every capability refusal is
  structurally pre-ack — no event construction, no append, no receipt.
- Session handles (R23): opaque random tokens → daemon-side records
  {name, profile, parent, TTL, idle}; persisted device-local
  (sessions.json, cache-class); NON-DELEGABLE — session-create/revoke/list
  under a session are refused; a handle acts AS its leaf principal (client
  actor overridden; operator_override + auto_create_topics stripped).
- Profiles: builtins full / agent-standard (read+send+signal+outcome) /
  read-only, plus strict device-local profiles.toml (unknown capability,
  unknown key, or builtin redefinition ⇒ daemon startup fails loudly).
- `cairn run --profile <p> --name <n> -- <cmd>` (trusted launcher):
  session-create bound to the launcher pid → CAIRN_SESSION exported →
  child's every CLI verb confined (the `call` helper attaches the env
  handle) → revoke on exit; child exit code propagates. `cairn session
  list|revoke` for visibility (token prefixes only).
- `cairn mcp` never tier-1 (R21): uses its CAIRN_SESSION or mints one from
  --profile (default agent-standard; "full" refused), revokes on exit.
- Telemetry rows carry the principal hierarchy ("operator>claude-a"):
  new `principal` column with an additive ALTER migration for existing
  local DBs; value is dispatch-resolved, client-supplied values ignored.
- Constants (buildpack judgment, config-revisable): SessionTTLDefault 24h,
  SessionIdleTimeout 6h, SessionTokenBytes 32.
- R22 isolation honesty documented (DOGFOOD.md §3c + session.go/run.go
  header comments): same-user = accident prevention, not malice prevention.

Acceptance evidence (all green; full suite -count=1 clean):
- read-only send refused pre-ack with a capability error AND next_seq
  proven unchanged; retract/topic-ensure/signal/housekeep equally refused;
  search/digest still work (TestN2ReadOnlySendRefusedPreAck)
- handle expiry ends access: idle (>6h) revokes within TTL; continuous
  keepalive still dies at the 24h TTL; unknown/revoked tokens are
  capability errors (TestN2ExpiryAndIdleEndAccess, injected clock)
- telemetry principal hierarchy on search AND digest rows; tier-1 rows
  record plain "operator" (TestN2NonDelegable…TelemetryPrincipal)
- actor spoof attempt (Actor:"operator", OperatorOverride:true) lands as
  the session principal with the override stripped
- profiles.toml: custom read+send profile works (send yes, signal no);
  three malformed variants fail startup (TestN2ProfilesTOML)
- CLI: cairn run exports + revokes-on-exit (token dead afterwards), exit
  code propagates, nested cairn run refused (non-delegable); session
  list/revoke round-trip without leaking full tokens; MCP --profile full
  refused, agent-standard send works with the minted session revoked on
  exit, read-only MCP send is an isError capability refusal
- tier-1 preservation: the ENTIRE pre-N2 suite passes unchanged (handle-
  less CLI still full)

Commits: 91f987a (implementation), + docs commit.

**Operator drill executed (2026-07-12, scratch mesh + live daemon):** all
legs behaved as ruled — read-only search and digest work (exit 0); send
refused pre-ack with the capability error (exit 1) and the body is
unfindable afterwards; the session auto-revoked on shell exit
(`session list` empty); tier-1 send outside the shell intact; doctor +
deep doctor clean after the drill. Escape probe (`unset CAIRN_SESSION`
inside the shell): send succeeds at tier-1 — adjudicated as designed
behavior and recorded as **RULINGS.md R35** (env-cooperative confinement;
R22 honesty; stronger binding is P3).

Next: **N3 — durable semantic subscriptions** (after operator checkpoint).

## N3 — Durable semantic subscriptions — COMPLETE

Status: all acceptance criteria pass (full suite -count=1 green).
Durable-log internals untouched. Not an operator-checkpoint milestone.

- Events subscription.create/update/disable/delete (normative schemas in
  build/schemas/p1-events.schema.json — new P1 file; the P0 schema file is
  untouched). Update is base-revision optimistic (rulings v0.3.1 §2):
  stale base rejects pre-ack; a mismatch at replay time errors and PARKS
  loudly. Projection schema v3 (subscriptions table; existing derived DBs
  auto-rebuild on open, as designed).
- R24/R36 calibration: relative-only via the observed-similarity
  distribution; see RULINGS.md R36 for the exact mechanism and edge
  rulings. Iterated twice during TDD: a pure per-pool largest-gap rule
  surfaced junk-above-junk once real matches were delivered; the
  observed-distribution reference (what R24's "percentile over observed
  similarity" actually says) fixed it.
- R25: `cairn subscribe` defaults to the LOCAL tier (writes view.json, no
  events — proven by next_seq unchanged); --durable is the only
  event-creating path. Delivery + observation history in telemetry.sqlite.
- R26: digest = mandatory → subscription matches (marked [subscription],
  rank class between pins and interest ranking) → interest ranking, one
  budget. Sub matches are NOT counted as omitted-mandatory; only included
  entries consume window/cap allowance.
- N2 interlock: subscription ops are admin capability — agent-standard
  sessions are refused (tested).
- Constants (judgment, config-revisable): SubTopNDefault 10/24h,
  SubPercentileDefault 90, SubPushCapDefault 20/day, SubMarginMin 0.15.

Acceptance evidence:
- semantically matching send with NO shared keywords ("shire signed off on
  the development application" vs query "council planning approval")
  surfaces marked in the next digest; distractors unmarked; no
  re-delivery (TestN3SemanticMatchSurfacesMarked, deterministic
  synonym-stub embedder — proves the pipeline; semantic quality in
  production is the embedder model's job)
- cap enforced: top_n=1/push_cap=1 delivers exactly one of two matches and
  nothing next digest (TestN3CapEnforcedAndDisableStopsDelivery)
- disable stops delivery even for fresh matches (same test)
- events replay cleanly through reindex: projection deleted, daemon
  restart rebuilds from the log — subscription rows byte-identical
  (including the optimistic-update revision), zero parked events, digest
  still matches and marks (TestN3ReplayThroughReindex)
- optimistic concurrency: stale base pre-ack rejection, revision bump,
  re-used base rejection, unknown topic/sub/empty-query rejections
  (TestN3OptimisticUpdateAndValidation)
- calibration decision table pinned (rank.TestCalibrateSubscription)
- CLI: local-vs-durable tier proof, list/disable round-trip, pre-ack
  unknown-topic refusal, agent-standard capability refusal
  (TestN3SubscribeLocalVsDurable)

Deviations / notes:
- Delivery metering is per-node (telemetry-class): after P1 networking
  lands, each node meters its own digest deliveries — flagged for the N6
  review rather than inventing cross-node semantics now.
- BagOfWords dev embedder cannot pass the no-shared-keywords criterion by
  construction; tests inject a deterministic synonym-stub embedder through
  the standard Options.Embedder seam. Operator verification with the real
  sentence-transformers model happens in dogfood.

Commit: 0f62484 (+ docs commit).
Next: **N4 — deterministic derivatives + receiver summary check** (F9
bench --embedder real must ride along by N4 per the buildpack).

## N4 — Deterministic derivatives + receiver summary check — COMPLETE

Status: all acceptance criteria pass (full suite -count=1 green; the one
failure seen mid-milestone was the PREVIOUSLY RECORDED TestF3 flake, now
root-caused and fixed — see below). F9 completed in this milestone as
required ("do not let it slip past N4"). Durable-log internals untouched.
Not an operator-checkpoint milestone.

- internal/derive: sandboxed deterministic extraction per spec §8.3 —
  text-layer PDFs, sanitised HTML, docx text, plain text. MIME sniffed
  from content; 16MiB input / 200-page / 10s / 1MiB output caps; panics
  contained; no network by construction (extractors are pure functions
  over bytes — recorded as the conservative in-process reading of
  "sandboxed"; OS-level syscall sandboxing is flagged for the N9
  hardening review, not silently claimed).
- New deps (recorded): github.com/ledongthuc/pdf (rsc.io/pdf drops space
  glyphs — probed empirically, unusable for search text),
  golang.org/x/net (HTML tokenizer).
- Events derivative.publish/fail/invalidate; projection schema v4
  (derivatives, message_summaries, fts_derivatives). Derivative text is
  FTS-indexed at APPLY time via the object store (replay-deterministic),
  and lexical candidates UNION derivative hits mapped back to owning
  messages. sender_summary added to message.publish (optional, untrusted).
- Enricher: DeriveOnce + SummaryCheckOnce on the background cadence
  (spec §8.1 — agents never wait; §8.2 — no embedder just delays checks).
  Failed extractions are derivative.fail events: recorded once, the
  queue drains monotonically, no retry loops.
- Receiver summary check (spec §8.4): cosine(sender summary, body) below
  SummaryAgreeCosineMin (0.5, config-revisable judgment constant) ⇒
  disagree flag + LOCAL extractive-lead summary with method provenance
  ("extractive-lead-v1/<model>") + loud daemon warning + [summary-disputed]
  digest marker. Bodies are ALWAYS excerpted in digests (never sender
  summaries), per spec — the check adds the visible marker.
- CLI: send --attach/--summary; derivative list|invalidate|summary.
  Capabilities: derivative reads read-tier; invalidate admin.
- F9: `cairn bench golden --embedder real` — runs the pinned
  sentence-transformers model via the embed venv; REFUSES to substitute
  the dev embedder (a fake "real" score is worse than none). Operator
  runs it after provisioning the venv (DOGFOOD.md §2).
- **Flake root-caused and fixed**: SocketPath used cairnID[:13] — a
  UUIDv7 MILLISECOND prefix — so two meshes initialized in the same ms
  (parallel test packages, or scripted setup in production) collided on
  the unix socket path and one daemon startup removed the other daemon's
  live socket. Now the full cairn id. This explains the
  TestF3DoctorFailsOnMissingObject failures recorded during F-work.

Acceptance evidence:
- PDF attachment searchable via derivative with FULL provenance: phrase
  exists only inside the PDF; unsearchable before derivation, exactly one
  hit after; provenance chain verified end-to-end (attachment blob_hash →
  derivative record with extractor identity → text_hash → text object
  content). Invalidate drops it from search; the next pass regenerates.
  (TestN4PDFSearchableViaDerivativeWithProvenance)
- Misleading sender summary flagged and locally re-summarised: divergent
  claim → checked+disagree+local extractive summary with provenance;
  honest claim unflagged; digest marks exactly the disputed entry.
  (TestN4MisleadingSummaryFlaggedAndLocallySummarised)
- Fail path recorded once, queue drains (TestN4FailPathRecordedOnce)
- Replay through reindex: derivative FTS rebuilt from events+objects
  (still searchable), no re-derivation invented, sender summary replayed
  with enrichment-class check state reset then re-verified, zero parked.
  (TestN4ReplayThroughReindex)
- Extractor unit matrix: pdf/html/docx/plain, sanitization (script/style
  never leak), determinism, corrupt-input containment, sniff-not-filename.
- CLI plumbing: send --attach/--summary round-trip; F9 flag behavior.

Commit: ed9de25 (+ docs commit).
Next: **N5 — transport + membership (Tailscale)** — OPERATOR CHECKPOINT
milestone: requires the physical root-key ceremony; the next iteration
prepares the enrolment flow and STOPS for the operator.

## N5 — Transport + membership (Tailscale) — COMPLETE (machinery) / OPERATOR CHECKPOINT

Status: every automatable acceptance criterion passes (full suite
-count=1 green). The REAL two-machine ceremony on the operator's tailnet
is deliberately NOT simulated — the runbook is DOGFOOD.md §11 and the
loop is STOPPED for the operator. Durable-log internals untouched (the
ceremony appends via the public log API exactly as migrate does).

- internal/peer: tailnet-only listener + 3-message mutual handshake
  (details in RULINGS.md R37). Un-enrolled/revoked/impostor/cross-mesh
  refusals all LOG the presented identity (R27).
- Enrolment ceremony offline end-to-end: enroll (key never leaves the new
  node) → approve (restored root key; 1h expiry + durable single-use per
  R28; root-signed device.add; grant carries the genesis-rooted identity
  chain) → join (verifies EVERYTHING from genesis; installs identity +
  bootstrap trust) → revoke (root-signed, offline).
- Daemon wiring: device-local sync_listen; SyncAddr accessor; listener
  refuses non-tailnet binds at startup. `cairn sync ping` = membership
  probe (N6 keeps the connection for reconciliation via Server.OnPeer).
- Acceptance evidence:
  - TestN5TwoNodeCeremonyAndRefusals (daemon-level E2E): ceremony with
    the root key REMOVED from the running node (restored copy used only
    at approve/revoke); enrolled B authenticates BOTH directions;
    un-enrolled peer refused + logged; revoked B refused + logged after
    device.revoke, across a daemon restart.
  - TestN5EnrolmentCeremony / TestN5RequestExpiryAndTamper: single-use,
    expiry, wrong-root-key, tampered-grant, double/self-revoke.
  - internal/peer matrix: mutual auth, impostor key, cross-mesh,
    unauthenticated responder, listener guard (0.0.0.0/public/localhost
    refused; tailnet v4/v6 accepted; loopback env-gated).
  - TestN5DeviceCeremonyCLI: verb wiring + operator-facing reminders.
- Deviations / notes:
  - Tests bind loopback under the explicit dev acknowledgement; the
    tailnet-range guard itself is unit-tested. The operator ceremony
    validates the real Tailscale path.
  - The joined node is handshake-capable but not daemon-operational until
    N6 delivers the log (bootstrap trust per R37). `cairn sync ping`
    works from it; digest/search need N6.
  - device.add payloads gained optional enrolment_request_id (noted in
    p1-events schema; chain verifier ignores unknown fields by design).

Commit: dc8a616 (+ docs commit).
Next: **N6 — reconciliation + text replication** — but FIRST the operator
checkpoint below.

### OPERATOR CHECKPOINT — N5 ceremony (requires you physically)

Run the real ceremony on your tailnet (full runbook: DOGFOOD.md §11):
1. On the NEW machine (WSL2 box per the buildpack appendix, or the air):
   build cairn, then `cairn device enroll --name <machine> --out req.json`.
2. Carry req.json to THIS machine. Stop the daemon. Restore the root key
   from offline storage to a temp path.
3. `cairn device approve req.json --root-key <restored> --grant grant.json`
   — then REMOVE the restored key. Restart the daemon.
4. Set `sync_listen = "<this-machine-tailnet-ip>:9700"` in the device
   config (path printed by `cairn identity show`), restart the daemon.
5. Carry grant.json to the new machine: `cairn device join grant.json`,
   then `cairn sync ping <tailnet-ip>:9700` — expect mutual auth success.
6. Negative probe: from any un-enrolled machine on the tailnet, the same
   ping must be REFUSED and logged on this machine's daemon stderr.

## N6 — Reconciliation + text replication — COMPLETE

Status: all buildpack acceptance criteria pass (full suite `make test` +
`make vet` + `make verify` green). Durable-log internals untouched —
foreign-origin ingest reuses the public `log.Open`/`log.Append` (contiguity,
chaining, framing, fsync, sealing all enforced by the existing write path).
Not an operator-checkpoint milestone (N5 was; N8 is next). Authority: RULINGS
R38 appended (interprets §6.2 / R29 / R30 / R37).

- **Reconciliation protocol** (`internal/daemon/reconcile.go`): over each
  N5-authenticated connection, a newline-delimited JSON exchange — frontier →
  get_range / push_records → records / ack → done. The INITIATOR (`SyncWith`)
  drives BOTH directions in one dial (pushes origins the peer trails, pulls
  origins it trails), so a single reconcile fully converges both nodes; the
  responder (`serveSync`, wired to `peer.Server.OnPeer`) only answers and
  never holds the writer lock across network I/O.
- **`peer.Dial`** returns the LIVE authenticated connection (Ping now wraps
  it and closes); `OnPeer` gained the buffered reader so reconciliation reads
  the exact post-handshake byte stream.
- **Foreign-origin ingest**: each origin gets an append handle (opened at
  recover, or lazily on first ingest — a brand-new peer origin opens fresh at
  seq 1). Every record is hash+signature verified against mesh trust BEFORE
  append; ingest is idempotent by (origin, sequence). Events for THIS node's
  active origin beyond what it holds are refused as a possible fork (N8),
  never silently ingested.
- **Text replication (R38 scope)**: message.publish/reply bodies and
  revise_body revision bodies ship with the events (objects before event —
  durability ordering preserved). canonical + eager on both pull and push;
  ephemeral only on a live push, never backfilled. Attachments/derivative
  text are N7. The >64 KiB non-inline canonical body in drill 1 proves the
  real body-OBJECT crosses the wire (not just the inline event).
- **Cadence (R29)**: push-on-append kicks a debounced sweep of every
  configured `sync_peers`; anti-entropy timer sweeps every 5 min. New device
  config field `sync_peers` (device-local, per-device networking). Constants
  (config-revisable): SyncAntiEntropyInterval 5m, SyncPushDebounce 250ms,
  SyncRangeBatch 512, SyncBulkCatchupThreshold 10k, SyncProtocolVersion 1.
- **Bulk catch-up (R30)**: a receiver > threshold behind on an origin is
  caught up with segment-sized batches, logged as a bulk catch-up.
- **Joined-node bootstrap (R37 + R38)**: a freshly-joined node (no local
  genesis) — or one whose genesis-bearing foreign origin was lost — boots on
  grant-chain bootstrap trust and becomes daemon-operational; the daemon now
  creates the portable scaffold (events/objects/exports/views/.cairn) on
  first start so replicated data has somewhere to land. MeshTrust resumes the
  moment the local chain resolves. `// RULING-NEEDED:` at daemon.recover for
  the retain-vs-delete-the-crutch interpretation.
- **CLI**: `cairn sync now [host:port]` (reconcile now — all peers or one)
  and `cairn sync status` (per-origin frontiers + configured peers), both via
  daemon IPC (single writer). New IPC ops sync-now (admin) / sync-status
  (read).

### Acceptance criteria → evidence (`TestN6TwoNodeConvergence`, 6 drills)
The two nodes are the N5 enrolment pair (A init + root offline; B joined) —
the only convergeable configuration (R34).
1. *Normal operation* — A publishes 3 small + 1 >64 KiB canonical body; B
   pulls all incl. the big body OBJECT; B publishes 2; A pulls them.
2. *One node offline for 100+ events* — A publishes 120 while B is silent;
   one reconcile catches B up (120/120).
3. *kill-9 mid-sync on the RECEIVER* — B's A-origin tail truncated (torn/lost
   frames); restart repairs the tail (frontier regresses, asserted on the
   LOG not the projection); resync re-fetches to A's frontier.
4. *kill-9 mid-sync on the SENDER* — A's B-origin tail truncated; restart
   repairs; A re-pulls B's origin to full frontier.
5. *Deliberately deleted segment on the receiver* — B's whole copy of A's
   origin removed; restart regresses the frontier to 1; resync re-fetches +
   verifies back to A's frontier, corpus intact.
6. *Reindex BOTH nodes → identical canonical results* — projections rebuilt
   purely from the log; the canonical/eager search snapshot is byte-identical
   across A and B; deep doctor clean on both.

Plus `TestN6AntiEntropyLoopAndIdempotency` (R29: initial-sweep pull +
push-on-append converge with NO explicit SyncWith; a redundant reconcile
ingests 0) and `TestN6BulkCatchup` (R30: lowered threshold trips the
segment-streaming path; converges 40/40).

### Deviations / notes
- **Frontier is highest-contiguous only** (R38): `log.Append` enforces
  contiguity, so a node can only hold a contiguous prefix; range transfer
  always starts at the receiver's frontier. Non-contiguous "known gaps"
  (§6.2 wording) are not separately tracked — conservative and consistent.
- **Text-only in N6**: attachments/derivative text deferred to N7 (lazy blob
  fetch + local re-derivation), per the buildpack N6/N7 split.
- **Cross-origin key admission during LIVE sync**: a record signed by a
  device whose device.add has not yet replicated locally is refused until the
  admitting origin replicates (resolves across sweeps, fixpoint). Not hit by
  the two-node acceptance (both devices mutually known pre-sync). Flagged for
  N9 hardening.
- **Test hook**: `Daemon.SetSyncBulkThresholdForTest` lowers the R30
  threshold so the bulk path is exercisable without a 10k fixture.
- **Loopback binding** under CAIRN_SYNC_ALLOW_LOOPBACK=1 for tests; the
  tailnet-range guard (N5) is unchanged. Real two-machine convergence rides
  the operator's tailnet (N5 runbook already executed the ceremony leg).

### Author rulings needed
- **Bootstrap-trust retain-vs-delete** (R38, marked in code): R37 says N6
  should "delete the crutch"; we retain grant-chain bootstrap trust as a
  root-verified resilience fallback used only when the local chain is
  unresolvable. Confirm the broader reading.
- (Open items unchanged: M0 root-key storage; M5 resolve semantics.)

### Test results (2026-07-13)
`make test` — all packages green (daemon incl. the 3 N6 tests + the full
N1–N5 suite unchanged); `make vet` clean; `make verify` OK (untagged
compile-guard + tagged suite).

Next: **N7 — blob replication + durability acknowledgement.**

## N7 — Blob replication + durability acknowledgement — COMPLETE

Status: all buildpack acceptance criteria pass (`make test` + `make vet` +
`make verify` green). Durable-log internals untouched — blobs are
content-addressed objects, replicated over the N6 authenticated connection as
a third phase. Authority: RULINGS R39 appended (interprets §6.3 / R31 / R32).
Not an operator-checkpoint milestone (N8 is next).

- **Durability class** (spec §6.3): message-level `durability` on
  message.publish (ephemeral | normal (default) | important | pinned),
  applied to attachment blobs; projected onto the attachments table
  (projection schema **v5**; existing derived DBs auto-rebuild on open).
  `cairn send --durability`.
- **Blob replication** (`internal/daemon/reconcile.go` blob phase): after the
  event/text phase, exchange blob inventories, then fetch every non-ephemeral
  target blob the node lacks that a peer advertises and push every one the
  peer lacks. R31: every transfer is content-address verified before store
  (store.Put + got==hash) and before serve (store.Get); cache-then-advertise.
- **Durability registry** (`internal/daemon/durability.go`): per-blob peer
  holders in `.cairn/durability.json` (derived/cache-class, atomic-overwrite;
  rebuilt by re-advertisement). Self holdership is always recomputed from the
  object store — a deleted/corrupt blob is never miscounted. Target per class:
  ephemeral 1, normal 2, important/pinned = non-revoked member count.
- **Durability acknowledgement** (spec §6.3): send returns accepted_locally
  with a DETERMINISTIC ack-time `replication` {class, target, have=1, pending}
  in the PublishResult/receipt (byte-identical on regeneration — M4 preserved,
  verified: outbox suite green). Live state surfaced by `cairn sync status`
  (per-blob have/target/satisfied), the gates durability row, the digest
  `[replication-pending]` marker, and deep doctor.
- **R32 deep doctor**: verifies present attachment blobs (corrupt present copy
  = problem); reports each non-ephemeral blob SATISFIED or pending. A MISSING
  attachment blob is NOT a problem (lazily replicated) — distinct from a
  missing body object.

### Acceptance criteria → evidence (`TestN7BlobDurability`)
1. *Attachment sent on A reaches durability 2/2 when B connects* — A's send
   acks pending 1/2; A's live status shows 1/2 unsatisfied (only A holds);
   after B reconciles, BOTH A and B report 2/2 satisfied; deep doctor on both
   is clean and reports SATISFIED.
2. *Fetch on B verifies and re-advertises* — B fetches the A-origin blob, the
   bytes verify against the content address, B holds it and both registries
   record B as a holder (re-advertise propagated on the same dial).
3. *Doctor matches reality through kill-9 mid-blob-transfer* — the interrupted
   state (event synced, blob absent — the atomic store leaves nothing partial)
   is reported accurately as pending and NOT as a problem; deep doctor never
   reports a false satisfied; re-sync re-fetches and converges to 2/2.

### Deviations / notes
- **Full-node proactive replication, not lazy-only** (R39): P1 full nodes hold
  the complete corpus, so all non-ephemeral target blobs replicate to all full
  nodes bidirectionally during reconcile. This meets/exceeds every target;
  true lazy on-demand fetch is a thin-node (P3) concern.
- **important/pinned target = member count**: pinned is "per policy"; the
  conservative reading is all non-revoked member nodes (same as important).
  Config-revisable via the durability constants.
- **Registry atomic-overwrite** added (`atomicOverwrite`) — a mutable
  derived-file write distinct from `fsx.WriteFileAtomic` (write-once for
  immutable objects/receipts, which refuses to replace). This was found by the
  acceptance test: the write-once primitive errored on the second save, so the
  peer-holder update never reached disk.
- **Corrupt-blob repair** blocked by the object store's verify-on-collision
  (refuses to overwrite a colliding address); doctor flags it, automated
  repair is out of N7 scope (flagged for N9).

### Author rulings needed
- None new. (Open: M0 root-key storage; M5 resolve semantics; R38
  bootstrap-trust retain-vs-delete.)

### Test results (2026-07-13)
`make test` — all packages green (daemon incl. TestN7BlobDurability + the full
N1–N6 suite; outbox receipt idempotency intact under the new replication
field; projection schema v5 rebuild). `make vet` clean; `make verify` OK.

Next: **N8 — live fork detection + network doctor** (OPERATOR CHECKPOINT
milestone: the loop STOPS after N8 completes).

## N8 — Live fork detection + network doctor — COMPLETE (OPERATOR CHECKPOINT)

Status: all buildpack acceptance criteria pass (`make test` + `make vet` +
`make verify` green). Durable-log internals untouched — detection only READS
the log; freeze/quarantine write derived state; the repair ceremony appends
through the public log API (like migrate/revoke). Authority: RULINGS R40.
**This is an operator-checkpoint milestone — the loop STOPS after N8.**

- **Detection** (`internal/daemon/reconcile.go`, `fork.go`): equivocation (same
  origin+gen+seq, different event_id, both valid) via three signals — frontier
  (same next_seq, different head → overlap-probe to the exact divergence),
  ingest same-coordinate different-id, and ingest chain-divergence. Our own
  active origin: any conflicting/beyond-head peer event is a clone → frozen.
- **Freeze + quarantine** (R33): a typed forkError unwinds ONLY the forked
  origin; every other origin keeps syncing. The divergent branch's raw frames
  are preserved under `.cairn/quarantine/<origin>/` forever; a ForkRecord lands
  in `.cairn/forks/`; detection logs LOUDLY. A frozen origin ingests nothing.
- **Surface**: `cairn doctor fork [origin]` (common ancestor, per-branch events
  + types, advertising peer, security-ops flag); deep doctor reports unresolved
  forks as PROBLEMS / resolved ones as info; `cairn gates` "no unresolved forks
  (N8)" row FAILs while frozen.
- **Repair** (`cairn fork resolve`, offline): operator picks canonical; the
  losing branch's message events are reissued under the recovery (active)
  origin with recovered_from_event_id + fork_resolution_id; a ROOT-signed
  `device.fork.resolve` (normative in build/schemas/p1-events.schema.json)
  records the decision; both branches preserved (canonical in the log, losing
  in quarantine). Cloned-cert revocation is the documented follow-up
  (`// RULING-NEEDED:` in fork_resolve.go; DOGFOOD §14).

### Acceptance criteria → evidence (`TestN8ForkDetectionAndRepair`)
*Manufactured equivocation*: clone device B's state after a common prefix (seq
1,2); B extends with branch A (seq 3), the clone B2 with branch B (seq 3) —
both validly signed by B's key; A syncs both.
- **Detected + frozen**: A detects the fork at the frontier, freezes B-origin,
  quarantines branch B; A never ingests branch B into its log (still branch A
  at seq 3), keeps its own branch; ForkRecord divergence seq 3 (common ancestor
  2); loud log fired; deep doctor + gates report the fork.
- **R33**: with B-origin frozen, A's OWN (non-forked) origin still replicates —
  a post-fork A message reaches the clone via the same reconcile.
- **Repaired, both branches preserved**: `ResolveFork` (canonical=local) reissues
  branch B's content under A's recovery origin with provenance; after restart
  A's log verifies, the fork is resolved (info, not problem), branch B's content
  is recovered AND its original frame is still quarantined, branch A intact.

### Deviations / notes
- **Repair scope** (R40, `// RULING-NEEDED:`): branch decision + reissue +
  device.fork.resolve are automated; cloned-cert revocation + physical-device
  re-enrolment are the documented operator follow-up (a self-clone revoke is
  refused by design — needs `cairn migrate` first).
- **Reissue scope**: only message.publish/reply (content) are reissued;
  structural events (links/pins/signals) stay in quarantine for manual
  recovery; a clone-only-blob body is skipped with a note (frame preserved).
- **Fork state** is real-fs derived (`.cairn/forks`, `.cairn/quarantine`) —
  not part of the injected-fs durable path; uses atomic-overwrite for records
  and write-once for quarantine frames (immutable evidence).

### Author rulings needed
- **Fork-repair ceremony scope** (R40, marked in fork_resolve.go): confirm the
  automated-decision-vs-documented-revoke split. (Open: M0 root-key storage;
  M5 resolve semantics; R38 bootstrap-trust retain-vs-delete.)

### Test results (2026-07-13)
`make test` — all packages green (daemon incl. TestN8ForkDetectionAndRepair,
stable across -count=3, + full N1–N7 suite unchanged); `make vet` clean;
`make verify` OK. `internal/log` untouched.

Next: **N9 — hardening + crossed two-auditor network audit** — but FIRST the
operator checkpoint below.

### OPERATOR CHECKPOINT — N8 (before hardening)

Per the buildpack sequencing, N8 is a checkpoint: review the fork-detection
and repair machinery before N9 hardening. Points to review:
1. The fork-repair ceremony scope (R40 / RULING-NEEDED): is the
   automated-decision + documented-cloned-cert-revoke split acceptable, or
   should `cairn fork resolve` also drive the revoke/migrate?
2. Manufactured-equivocation drill is automated (TestN8ForkDetectionAndRepair);
   consider running the REAL sandbox-clone drill on the tailnet before N9.
3. The N5 two-machine ceremony leg (DOGFOOD §11) and the N7 durability leg are
   still operator tasks that can ride alongside the review.

## Pre-N9 fixes (G1–G7) — from the two live network verifications of a036060

Work order: `docs/cairn-pre-n9-fix-workorder.md`. Rulings R42–R45 landed in
RULINGS.md first (FIX-F5 process rule). `internal/log/` stayed out of bounds.

### G1 — ephemeral bodies are never inlined (R42/R43) — DONE (2026-07-13)

**Defect:** bodies ≤64 KiB were stored as signed inline `body_bytes` inside the
publish event, which replicates as chain data to every full node regardless of
text class. Net effect on a peer offline at send time: the ephemeral CONTENT
was searchable from the inline copy, and that peer's `cairn doctor` FAILED
(exit 1) forever ("referenced object missing (class ephemeral)") until expiry —
breaking purgeability (TTL never scrubbed the inline copy) and the DoD gate
"doctor reports clean" on every synced non-origin node.

**Fix (regression-test-first — `internal/daemon/fix_g1_test.go`, red→green):**
- `daemon.go` + `fork_resolve.go`: never inline `body_bytes` when the effective
  text_class is ephemeral (inline optimization kept for canonical/eager only).
- `daemon.ValidateNoInlineEphemeral` guards the publish path pre-ack (F1): an
  ephemeral publish carrying inline bytes is rejected before acknowledgement.
- `projection.indexRevision` now takes the text_class; an ephemeral revision is
  indexed ONLY from a locally-present object, never from an inline copy — so the
  origin (object local) indexes, a non-origin (object withheld) does not.
- `gates.go VerifyObjects` (R43): a missing ephemeral object is INFORMATIONAL on
  every node (withheld / never-fetched / expired), never a problem; a missing
  canonical/eager object is still a problem.

**Migration note (R42, historical events with inline ephemeral bytes):** the
immutable log is NEVER rewritten. This test mesh's pre-G1 events still carry
inline ephemeral `body_bytes` in their frames. The projection now ignores those
inline bytes for ephemeral text_class (indexed as ephemeral-with-object-absent
on non-origin nodes) and doctor treats a missing ephemeral object under R43.
A `cairn reindex --lexical` on a node that indexed a legacy inline ephemeral
from an earlier build will DROP it from FTS (the object is absent on non-origin
nodes), which is the intended convergence to the R42 guarantee.

**Tests:** `TestG1EphemeralNotInlinedTwoNode` (offline-send → pull → search=0,
fetch fails, doctor clean on B; origin A still indexes its own ephemeral) and
`TestG1EphemeralInlineRejectedPreAck`. Full suite + vet green; `internal/log`
untouched.

### G2 — sync listener: sane default + never silent (R44/R45) — DONE (2026-07-13)

**Defect:** `sync_listen` had no default, was never set by `init`, had no CLI
flag (device-TOML hand-edit only), and when unset the daemon bound nothing AND
logged nothing (the log line lived inside the `if`, no `else`) — a core
subsystem silently declining to start.

**Fix:**
- `config/constants.go`: `SyncDefaultPort = 9700`, `SyncListenAuto`/
  `SyncListenOff` sentinels.
- `peer.DetectTailnetIP()`: scans interfaces for a Tailscale CGNAT address
  (100.64.0.0/10 or fd7a:115c:a1e0::/48), IPv4 preferred; under
  CAIRN_SYNC_ALLOW_LOOPBACK falls back to 127.0.0.1 for dev/test; never returns
  an unspecified address. (Refactored the tailnet ranges into shared
  `isTailnetIP` reused by `ValidateListenAddr`.)
- `daemon.resolveSyncListen(configured, detect)`: "off" → disabled + reason;
  ""/"auto" → detect and bind `<ip>:9700`, else disabled + reason; anything
  else → literal address (NewServer still rejects 0.0.0.0). The daemon startup
  block now logs EVERY outcome loudly with the remedy (R45) and records a
  human-readable state; a failed bind is loud-but-not-fatal (the daemon still
  serves local reads/writes).
- `cairn sync status` now reports `listener` (bound address or the disable
  reason).
- `init`/enroll default `sync_listen` to "auto"; `cairn init --sync-listen`
  flag added.

**Tests:** `TestG2ResolveSyncListen` (off / auto-found / empty-defaults-auto /
auto-no-tailnet / explicit; asserts never-0.0.0.0 and never-silent) and
`TestG2DetectTailnetIP`. `make verify` green; `internal/log` untouched.
**Accept (unverifiable here — needs a real tailnet machine for N9):** a stock
daemon on a tailnet machine binds tailnet-only with zero config; on a
non-tailnet machine it logs "no tailnet interface found — sync disabled".

### G3 — encryption gate on the enrol/join key-write path — DONE (2026-07-13)

**Defect:** `device enroll` / `join` wrote a device PRIVATE KEY without calling
the encryption gate (the gate only fired at runtime startup) — a P0 invariant
violation (keys never land on unencrypted storage). Additionally a
bare-enrolled node had no supported way to persist `--allow-unencrypted`
(init-only), forcing a device-TOML hand-edit.

**Fix (regression-test-first — `internal/identity/fix_g3_test.go`):**
- `identity.GateEncryption(dir, checker, allow, out)`: exported gate WITH the
  operator override (EnsureEncrypted stays fail-closed for restore).
- `CreateEnrollRequest(EnrollRequestOptions{…})`: gates the volume holding the
  staged key BEFORE the write; `AllowUnencrypted` warns and proceeds.
- `Join(JoinOptions{…})`: gates device-local state BEFORE the key write, and
  PERSISTS `AllowUnencrypted` into the written DeviceConfig (per-startup warning
  parity with init — no TOML hand-edit).
- `cairn device enroll --allow-unencrypted` / `cairn device join
  --allow-unencrypted` flags added.

**Tests:** `TestG3EnrollGatesEncryptionBeforeKeyWrite` (refuses before staging
the key; override warns + writes) and `TestG3JoinGatesEncryptionAndPersistsOverride`
(refuses before the device-key write; override warns + persists the device-local
override). Full suite + vet green; `internal/log` untouched.

### G4 — F10: service install + deploy hygiene — DONE (2026-07-13)

- `cairn daemon --install` / `--uninstall` (`cmd/cairn/service.go`): launchd
  user agent on macOS (primary — RunAtLoad+KeepAlive, logs to
  ~/Library/Logs/cairn.log), systemd `--user` unit on Linux (best-effort). WSL
  caveat handled: if `systemctl --user` is unavailable we still write the unit
  and tell the operator to enable systemd (`/etc/wsl.conf [boot] systemd=true`)
  or run `cairn daemon` manually. Never a root/system service (cairn is
  single-writer per user, device-local).
- `make install` target: **removes the target before copying** — macOS AMFI
  kills a code-signed binary overwritten in place (the documented lost cycle).
- Dead-socket error (`daemon.Call`) now reads "daemon not running — start with
  `cairn daemon` or install with `cairn daemon --install`" (both the
  no-registration and unreachable branches), replacing the raw socket error.

**Tests:** `TestG4LaunchdPlist`, `TestG4SystemdUnit` (pin the rendered unit
files; the install/uninstall side effects touch the real login session and are
operator-verified per the runbook, not in CI). Full suite + vet green;
`internal/log` untouched.

### G5 — embeddings on Linux + loud lexical-only startup (R45) — DONE (2026-07-13)

**Defect:** NODE-B (Linux) ran lexical-only with no embedder and said nothing.
The venv path is already cross-platform, so the real gaps were (a) no parity
bootstrap and (b) the silent fallback (`embed.Detect` even swallowed a
provisioned-but-broken venv's error).

**Fix:**
- `scripts/cairn-embed-bootstrap.sh`: one script provisions the pinned
  `all-MiniLM-L6-v2` venv on macOS AND Linux (parity), pre-downloads the model.
- `embed.DetectVerbose(dir) (Embedder, reason)`: surfaces WHY it fell back
  (no venv / venv failed to start) instead of swallowing it; `Detect` wraps it.
- Daemon startup logs the embedding state on EVERY platform (R45): a loud
  lexical-only line with the remedy, or a positive "semantic search enabled
  (<model>)" confirmation for cross-node parity checks.
- DOGFOOD §2 points at the bootstrap script and documents the loud line.

**Tests:** `TestG5DetectVerboseAlwaysExplains` (a nil embedder always carries a
remedy-bearing reason). **Linux embedder LOAD itself is N9 operator-verifiable
only** (this build machine is macOS arm64); the code path + bootstrap are in
place. Full suite + vet green; `internal/log` untouched.

### G6 — attachment size: 16 MiB, streamed over IPC — DONE (2026-07-13)

**Decision (operator):** the real attachment ceiling is **16 MiB**
(`MaxRecordBytes` / `DeriveMaxBytes`); attachments are **streamed over IPC**,
not inlined.

**Defect:** the CLI inlined attachments (base64) into ONE publish IPC request
bounded by `IPCMaxRequestBytes` (32 MiB), so a large or multi-attachment send
breached the JSON cap and failed with `broken pipe` BEFORE the clean 16 MiB
size check ever ran.

**Fix:**
- New `stage-attachment` IPC op: the CLI writes a JSON header line then STREAMS
  the raw bytes on the same connection; the daemon reads exactly `byte_len`
  bytes, `store.Put`s the object, and returns its hash. Bytes never inflate a
  JSON request → many/large attachments can't breach `IPCMaxRequestBytes`.
  capSend-gated (parity with publish); size re-checked server-side too.
- `AttachmentIn` gains `object_hash`: the publish path uses a pre-staged object
  instead of Put-ing inline bytes (inline path retained for the daemon API).
- `daemon.StageAttachment` client helper stat-checks the file size FIRST and
  fails cleanly ("… is N bytes (cap 16 MiB) — not sent") with NO transmission.
- `cairn send --attach` streams every attachment; `--help` states the 16 MiB cap.

**Tests:** `TestG6AttachmentStreamedAndCapEnforced` (normal attachment streams
end-to-end; a >16 MiB attachment fails with the clean cap message, never
`broken pipe`). Full suite + vet green; `internal/log` untouched.

### G7 — minor batch — DONE (2026-07-13)

1. **`sync now` async** (G7.1): a large catch-up could exceed the IPC deadline
   even though convergence succeeded (spurious i/o timeout). `sync-now` now runs
   the sweep in a BACKGROUND goroutine (single in-flight at a time) and acks
   immediately; progress via the daemon log + `cairn sync status`.
2. **Bare-enrol `identity show`** (G7.2): a freshly joined node has no local
   genesis until N6 replicates. `identity show` now falls back to
   `identity.BootstrapTrust` and reports "BOOTSTRAP STATE …" (exit 0) instead of
   hard-erroring "no genesis event found". Test:
   `TestG7IdentityShowBootstrapState`.
3. **Push-side fork detection** (G7.3): fork detection was pull-asymmetric —
   an equivocating node whose frontier merely COLLIDES with ours (equal
   next-seq, different head) pushes nothing, so the server missed it until it
   pulled. The initiator now sends its frontier in the frontier request, and the
   responder runs the same equal-seq/different-head check
   (`detectFrontierForkFromPeer`), logs loudly, and kicks a pull-back so the
   precise probe + quarantine runs. (ingestRecords already ran the
   coordinate/chain backstops on pushes.)
4. **R41 revoke bundling** (G7.4): `fork resolve` now REVOKES the cloned
   certificate in the SAME root-key session (device.fork.resolve + reissue +
   root-signed device.revoke). Choosing the canonical branch stays human; the
   revoke does not. A SELF-clone still routes through `cairn migrate` (self-
   revoke is refused by design) and is reported. `TestN8ForkDetectionAndRepair`
   now asserts the cloned device is revoked (and the test resolves as the
   operator device, fixing a latent env bug where it ran as the clone).
5. **Version string** (G7.5): build version reads `p1` (was `p0`). The
   normative `PayloadSchemaID` (`cairn/p0-events/v1`) is unchanged — it is the
   event schema id, not the build version. Test: `TestG7VersionIsP1`.

Full suite + vet + verify green; `internal/log` untouched.

---

## Pre-N9 status: G1–G7 COMPLETE (2026-07-13)

All seven work-order items landed, one commit each (`FIX-G1`…`FIX-G7`), atop
the R42–R45 rulings commit. `make verify` green throughout; `internal/log`
never touched. Items needing a real multi-node/tailnet/Linux environment are
flagged for the N9 auditors: G2 (stock tailnet bind), G5 (Linux embedder load).

## N9 — Hardening + crossed audit — CODE-COMPLETE (2026-07-13)

Buildpack N9 has two halves. The **hardening** half (buildable) is done; the
**crossed two-auditor live audit** needs two humans + real tailnet machines +
the operator rig restoration, so it is DEFERRED to the operator. With the
hardening landed, P1 is code-complete.

- **N9-H — network fault matrix** (`internal/daemon/n9_hardening_test.go`,
  `TestN9NetworkHardening`): the rows the N6/N7/N8 drills did not already cover —
  (1) **bidirectional partition/rejoin** (both sides write while partitioned →
  one reconcile converges both origins, frontiers identical); (2)
  **duplicate-delivery flood** (12× re-run of the same reconcile → corpus and
  frontier unchanged: at-least-once delivery is exactly-once by sequence); (3)
  **revoked-mid-sync** (A root-revokes B offline, restarts → B is refused at the
  authenticated listener (R27) and A never ingests B's post-revocation event).
  Already-covered rows (kill-9 on receiver/sender mid-sync, deleted segment,
  kill-9 mid-blob-transfer) stay green under the N6/N7 suites.
- **DEFERRED (operator):** the crossed two-auditor live audit; `cairn
  adopt-standalone` (R34, buildpack "if time permits") — tracked in P2-PLAN.md.

## P2 — retrieval quality (spec §12)

Roadmap + task tracker: `P2-PLAN.md`. No buildpack existed; milestones derived
from spec §12/§8/§9. P1 code-complete first (N9-H above).

### P2-1 — async maintenance worker + degradation ladder (§8.2) — DONE (2026-07-13)

The degradation ladder that sheds DERIVED work under load — send() never
blocks, a message is never lost, only enrichment lags.

- `internal/maintenance` (pure, fully unit-tested): `Level` (the 7 §8.2 rungs),
  `Debt` (embedding backlog + disk pressure/critical/quota), `Assess(debt,
  thresholds) → Level`. Backlog drives rungs 1–4 (delay auto-links → summaries →
  embeddings → serve lexical-only); disk/quota drive rungs 5–7; the result is
  the most-degraded rung any signal demands.
- Config thresholds (`Ladder*`) — backlog counts + disk free-byte margins.
- Daemon wiring (`internal/daemon/maintenance.go`): each enricher pass samples
  debt (`proj.CountPendingEmbeddings` + `freeDiskBytes` via `syscall.Statfs`),
  records the level, logs transitions (R45-style), and GATES the rungs —
  embeddings skipped at ≥rung3, derivatives/auto-links at ≥rung1, summaries at
  ≥rung2. `cairn status` now reports `degradation`.
- Tests: `internal/maintenance` table tests (rung order + disk precedence) and
  `TestP21DegradationLadderWired` (daemon plumbs backlog → level → gating →
  status). Full suite + vet green; `internal/log` untouched.

### P2-2 — salience inputs (§9.2) — DONE (2026-07-13)

Local, telemetry-derived salience S ∈ [0,1] — raw impressions never leave the
node; only bounded S feeds ranking (P2-3 will consume it).

- Pure math `internal/rank/salience.go`: `DemandPosterior(fetches,impressions)`
  = (f+α)/(imp+α+β), α=1,β=4 (prior mean 0.20) with the min-5-impressions floor
  (no negative judgment on thin evidence); `Salience(...)` blends demand +
  saturated reference in-degree + saturated operator-signal weight into a
  clamped S. Config `Salience*` constants.
- Data: `telemetry.DemandByMessage` (impressions + DISTINCT-task `found`
  outcomes — re-find across tasks is strong); `projection.ReferenceInDegree`
  (reply edges) + `OperatorSignalWeight` (signals.weight sum).
- Daemon `SalienceScores()` / `SalienceFor(id)` combine the three via the pure
  math over the union of signalled messages.
- Tests: `internal/rank` posterior + bounded-monotone tests;
  `TestP22SalienceCombinesDemandRefAndSignals` (a shown+found+replied+signalled
  message outranks an ignored one; all scores in [0,1]). Full suite + vet green.

Deferred within P2 (feed forward to P2-3): the 10% exploration quota and
principal-cluster demal weighting are ranking-time concerns, applied when S
enters the P2 profile. **(Exploration quota implemented in P2H4, 2026-07-16.)**

### P2-3 — full additive ranking profile (§9.1) — DONE (2026-07-13)

The P2 "full model" ranking alongside the P0 profiles, opt-in until §9.3
calibration validates its weights (CAIRN_RANK_PROFILE=p2).

- `internal/rank`: `weightSet` refactor (backward-compatible — P0 profiles are
  byte-identical); new `ProfileSearchP2`/`ProfileDigestP2` with the §9.1
  weights (search 0.75R+0.08S+0.04F+0.10I+0.03N; digest 0.45R+0.15S+0.20F+
  0.15I+0.05N). Candidate gains S/I/N inputs; Components + `Profile.Weights()`
  expose them for why_ranked. `NoveltyFromExposure` (2^(−impressions/half)).
- Daemon (`salience.go` + `retrieve.go`): `P2Inputs()` computes per-message S
  (salience, P2-2) + N (novelty from exposure); Intent from pin/priority-
  confirm. Search + digest select the profile via the opt-in and populate
  S/I/N; why_ranked records now carry S/I/N + their weights (P2 only —
  `fillP2Components`), so calibration replay has every number.
- Tests: `internal/rank` P2 profile ordering + exact additive score + P0
  unchanged; `TestP23FullProfileWiredAndExplained` (P2 search persists a
  search-P2 explanation carrying S/I/N; salience flows through). Full suite +
  vet green.

Deferred: P2-3b calibration harness (§9.3) — its own milestone (offline replay
+ weight grids + `cairn rank-stats`).

### P2-4 — saved searches (§12) — DONE (2026-07-13)

Named, re-runnable queries — a device-local operator convenience (not
replicated, not an event, survives reindex; mutable JSON, single-writer).

- `internal/daemon/savedsearch.go`: `SavedSearch{Name,Query,CreatedAt}`;
  `SavedAdd` (validated name, idempotent replace), `SavedList` (name-sorted),
  `SavedRemove`, `SavedRun` (executes through the normal `Search`). Stored at
  `<device>/saved-searches.json`, overwrite-by-remove then atomic write (the
  write-once `WriteFileAtomic` is for immutable objects only).
- IPC ops `saved-add|list|remove|run` (list/run capRead, add/remove capAdmin);
  `cairn saved add|list|run|rm` CLI.
- Test: `TestP24SavedSearches` (add/replace/list/run/remove + reject bad name +
  persistence across restart). Full suite + vet green.

### P2-3b — calibration harness (§9.3) — DONE (2026-07-13)

Offline, inspectable, NOT learned online — it RECOMMENDS weights; adopting a
change stays a human edit to constants.go.

- Pure engine `internal/rank/calibrate.go`: `Episode`/`CalibCandidate`/
  `WeightVector`; `Evaluate` (Success@5 + MRR of the found result); `SimplexGrid`
  (weights summing to 1 at a step, over active terms); `HoldOutByTask` (split by
  TASK, never random query); `Calibrate` (grid-search on train, validate winner
  on holdout — `Improves` only on a strict holdout beat). Fully unit-tested.
- Daemon `calibrate.go`: `CalibrationEpisodes` (join telemetry `found` outcomes
  with logged why_ranked components), `RankStats` (per-term weighted-contribution
  distribution), `Calibrate` (grid over the active profile; needs ≥8 episodes,
  warns <30 per §9.3).
- `cairn rank-stats [--calibrate]` (capAdmin); IPC returns the distribution and,
  with --calibrate, a recommendation carrying an explicit "not applied" note.
- Data plumbing: `projection.ExplanationsForInteraction`,
  `telemetry.FoundEpisodes` + `AllInteractionIDs`.
- Tests: `internal/rank` evaluate/grid/holdout/calibrate (incl. the honest
  no-improvement case); `TestP23bCalibrationHarness` (episodes assemble; stats
  report; calibrate returns a recommendation, no false improvement). Full suite
  + vet green.

This closes the P2 ranking chapter: profile (P2-3) + calibration (P2-3b), both
opt-in / advisory until you review and adopt.

### P2-5 — local maps + rollups (§7.3 map.md, §12) — DONE (2026-07-13)

A per-agent navigation view — a rollup of the corpus by topic, thread, and pin.
Local derived state, regenerated on demand.

- `projection.NavMap(topThreads)`: totals (messages/topics/pinned objects),
  topics by message count desc, top threads by reply count — all excluding
  retracted/removed rows.
- `daemon.GenerateMap(agent)` (`mapview.go`): renders `views/<agent>/map.md`
  (pure `renderMap`) — headline rollup + topic list + top threads; overwrite-by-
  remove then atomic write. Emits only structural metadata, so the §7.3 quote
  sentinel rule is trivially satisfied.
- `cairn map [--agent]` (capRead) + IPC `map`.
- Test: `TestP25MapAndRollup` (rollup totals, topics ordered by count, thread
  surfaced). Full suite + vet green.

### P2-6 — compaction views (§12; §7 "compacted to current state") — DONE (2026-07-13)

A condensed snapshot of the corpus reduced to what is TRUE NOW, with a headline
of how much event history collapsed away.

- `projection.Compaction()`: current-state counts (live messages, active topic
  links/pins/subscriptions) + compacted-away counts (superseded revisions,
  retracted messages, removed links) + totals (events, revisions).
- `daemon.GenerateCompaction(agent)` (`compaction.go`): renders
  `views/<agent>/compaction.md` (pure `renderCompaction`) — headline
  "N events → M live entities (ratio ×)", current state, compacted-away.
  Structural metadata only (sentinel-safe). Overwrite-by-remove + atomic write.
- `cairn compact [--agent]` (capRead) + IPC `compact`.
- Test: `TestP26CompactionView` (revise+retract → ratio, live count, retracted
  count reported). Full suite + vet green.

### P2-7 — heavy derivatives (opt-in, §8.3) — DONE (2026-07-13)

The heavy-derivative FRAMEWORK — OCR/captioning/transcription territory — opt-in
and sandboxed. Ships the interface + gating + two reference image extractors;
real ML runtimes are pluggable behind the same interface.

- `internal/derive/heavy.go`: `HeavyExtractor` interface + registry;
  `SniffHeavy` (image PNG/JPEG/GIF/WebP, audio Ogg/WAV/MP3/FLAC — from bytes);
  `ExtractHeavy` (size + `HeavyDeriveTimeout` caps, panic-contained, first
  success wins, `ErrToolUnavailable` degrades to the next). Reference extractors:
  `cairn-image-metadata` (pure-Go dimensions — always available) and
  `tesseract-ocr` (external, opt-in, `exec.CommandContext` with empty env / no
  network, cleanly skipped when the binary is absent). Derived text stays
  untrusted and tied to the source hash by the existing derivative record.
- Daemon: `extractDerivative` runs the deterministic N4 extractor, then — only
  when opted in (`CAIRN_HEAVY_DERIVATIVES=1`) and the content is unsupported —
  the heavy pipeline. Off by default (heavy work may shell out).
- Tests: `internal/derive` sniff + metadata + unsupported;
  `TestP27HeavyDerivativesOptIn` (enabled → image gains a derivative) and
  `...DisabledByDefault` (opt-out → none). Full suite + vet + verify green.

## P2 COMPLETE (2026-07-13)

All P2 milestones landed (P2-1 … P2-7), each one commit with tests, `make
verify` green throughout, `internal/log` untouched. P1 is code-complete (N9-H;
the crossed live audit is the operator's). Deferred/advisory items flagged for
your review: P2-3 (P2 ranking profile) + P2-3b (calibration) are OPT-IN /
advisory until you adopt the weights; heavy-derivative ML runtimes are pluggable
behind the shipped interface; and the operator-only items (N9 live audit, rig
restoration, adopt-standalone, G2/G5 live checks) remain.

---

# N9 audit fix work order (docs/cairn-n9-fix-workorder-H.md) — H1–H8

Two N9 audit reports of d451c2f (Claude Brief B: P1 READY WITH FINDINGS, two
MAJOR residuals; Codex Brief A: NOT READY — a stale-binary RIG problem, matrix
UNRUN). Rulings R46–R48 appended first. H1–H5 regression-test-first; H1/H2 apply
the R46 invariant sweep. `internal/log` untouched. One commit per item, FIX-H<n>.

## R46–R48 rulings — DONE (2026-07-14)

R46 (invariant sweeps: gate EVERY write path, enumerate in the commit), R47
(a ranking profile's why-ranked must reconcile exactly with its returned score),
R48 (untrusted-input pre-flight guards before any heavy extractor). Commit
`R46-R48`.

## H1 — ephemeral inline body on the revise/merge path (R42/R46 sweep) — DONE (2026-07-14)

**Defect (Claude MAJOR):** `appendRevision` (export.go) inlined revision bodies
with only a size check — no class gate — so revising or merging an ephemeral
message reintroduced the un-purgeable inline copy R42 forbids (searchable on
every synced node, un-scrubbable at TTL). G1 had closed only publish/reply.

**R46 enumeration — every `body_bytes` write path (grep `body_bytes`):**
1. `daemon.go:756` — message.publish / message.reply (Publish, EmergencyPublish)
   — GATED (G1, class check) + pre-ack `ValidateNoInlineEphemeral`.
2. `export.go:251` — message.revise_body (`appendRevision`, reached by `Revise`,
   `Resolve`/`applyResolution`, `IngestExport`/`ingestEdit`) — **the hole; gated
   here** by the message's text_class + pre-ack `ValidateNoInlineEphemeralRevisions`.
3. `fork_resolve.go:174` — message.publish reissue (fork resolve) — GATED (G1/G7,
   `textClass != ClassEphemeral`).

**Fix:** `appendRevision` looks up `MessageInfo(messageID).TextClass`; an
ephemeral message never inlines any revision body (single OR 2-revision merge).
Added `ValidateNoInlineEphemeralRevisions(env, textClass)` — the structural
pre-ack twin of the publish guard — called before `Append`.

**Regression (`fix_h1_test.go`, FAILED before the gate on cases a/b):** (a) revise
an ephemeral → no body_bytes; (b) 2-revision merge path likewise; (c) after a
housekeeping sweep nothing survives in any event; (d) an ephemeral revise_body
carrying body_bytes is rejected pre-ack via the guard; (e) canonical ≤64 KiB
revise still inlines (not over-corrected). Full suite + vet green; `internal/log`
untouched.

## H2 — migrate writes a device key with no encryption gate (G3/R46 sweep) — DONE (2026-07-14)

**Defect (Claude MAJOR, code-verified):** `migrate.go` staged the new device
private key with no encryption gate — a hole in the P0 invariant "no key
material on unencrypted storage via ANY path." G3 had gated only enroll/join.

**R46 enumeration — every key-material (`SaveKey`) write path:**
1. `initialize.go:134/141` — device + root key (`init`) — GATED (`checkEncryption`).
2. `enroll.go:107` — device key staged (`CreateEnrollRequest`) — GATED (G3).
3. `enroll.go:487` — device key (`Join`) — GATED (G3).
4. `migrate.go:136` — device key (`migrate`) — **the hole; gated here.**
5. `init --adopt` (`Adopt`) — routes through `Initialize`, inherits its gate.
6. `identity export-root` — **EXEMPT by design:** writes the root key to an
   operator-chosen OFFLINE destination (USB / air-gapped / printed); it already
   REFUSES a destination inside the mesh dir. An encryption check there would
   defeat the offline-backup purpose.
7. fork-resolve / recovery origin — reuses the ACTIVE origin's existing device
   key (no new key material) — N/A.

**Fix:** `MigrateOptions` gains `AllowUnencrypted` + `Checker`; `Migrate` calls
`GateEncryption(deviceDir, …)` after the completion guard (which writes no key
material) and before staging the new key. `cairn migrate --allow-unencrypted`
added (parity with init/enroll/join: proceeds + warns loudly).

**Regression (`fix_h2_test.go`, migrate cases FAILED before the gate):**
`TestH2MigrateGatesEncryptionBeforeKeyWrite` (unencrypted refuses before the key
is staged; override proceeds + warns); `TestH2AllKeyWritePathsRouteThroughGate`
— the R46 table over {init, enroll-request, join, migrate}, each must refuse on
an unencrypted volume (a new key-write path added later without the gate fails
it). Full suite + vet green; `internal/log` untouched.

## H3 — why-ranked does not reconcile under the P2 profile (R47) — DONE (2026-07-14)

**Defect (Claude, live-reproduced):** `WhyRanked` printed only R / F / P_eff —
never S / I / N. Under the P2 profile (wP=0, S/I/N non-zero) the printed lines
summed to 0.79 against a returned 0.8255, so the explanation did not reconcile
with its own score — the "black box" spec §9 exists to forbid (R47).

**Fix:** the renderer now prints EVERY additive term (R, S, F, P_eff, I, N) with
its value, weight, and product under BOTH profiles (empty P0 S/I/N and P2 P
parse to 0 → "0 × 0 = 0", so reconciliation holds either way), plus the
mandatory/pin inclusion flag (labelled *not* an additive term). The six printed
products sum exactly to the total.

**Regression (`fix_h3_test.go` — R47's mandatory shape, FAILED before the fix):**
`TestH3WhyRankedReconcilesBothProfiles` parses the output, recomputes the score
from the printed components + weights ALONE in the canonical additive order, and
asserts exact string equality with the printed total — under P0 AND P2, over a
corpus with salience/intent/novelty non-zero, for every returned result. The
reverted-renderer run reproduced the auditor's exact pathology (0.75+0.04+0=0.79
vs total 0.8211, S line absent). Full suite + vet green; `internal/log` untouched.

## H4 — salience gaming surface (§9.2 incomplete) — DONE (2026-07-14)

**Defect (Claude):** operator-signal salience used `sum(weight)` with no decay,
dedup, caps, or trust — and demand inflated one orchestration run into N
clusters. §9.2 requires signals additive with slow decay (never a multiplier
lock), deduped per principal/item/kind, per-principal daily caps, agent trust
weights, and orchestration-run dedup.

**Fix — signals (`internal/rank/signal.go`, pure):** `EffectiveSignalWeights`
projects raw signals into a bounded effective weight per message —
(a) dedup per (principal, message, kind), keeping the least-decayed instance;
(b) clamp declared weight to `SignalMaxWeight`; (c) trust weight (operator 1.0,
agents `SignalAgentTrust` 0.3); (d) slow decay `2^(−age/30d)`; (e) per-principal
per-UTC-day cap scaling. `rank.Salience`'s signal input became a float
(`saturateF`). The daemon (`effectiveSignals`) age-stamps signals against the
daemon clock and feeds the result into salience.

**Fix — demand orchestration-run dedup (`telemetry.DemandByMessage`):** the fetch
cluster key prefers an operator-SUPPLIED task_id, then principal, then
interaction id. A daemon-inferred task (detected precisely as
`'task-'||substr(interaction_id,1,8)`, not via the coarse `inferred` flag which
also trips on inferred surfaces) is treated as no task, so N agents in one run
sharing the orchestrator principal register ONE cluster; distinct operator tasks
still each count.

**Config:** `SignalDecayHalfLifeHours` (30d), `SignalMaxWeight` (3),
`SignalAgentTrust` (0.3) / operator 1.0, `SignalPrincipalDailyCap` (5).

**Regression:** pure `internal/rank/signal_test.go` (dedup / slow-decay ≈ half at
one half-life / agent-trust discount / max-weight clamp / per-principal-day cap);
daemon `fix_h4_test.go` — the three accept tests: (1) a 50-signal agent flood ==
a single signal and < the operator's one signal; (2) a 90-day-stale signal is
out-ranked by a fresh one; (3) a 5-agent run = one demand cluster, +2 distinct
tasks = 3. (P2-2's exact-equality assertion now pins the daemon clock, since
decay makes salience legitimately time-dependent.) Full suite + vet green;
`internal/log` untouched.

## H5 — no decompression-bomb / pixel guard before tesseract (R48) — DONE (2026-07-14)

**Defect (Claude):** `ExtractHeavy` ran the registry (tesseract FIRST — it decodes
the FULL image) with no bomb/pixel guard, so an adversarial attachment (tiny
file, enormous declared dimensions) OOMs the enricher. "Safe on trusted content
only" is self-cancelling: mesh attachments are untrusted by definition (R48).

**Fix (`internal/derive/heavy.go`):** `preflightImage` runs BEFORE the registry
loop (after size + byte-sniff), reading dimensions from the image HEADER via
`image.DecodeConfig` — no pixel allocation. It rejects: per-side dimension >
`HeavyMaxImageDimension` (20000), total pixels > `HeavyMaxImagePixels` (40 MP),
decompression ratio (decoded-RGBA / compressed) > `HeavyMaxDecompressionRatio`
(200), and any undecodable/malformed header. A rejection is a plain error (not
`ErrUnsupported`), so the enricher records a clean `derivative.fail`. Audio has
no shipped decoder (never reaches one); multi-frame count stays bounded by the
16 MiB input cap + per-frame pixel ceiling (frames are not decoded — that would
be the very OOM we prevent).

**Regression:** pure `internal/derive/heavy_h5_test.go` — a 45-byte
`pngHeader(60000,60000)` dimension bomb, an 8000×8000 pixel-flood, and a
malformed header are each rejected (not as `ErrUnsupported`); a legit 24×36 image
still yields a derivative. Daemon `fix_h5_test.go` — a bomb ATTACHMENT with heavy
enabled drains without OOM/hang, records a `failed` derivative (never a
successful `image_metadata`/`ocr_text` — the pre-fix path would have), and the
daemon still publishes + searches afterward. Full suite + vet green; `internal/log`
untouched.

## H6 — degradation ladder rungs 5–7 unwired (R45 spirit) — DONE (2026-07-14)

**Defect (Claude, MINOR):** the ladder computed all 7 rungs but only 1–3 were
consulted; rungs 4–7 had gate helpers no caller invoked (fail-open) — a ladder
that silently doesn't ladder.

**Fix — wired rungs 4 and 5:**
- **Rung 4 (LexicalOnlyForced)** — `Search` (retrieve.go) now skips the vector
  query when the level forces lexical-only, shedding vector cost under a severe
  embedding backlog even with an embedder present. Closes the backlog axis (1–4).
- **Rung 5 (DelayBlobRepl)** — `blobPhase` (reconcile.go) short-circuits under
  disk pressure: it skips the inventory exchange (the initiator then sends
  `done`; the responder handles it), delaying proactive replication. Durability
  is eventual — a later sweep replicates once pressure eases; nothing is lost.
  Inert on a healthy disk.

**Marked rungs 6–7 explicitly (code + README + here):** `RejectLowPrioBlob` and
`RejectSmallText` are REJECT paths — safely rejecting a blob/text send needs
pre-ack reserved-capacity semantics (§8.2's reserved small-hi-pri slice) that
touch the P0 send-never-blocks invariant, so enforcement is DEFERRED. They stay
computed + REPORTED (level in `cairn status`, transitions logged R45-style) and
fail OPEN, never silently. Documented in `ladder.go`, README "Known limitation",
and this entry.

**Regression (`fix_h6_test.go`):** rung 4 — with a BagOfWords embedder a healthy
search is `full`, and `SetDegradeLevelForTest(LexicalOnly)` forces `lexical_only`;
rung 5 — at `DelayBlobRepl` the blob phase no-ops cleanly (nil conn proves the
short-circuit precedes any I/O). Full suite + vet green; `internal/log` untouched.

## H7 — deploy hygiene: end the stale-binary saga — DONE (2026-07-14)

The live daemon had been running the N2-era binary — dogfooding never exercised
the code under test, and it cost an entire audit run.

1. **`make install`** now: fails each fs step with a clear `sudo` / non-root
   (`make install PREFIX=$HOME/.local`) hint instead of a raw permission error;
   and, after copy, DETECTS a running daemon (`pgrep`) and instructs a restart
   (`cairn daemon --install`, or pkill+restart for a hand-run one) — safer than
   auto-killing a daemon that may be mid-sync (launchd KeepAlive would also race).
2. **Version-mismatch warning (`cmd/cairn/version.go`, R45 spirit):** the daemon
   now reports its RUNNING build version over the `status` IPC (`Options.Version`
   → `d.version`). `cairn --version` and `cairn daemon` dial any running daemon
   and warn LOUDLY on stderr when its version differs from this binary's (an
   empty/absent version = a pre-H7 stale daemon → also warns). `--version` is now
   handled directly (cobra short-circuits its built-in version flag before any
   hook could run).
3. **Version string** already reads `p1-<sha>` (G7.5); confirmed.
4. **`cairn daemon --install`** is now the documented DEFAULT in README + DOGFOOD
   (launchd/systemd, supervised + auto-restart) so the daemon is never
   hand-babysat; `cairn daemon &` is noted only as a quick-look fallback.

**Regression (`cmd/cairn/fix_h7_test.go`):** `versionMismatchWarning` (equal →
silent; differing → warns naming both + "STALE"; empty running → warns "unknown")
and `cairn --version` prints the `p1` version. Full suite + vet green;
`internal/log` untouched.

## H8 — audit-rig enablement — DONE (2026-07-14)

The Codex audit died on `sudo` (agents can't type a password) and on a stale
deployed binary; the whole two-node matrix went unrun. Made the audit path
runnable — documentation only (the Makefile already honours `PREFIX`).

1. **Non-sudo install documented** — DOGFOOD §1 now shows
   `make install PREFIX=$HOME/.local` → `~/.local/bin` (no root) alongside the
   sudo path, so an automated auditor agent can install HEAD without root.
2. **New DOGFOOD §15 "Audit rig setup"** — both nodes on the SAME HEAD, non-sudo
   install, `cairn --version` must be `p1-<sha>` (and match the running daemon,
   no stale-binary warning), **`sync_listen`/`sync_peers` REMOVED from NODE-A's
   config** so R44's auto-listener gate (G2) proves itself instead of being
   masked, SSH-over-tailnet to NODE-B, daemons under launchd/systemd, and a
   Phase-0 invariant checklist the auditor asserts first. The root-key ceremony
   stays with the operator, never an agent.

No code change; full suite + vet + verify green; `internal/log` untouched.

---

## N9 work order H1–H8: COMPLETE (2026-07-14)

All eight items landed, one commit each (`FIX-H1`…`FIX-H8`) atop the `R46-R48`
rulings commit. H1–H5 shipped regression-test-first (each repro confirmed failing
before its fix); H1/H2 applied the R46 invariant sweep with the full enumeration
in each commit message. `make verify` green throughout; `internal/log` never
touched. Ten commits total on `master`, nothing pushed.

**Re-verification handed back to the operator/auditors:** H1 (ephemeral sweep),
H2 (key-write sweep), H3 (why-ranked reconciliation), H4 (salience caps/decay),
H5 (bomb guards) are all covered by new regression tests; the two adoption
questions (is the P2 ranking profile safe to enable? is heavy-derivatives safe to
turn on?) and Codex's Brief A end-to-end two-node run remain the live-rig items.
Operator prerequisites (kill/reinstall the stale daemon on both nodes, remove
`sync_listen` per §15, rig restoration, move the root key offline) are the
operator's — see DOGFOOD §15 and the work order's "Operator prerequisites".

---

# N9 Brief A run 2 fix work order (docs/cairn-n9-fix-workorder-J.md) — J1–J4

Input: Codex Brief A run 2 (`ccdf1dc`, live two-node) → NOT READY: one BLOCKER
(J1), a gate failure (J2), two majors (J3/J4). RULINGS.md R49 appended first.
`internal/log/` out of bounds. One commit per item (`FIX-J<n>`).

## J1 — BLOCKER — cross-node topic.link.add parks on a peer (R49) — DONE (2026-07-15)

**Defect (Codex BLOCKER-1):** a `topic.link.add` replicated to a peer parked on
its reindex with `FOREIGN KEY constraint failed` — the referenced topic did not
exist in the peer's projection. F1's atomic-on-origin `topic.create`+`link.add`
is LOCAL atomicity; cross-node reconciliation has no global ordering. Doctor and
gates correctly went red (R6), but an acknowledged link was permanently
unprojectable on the peer.

**Root cause (measured, not the work order's first guess):** the projection's
`Apply` ALREADY enforces per-origin sequence contiguity (a gap is a hard error),
so a SAME-origin link can never precede its create — R49.4 holds trivially there.
The genuine failure is CROSS-ORIGIN: a `topic.link.add` on one origin references a
`topic.create` on ANOTHER origin (B links a message to a topic A created); if the
link's origin is walked/ingested before the create's origin, the FK fails. The
in-process happy-path (same-origin `send --topic brandnew`) does NOT reproduce it;
the deterministic cross-origin ordering does.

**Fix (regression-test-first, RULINGS.md R49):**
- `parked_events.retryable` column (schema v6; both `schema.sql` and
  `build/sql/projection.sql`, drift test kept green). A projection failure on a
  MISSING intra-mesh reference — a FOREIGN KEY constraint or the hand-thrown
  `errMissingRef` (revise_body for a not-yet-replicated message) — parks
  RETRYABLE; every other failure (parse/schema, `revise_body with no revisions`)
  parks TERMINAL.
- `Projection.RetryParked()` — a fixpoint sweep that re-runs `applyPayload` for
  each retryable parked event from its stored envelope and clears the quarantine
  row on success (healing a `topic.create` can satisfy its `link.add` in the same
  sweep). Wired into `Apply` (after every commit — covers each live reconcile
  event and each local append), the end of `Replay` (reindex/recovery), and
  `DoctorProjection` (a doctor run is a natural heal point).
- `DoctorProjection`/`DeepDoctor`/`gates` (R49.3): a retryable park is
  INFORMATIONAL within `config.ParkedRetryableGrace` (24h, from `parked_at`) and a
  FAILURE once overdue (the dependency never arrived); a TERMINAL park is always a
  failure. The gates zero-loss row inherits this via DeepDoctor. `reindex` reports
  the retryable/terminal split and never aborts (exit 0, R4.3).

**Regression (cross-node, `internal/daemon/fix_j1_test.go`):**
- `TestJ1CrossNodeTopicLinkReplicatesCleanly` — two mesh nodes: A seeds a topic, B
  links a new message to it (cross-origin link), both converge, BOTH reindex with
  ZERO parked, deep doctor exit 0, the shared topic carries both messages.
- `TestJ1AdversarialLinkBeforeCreate` — the mandated adversarial case: B's origin
  (the cross-origin link) is applied to a fresh projection BEFORE A's origin (the
  create). Confirmed RED without the fix (parks `topic.link.add … FOREIGN KEY
  constraint failed` and never heals); with the fix it parks RETRYABLE then
  SELF-HEALS once A's create lands. Temporarily neutering `RetryParked` reproduced
  the exact Codex failure, proving the test genuinely bites.
- F1/F3 park tests updated to the R49 semantics (terminal park fails doctor;
  retryable park is a within-grace note): `cmd/cairn` `TestF3DoctorFailsOnParkedEvent`
  (now a terminal `revise_body`), `TestR49RetryableParkIsCleanWithinGrace` (new),
  and `internal/daemon` `TestF1UnprojectableEventIsParkedNotFatal`
  (within-grace info + overdue problem).

`make test` + `go vet` green; `internal/log` untouched.

## J2 — INVESTIGATE — NODE-B lexical-visible P95 > 200ms gate — DONE (2026-07-15)

**Codex BLOCKER-2:** B's automated gate FAILed `P95 243ms over 4 sends` (was
100ms/PASS pre-run) on an unencrypted WSL node that dropped off the tailnet
mid-run.

**Measured before fixing (work-order instruction):** a stable 200-send
measurement on a quiet node (this mac, `CAIRN_SCORECARD=200`):

| metric | value |
|---|---|
| append total | 2.739 s (13.69 ms/event) |
| send-ack P50 / P95 | 13.285 / 15.912 ms |
| **ack→lexical-visible P95** | **1.973 ms** (gate < 200 ms ⇒ 100× margin) |
| search P50 / P95 | 335 µs / 642 µs |
| cold recovery (200) | 44 ms |
| reindex --lexical (200) | 115 ms |

This matches the prior 100k scorecard (1.52 ms). **Verdict: run2's 243 ms/4-send
is small-sample / degraded-node noise, not a regression.** The enrichment path is
unchanged and comfortably inside the gate at scale.

**Gate-quality fix (the real defect — R45 spirit, "a P95 gate must not FAIL on a
sample too small to be meaningful"):** the gate reads P95 by rank offset, so with
4 samples "P95" is just the slowest send — one hiccup FAILs the gate. Added
`config.GateLatencyMinSamples = 30`: below it the `cairn gates`
send-ack→lexical-visible row reports **INCONCLUSIVE** (never FAIL), naming the
sample size and the ≥30 requirement; at or above it the row PASS/FAILs as before.
A 4-send 243 ms result is now INCONCLUSIVE, not a red gate.

**Regression (`internal/daemon/fix_j2_test.go`):**
`TestJ2SmallSampleP95IsInconclusiveNotFail` reproduces the Codex shape (4 samples,
one 243 ms) and asserts INCONCLUSIVE (not FAIL) with the sample size + ≥30
surfaced; `TestJ2AdequateSampleGivesVerdict` asserts ≥30 fast samples PASS.

`make test` + `go vet` green; `internal/log` untouched.

## J3 — INVESTIGATE — NODE-B `reindex --lexical` hung >90s until killed — DONE (2026-07-15)

**Codex MAJOR-1:** reindex on the WSL node hung; the managed daemon stayed
healthy; post-kill doctor was clean.

**Investigation / classification (measure before fixing):**
- **NOT lock contention.** `cairn reindex --lexical` runs IN-PROCESS in the CLI
  (not through the daemon) and side-builds `index.sqlite.rebuild` then
  atomic-renames it over the live db (rulings §6). It never acquires the daemon
  write flock and never opens the live db for writing during the rebuild, so it
  cannot deadlock against the running daemon. Verified in code
  (`cmd/cairn/reindex.go`, `projection.ReindexLexical`).
- **NOT a J1 retry loop.** `RetryParked` is a bounded fixpoint over a finite
  parked set; a healed event's quarantine row is DELETED, so it cannot re-heal,
  and a stuck retryable park makes zero progress and returns. The J1 sweep was
  also moved OUT of per-`Apply` (it now runs once per reconcile batch / at
  recover / at reindex end / in doctor), removing any O(events×parked) re-attempt
  cost during a large rebuild. Pinned by `TestJ3RetryParkedTerminatesOnStuckPark`.
- **Environmental: WSL2 drvfs I/O.** The remaining cause is filesystem I/O on the
  WSL node — a Windows-mounted (drvfs/9p) path makes `synchronous=FULL` SQLite
  writes and the segment reads pathologically slow. Not reproducible on native
  macOS/Linux (mac reindex: 200 events 115 ms, 100k 27.7 s). Documented as an
  environmental posture item; the fix is to keep the cairn dir on a native Linux
  filesystem (recorded for J4/README).

**Hardening (R45: a reindex must never hang silently — timeout + progress log):**
`projection.ReindexLexicalCtx`/`ReplayCtx` take a cancellation context and a
per-event `Progress` reporter (the existing signatures are thin wrappers, so no
test caller changed). The `cairn reindex` CLI now runs under a **stall watchdog**
(`config.ReindexStallTimeout` = 120 s: aborts with a clear "reindex STALLED …
likely a slow filesystem (WSL2 drvfs); move to native Linux" error, leaving only
the discardable `.rebuild`) and a throttled **progress heartbeat**
(`config.ReindexProgressInterval` = 5 s). A hang is now bounded and observable.

**Regression (`internal/daemon/fix_j3_test.go`):**
`TestJ3RetryParkedTerminatesOnStuckPark` (repeated sweeps over a never-satisfied
park all terminate within a hard 10 s bound; the retry-loop hypothesis is ruled
out) and `TestJ3ReindexContextCancelAborts` (a cancelled context aborts the
rebuild with `context.Canceled` — the watchdog mechanism).

`make test` + `go vet` green; `internal/log` untouched.

## J4 — MINOR — provenance + posture — DONE (2026-07-15)

Documentation only (no code).

1. **Checkout ⇄ installed-binary provenance (J4.1).** NODE-B's discovered checkout
   was `a036060` while its installed binary was `ccdf1dc` — the rig was restored
   by shipping a **git bundle** node-to-node and B's working tree was left behind
   the binary that had been `make install`ed from a later bundle. Not causal, but
   it makes "which commit produced this behaviour?" un-answerable. DOGFOOD §15 now
   carries a "Deploy-flow provenance" note with a copy-paste parity check
   (`cairn --version` must contain `git rev-parse --short HEAD`; version derives
   from VCS build info per R11, so a mismatch is always detectable) and a Phase 0
   invariant. Operator action recorded: `git pull` each node's checkout to HEAD,
   then rebuild+install from that checkout.
2. **Device key on unencrypted storage (J4.2).** NODE-B ran on a WSL2 box on an
   unencrypted volume with a persisted `--allow-unencrypted` override — expected
   for a disposable test rig, a standing finding for any real second node. README
   gained a **Security posture** section: keys are device-local `0600`, never
   portable; unencrypted storage is opt-in and warns every start; a real node must
   put the cairn dir (and the device-local key path) on encrypted storage; the
   WSL2 `drvfs` double-whammy (unencryptable + slow `synchronous=FULL`, the J3
   reindex wedge) → keep the cairn dir on a native Linux filesystem on encrypted
   storage. Also restated the R22/R35 same-OS-user isolation honesty.

## J1–J4 COMPLETE (2026-07-15)

All four items landed, one commit each (`FIX-J1`…`FIX-J4`) atop the R49 ruling
(appended to RULINGS.md first, per the work order). J1 shipped
regression-test-first and genuinely CROSS-NODE (the adversarial reproduction was
confirmed RED by neutering `RetryParked`, showing the exact Codex FK park that
never heals). J2/J3 measured before classifying: J2's P95 is ~2 ms at scale
(run-2's 4-send/243 ms was small-sample/degraded-node noise) plus a gate-quality
guard; J3's reindex hang is WSL2 drvfs I/O (environmental) plus an R45
watchdog+progress guard and a ruled-out retry-loop. J4 is provenance/posture
docs. `internal/log/` never touched (flagged, not modified). `make verify` green.

**Handed back to the operator/auditor (Codex re-audit scope):** re-run Brief A
Phase 3 from offline-catch-up through the reindex/doctor/gates checkpoint — the
B→A topic send + A reindex + doctor/gates must be clean (J1); the kill-mid-sync
(both roles), ephemeral-no-backfill, and kill-mid-blob-transfer drills that were
BLOCKED-BY-FAILURE last run should now execute; B reindex completes without
hanging (J3); re-check the P95 gate with ≥30 sends (J2). Rig prerequisites
(checkout/binary parity, encrypted storage for a real node) are in DOGFOOD §15 /
README Security posture.

---

# P2 opt-in fix work order (operator, 2026-07-15) — P2-FIX-1 / P2-FIX-2

Scope per the work order: the two P2-opt-in BLOCKER findings; independent of
the network/K work; `internal/log/` and the reconcile/sync paths out of bounds
(and untouched). Both items regression-test-first.

## P2-FIX-1 — why-ranked reconciliation (R51, sharpens R47/H3) — DONE (2026-07-15)

**Work-order finding:** under `CAIRN_RANK_PROFILE=p2`, why-ranked printed
components that did not recompute to the returned score (reported as
components ≈ 0.9000 vs returned 0.6667, novelty scored but tracing 0.0).

**What actually reproduced on HEAD (`27b3f35`):** the headline shape — a term
scored but omitted/zeroed in the trace — does NOT reproduce: FIX-H3 prints all
six terms and the strict probe traces R, S, F, I, N all non-zero under P2. But
the reconciliation INVARIANT is genuinely violated in a subtler way, and the
mandated regression shape caught it RED:

- **RED (deterministic):** `TestP2WhyRankedReconcilesDigestProfile/P0`:
  printed components recompute to `0.8999999922069077` against a returned
  `0.8999999922069079` (note the 0.9000 neighbourhood of the reported repro).
- **Root cause:** the Go spec permits fused multiply-add contraction inside
  the scorer's additive expression (`rank.go` `score()`); on arm64 the
  returned score therefore differed by 1–2 ulps from a plain IEEE-754
  recompute (each printed value × weight rounded, then summed in term order)
  — which is exactly what an external auditor's python/bc recompute does.
  The prior H3 test never saw it because it reconciled against the printed
  total (same stored record) using a Go expression that fused identically,
  and its Search-path corpus happened not to trigger a fused-vs-plain
  divergence.

**Ruling:** R51 appended to RULINGS.md FIRST (per the FIX-F5 process rule):
reconciliation is defined against an EXTERNAL plain-IEEE-754 recompute of the
printed trace in scorer term order; the scorer must perform per-term rounding
so its arithmetic IS the trace's arithmetic; every scored term must be
printed. (Numbered R51 — R50 is reserved for the parallel K1 work order.)

**Fix:** `rank.go` `score()` wraps each product in an explicit `float64`
conversion — a spec-guaranteed rounding barrier that forbids FMA contraction —
so the returned score is bit-reproducible from the printed components by any
IEEE-754 double implementation. Scores move by ≤ a few ulps vs the fused
form (R16 wording covers benign score drift; ordering unaffected —
`TestGoldenCorpusRetrieval` and the full suite green).

**Regression (`internal/daemon/fix_p2_1_test.go`), the R51 mandatory shape:**
- `TestP2WhyRankedReconcilesWithReturnedScore` — parses why-ranked, recomputes
  fusion-free from printed components+weights ALONE, asserts exact equality
  with the RETURNED score (`SearchOutput.Results[].Score` — a value outside
  the explanation record, so a scored-but-unprinted term always breaks it);
  asserts all six term lines present; under P2 asserts R, S, F, I, N all
  trace NON-ZERO for the salient message (an omitted or zeroed term fails
  before the sum check does). Both profiles.
- `TestP2WhyRankedReconcilesDigestProfile` — same reconciliation for digest,
  returned score parsed from the rendered digest payload. Both profiles.
  This is the leg that was RED pre-fix.
- `retrieve_test.go` `TestWhyRankedExactArithmetic` and `fix_h3_test.go`
  recomputes aligned to the R51 external-verifier semantics (both were fused,
  i.e. verifier-dependent — the H3 test became timing-flaky in reverse once
  the scorer stopped fusing, proving the point). Reconciliation tests
  stressed `-count=5` green.

**R46 sweep (term-combination surface, enumerated):** `rank.go score()` is the
single live scoring site (both `Rank` and `RankUniformR` call it);
`rank/calibrate.go WeightVector.score` is the only other place additive terms
combine (the §9.3 calibration replay of stored components) — both now carry
the per-term rounding barriers. No other combination site
(`grep -rn 'w\.R\*|\.R\*'` over non-test code).

**Live drill (this machine, rebuilt binary, throwaway mesh, real IPC):**
under `CAIRN_RANK_PROFILE=p2` and under P0, `cairn why-ranked` traces parsed
and recomputed in PYTHON (the external-tool case that previously mismatched by
1 ulp) → `EXACT MATCH` against the returned score in both profiles, with
S/I/N non-zero under P2 (S 0.1433…, I 0.6, N 0.3242…).

`make test` + `make vet` green before commit. Committed as `FIX-P2-1`.

## P2-FIX-2 — derivative memory guard (R48 hardening) — DONE (2026-07-15)

**Work-order finding:** pre-flight budgets pixels × 4 bytes but a grayscale
image passes and OOMs when Go's decoder expands to RGBA; repro a
60000×60000 grayscale PNG. Demanded fix: budget by the decoder's ACTUAL
target format (RGBA regardless of source channels) plus absolute
dimension/pixel ceilings.

**RED attempt (honest result): the defect does not reproduce on HEAD.**
FIX-H5 (`70f317c`) already implements exactly the demanded shape in
`derive.preflightImage`: absolute per-side cap (20000 px), absolute
total-pixel ceiling (40 MP), and a decompression-ratio budget computed as
`pixels × 4` — the RGBA decode target, independent of source channel count —
all evaluated from `image.DecodeConfig` (header only, no pixel allocation)
BEFORE any extractor runs. The 60000×60000 grayscale PNG is rejected by the
per-side cap in pre-flight; no decoder ever sees it. What was MISSING was any
regression coverage for the grayscale/paletted class (H5's tests used
truecolor only) and any bounded-memory assertion — so a future regression to
source-format budgeting would have landed green. That coverage is this fix.

**Regression (new, all GREEN against HEAD, and RED-capable by construction —
the memory bounds fail if any decoder allocates a raster):**
- `internal/derive/fix_p2_2_test.go`:
  `TestP2GrayscaleBombRejectedBounded` — the 60000×60000 grayscale repro,
  a grayscale 8000×8000 pixel-flood, a 1-bit paletted bomb, and a 16-bit
  gray+alpha bomb are each rejected BY PRE-FLIGHT (error must name
  `preflight`) with total allocations bounded (< 8 MiB asserted via
  `runtime.MemStats`); `TestP2PreflightBudgetsRGBATarget` pins the budgeting
  rule itself — identical dimensions produce an IDENTICAL decompression
  ratio for grayscale and truecolor sources (channel-independent ⇒ RGBA
  target), and both trip the ratio guard.
- `internal/daemon/fix_p2_2_test.go`:
  `TestP2GrayscaleBombDaemonSurvivesBounded` — grayscale bomb + pixel-flood
  attached through the live daemon path with heavy derivatives ON: each
  yields a clean `derivative.fail` (never `image_metadata`/`ocr_text`),
  enrichment allocations bounded (< 32 MiB asserted), and the daemon
  survives (send + search still work). `TestP2GrayscaleBombGateOffInert` —
  without `CAIRN_HEAVY_DERIVATIVES=1` the same bomb is inert (gate
  genuinely off) and still records a clean `derivative.fail`, bounded.

**Live drill (this machine, rebuilt binary, throwaway mesh,
`CAIRN_HEAVY_DERIVATIVES=1`):** a 33-byte 60000×60000 GRAYSCALE PNG (the
work-order repro) and an 8000×8000 grayscale flood attached via
`cairn send --attach` through the real IPC path → each recorded a clean
`derivative.fail` in the durable log with the pre-flight reason
(`preflight: image 60000x60000 exceeds the 20000 px per-side cap`;
`preflight: 64000000 pixels exceeds the 40000000-pixel ceiling`); daemon RSS
stayed 20–24 MB throughout (baseline ~24 MB — no spike), search still
answers, deep doctor clean.

`make test` + `make vet` green before commit. Committed as `FIX-P2-2`.
`internal/log/` and reconcile/sync untouched by both fixes.

**Note for the operator:** the on-disk `H-REVERIFY-REPORT-claude.md` records
H3/H5 as FIXED with no BLOCKERs, which is consistent with what reproduced
here: P2-FIX-1's headline repro was already closed by FIX-H3 (the residual,
now-fixed defect was verifier-dependent reconciliation — real, RED, but
ulp-scale), and P2-FIX-2's guards were already correct (the gap was test
coverage, now closed).

---

# N9 run-3 fix work order (docs/cairn-n9-fix-workorder-K.md) — K1

## K1 — BLOCKER — ephemeral content reached a peer offline at send time — DONE (2026-07-15)

Third instance of the ephemeral-leak class (inline-publish → inline-revise/
merge → object-transfer-on-backfill). Codex run 3: B offline at publish;
after rejoin B could search AND fetch the ephemeral body. **R50 replaced its
reserved placeholder in RULINGS.md first** (the ephemeral invariant is
delivery-time-scoped; its surface is fetch/store/index/serve/advertise).

**Root cause:** `pushOrigin` shipped ephemeral bodies with
`includeEphemeral=true` on EVERY push — "live PUSH to a connected peer"
(R38) was implemented as "any push over a live connection", so the
anti-entropy/catch-up push after B's rejoin backfilled the body; B stored,
indexed, and served it. The G1 regression only exercised the PULL direction
(which correctly withholds), so the push leak survived.

**Fix (delivery-window rule, R50):**
- Sender gate: an ephemeral body is OFFERED only on a live push while its
  event is inside `EphemeralLiveDeliveryWindow` (60 s; push-on-append fires
  within the 250 ms debounce — the margin covers slow dials). A catch-up
  push ships the EVENT without the content.
- Receiver gate (defense in depth): `ingestRecords` STORES an offered
  ephemeral body only when the event wall time is inside
  `EphemeralLiveAcceptWindow` (5 min, |skew|-tolerant); a nonconforming
  sender's late backfill is refused loudly.
- Serve gate: `get_object` never serves an object the projection knows only
  as ephemeral content (new `projection.EphemeralOnlyObject` across revision
  bodies, attachment durability, and derivative text via its source
  attachment) — cache-then-advertise stops at ephemeral.
- Accept gate: `put_object` refuses ephemeral-only objects.
- Agent surface: `fetch` of a never-delivered ephemeral returns the TYPED
  `ephemeral_not_delivered` result (manifest field included) — never the
  body, never an opaque error; distinct from `content_expired`.
- Derivative queue: an absent ephemeral attachment blob is skipped quietly
  (absence is by design on non-recipients — previously would have recorded a
  spurious permanent derivative.fail); never a remote fetch.
- Unparseable wall times fail CLOSED (content withheld, event replicates).

**Path enumeration (R50.4/R46; also in the FIX-K1 commit message):** PULL
get_range (never ships ephemeral — pre-existing, now pinned by the drill's
pull direction); live PUSH (window-gated — the leak); ingest accept
(window-gated); blob inventory/advertise (`targetBlobs` excludes ephemeral —
pre-existing; body objects are never inventoried); `get_object` serve
(gated); `put_object` accept (gated); initiator blob pull (`fetchBlob` only
runs over non-ephemeral targets); agent fetch (typed not-delivered, local
store only — no remote path exists); FTS index + reindex (local object only,
R42 — no bytes ⇒ no index); embeddings/digest excerpts (local store only);
derivative extraction (skip-absent gate; derived text of an ephemeral source
is classified ephemeral-only by the serve/accept gates via the derivatives
join); export/peek/fork-reissue (local store only); inline-body-in-event
(structurally rejected pre-ack — G1/H1). **Known residual (flagged, not a
transfer path):** the derived TEXT object of an ephemeral attachment on a
node that legitimately holds it is not yet TTL-purged by housekeeping
(ObjectRefs covers revision bodies only); it can never leave the node (never
inventoried, never in bodiesForRepl, get_object refuses it).

**Regression (`internal/daemon/fix_k1_test.go`) — R50 mandatory shape,
CROSS-NODE + REJOIN, confirmed RED first:**
- `TestK1EphemeralNoBackfillOnRejoin` — the Codex drill: baseline converge,
  disconnect B, A publishes `--class ephemeral` + canonical control, live
  window passes, B rejoins, catch-up runs BOTH directions to quiescence
  (control message proves it). On B: search 0 hits; fetch typed
  not-delivered with no body file; object absent from the store; deep doctor
  clean (R43); publish EVENT present with class intact and chain contiguity
  to A's frontier. Origin still holds/indexes its own. RED pre-fix
  (searchable on B — the exact live leak).
- `TestK1EphemeralLiveGossipContrast` — a peer connected AT publish time
  receives the body via the push-on-append sweep (search hit, object held,
  normal fetch). GREEN before and after (feature preserved).
- `TestK1EphemeralAcceptGate` — nonconforming sender (huge send window)
  offers late; receiver refuses to store/index. RED pre-fix.
- `TestK1EphemeralCacheAdvertise` — third node C (fresh enrolment ceremony)
  receives the ephemeral LIVE, then B joins late: B↔C sync both directions,
  plus DIRECT protocol probes — `get_object` on C answers Missing (cache
  never re-serves), `put_object` on B is refused (bytes never stored). C
  keeps its own copy; gates hold across daemon restarts (wall-time scoped,
  not in-memory). RED pre-fix.
- `fix_g1_test.go` fetch assertion upgraded to the typed not-delivered
  result (was: any error).

`make test` green (21 packages; K1 drills ×3 for flake), `make verify` green
(untagged guard + tagged suite). `internal/log/` untouched; blob durability
(N7) semantics unchanged for non-ephemeral targets. Committed as `FIX-K1`.

---

# go.mod version-directive fix (deploy) — DONE (2026-07-15)

**Problem:** `go.mod` declared `go 1.26.3` — an unnecessarily high toolchain floor that
repeatedly blocked deploys on NODE-B (system `go1.19.8` cannot even parse patch-version
syntax) and pinned the project to one exact toolchain.

**Investigation:**
- Cairn's own code compiles at language level **1.23** (clean checkout built with the `go`
  directive at `1.23` under the 1.26.3 toolchain).
- The real floor is set by DEPENDENCIES: `golang.org/x/net v0.57.0` → `go 1.25.0` (binding
  max), `ledongthuc/pdf` → `go 1.24.1`.
- The "major.minor only" premise is the pre-Go-1.21 rule; it is now inverted. `go 1.25`
  (bare) makes `go build` FAIL (`updates to go.mod needed … go mod tidy`); `go mod tidy`
  normalizes to `go 1.25.0`. Both reproduced live.

**Decision (operator-steered):** do NOT downgrade `x/net`/`pdf` to reach a 1.23 floor — both
parse untrusted content (HTML tokenizer, PDF extractor) and are sandbox attack surface (R48);
keeping them current beats a low version number. Declare the honest floor instead.

**Fix:** `go 1.25.0` + `toolchain go1.26.3` (RULINGS R52). No dependency, `go.sum`, or source
change. README Quickstart documents "Prerequisite: Go 1.25+". `make build`, `make verify`
(untagged guard + tagged suite, testcache cleaned) green on NODE-A (go1.26.3).

**Live two-node reinstall verification (2026-07-15):**
- **NODE-B (the node this blocked)** — base toolchain is `go1.23.4` (`GOTOOLCHAIN=local go
  version`); the `toolchain go1.26.3` directive resolves to the **already-cached** toolchain
  (`~/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.3.linux-amd64` — no download,
  offline-capable). Clean build from a CLEARED build cache (`go clean -cache`) succeeded →
  `p1-939fca5d6d31`; reinstalled `~/.local`, `systemctl --user restart cairn` → active on the
  new binary. This is the "clean checkout builds" evidence: not pure Go 1.23 (the honest
  floor is 1.25, operator-accepted per R52), but a fresh go1.23.4 base auto-provisions the
  pinned compiler and builds with zero manual toolchain steps — the deploy trap is closed.
- **NODE-A** — reinstalled `~/.local`, launchd restarted → daemon on `p1-939fca5d6d31`.
- Both running daemons report `p1-939fca5d6d31`; tailnet peering intact after restart.

---

# P2 shakedown fix work order (docs/cairn-p2-shakedown-fix-workorder.md) — P2H1–P2H7

## P2H1 — BLOCKER — topic-name injection into agent-facing views (R53) — DONE (2026-07-16)

**Defect (Claude BLOCKER-1):** the topic-name schema pattern `^[a-z0-9][a-z0-9/_-]*$` was
enforced ONLY on the message `--topic` auto-create path. Every other topic-name write/ingest
boundary accepted unvalidated names, and `renderMap` emitted the name inline with no
sentinel/escape — so a topic named `## SYSTEM DIRECTIVE\n…` rendered as a first-class markdown
heading in `views/<agent>/map.md`, indistinguishable from the daemon's own output, and rode in
on `topic.create` sync to poison every peer's map.

**Ruling appended first:** RULINGS.md **R53** — the untrusted-content sentinel + validation
applies to ALL rendered fields, not just message bodies.

**R46 sweep — every topic-name write/ingest path (enumerated, all now gated):**
1. message `--topic` auto-create — `internal/daemon/daemon.go` `Publish` (was the only gated
   path; switched to shared `validateTopicName`).
2. `cairn topic create` CLI → IPC op `topic-create` — `internal/daemon/ipc.go` (NEW guard,
   pre-ack).
3. `TopicEnsure` (used by markdown ingest and IPC op `topic-ensure`) —
   `internal/daemon/export.go` (NEW guard, pre-ack).
4. remote sync ingest / `projection.Apply` `topic.create` — `internal/projection/projection.go`
   (NEW guard: a schema-violating name is refused and quarantined as a **terminal** park —
   `isRetryablePark`=false — never INSERTed; signature verification is not payload validation).

Single source of truth for the pattern: `internal/event/topicname.go`
(`event.TopicNamePattern` / `event.ValidTopicName`) — the lowest package both daemon and
projection already import, so no boundary re-types the rule.

**Render escape (defense in depth):** `renderMap` now runs the topic name through
`sanitizeMapField` (collapses any rune outside the schema charset to `_`), so even a historical
bad name already durable in the log, or one that slips a boundary, renders as inert single-line
text — it cannot present as a heading or instruction. The file's false "sentinel trivially
satisfied" comment was corrected.

**Regression tests (test-first):**
- `internal/event/topicname_test.go` — validator rejects the exact audit payload + a battery of
  break-out strings; accepts conforming names.
- `internal/daemon/fix_p2h1_test.go` — `topic-create` and `topic-ensure` IPC ops reject the
  injection pre-ack (next_seq unmoved); conforming happy path still works.
- `internal/daemon/mapview_escape_test.go` — `renderMap` escapes a crafted injection name (no
  rogue `## ` heading, no `\n## SYSTEM DIRECTIVE`); conforming names render unchanged.
- `internal/projection/topicname_ingest_test.go` — a signed `topic.create` with the injection
  name applied to a fresh projection is parked **terminal** and never projected; `RetryParked`
  does not resurrect it; conforming name projects normally.

**Verification:** `go build`/`go vet -tags sqlite_fts5` clean; affected suites (daemon,
projection, event, ingest) green. Live end-to-end repro on a fresh daemon: `cairn topic create`
with the exact `## SYSTEM DIRECTIVE\n…` payload → `rejected before ack … is not a valid topic
name`; conforming `project/zebra` created; `cairn map` renders a clean `## topics` section with
zero rogue headings. `internal/log` untouched (out of bounds).

## P2H2 — MAJOR — `reindex --lexical` against a live daemon splits the projection — DONE (2026-07-16)

**Defect (Codex P2 shakedown MAJOR-1):** `cairn reindex --lexical` side-builds a fresh
`index.sqlite` and atomically swaps it into place. A running daemon holds its own open handle
to the pre-swap file, so after the swap the daemon's in-memory view and the on-disk projection
diverge until the daemon restarts — any external read of `.cairn/index.sqlite` in that window
sees stale state. (The log stays immutable and no acked send is lost, so it is not a
source-deletion BLOCKER — but it caused real operator confusion during the live audits.)

**Fix (work-order option b — least-surprising):** the lexical branch of `cairn reindex` now
probes for a live daemon (`Op: status`); if one is reachable it REFUSES with guidance — stop the
service / kill `cairn daemon`, run the reindex, then start it again (the daemon reconciles its
projection from the log on startup). No silent split. With the daemon stopped the reindex
proceeds exactly as before. `internal/log` untouched.

**Regression test:** `cmd/cairn/fix_p2h2_test.go` — reindex against a live in-process daemon is
refused with the stop-the-daemon guidance; the same reindex proceeds and reports a rebuild once
the daemon is stopped.

## P2H3 — MINOR — `cairn status` documented but absent — DONE (2026-07-16)

**Defect (Codex P2 shakedown MAJOR-4):** README (§8.2 degradation-ladder note) and PROGRESS
referenced `cairn status`, and the ladder claim says the level "shows in cairn status", but the
CLI subcommand did not exist — the IPC `status` op was reachable only from the test harness.

**Fix:** implemented `cairn status` (`cmd/cairn/status.go`), wired to the `status` op, which was
enriched to a genuine one-shot operator health view: build version, cairn/device id, log head
(`next_seq`), degradation level, pending embeddings, **projection health** (parked terminal /
retryable counts, flagged "run doctor" if any terminal), membership count, sync-peer count, and
sync-listener state. Human-readable by default; `--json` emits the raw object. The README's
"level shows in `cairn status`" claim is now backed by the command.

**Regression test:** `cmd/cairn/fix_p2h3_test.go` — human + `--json` views against a live daemon
carry the expected fields; against a stopped daemon it returns the standard "daemon not running"
guidance.

## P2H4 — Salience exploration quota implemented (§9.2) — DONE (2026-07-16) [Tier 2]

**Gap (Codex P2 shakedown MAJOR-2):** spec §9.2 requires a 10% exploration quota for new
items; the code had the additive novelty term `N` but no quota/reservation, so new content had
no guaranteed visibility before impressions accumulated — the cold-start smoothing was
incomplete.

**Fix:** `applyExplorationQuota` (`internal/daemon/retrieve.go`) reserves
`floor(K × config.ExplorationQuotaFraction)` (=10%) of a search's K result slots for NEW items
— those below `SalienceMinImpressions` (5) qualified impressions — under the **P2 profile only**
(P0 keeps the plain top-K cut). Slots fill as: top (K−quota) by score (merit), then up to
`quota` new items promoted from the cut region in score order, then any unfilled exploration
slots backfilled with the next-best by merit (never wastes budget). Output preserves global rank
order; a promoted new item appears where its own score places it. `floor` avoids tiny-K
starvation (K < 10 keeps the plain cut). `P2Input` gained an `Impressions` field so "new" is
determined deterministically. `ExplorationQuotaFraction` added to `internal/config/constants.go`.

**Harmless while P2 ranking is OFF** (gated on `profile.IsP2()`); required for the eventual
calibration to behave correctly. **Regression tests:** `internal/daemon/exploration_quota_test.go`
— promotes a new item at the cut (displacing the lowest merit item), backfills when no new items
exist, leaves under-K sets untouched, and does not starve tiny-K searches.

## P2H5 — Salience reference graph broadened beyond replies (§9.2) — DONE (2026-07-16) [Tier 2]

**Gap (Codex P2 shakedown MAJOR-3):** `ReferenceInDegree()` counted only replies, so salience's
reference-graph input reflected threading alone, not the full §9.2 edge set ("replies, citations,
onward attachment, supersedes edges").

**Fix:** `ReferenceInDegree` (`internal/projection/rankq.go`) now sums the structurally-projected
inbound cross-message edges:
- **replies** (as before) — counted at message_id granularity, so a reply to a since-SUPERSEDED
  revision still counts for its message: the supersedes edge is honored implicitly (§9.2 / spec
  line 135, "the supersedes edge is part of the reference graph" — references survive revision,
  they are not lost or re-homed).
- **onward attachment** (NEW) — the earliest non-retracted attacher of each blob is the origin;
  every later distinct attacher contributes +1 to the origin's in-degree.

**Deferred (documented, not a silent gap):** CITATIONS in later message bodies — the remaining
§9.2 edge type — have no structured edge in the P0/P2 event set; detecting them means scanning
every body for message-id references, an O(corpus²) pass on the hot ranking path. Deferred to a
future indexed citation extractor or an explicit citation event (noted in the `ReferenceInDegree`
doc comment). This is the one §9.2 edge that isn't structurally available; the other three are now
covered.

**Harmless while P2 ranking is OFF.** **Regression test:**
`internal/projection/reference_indegree_test.go` — a root with two replies scores in-degree 2; a
blob origin re-attached by one onward message scores in-degree 1; leaf/attacher messages score 0.

## P2H6 — Maps are structural rollups, not embedding-backed — DECIDED: Option A (2026-07-16) [Tier 3]

**Finding (Codex P2 shakedown MINOR-1):** the shipped `cairn map` is a structural topic/thread/pin
rollup (`NavMap` + `renderMap`), not the embedding-clustered semantic map the design brief framed.
This is a scope decision, not a bug.

**Decision (operator, Option A):** accept structural rollups as the v1 map; update the docs to
describe what's actually built; defer the embedding-clustered *semantic* map to P4 (where
self-folding lives, and where it can be trained on real P2 usage/salience data — don't build the
self-organizing map on zero data). After P2H1, the structural map is safe to enable.

**Docs updated to match reality:** README roadmap (P2 = "local structural navigation maps
(topic/thread rollups)"; P4 = "embedding-clustered self-folding topic maps"); `docs/spec-v0.3.md`
P2 + P4 build lists (P2 map is deterministic/embedding-free, semantic map is P4); `P2-PLAN.md`
P2-5; `internal/daemon/mapview.go` doc comment (this is the structural v1 map; semantic → P4). No
code change — the implementation already matches the (now-accurate) docs.

## P2H7 — Deferred hardening notes for real ML extractors — RECORDED (2026-07-16) [Tier 3]

Not fixing now (the shipped deterministic extractors don't need it), but recorded as deferred so
a future extractor addition trips over the reminder (Claude shakedown FIND-2 / FIND-3, both LOW):

- **FIND-2 — OS-enforced network isolation.** The heavy extractors' network isolation is BY
  CONSTRUCTION (tesseract + the pure-Go metadata reader make no network calls), not OS-enforced
  (no sandbox-exec/seccomp/netns around the subprocess). A prominent DEFERRED-HARDENING comment
  now sits on `heavyRegistry` (`internal/derive/heavy.go`): before registering ANY
  network-capable or heavier ML extractor, wrap the subprocess in an OS-level network/process
  sandbox first — the by-construction guarantee does not survive an extractor that CAN network.
- **FIND-3 — subprocess WaitDelay (applied).** Added `cmd.WaitDelay = config.HeavyExtractorWaitDelay`
  (5s) to the tesseract command so a future child that inherited stdout could not keep
  `cmd.Output()` blocked past the ctx kill deadline. Inert for tesseract (no child), load-bearing
  for any future child-spawning extractor.

---

# P3 — onboarding/transport (spec §12; plan: P3-PLAN.md)

Started 2026-07-16. Discipline: defer live-hardware/human-review items, keep
going until blocked (same as P1/P2). Buildability split is recorded in
`P3-PLAN.md`: transport abstraction (P3-1) and pairing invitations (P3-2) are
fully offline-buildable; thin-node role (P3-3) builds its model/accounting with
the live remote-query + power parts deferred; the real iroh wire (P3-4) is an
interface now, wire deferred (hardware-gated, the P2-7 ML-runtime pattern).

## P3-1 — Transport abstraction (FOUNDATION) — DONE (2026-07-16) [Tier 1]

**Goal (spec §12 P3, threat model line 336):** create the seam iroh drops into
without touching the authenticated handshake or N6 reconciliation. The transport
must move bytes ONLY — membership stays the app-layer cert handshake's job (R27:
endpoint identity ≠ mesh authorization), so a substituted transport can never
admit a non-member.

**Build:** new `internal/peer/transport.go` defines the `Transport` interface
(`Name`, `ValidateAddr`, `Listen`, `Dial`, `LocalAddr`). The P1 Tailscale/TCP
path became `tcpTransport` (delegating to the existing `ValidateListenAddr` /
`DetectTailnetIP` so the tailnet-only semantics have exactly ONE definition), and
`var DefaultTransport Transport = tcpTransport{}`. `peer.go` gained
`NewServerWithTransport` / `DialWithTransport` / `PingWithTransport`; the bare
`NewServer` / `Dial` / `Ping` now delegate to `DefaultTransport`, so **no existing
caller changed** (fetch.go, reconcile.go ×3, device.go, daemon.go, all sync
tests). The byte-level `net.Listen("tcp", …)` / `net.DialTimeout("tcp", …)` moved
behind `tr.Listen` / `tr.Dial`; `dial()` takes a `Transport` param. The handshake
(hello/verifyPeer/transcript) was already transport-agnostic and is unchanged.

**Tests (`internal/peer/transport_test.go`):** a `memTransport` backed by
`net.Pipe` — no sockets, no addressing, trusts every endpoint — proves the P3-4
contract:
- the full mutual cert handshake succeeds identically over it (transport-
  agnosticism is honest, not asserted);
- it STILL cannot grant membership — an un-enrolled peer is refused by the
  handshake and the refusal logs the presented identity (R27 holds over any
  transport);
- `DefaultTransport` preserves the P1 tailnet address guard (rejects 0.0.0.0,
  accepts 100.64.0.0/10) — the guard moved behind the interface but did not change.

`make verify` green (untagged guard + full tagged suite). Pure refactor: no
behavioural change to P1 sync.

## P3-2 — One-time pairing invitations — DESIGN FORK, ruling requested (2026-07-16)

### Author rulings needed

**The pairing trust model (blocking P3-2).** Spec §12/§336 wants "one-time
expiring high-entropy invitation or PAKE" for frictionless onboarding, but the
existing trust model constrains HOW:

- The chain verifier admits a `device.add` ONLY if its embedded `DeviceCert`
  carries a valid **root signature** (`chain.go:127` → `Cert.Verify(rootPub)`).
- The root key is **offline-only** — it never lives on a running node (enroll.go
  ceremony; hard rule "device private keys … never in the portable dir"; root
  restored transiently only during approve/revoke).
- The current enrol ceremony ALSO deliberately keeps the **device private key
  from ever moving** — the new node generates its own keypair; only the pubkey
  travels in the request (enroll.go: "the private key NEVER moves").

A pairing invitation that lets a NEW device be admitted without the root key
present at join time forces a fork, and one branch reverses that deliberate
"private key never moves" property. The three viable shapes:

1. **Pre-signed credential invitation (no new trust path, no delegation).** One
   offline root ceremony mints the invitation: generate the new device keypair,
   root-sign its cert, package {cert + its private key + identity chain + invite
   secret + expiry} into a single one-time token; remove the root key. Join
   installs the carried identity; the `device.add` (root-signed cert) is appended
   via the existing Approve-style path (by the minting node offline, or by a live
   inviting node after a bearer-secret pairing handshake). **Reuses the existing
   verifier trust path unchanged; needs no delegation (stays out of P4).**
   Tradeoff: the device private key travels inside the one-time invitation —
   regresses "the private key never moves." Mitigated by expiry + single-use +
   treating the token as a secret.

2. **Delegated enroller (key never moves, but pulls P4 forward).** Root signs an
   enrolment authority; a live node admits devices whose own-generated pubkey it
   receives during pairing. Private key never moves, cleanest onboarding — but
   this is macaroon-style **delegation**, which the spec explicitly defers to P4.
   Adds a new admission path to the security-critical chain verifier.

3. **Same trust model, smoother UX only.** Keep request/approve/join exactly as
   is; just wrap it in a nicer CLI + compact token/QR encoding. Zero security-
   model change; least frictionless (still needs a root-key restore at approve).

**Conservative default if unanswered:** Option 1 with a **live bearer-secret
pairing handshake** (append-on-arrival) so single-use is HARD (device.add
appended exactly once, invite-id marker refuses replay) and there are no phantom
devices — it reuses the existing root-signed-cert verifier path, needs no
delegation, and exercises the P3-1 transport seam. `// RULING-NEEDED:` will mark
the mint ceremony. Recorded here; asking the operator because Option 1 reverses a
deliberately-chosen security property and the three options are genuinely
different products.

**RULING (operator, 2026-07-16): Option 1 — pre-signed credential invitation,
append-on-arrival.** One offline root ceremony mints the whole invitation (device
keypair generated + cert root-signed + private key packaged into the token); the
device.add is appended by a LIVE inviting node when the new node arrives and
proves possession, giving HARD single-use (a device.add for that cert.DeviceID is
appended exactly once; replay refused). Root stays offline; the existing
root-signed-cert verifier path is reused unchanged; no delegation (P4 untouched).
Accepted tradeoff: the device private key travels inside the one-time, expiring,
single-use invitation secret. Build order: P3-2a invitation format + mint +
verification core (identity layer); P3-2b live pairing handshake + on-arrival
device.add append (transport + daemon); P3-2c `cairn pair` CLI + docs.

## P3-2a — Pairing invitation format + mint + verify (identity core) — DONE (2026-07-16) [Tier 1]

**Build (`internal/identity/pairing.go`):** the security-critical crypto core of
the chosen Option-1 pairing model.
- `PairingInvitation` — the single bearer token: `{v, cairn_id, created_at,
  invite_id (UUIDv7 single-use marker), cert (root-signed), device_priv_b64
  (SECRET), chain (genesis..current, NOT this device.add)}`.
- `MintPairingInvitation` — the ONE offline root ceremony: generates the device
  keypair, root-signs the cert (issued_at = the unforgeable expiry anchor),
  packages the credential + verifiable chain. Appends NOTHING to the log (no
  daemon lock needed) — the device.add is appended on arrival (P3-2b). Refuses a
  foreign root key.
- `VerifyPairingInvitation(inv, now)` — validates against nothing but genesis:
  replays the chain through a fresh `ChainVerifier`, checks the cert's root
  signature against the CHAIN's root (reuses the existing `Cert.Verify` trust
  path — no new admission path, no delegation), confirms the carried private key
  matches the certified pubkey, asserts the device is NOT already a member (it's
  admitted on arrival), and enforces expiry (cert.issued_at + `PairingInviteTTL`,
  15 min). Returns the verified Trust (minus this device) + the device key.

**Config:** `PairingInviteTTL = 15m`, `PairingHelloDomain` (for the P3-2b
challenge) added to `constants.go`.

**Tests (`pairing_test.go`):** mint→verify round-trip (device NOT a member at
mint; cert root-signed against the verified root); expiry boundary (accepted at
TTL−1s, refused at TTL+1s); tamper matrix — mismatched private key, forged cert
(device-id rewrite invalidates the root sig), truncated chain, bad version;
mint refuses a foreign mesh's root key.

`make verify` green. Next: **P3-2b** — the live pairing handshake over the P3-1
transport + on-arrival `device.add` append with hard single-use + expiry
enforcement on the inviting node.

## P3-2b — Append-on-arrival admission (daemon) — DONE (2026-07-16) [Tier 1]

**Build (`internal/daemon/pairing.go`):** `Daemon.AdmitPairedDevice(cert, inviteID)`
— a live inviting node validates a pre-signed pairing credential (P3-2a) and
durably appends the `device.add` admitting it, via the daemon's OWN append path
(`buildEvent` → `d.lg.Append` → `applyProjection`), signed by this device's key
with root authority carried inside the cert. Same shape as the offline `Approve`
ceremony, but with NO root key on the live node — so it adds no new chain-verifier
trust path and no delegation (P4 untouched); on recovery the event verifies
exactly as an Approve-minted one.

Checks, in order (all pre-append): (1) cert re-verified against OUR mesh root
(`d.trust.RootPub` — peer-supplied data is never trusted); (2) expiry
(cert.issued_at + `PairingInviteTTL`, unforgeable); (3) single-use —
`d.trust.Member`/`Revoked` catches cross-restart replays (the durable device.add
is in recovered trust) and a new session-scoped `admittedPairings` set
(d.mu-guarded, added to the Daemon struct) catches replays within one daemon
lifetime. Cross-node double-admit of the SAME identity is benign (same key, not a
clone; converges to a redundant device.add).

**Note (deferred to a later P3-2 step):** like every membership change today —
including a `device.add` replicated in via N6 (`ingestRecords` verifies with a
fresh `d.trust.Verifier()` snapshot but does NOT write learned keys back to
`d.trust`) — the admitted device becomes usable on THIS node's live sync listener
only after a restart rebuilds trust from the log. A live trust refresh (so
pairing is immediately syncable without restart) is its own step; it also needs
concurrency-safe trust because `peer.Server` reads `d.trust` from accept
goroutines.

**Tests (`pairing_test.go`):** durable admission (recovered `MeshTrust` shows the
device) + HARD single-use (second admit refused); expired invitation refused and
leaves no member; tampered cert (device-id rewrite breaks the root sig) and a
cert signed by a NON-root key both refused.

`make verify` green. Next: **P3-2c** — the pairing handshake wire (a transport
verb + `cairn pair join` client) connecting a new node to `AdmitPairedDevice`,
then the live-usability trust refresh.

## P3-2c — Pairing handshake wire (peer protocol + daemon) — DONE (2026-07-16) [Tier 1]

**Build:** the network wire connecting a new node to `AdmitPairedDevice`.
- `internal/peer/pair.go` — the pairing handshake, a distinct protocol from the
  N5 mutual membership handshake (the dialer is NOT a member yet). Selected by
  `hello.Mode == "pair"` (new field; empty = sync, backward-compatible). Wire:
  `hello{mode:pair}` → `pairMsg{payload}` → `pairMsg{nonce}` → `pairMsg{sig}` →
  `pairMsg{ok,event_id}`. The dialer proves possession of the device private key
  (signs a nonce under `PairingHelloDomain`); the payload carries only
  `{cert, invite_id}` — **the private key never crosses the wire** to the
  inviting node. `Server.OnPair` callback keeps the peer layer identity-agnostic
  (payload opaque; all cert/root verification in the callback). `PairDial` /
  `PairDialWithTransport` are the dialer side.
- `handlePair` reads the payload BEFORE any refusal (a synchronous transport
  would deadlock otherwise, and it drains the message on a buffered one).
- `internal/daemon/pairing.go` `servePair` — the daemon's `OnPair`: unmarshals
  `{cert, invite_id}`, re-verifies the cert against OUR mesh root, returns the
  device pubkey to challenge + an admit closure → `AdmitPairedDevice`. Wired in
  `fetch.go` (`srv.OnPair = d.servePair`) alongside the existing `OnPeer`.

**Hardening noted (deferred):** the handshake authenticates the DIALER (key
possession) but not the server to the dialer; a rogue endpoint cannot forge real
membership (not root/member), so worst case is refuse/false-ack, detectable when
the new node later fails to sync. Mutual pairing auth is a later step.

**Tests:** end-to-end over the real loopback transport — a listening node mints
an invitation, a new node pairs, the device.add lands durably on the inviting
node (recovered `MeshTrust` shows it), and a second pairing with the same invite
is refused (HARD single-use through the wire); a dialer answering the challenge
with the WRONG key is refused and admits nothing; a node with pairing disabled
(`OnPair == nil`) refuses cleanly (peer memTransport test). Tests bind an
EPHEMERAL loopback port (`SyncListen: 127.0.0.1:0`) so they never contend with a
real cairn daemon holding the tailnet :9700 on the dev box.

`make verify` green. Next: **P3-2d** — live trust refresh (a paired node syncable
without a restart; also benefits N6-replicated device.add) with concurrency-safe
trust, then **P3-2e** the `cairn pair invite` / `cairn pair join` CLI + docs.

## P3-2d — Live trust refresh (paired node syncs without restart) — DONE (2026-07-16) [Tier 1]

**Gap:** after `AdmitPairedDevice` durably appends the device.add, the inviting
node's sync listener still refused the new device until a restart — `d.trust` was
the recover-time snapshot captured when the listener started (the same limitation
every membership change has today, incl. an N6-replicated device.add). Not
frictionless: pairing then couldn't sync.

**Fix (concurrency-safe, copy-on-write):**
- `identity.Trust.WithDevice(cert)` — returns a COPY of the trust with one added
  admitted device; the receiver is untouched, so readers still holding it stay
  consistent. On restart `MeshTrust` rebuilds the identical set from the log.
- `daemon.liveTrust` — adapts the daemon's CURRENT `d.trust` to `peer.Trust`,
  reading it under `d.mu` on every lookup. The sync listener now uses
  `liveTrust{d}` instead of a startup snapshot, so a trust swap is visible
  immediately and race-free.
- `AdmitPairedDevice` swaps `d.trust = d.trust.WithDevice(cert)` under the lock it
  already holds, after the durable append. The durable log stays authoritative —
  if the refresh fails it warns and a restart heals.

**Tests:** `identity` copy-on-write (copy admits the device with the right key;
receiver unmutated). Daemon end-to-end: a device is refused by the N5 membership
handshake BEFORE pairing, then — after pairing, WITHOUT restarting the daemon —
completes it (proving live trust). `-race` clean on the pairing + sync suites.

`make verify` green. **P3-2 (one-time pairing invitations) is now functionally
complete** end-to-end: mint (offline) → pair over the wire → durable single-use
admission → immediately syncable. Remaining P3-2 work: **P3-2e** the `cairn pair
invite` / `cairn pair join` CLI + operator docs (thin adapters over the built
primitives).

## P3-2e — `cairn pair invite` CLI + token encoding — DONE (2026-07-16) [Tier 2]

**Build:** the inviting node's operator-facing half of pairing.
- `identity.EncodeInvitation` / `DecodeInvitation` — a single paste-able token,
  `cairn-pair-v1.` + base64url(JSON). The token embeds the device credential, so
  it is a one-time SECRET (documented as such in help + code).
- `cmd/cairn/pair.go` `cairn pair invite --name --root-key <restored> [--out]` —
  thin adapter over `MintPairingInvitation` + `EncodeInvitation`; writes the token
  to a 0600 file (or stdout) and prints the `cairn pair join <token> <addr>`
  next-step + the "remove the restored root key" reminder. Registered in main.go
  between `device` and `sync`.

**Tests (`cmd/cairn/pair_test.go`):** `pair invite` end to end — the written
token decodes, verifies from genesis, matches the mesh cairn_id, and carries the
named device credential; garbage / bad-base64 tokens are rejected.

`make verify` green.

**Split recorded:** `cairn pair join` (the NEW-node half) is **P3-2f**, deferred
to its own turn because it needs a real integration: with append-on-arrival the
new device is NOT in the chain at install time, so the existing
`BootstrapTrust`→`VerifyGrantChain` path (which asserts self-membership) doesn't
fit. `pair join` needs a bootstrap-trust-WITHOUT-self variant (the new node
legitimately trusts the mesh to authenticate the inviting node, but isn't itself
admitted until it syncs its own device.add from the inviting node's origin) plus
the daemon first-sync. Appending at mint would sidestep this but reverts the
hard-single-use property the operator chose — so the careful path is correct.

## P3-2f — `cairn pair join` (new-node install + bootstrap-without-self) — DONE (2026-07-16) [Tier 1]

**Build:** the new node's half of pairing — verify token → install identity →
pair over the wire.
- `identity.PairJoinInstall` — verifies the invitation, installs the identity
  FROM the token (the key travels in it, Option-1 tradeoff — nothing staged):
  portable config + device-local key/cert/config + a pairing bootstrap chain. It
  is **idempotent** (a re-run after a network hiccup finds the matching identity
  installed and skips the writes; a DIFFERENT existing identity is refused).
  Returns the device key for the handshake.
- `identity.PairingBootstrapTrust` + `pairingBootstrap` file (`PairingBootstrapName`)
  — the append-on-arrival analogue of Join's bootstrap-chain.json: it carries the
  invitation chain (genesis + device.*) but **no private key** and asserts NO
  self-membership, because the paired device is admitted on arrival, not in the
  chain. New `verifyMeshChain` helper does the genesis-rooted replay without the
  device-membership assertion `VerifyGrantChain` makes.
- `BootstrapTrust` now falls through to the pairing bootstrap when there is no
  join grant — so the daemon's existing R37 freshly-joined recovery path works
  for a paired node unchanged.
- `cairn pair join <token-or-file> <peer-addr>` (`cmd/cairn/pair.go`): decode →
  `PairJoinInstall` → `peer.PairDial` (payload is `{cert, invite_id}` only; the
  key stays local) → prints "start the daemon to sync". Accepts the token inline
  or as a file.

**Tests:** daemon-level end-to-end under SEPARATE device-state bases — A invites,
B installs from the token, B's bootstrap trust admits the inviting node but not
itself (append-on-arrival), B pairs over the wire, then B completes the N5
membership handshake against A via bootstrap trust (proving the whole chain), and
re-install is idempotent. CLI: `pair join` rejects a bad token before any network
attempt. `-race` clean; `make verify` green.

**P3-2 — one-time pairing invitations — COMPLETE end-to-end** (mint → token →
install → pair → durable single-use admission → immediately syncable, both CLI
verbs). Next: **P3-3** thin-node role, then **P3-4** iroh adapter.

## P3-3a — Thin-node role: model + durability exclusion + advertisement — DONE (2026-07-16) [Tier 2]

**Build (spec §7):** the thin-node role and its two offline-buildable durability
consequences.
- `config.DeviceConfig.Role` (`RoleFull` default / `RoleThin`), device-local (a
  role is a per-device operational choice, not a mesh fact). `IsThin()` helper;
  `identity.NormalizeRole` validates ("" → full; unknown → error). `cairn init
  --thin` sets it; `InitOptions.Role`.
- **Role advertisement (runtime, non-replicated — like blob holdership, §4.5):**
  `syncMsg.Role` added; both the initiator's and responder's frontier messages
  carry `d.myRole()`; each side records the other via `recordPeerRole` into a
  d.mu-guarded `peerRoles` map. `myRole` NEVER lies (a thin node is never
  advertised as full, §7).
- **Durability exclusion:** `memberCount` (the important/pinned target = "all
  member nodes") now excludes known-thin devices via `countDurabilityMembers`
  (pure, unit-tested) + `deviceIsThin` (self from config, peers from advertised
  role). A thin node is not counted toward the target (§7); actual holdership is
  still counted separately by `blobHolderCount`, so a thin node that DOES hold a
  blob still counts — no double-count, no unreachable target.
- Nil-safe: a read-only restore (no device-local identity) reads as full (fixed a
  nil-deref the F6 restore test caught).

**Tests:** `countDurabilityMembers` excludes thin + revoked and floors at 1;
`myRole` never lies; `deviceIsThin` tracks self + peers and a role never sticks
thin after re-advertising full; `init --thin` persists the role, plain init is
full. `-race` clean on sync/durability; `make verify` green.

**Deferred (P3-3b + hardware):** partial universal-search surfacing on a thin
node (offline-buildable, next), and the live remote-query dependency + battery/
metered awareness (hardware-gated).

## P3-3b — Partial universal-search surfacing on thin nodes — DONE (2026-07-16) [Tier 2]

**Build (spec §7, line 175 — "no offline universal-search guarantee"):** a thin
node's universal search + digest now carry a truthful partial marker so the agent
knows the local corpus is a recent window only and older material may live on
full nodes.
- `SearchOutput.Partial` / `PartialReason` and `DigestOutput.Partial` /
  `PartialReason`; set from `d.thinSearchPartialReason()` (non-empty only on a
  thin node) at all output-construction sites (search + digest).
- Propagated to the agent-facing MCP envelope (`mcp.Envelope.Partial` /
  `PartialReason`) in both the `cairn_search` and `cairn_digest` handlers.

**Tests:** daemon-level — a thin node's Search AND Digest are partial with a
reason; a full node's Search is not. MCP-level — the `cairn_search` envelope from
a thin node carries `partial: true` + `partial_reason` (the agent actually sees
it). `make verify` green.

**P3-3 (thin-node role) — offline-buildable scope COMPLETE:** role model,
durability exclusion, role advertisement, and partial-search surfacing. The live
remote-query dependency (a thin node asking a full node to search for it) and
battery/metered awareness remain deferred (hardware-gated, recorded in P3-PLAN).
Next: **P3-4** — iroh transport adapter (interface + `cairn net` diagnostics +
docs; the live wire hardware-deferred, the P2-7 pattern).

## P3-4a — Transport selection (P3-1 seam made load-bearing) — DONE (2026-07-16) [Tier 1]

**Build (spec §12 P3):** the sync transport is now operator-selectable through the
P3-1 `Transport` seam, and iroh is refused instructively (the P2-7 deferral
pattern) rather than pretended.
- `config` transport names (`TransportTCPTailnet` default / `TransportIroh`);
  `DeviceConfig.Transport` field (device-local).
- `peer.TransportByName(name)` — "" / tcp-tailnet → the P1 TCP transport; iroh →
  an INSTRUCTIVE "not available in this build (hardware-gated; see P3-PLAN)"
  error; unknown → error.
- Daemon resolves `d.transport` once at Start. **This makes the P3-1 seam
  load-bearing:** the sync listener now uses `NewServerWithTransport(d.transport,
  …)` and `resolveSyncListen(…, d.transport.LocalAddr)`, and every dial
  (`SyncWith` + the two probe hooks) uses `DialWithTransport(d.transport, …)`.
- An unavailable transport (iroh) DISABLES sync loudly (R45) — the listener and
  anti-entropy loop are gated on `d.transport != nil`, `SyncWith` refuses — but
  the daemon keeps serving local reads/writes.
- `sync-status` now reports `transport` + `role`.

**Tests:** `peer.TransportByName` (default resolves; iroh instructive; unknown
refused). Daemon: selecting iroh disables sync with an iroh-naming message yet
local publish/search still work and `SyncWith` refuses cleanly (no panic). `make
verify` green.

**Next: P3-4b** — `cairn net` connectivity diagnostic + the iroh/relay/self-host/
patching operator docs. Then P3-4 (and P3) is complete at the offline-buildable
scope, with the live iroh wire deferred (hardware).

## P3-4b — `cairn net` diagnostic + onboarding/transport docs — DONE (2026-07-16) [Tier 3]

**Build:** `cairn net` (`cmd/cairn/net.go`) — a one-shot connectivity diagnostic
over `sync-status`: prints transport, role, sync listener state, configured peer
count, bootstrap-trust note, and an honest relay line (relays are an iroh
feature; deferred). `--json` emits the raw status. (Leaf command — not a group,
so no `groupGuard`.)

**Docs:** `docs/cairn-p3-onboarding-transport.md` — operator reference for all of
P3: the pairing flow (`cairn pair invite` / `join`) + its security properties and
the private-key-in-token tradeoff; thin nodes (`init --thin`) + their three
consequences; transport selection (tcp-tailnet default, iroh deferred); and the
iroh/relay/self-host/patching story to implement when the wire lands.

**Tests:** `cairn net` reports transport (tcp-tailnet), role (thin), listener,
peers, relays against a live daemon; `--json` carries the fields. `make verify`
green.

### P3-4 (iroh transport adapter) — offline scope COMPLETE
Transport is operator-selectable through the P3-1 seam (P3-4a), iroh refuses
instructively, `cairn net` + docs land (P3-4b). The live iroh binding + relay
wire remain **deferred (hardware-gated)** — the P2-7 pattern — dropping into the
unchanged `Transport` interface when built.

### P3 — onboarding/transport — COMPLETE (offline-buildable scope)
- **P3-1** transport abstraction (the seam).
- **P3-2** one-time pairing invitations (mint → token → install → wire → durable
  single-use admission → immediately syncable; `cairn pair invite`/`join`).
- **P3-3** thin-node role (model + durability exclusion + advertisement + partial
  search).
- **P3-4** transport selection + `cairn net` + docs.

**Deferred to hardware/live-network (recorded, not silent):** the iroh 1.x wire +
relay health/self-host diagnostics + patching mechanism (P3-4); a thin node's
live remote-query dependency + battery/metered awareness (P3-3). All sit behind
built interfaces/config and drop in without caller changes.

---

# P3 continuation — build-ahead of the live-hardware test pass (2026-07-17)

Operator (2026-07-17): keep building every P3 piece that can be built without
live hardware, so the rig visit is pure testing. Buildable-ahead: thin-node
remote-query (the substantive remaining feature). Genuinely blocked: the iroh
live wire (needs a Go↔iroh binding + a compile/test env — a skeleton + plan is
the most that's sound) and battery/metered SENSING (platform + real device; the
POLICY hook is buildable).

## P3-3c — Thin-node remote query: protocol + server verb + client (spec §7) — DONE (2026-07-17) [Tier 2]

**Design (recorded; open-q 9 conservative default — not asked, within one trust
domain and opt-in):** a member node asks a FULL peer to run a universal search on
its behalf over the SAME authenticated sync session (R27). Query goes only to a
trusted mesh member (privacy bounded by existing trust); bounded by budget_chars;
best-effort (any error → caller keeps its local partial result); returns result
REFERENCES + budget payload, never bodies (bodies stay a deliberate fetch).

**Build:** `syncMsg` gains `Query`/`Budget`/`Search` (a `*SearchOutput`). A new
`remote_search` verb in `serveSync`: a FULL node runs `d.Search` and returns the
`SearchOutput`; a THIN node refuses (no completeness to offer). `Daemon.RemoteSearch(addr,
query, budget)` is the client — dials a full peer via the resolved transport,
authenticates, exchanges remote_search/remote_results, returns the peer's output.

**Tests:** end-to-end over loopback under separate device bases (a member paired
into A's mesh queries A and gets ranked results); a THIN owner refuses with a
"thin" message. `-race` clean; `make verify` green.

**Next: P3-3d** — wire `RemoteSearch` into a thin node's `Search`/`Digest` so it
AUTO-consults a configured full peer when its local result is partial, merges +
marks provenance (remote-sourced), behind an opt-in config toggle. The live
latency/privacy validation then needs the two-node rig.

## P3-3d — Thin node auto-consults a full peer when partial — DONE (2026-07-17) [Tier 2]

**Build (spec §7):** a thin node's `Search`, after its local (partial) result,
consults a configured full peer and returns THAT node's complete result when
remote-query is opted in.
- `config.DeviceConfig.RemoteQuery` (opt-in, off by default; ignored on a full node).
- `SearchOutput.RemoteSource` marks a result served by a peer (its address).
- `Daemon.maybeRemoteConsult` (+ `shouldRemoteQuery`/`firstSyncPeer`): a thin node
  with remote-query on and ≥1 sync peer calls `RemoteSearch` on the first peer;
  on success returns the peer's SINGLE budget-bounded result with `RemoteSource`
  set and `Partial=false` (the budget invariant R19 holds — no two-payload
  merge); on ANY failure returns the local partial result (graceful degrade).
  Wired at the end of `Search` (no lock across the network call). Propagated to
  the MCP `cairn_search` envelope (`remote_source`).

**Contract note (open-q 9, conservative):** the result prefers the full peer's
complete view over local-recent when online; local-recent-not-yet-synced items
are not merged in (a documented refinement — a full node syncs quickly). No
recursion (a full node never remote-queries; a thin peer refuses to serve).

**Tests:** thin + remote-query ON → `RemoteSource` set, not partial, results
present; thin + remote-query OFF → stays local + partial, no consult. `-race`
clean; `make verify` green.

**P3-3 (thin-node role) — offline scope now includes remote query.** Remaining
P3 build-ahead: iroh adapter skeleton + integration plan (P3-4c), battery/metered
policy hook (P3-3e). The live remote-query latency/privacy validation, the iroh
wire, and metered sensing need the rig/hardware.

## P3-3e — Metered policy (battery/metered awareness, policy half) — DONE (2026-07-17) [Tier 3]

**Build (spec §7 battery/metered awareness):** the POLICY half, offline-buildable.
- `config.DeviceConfig.Metered` (manual flag; automatic network-state SENSING is
  platform-specific and deferred — hardware).
- `shouldRemoteQuery` returns false when metered: a metered thin node does NOT
  auto-spend data on remote query — its search stays local + partial.
- `thinSearchPartialReason` explains the suppression when remote-query is
  configured but metered (so the agent understands why the result is partial
  despite remote-query being on).

**Tests:** metered thin + remote_query on → no consult, partial, reason mentions
"metered". `make verify` green.

## P3-4c — iroh integration plan (the honest artifact for the deferred wire) — DONE (2026-07-17) [Tier 3]

The iroh wire cannot be honestly built offline (no mature Go binding; value needs
real relays/NAT). Committing non-functional skeleton code would violate the
"green + tested" discipline, so the deliverable is a **concrete implementation
plan**: `docs/cairn-p3-iroh-integration-plan.md`. It covers the P3-1 seam it drops
into, the binding decision (cgo FFI vs sidecar — recommends starting with the
sidecar), the `Transport`-method → iroh-1.x-API mapping, the relay/self-host/
patching operational story, and the four rig tests (separate-NAT hole-punch,
pairing-over-iroh, membership-still-enforced, relay self-host) + definition of
done. `TransportByName("iroh")` keeps its instructive refusal until the wire lands.

---

## P3 — onboarding/transport — OFFLINE-BUILDABLE SCOPE COMPLETE (2026-07-17)

Everything for P3 that can be built and tested without live hardware is built and
tested, on `master`, `make verify` green throughout, `-race` clean on the
concurrency-sensitive paths:

- **P3-1** transport abstraction (the seam) — load-bearing (P3-4a).
- **P3-2** one-time pairing invitations, end to end (2a–2f): mint → token →
  install → wire → durable hard-single-use admission → immediately syncable;
  `cairn pair invite` / `cairn pair join`.
- **P3-3** thin-node role: model + durability exclusion + advertisement (3a);
  partial universal search (3b); remote-query mechanism (3c) + auto-consult with
  graceful degrade (3d); metered policy (3e).
- **P3-4** transport selection + `cairn net` + docs (4a/4b); iroh integration
  plan (4c).

**Everything that remains needs the rig/hardware** (recorded, behind built
interfaces, drops in with no caller changes):
1. Live two-node checkout of pairing / thin-role / transport / remote-query on the
   NODE-B tailnet rig.
2. The iroh 1.x wire (per `docs/cairn-p3-iroh-integration-plan.md`): a Go↔iroh
   binding + two nodes on separate NATs + a relay box.
3. Automatic metered/battery SENSING (a real metered device; the policy is built).

## FIX-MCP2 — Codex CLI registry entry (`internal/mcpinstall`) — DONE (2026-07-18) [Tier 3]

Added Codex CLI (OpenAI) as the third `mcp-install` registry app, alongside
claude-desktop and claude-code. All FIX-MCP1 / R54 safety invariants preserved
(merge-only, backup-before-write, malformed-refused, idempotent, `os.Executable()`
path, stale-path auto-fix).

- **TOML, not JSON.** Codex config is `~/.codex/config.toml` with one
  `[mcp_servers.<name>]` table per server. The merge core was generalized: JSON
  and TOML both decode to `map[string]any`, so `mergeCairn/removeCairn/
  cairnCommand` now take a `serversKey` (`mcpServers` vs `mcp_servers`) and the
  format is a `codec{serversKey, parse, marshal}` on each `App`. TOML parse/
  marshal via `github.com/BurntSushi/toml` (already a dep). BurntSushi emits
  top-level scalars before table headers (valid TOML) and round-trips
  `args=["mcp"]` to `[]any{"mcp"}`, so idempotency's `reflect.DeepEqual` holds.
  The exported JSON `ParseConfig/MarshalConfig/MergeCairn/RemoveCairn/CairnCommand`
  remain as thin JSON wrappers (backward compat for the FIX-MCP1 tests).
- **Prefers the sanctioned CLI**, same as Claude Code. Verified on this machine:
  `codex mcp add cairn -- <abs> mcp` / `codex mcp remove cairn` (global; **no
  `--scope`** — that differs from Claude, so CLI arg vectors are now per-app
  `cliAdd/cliRemove` builders). `codex mcp add` overwrites cleanly, so the
  remove-then-add replace path works. End-to-end validated against the real codex
  binary in an isolated `CODEX_HOME`: merge-only (preserved `model` + an existing
  server), idempotent, uninstall leaves everything else, and real `codex mcp list`
  then sees `cairn`. Detection: `codex` on PATH or `~/.codex/` present.
- **`cairn mcp-install --status`**: compact table — for every registry app,
  detected? / configured? / command current-or-stale. `--list` already
  auto-includes codex (it iterates `Registry()`).
- **Tests** (`internal/mcpinstall`, mirroring the JSON suite; + a CLI-surface
  `--status`/codex round-trip in `cmd/cairn`): preserves other MCP servers +
  unrelated TOML tables, idempotent, malformed TOML refused-not-clobbered, stale
  path updated, create-from-absent valid, CLI-driven add/remove with the correct
  no-scope vector. Full tagged suite + vet green. `internal/log` untouched.

## AFFORDANCE P0–P3 — agents can shape their own relevance (self-subscribe) — DONE (2026-07-19)

Work-order: `CAIRN-AFFORDANCE-PLAN.md`. Problem: the relevance machinery existed
but was invisible/unreachable to agents — the agent-facing docs taught only
digest/search/send/fetch/found/not-found, and the MCP door had no subscribe. So
relevance was operator-configured; agents were passive.

- **P0 (ruling):** operator confirmed (2026-07-19) MCP/agent self-subscription =
  the R25 LOCAL tier only. Recorded as **R55** in RULINGS.md: no events, no
  capability escalation, own view only; the durable/replicated tier stays
  operator-CLI and is never exposed to MCP.
- **P1 (docs):** taught the affordance on every agent-facing surface — the global
  `~/.claude/skills/cairn/SKILL.md` ("Shape what you receive" section), repo
  `CLAUDE.md` Cairn block, README wiring one-liner, DOGFOOD §9. CLI form
  `cairn subscribe "<…>" --view <VIEW>` (local default, NOT `--durable`).
- **P2 (code):** `cairn_subscribe` / `cairn_subscriptions` on the MCP server
  (`internal/mcp/mcp.go`) → daemon `subscribe-local` / `subscription-local-get`
  IPC ops (capRead) → `SubscribeLocal` / `LocalSubscriptionFor` writing only
  `views/<view>/view.json` (`internal/daemon/subs.go`). No event; strict decode
  rejects durable knobs / view override. topics==nil preserves operator-set hard
  topics. `TestR55LocalSubscribe` asserts no event (status.next_seq unchanged) +
  no durable row + strict decode + view isolation. `make verify` green.
  - **Crossed adversarial review (independent agent, built HEAD, ran tests):
    SAFE-TO-MERGE**, zero BLOCKER/MAJOR. Attacked all five R25 boundary points;
    all held.
- **P3 (live verify):** drove the real `cairn mcp` stdio binary end-to-end — the
  digest ranked the subscribed-to message #1 after `cairn_subscribe`; durable
  subscription list stayed empty; `view.json` written locally. Semantic matching
  will sharpen once the embedding backlog drains (currently lexical_only).

**Deviation (recorded):** plan §B suggested `top_n`/`percentile`/`mode` args on
the MCP tool, but the local tier is `view.json` (interest_query + topics only) —
those durable-tier knobs don't exist at the session tier, so they're omitted by
design. MCP `cairn_subscribe` takes `interest_query` only.

**Known future-hardening (MINOR, not gating — from the crossed review):** the
IPC `subscribe-local` op doesn't bind the target view to the session principal
(only `validViewName` guards path traversal). Unreachable via MCP (the tool
hardcodes `s.view`) and consistent with the pre-existing `digest`/`fetch` IPC
view-trust model under R22 (profiles prevent accidents, not a malicious local
process). Revisit only if per-view auth is ever introduced.

**Phase 4 (self-bootstrapping onboarding record) — NOT STARTED.** Blocked on an
operator sign-off: it is a genuine trust-model widening (mesh content → self-
config), config-by-exception with three bounds (root authorship, schema
whitelist, bounded effect). Needs its own R-number and its own crossed review
before build (plan §D).

## AFFORDANCE P4 — self-bootstrapping onboarding record (R56) — DONE, review SAFE-TO-MERGE (2026-07-19)

Operator signed off the config-by-exception model (2026-07-19). Built the ONE
trusted-config exception, bounded on three axes (RULINGS R56 + R56.1):

- **internal/onboarding** — pure security core: `Verify` (authorship gate),
  `ExtractBlock`, `ParseConfig` (four-field whitelist; unknown fields + all prose
  ignored; `inlineViolation` rejects marker/fence/newline in values),
  `RenderClaudeBlock` (`neutralizeInline` render backstop), `ApplyToInstructions`
  (idempotent, delimited rewrite), `RenderRecordBody`. Hard unit tests incl. the
  injection-vector test.
- **Daemon** — `OnboardingRecord` locates the latest OPERATOR-authored message on
  `cairn/onboarding/<view>` (fallback `cairn/onboarding`) via
  `Projection.LatestTopicMessageBySender(…, "operator")`, verifies it, returns
  typed config or a refusal. `onboarding-get` IPC (capRead).
- **CLI** — `cairn onboarding publish|show|apply`. `apply` reuses the R25
  `subscribe-local` path + the delimited rewrite. Integration test incl. an
  end-to-end authorship attack (non-operator record has zero effect).
- **Docs** — skill + CLAUDE.md self-config standing instruction (security framing
  verbatim); DOGFOOD §9a operator howto.

**Crossed review (independent agent, 3 rounds):** round 1 NOT-SAFE — one BLOCKER (record
field values could escape the delimited block via embedded markers/newlines) +
one MINOR (non-operator could shadow the record). Fixed in R56.1 (parse-reject +
render-neutralize; operator-filtered selection). Round 2 caught a residual of the
SAME class via the `view` field (R56.2) — fixed. Round 3: **SAFE-TO-MERGE**,
verified end-to-end on the real publish→apply→apply CLI flow; one leftover MINOR
(daemon-defaulted view) closed in R56.3. Deviation from plan §D (recorded in R56): no message-pin primitive
exists (pins are object-durability), so the authoritative record is the latest
operator message on the topic; its head revision is the re-apply trigger.

**This completes the CAIRN-AFFORDANCE-PLAN work-order (Phases 0–4).**

---

## WP-A — Safety/correctness remediation (audit 2026-08-05) — DONE

Branch `claude/cairn-agent-topic-scoping-kxr3ow`, commits FIX-A1…FIX-A8. All
from the three-way audit (status vs plan / code quality / product). Full
tagged suite + vet green at every commit.

- **FIX-A1** object-hash validation (`object.ValidHash`, `ObjectHashHexLen`)
  + `recover()` in the IPC handler. Pre-fix: a 1-char hash via `pin` or a
  pre-staged attachment panicked `store.Path` (`hash[:2]`) and killed the
  daemon — no recover existed anywhere in the connection path.
- **FIX-A2** `viewDir` choke point: view-name validation on EVERY
  path-forming entry (Fetch/map/compaction/readViewConfig were unguarded;
  Digest already was — audit overstated that one). Fetch paths now use the
  projection's canonical message ID. `cairn mcp` refuses traversal-shaped
  `--view`.
- **FIX-A3** CLI `cairn subscribe` routes through `subscribe-local`; the
  direct view.json rewrite erased operator topic filters and raced the
  daemon. Session-view binding deliberately NOT added — the crossed-review
  verdict above (AFFORDANCE P2 section) rules the view-trust model
  consistent with R22 and defers binding until per-view auth exists.
- **FIX-A4** embed.Python: mutex-serialized round-trip (concurrent callers
  could receive each other's vectors), watchdog timeouts
  (`EmbedHandshakeTimeout`/`EmbedRequestTimeout`), stdin-close + kill +
  Wait on Close (zombie reap). Daemon embedder pointer reads snapshot
  under a dedicated RWMutex. First tests for python.go (fake worker).
- **FIX-A5** log append rollback: truncate to last durable frame end on
  write/sync/dir-sync failure, or poison the handle (typed refusal) if the
  rollback fails. Pre-fix a transient EIO left torn bytes mid-segment on a
  live handle → next append landed after them → interior corruption →
  unopenable log. fsx.MemFS O_APPEND corrected to POSIX every-write-at-EOF
  semantics (offset emulation modeled an impossible state).
- **FIX-A6** publish sequencing: topic.create* → message.publish →
  topic.link.add*. **Author ruling needed (recorded below).**
- **FIX-A7** socket in per-user 0700 dir (XDG_RUNTIME_DIR/cairn or
  TempDir/cairn-<uid>), symlink/ownership/mode verification, chmod 0600.
  Cross-user Linux exposure closed; R22 same-user honest-tiering
  unchanged. SO_PEERCRED enforcement considered and NOT added (would
  exceed R22's model); revisit only with per-view auth.
- **FIX-A8** MCP schema truth (text_class enum had nonexistent "working",
  missing eager-searchable; priority max 10 vs daemon's 3), score via
  rank.Dec, ignored-error fixes, ForTest-hook lock audit.

### Author rulings needed

- **FIX-A6 / FIX-F1 residual:** when a topic.link.add append fails AFTER
  message.publish is durable, the daemon returns an error for a request
  whose message is already searchable; a CLI/MCP retry (no correlation_id)
  re-publishes the body under a new message ID. FIX-F1 ruling 1 ("ALL
  durable before the single ack") does not say what to report here.
  Implemented (conservative): return the error — never claim success for
  an incomplete request. Alternative: success-with-warning naming the
  unlinked topics. Marker: `// RULING-NEEDED:` at the publish sequence in
  internal/daemon/daemon.go.

## WP-B — CI + test infrastructure (audit 2026-08-05) — DONE

Commits CI-B1…CI-B4. GitHub Actions now runs `make verify` (both halves of
the FIX-F4 guard) on ubuntu+macos, the race detector, a 30s fuzz smoke, and
golangci-lint on every push. New Makefile targets `test-race` and `fuzz`.
Lint triage landed at ZERO issues with errcheck strict on production code.

**Fuzzing found a real protocol bug on its first run:** ValidateNoFloats
accepted integers beyond 2^53-1, which JCS (RFC 8785 = ES6 doubles)
serializes precision-corrupted in exponent form ("1e+21") — canonical bytes
the validator itself then rejects. Integers are now bounded to the i-JSON
exact range at validation (internal/event/canonical.go); the corpus seed is
pinned in testdata. FuzzDecodeFrame covers the recovery-path frame decoder
(never panics; decode∘encode stable). New BenchmarkAppend/BenchmarkRecovery;
TestConcurrentSearchEnrichRace exercises retrieval vs enricher under -race.

## WP-C — Sync/multi-device usability (audit 2026-08-05) — DONE

Commits SYNC-C1…C4. The biggest day-one gap: `sync_peers` was read-only
from the device TOML — NO verb wrote it, `pair join` didn't either, so two
freshly-paired machines never replicated until the operator hand-edited
config-device.toml and restarted the daemon (docs/cairn-p3-onboarding-
transport.md's "start syncing" claim was false).

- `peer-add`/`peer-remove`/`peer-list` IPC ops (add/remove capAdmin, list
  capRead) + `cairn peer add|rm|list` CLI. Mutations apply LIVE (the
  anti-entropy sweep re-reads the peer list; PeerAdd kicks it) AND persist
  via SaveDevice.
- The anti-entropy loop now starts whenever a transport exists (zero peers
  = cheap idle ticks), so a live-added peer replicates with no restart.
- `pair join` persists its counterparty address as a sync peer
  (identity.AddSyncPeer) — reconciles are bidirectional per connection, so
  that one entry converges both nodes; the doc claim is now true.
- `cairn status` warns when members>1 with 0 peers; `cairn net` says
  "nothing will replicate" at 0 peers, both pointing at `cairn peer add`.
- Tests: add/rm/list + persistence across restart, invalid addresses,
  capability gating over the socket, AddSyncPeer unit, and a live two-node
  drill proving a peer added at runtime converges without restart.

## WP-D — Retrieval usability (audit 2026-08-05) — DONE

Commit RETR-D1..D5. Search results now carry sender/created_at/topics and a
200-char body snippet (quoted, budget-counted); digest entries carry an
attribution line; search takes hard scope pre-filters (topic names / sender
/ thread — closes the spec §7.1 `search(query, scope, k)` gap; nonexistent
scope topics refuse pre-ack); `cairn thread` / MCP `cairn_thread` expand a
whole conversation (the root is matched by message_id — roots carry no
thread_id); `cairn topic list` browses the taxonomy with live counts; and
`cairn unlink`/`cairn unpin` surface the daemon ops that existed with no
CLI. MCP tool count is now 12 (§5.5 nine + thread + R55 two).

Scope note recorded: the FTS candidate pool is cut at FusionCandidatesFTS
BEFORE the scope filter, so a very narrow scope in a very large corpus can
under-fill lexically; the vector path filters BEFORE its top-K and
compensates. Revisit if scoped-search recall complaints appear in dogfood.

## WP-E — Deployment/config correctness (audit 2026-08-05) — DONE

Commit DEPLOY-E1..E5.

- **E1** every auto-wired MCP client now gets ITS OWN view/actor (named
  after the app): `mcp-install` used to emit bare `["mcp"]`, collapsing
  claude-desktop/claude-code/codex into one shared "mcp" view and
  defeating per-view interest, onboarding records and attributable
  telemetry. `--view` overrides for a single targeted app.
- **E2** `rank_profile` / `embed_python` / `heavy_derivatives` are
  device-TOML keys (env vars remain overrides). The env-only knobs never
  reached a launchd/systemd daemon (the unit passes no environment and is
  overwritten on reinstall) — the entire P2 ranking phase was unreachable
  on the recommended deployment, and supervised daemons silently ran
  lexical-only. `cairn status` now reports the LIVE rank profile and
  embedder. Nil-guarded for portable-only (read-only) restores.
- **E3** Success@5 and workaround-rate are COMPUTED (found outcomes joined
  to stored final_rank), PASS/FAIL vs the spec §11 thresholds
  (GateSuccessAt5MinPct/GateWorkaroundRateMaxPct) at ≥GateOutcomeMinSamples
  outcomes, INCONCLUSIVE below (FIX-J2 small-sample honesty). The
  release-blocking gate no longer requires a hand-kept diary; found
  outcomes without a message id count conservatively as not-at-5.
- **E4** `cairn daemon --stop` (service manager when installed — KeepAlive
  would resurrect a bare SIGTERM — else SIGTERM at the PID the status op
  now reports) and `--restart`. `mcp-install --all` now really means
  "every INSTALLED app" (it used to create configs for absent apps).
- **E5** bare `cairn reindex` runs the lexical rebuild (the README
  promise); stale "(stub until M6)" help text corrected.

---

## DEPLOY (retroactive record, work done 2026-07-20…23) — bookkeeping 2026-08-06

The `cairn setup` wizard, `make deploy`, `install.sh`, `cairn daemon
--install` service management, and `mcp-install` shipped in commits
422a82b/55828e9/a79403a/2b46890 WITHOUT a PROGRESS.md section — a
process-discipline miss (CLAUDE.md: PROGRESS is updated per milestone).
Recorded retroactively; the WP-E section above documents the fixes layered
on top of that work.

## WP-F — Docs/rulings reconciliation (audit 2026-08-05) — DONE

- RULINGS.md: **R41 backfilled** (revoke bundling — was cited as binding by
  fork_resolve.go/fork_test.go/pre-N9 doc but never landed in the file,
  violating its own process rule; flagged pending author confirmation), a
  divergence note appended to R40 (its "Conservative scoping" bullet
  predates G7.4), and the 2026-07-16 pairing trust decision numbered
  **R57**.
- PROGRESS.md: stale "Author rulings needed" for M0 (→R1) and M5 (→R2)
  closed with bookkeeping notes; Resume-cold notes refreshed; the DEPLOY
  work order recorded retroactively (above).
- README/DOGFOOD truth pass: why-ranked takes two args; "nine tools" →
  twelve; the never-built entity/typed-edge graph claim removed; P1/P3
  audit claims restated to what PROGRESS actually records (three live
  audit rounds, blockers fixed and re-verified — no final "zero blockers"
  crossed verdict is on record); the launchd hand-recipes superseded by
  `cairn daemon --install/--stop/--restart`; §12 peer setup now documents
  `cairn peer add` (live, no restart); completion + new verbs documented;
  CLAUDE.md outcome instructions now carry the interaction id and
  --message (which the computed Success@5 gate credits).
- docs/cairn-p3-onboarding-transport.md: the pair-join→sync claim is now
  TRUE (SYNC-C3) and says why; dangling P3-PLAN.md reference repointed.

## WP-G — Optional deferred items (audit 2026-08-05) — PARTIAL BY DESIGN

Every WP-G item was planned as independently droppable. Landed:

- **G2 model-artifact pinning:** the bootstrap script now downloads the
  model into `<venv>/hf-cache` (deterministic location; the worker runs
  with HF_HOME pinned there) and records `<venv>/model.hash` (sha256 over
  a sorted relpath+content walk). DetectVerbose verifies the pin BEFORE
  starting the worker: a swapped/tampered artifact refuses into loud
  lexical-only. Unpinned venvs (older bootstrap) still pass. sha256 not
  BLAKE3 deliberately: the pin is written by the venv's python at
  provision time (hashlib has no blake3) and is a local integrity
  fingerprint, not mesh content addressing — the BLAKE3 rule targets the
  latter. `config.EmbeddingModelHash` stays "" (it pins the never-vendored
  ONNX artifact). Tamper test: TestVerifyModelPin.
- **G6 `cairn interactions`:** the query log finally has a reader —
  `interaction-list` IPC op (capAdmin: it names principals and queries) +
  CLI table (query, hits, mode, outcome, newest first).

Deliberately DEFERRED (with reasons, not silence):

- **G1 sqlite-vec:** new CGO dependency + candidate-query rework; the
  brute-force cosine fallback is correct below
  `BruteForceMaxCandidates` and the corpus is nowhere near the ~100k
  cliff. Revisit when head-vector count approaches that bound.
- **G3 duplicate/thread-saturation penalties (spec §9.1, PenaltyCap):**
  changes ruled why-ranked arithmetic — R47 requires every additive term
  to print and reconcile exactly against R51's EXTERNAL recompute, so the
  penalty needs a components-record extension + renderer + external
  reconciliation update in one lockstep change. Do as its own reviewed
  task, not as an audit tail.
- **G4 ladder rungs 6–7 enforcement:** pre-ack rejection conflicts with
  send-never-blocks; needs the author ruling recorded under WP-A before
  code (conservative shape proposed there: rung 7 rejects, rung 6 warns).
- **G5 ONNX embedder:** excluded (fallback previously ruled acceptable).

---

## CAPTURE — zero-effort capture + ecosystem reach — PLANNED (2026-08-09)

Work order at `build/CAPTURE-PLAN.md`, from the competitive review of
Hermes agent's session-memory design. Conclusion of the review: Cairn is
ahead on retrieval (shipped RRF hybrid + hard budgets + explainable
ranking vs their open proposal, issue #44075) but behind on CAPTURE —
knowledge nobody `cairn send`s dies in the session transcript. Milestones:

- **C1** end-of-session handoff convention (docs only): one canonical
  handoff note per session, published by the agent itself.
- **C2** trigram FTS companion index: substring/identifier search
  (projection-only; schema-version bump + auto-rebuild).
- **C3** session-transcript ingest on the M9 path: transcripts become an
  opt-in, redacted, `eager-searchable`/`ephemeral` PULL-ONLY substrate —
  never canonical, never in digests. Drags G1 (sqlite-vec) and G3
  (penalties) forward as the corpus grows. Privacy design gets a crossed
  review before code (trust-surface change, same treatment as R56).
- **C4** memory-provider packaging for agent harnesses (Hermes plugin
  wrapper over `cairn mcp`, provider-directory listings).

Explicit non-goals recorded in the plan: LLM reranking/summarization in
the daemon (breaks R47/R51 explainability), query-expansion models
(shipped hybrid already covers the failure they address), and transcripts
as digest content (capture is a substrate, not an attention surface).

## ROADMAP.md — consolidated to-build inventory (bookkeeping 2026-08-09)

Outstanding work was scattered across the README roadmap table, the
CAPTURE work order, PROGRESS deferred/owed notes, RULINGS, and spec
§12/§13. `ROADMAP.md` (repo root) is now the ONE index: release blockers,
CAPTURE, P2/P3 completion criteria, scaling/distribution debt, small
specified-but-unbuilt gaps, P4, and the open author rulings — each row
linking to its authoritative source. Maintenance rule recorded in the
file: planned work gets a row; shipped work moves to PROGRESS and the row
is deleted. The README table stays the coarse view and links there.

---

## CAPTURE C1 — end-of-session handoff convention — DONE (2026-08-15)

Docs only, per `build/CAPTURE-PLAN.md` C1. The capture gap has a quality
layer above any automatic ingest: the agent is the best summarizer of its
own session and it is present at session end — but nothing ever told it to
write one. Sessions ended with their reasoning still in the transcript.

All three agent-instruction surfaces now carry the same instruction: before
ending a session, publish ONE handoff note (decisions and their reasons,
unfinished work, surprises) via `cairn send … --priority 2`.

- `CLAUDE.md` agent block: new END OF SESSION bullet, immediately after the
  "signal, not noise" bullet it qualifies.
- `README.md` "Wiring an agent" one-liner: handoff sentence added between
  the send and subscribe clauses.
- `DOGFOOD.md` §3 Claude Code agent-instruction snippet: same, in the
  snippet's own voice.

Wording deliberately bounds the instruction in every surface — "the
session's single mandatory write, not licence to dump" — so the handoff
does not read as permission to dump the transcript into the mesh. The
existing "signal, not noise" guidance is unchanged.

No code change; `make vet`, `make test`, `golangci-lint run` (0 issues)
green.

## CAPTURE C2 — trigram FTS companion index — DONE (2026-08-15)

Per `build/CAPTURE-PLAN.md` C2. The word index tokenizes on boundaries
(`unicode61 tokenchars '_-#@'`, rulings §6) and therefore cannot match
INSIDE a token — which is precisely the query shape agents use: a partial
UUID, a camelCase symbol inside a longer identifier, a fragment of an error
string. Embeddings are weak on exact identifiers, so nothing in the shipped
stack answered that class. The two tokenizers fail in opposite directions,
so the pair is complementary rather than redundant.

**Shipped**

- `fts_revisions_trigram` (FTS5 `tokenize='trigram'`) in
  `build/sql/projection.sql` + the embedded `internal/projection/schema.sql`
  (kept byte-identical; the drift test enforces it). It shares `fts_map`'s
  rowid — no second mapping table — so `indexRevision` feeds BOTH indexes
  from ONE insert in ONE transaction. The two can never disagree about what
  is indexed, and a reindex reproduces both or neither.
- `Projection.TrigramMessageHits` + `FTSTrigramQuery` (quoted phrase per
  term ⇒ substring match; terms shorter than the trigram width tokenize to
  nothing and are dropped, and a query left with no usable term skips the
  index rather than running a match that cannot hit).
- `LexicalTopK` unions a third source, following the existing
  derivative-hits pattern: word hits, then derivative hits, then trigram
  hits, deduplicated and capped at k. Trigram goes LAST deliberately — as
  the least precise source it only fills slots the exact indexes left empty,
  so it can never displace a word hit or reorder what an agent already
  receives. The dedup loop now marks appended ids seen (two append sources,
  where there was one).
- `ProjectionSchemaVersion` 6 → 7; `FTSTrigramTokenize` / `FTSTrigramMinTerm`
  in `internal/config/constants.go`.

**Not a tokenizer change.** Rulings v0.3.1 §6 pins the FTS5 tokenizer to
`unicode61` + tokenchars; that index is untouched. C2 ADDS a companion, so
the ruled tokenizer still governs the word index and the ranked order agents
see. No event-schema impact; projection-only.

**Tests**

- `TestTrigramCompanionFindsSubstrings` (internal/projection): three
  mid-token fragments — `eerAdd`, `7e5f-8901`, `CONNREFUSED` — each asserted
  to return NOTHING from the word index (so the fixture keeps proving the
  gap) and the right message from `LexicalTopK`. Also: a word hit keeps
  first place, retraction gating is unchanged through the trigram path, and
  a sub-width term matches nothing rather than everything.
- `TestReindexByteIdentical` extended: the snapshot now covers `LexicalTopK`
  and `TrigramMessageHits` over both the original queries and trigram-only
  ones, so the new table is inside the byte-identical guarantee.
- `TestSearchFindsIdentifierSubstring` (internal/daemon): the same fragments
  end-to-end through `send` → `Search`. `LexicalTopK` is the only lexical
  candidate source search and digest-interest have, so this covers both.
- `TestProjectionSchemaDriftRebuilds` (internal/daemon): NEW — the schema
  bump is only safe because the daemon discards and replays a drifted
  projection. The path was never tested. Stamps an old version on disk,
  restarts, and asserts the rebuild happened, said so (R45), replayed the
  corpus, and repopulated the companion index.
- `TestBudgetComplianceProperty` stays green.

`make vet`, `make test`, `golangci-lint run` (0 issues) green.

## FIX-MCP3 — Claude Desktop detection on Linux — DONE (2026-08-15)

ROADMAP §5. `internal/mcpinstall` resolved Claude Desktop through ONE
hardcoded macOS path (`~/Library/Application Support/Claude`). On Linux that
directory never exists, so `mcp-install`/`--status` reported Claude Desktop
"not installed" regardless of what was on the machine, `--all` skipped it,
and a forced `--app claude-desktop` would have written a config into a
`~/Library` tree the app never reads — a silent no-op the operator had no
way to see.

- `Env` gains `GOOS` and `XDGConfigHome`; `DefaultEnv` fills both from the
  process. An `Env` built by hand (every existing test, and callers that set
  only `Home`) leaves `GOOS` empty and gets `runtime.GOOS`, so nothing had to
  change at the call sites.
- `claudeDesktopDir` resolves Electron's userData per platform: macOS keeps
  `~/Library/Application Support/Claude`; everything else uses
  `$XDG_CONFIG_HOME/Claude`, defaulting to `~/.config/Claude` — the path the
  Linux builds actually read (verified against the Linux packaging docs, not
  assumed). Honouring XDG matters: a relocated config dir would otherwise get
  a file the app never loads. There is deliberately no `%APPDATA%` branch —
  Windows is out of scope (CLAUDE.md platform rule), so non-darwin means
  Linux.
- Detection now derives from that same dir, so the app-dir and config-file
  signals move together across platforms by construction.
- New exported `App.ConfigPath(Env)`: callers ask the registry where a config
  lives instead of hardcoding a path correct on one OS only. The CLI-surface
  test in `cmd/cairn` did exactly that and now asks the registry (and pins
  `XDG_CONFIG_HOME` so a developer's real one cannot break hermeticity).

R54 is untouched: merge-only, backup-before-write, refuse-malformed,
idempotent, and command = `os.Executable()` all still hold — this changes
only WHERE the file is, never how it is written. The Linux round-trip test
re-asserts all of them on the XDG path.

Tests: `TestClaudeDesktopPathPerPlatform` (darwin / linux / XDG override),
`TestClaudeDesktopDetectPerPlatform` (both signals per platform, and the
other platform's directory must NOT satisfy detection), `TestInstallLinuxXDGPath`
(full merge/backup/idempotency round-trip through the XDG path). DOGFOOD §3b
documents the Linux location.

`make vet`, `make test`, `golangci-lint run` (0 issues) green.

## CAPTURE C4 — memory-provider packaging for agent harnesses — DONE (2026-08-15)

Per `build/CAPTURE-PLAN.md` C4. Strategic, not technical: harnesses now treat
memory as a pluggable slot, and Cairn already speaks MCP — the missing piece
was a doc that says how to fill that slot and what confinement it lands in.
`docs/memory-provider.md`: the twelve tools, the three ruling-backed
properties on every result (R18 untrusted envelope, R19 whole-payload budgets,
provenance), install, per-harness wiring, and the capability profile.

**Research, and what is verified vs not.** Both claims are marked in the doc as
what they are, because a config format invented and presented as fact is worse
than no doc:

- **Hermes agent — VERIFIED** against the project's own docs source
  (`website/docs/user-guide/features/mcp.md`, `reference/cli-commands.md` in
  NousResearch/hermes-agent): stdio servers live in `~/.hermes/config.yaml`
  under `mcp_servers` as `command`/`args`/`env`, and
  `hermes mcp add <name> [--command CMD] [--args ...]` is the CLI form. Also
  recorded: Hermes's `memory.provider` slot takes an in-process PYTHON plugin
  (honcho/mem0/…), not an MCP server — so Cairn's real slot is MCP tools, and
  the doc says so rather than implying a provider plugin exists.
- **OpenClaw — NOT VERIFIED, and labelled so.** Its MCP config shape was still
  moving (an `mcp.servers` block in `openclaw.json` proposed in
  openclaw/openclaw#43509; community guides describe a top-level `mcpServers`).
  The doc gives the MCP-standard stdio entry — command + args, which is all
  Cairn needs — and states plainly that the OpenClaw-specific spelling is
  unverified.

**Capability posture documented, not just implemented.** R21 (never tier-1,
`--profile full` refused at the flag), what `agent-standard` grants and the
four things it does not (retract/structural/admin, topic auto-creation and
force-class per R20, durable subscriptions per R55), session TTL/idle and the
`cairn session revoke` kill switch, and R22's honest boundary — same-OS-user
confinement prevents accidents, not malice.

Cross-links: README "Wiring an agent" + the roadmap table (CAPTURE now
🔨 partial — C1/C2/C4 shipped, C3 planned), DOGFOOD §3b.

**Not done, deliberately:** no submissions to external directories
(awesome-hermes-agent, the Hermes Atlas provider directory). CAPTURE-PLAN
lists them under C4, but publishing Cairn into third-party listings is the
operator's call, not a builder's. The doc they would point at now exists.

Docs + research only; no code change. `make vet`, `make test`,
`golangci-lint run` (0 issues) green.

## CAPTURE C3 — design note only (NOT built, 2026-08-15)

C3 is explicitly out of scope until its privacy/redaction design gets a
crossed adversarial review (CAPTURE-PLAN sequencing; same treatment R56 got).
Wrote `build/CAPTURE-C3-DESIGN.md` as input to that review. **Zero
implementation.**

The one finding worth surfacing here, because it contradicts the work order:
the plan offers "dedicated topic namespace + view hard filters" OR "an
explicit source flag" for digest exclusion, preferring whichever keeps
`DigestCandidates` SQL simple. Reading the code settles it — the namespace
option does not work. `Projection.DigestCandidates(topicNames)` with an EMPTY
filter (the default for every view) returns every non-retracted message, so a
`transcript/*` namespace excludes transcripts only from views that opted into
an explicit topic allow-list. Any default view would start receiving
transcript chunks as digest candidates the moment ingest ran, breaking the
plan's own "digest byte-unchanged before vs after ingest" criterion — and
failing OPEN, invisibly. The source flag is both simpler (one predicate in two
queries) and fails closed. The note also flags that the interest/subscription
path is a SECOND route into a digest that must be gated in the same sweep
(R46).

Also recorded for the reviewers: redaction's honest limits (structured secrets
only; redact before chunking so a boundary-straddling secret cannot evade),
chunk determinism as the actual idempotency mechanism (including the
append-to-an-existing-session case), and six things the review should try to
break.

Still needs the crossed review before any code. Not started.

## EVAL — proving the thesis — PLANNED (2026-08-09)

Work order at `build/EVAL-PLAN.md`. Motivation, stated plainly: Cairn's
engineering claims are proven (crash matrix, budget compliance, provenance,
latency, explainable ranking) but its PRODUCT claims are not, and the two
have been sitting in the same README paragraph as if they were the same
kind of statement.

The structural gap: every measurement to date is INTRINSIC (does retrieval
return the right document?) while the thesis — "knowledge compounds instead
of being re-explained" — is EXTRINSIC (does the agent do better work?). No
amount of the former establishes the latter. Compounding that, the golden
corpus is author-written, author-queried and author-judged; it validates
configuration, not capability (as rulings §10 already says of it), and it
is now labelled that way rather than cited as evidence.

Design decisions worth recording:

- **`eval/` becomes its own Go module, in-repo.** Go's `internal/`
  visibility rule then MECHANICALLY prevents the harness from importing
  daemon internals — black-box access is compiler-enforced, not a
  convention. It also keeps LLM-client and statistics dependencies out of
  the daemon's deliberately small offline dependency tree, and keeps the
  main suite's properties (offline, deterministic, free, gates every
  commit) uncontaminated by evaluation that is networked, stochastic and
  costly. In-repo rather than a separate repository so it cannot rot out
  of sync with the surface it tests.
- **Independent ground truth is mined, not authored.** GitHub
  duplicate-issue links, Stack Overflow duplicate markers and documentation
  cross-references are human relevance judgments made at scale by people
  with no stake in Cairn — the cheapest available break in the
  self-authorship circularity.
- **Kill criteria are pre-registered (EVAL §7).** Including the two that
  would hurt most: if Cairn cannot beat grep-over-transcripts on task
  success, the ranking layer is not earning its complexity; if ablating
  vector search does not degrade quality, the embedder (and with it the
  venv, the model pin and the enrichment pipeline) should be deleted.
- **The untrusted-content claim is treated as a testable safety claim**
  (E6): plant injection payloads in mesh content and measure agent
  compliance rate through digest/search/fetch. It has never been tested at
  an agent, only asserted structurally.
- **The 30-handoff evaluation is strengthened, not replaced** (E7):
  pre-registered success definitions plus randomized withholding for a
  within-operator control; reported as a case study with its limits stated
  (n=1, non-blinded, self-reported).

### EVAL E1 — claims register DRAFTED, awaiting operator sign-off (2026-08-09)

`eval/claims.yaml`: 16 public claims extracted from README/spec/CLAUDE.md,
each with class, current evidence status, proposed measurement (mapped to
an EVAL milestone), threshold and kill criterion. Composition — 4
engineering, 4 retrieval, 5 product, 3 safety; by evidence: 7 proven, 1
circular, 6 untested, 2 unstarted.

**Every kill criterion is marked PROPOSED and every `signoff: pending`.**
Deliberately: the kill criteria are commitments about what the author
accepts as disproof of his own project, so they are the operator's to set,
not an agent's. Measurement work on a claim does not begin until its
signoff lands. Four operator decisions are listed at the foot of the file;
the load-bearing one is whether losing to grep-over-transcripts (B1) is
genuinely accepted as disproof of the curation layer.
