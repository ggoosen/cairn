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
