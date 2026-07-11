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

## Resume-cold notes
- Next milestone: **M7 — Telemetry, gates harness, full fault matrix**.
  telemetry.sqlite (SEPARATE db, never events/replicated): interactions
  with ids/positions/budgets/outcomes, inferred=true flags; outcome
  commands (found/not-found/manual-workaround) bound to interaction_id
  (search/digest already return one); `cairn gates` (engineering gates
  computed: zero-loss from matrix runs, provenance 100%, budget 100%,
  P95 ack→lexical-visible < 200 ms measured; product gates template);
  emergency reserve (64 MiB preallocated file, operator-only release,
  ≤64 KiB, rulings §11 + reserve fault rows); remaining TESTING.md rows
  (power-sim already in MemFS; volume-state rows via CAIRN_FAKE_VOLUME_STATUS;
  restored-portable-data row; clock rollback row); 1M-event synthetic
  scorecard (append/recover/search timings into PROGRESS.md).
  Remember -tags sqlite_fts5 (make test).
- `// RULING-NEEDED:` markers in code: one (root-key storage, M0).
