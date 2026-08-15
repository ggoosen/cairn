# Cairn P0 — Build Plan (M0–M8)

> **STATUS: COMPLETE.** Every milestone here (M0–M8, plus the post-P0 M9
> ingest stub) is built and recorded in PROGRESS.md. This file is retained
> for its acceptance criteria and decision trail, not as a work queue —
> P1/P2/P3 and everything since were planned elsewhere.
>
> **For what is still to be built, read [`../ROADMAP.md`](../ROADMAP.md)** —
> the single consolidated index. Active work orders it points to:
> [`CAPTURE-PLAN.md`](CAPTURE-PLAN.md) (zero-effort capture) and
> [`EVAL-PLAN.md`](EVAL-PLAN.md) (proving the claims, with pre-registered
> falsification criteria in `../eval/claims.yaml`).

Rules: one milestone at a time, in order. A milestone is done when ALL its
acceptance criteria pass and PROGRESS.md is updated. Spec references are to
docs/spec-v0.3.md (§) and docs/rulings-v0.3.1.md (R§).

---

## M0 — Scaffold, config, identity, encryption check
**Build:** repo layout per CLAUDE.md; TOML config (portable + device-local)
with versioned schema; `internal/config/constants.go` holding EVERY tunable;
Ed25519 keygen; device-local state dir; `cairn init` (creates cairn dir,
genesis + initial device cert per R§2/R§4); `cairn identity show`;
encrypted-volume check (macOS `fdesetup status`; Linux best-effort dm-crypt;
unknown → fail closed; `--allow-unencrypted` persisted device-local,
warned every start, per R§9).
**Accept:** `cairn init` on a FileVault Mac produces a valid genesis event
(verifiable), portable/device-local separation is correct (no key material
under the cairn dir), unencrypted volume refuses start without the flag.

## M1 — Event log core (the crash-safe heart)
**Build:** canonical JSON (RFC 8785) with no-floats enforcement + test
vectors; envelope + signing (signing_bytes/record_bytes split, R§1);
frame format + CRC32C; segment append with full durability ordering (R§3);
open/sealed segments (seal at 64 MiB / 10k events, sealed header + BLAKE3
root); startup recovery (trailing-frame truncation, chain + signature
verification, seq-from-log); `cairn doctor` (walk, verify, report).
**Crash tests written WITH the code:** kill/power-sim at every point in
TESTING.md §1 rows 1–8; ENOSPC/EIO/short-write injection via a fault-
injecting fs wrapper.
**Accept:** full M1 slice of the crash matrix green; 10k-event append+recover
benchmark recorded in PROGRESS.md; doctor detects a deliberately corrupted
frame and a broken chain.

## M2 — Object store + text classes
**Build:** BLAKE3 content addressing, temp→fsync→rename→dirfsync writes,
never-overwrite verify-on-collision; text classes with policy enforcement
(>1 MiB auto-downgrade, operator override CLI-only, daily ceilings — R§5);
ephemeral TTL housekeeping loop; typed content_expired on fetch;
object-before-event ordering wired into the M1 append path.
**Accept:** crash rows for object writes green; expired ephemeral fetch
returns content_expired while its event replays fine; downgrade policy
covered by tests.

## M3 — SQLite projection + FTS + reindex
**Build:** DDL from build/sql/projection.sql; replay-from-log; checkpoint
row in same tx (R§6); contentless FTS5 keyed by revision_id +
current-head table; retraction as projection flag; observed-remove link/pin
tables; `cairn reindex --lexical` (side-build + atomic swap) and the
`--semantic` stub; unicode61 tokenchars `_ - # @`.
**Accept:** delete index.sqlite → reindex reproduces byte-identical query
results; crash mid-projection resumes idempotently; property tests for
concurrent link add/remove and retraction visibility pass.

## M4 — Daemon, CLI, outbox, receipts
**Build:** daemon lifecycle (file lock + unix-socket IPC, single writer);
cobra CLI (`send, reply, retract, link, pin, signal, search, digest, peek,
fetch, why-ranked, found/not-found/manual-workaround, doctor, reindex,
resolve, init, identity, migrate`); outbox watcher: atomic bundle contract
+ `.md` convenience shorthand, request_id idempotency, receipt-after-
durability, rejected/ with structured errors (R§8); `cairn migrate` offline
ceremony incl. device.revoke (R§4).
**Accept:** duplicate bundle returns identical receipt; crash-during-receipt
retry regenerates same receipt; concurrent CLI senders serialize correctly;
migrate crash between add/revoke recovers per matrix.

## M5 — Exports, 3-way merge, conflicts
**Build:** export generator with read-only front-matter (R§8); ingest of
edited exports: base==head → normal revision; clean diff3 → single event
creating TWO revision objects (operator branch + merged, flagged); conflict
→ conflicts/<id>/{BASE,CURRENT,OPERATOR_EDIT,RESOLVE}.md + `cairn resolve`;
front-matter mutation rejected; retracted-target rejected with error
receipt; `cairn doctor conflicts`; LF/UTF-8 normalization.
**Accept:** merge race tests (stale edit vs 1 and vs N intervening
revisions; retraction racing ingest) green; crash-during-merge leaves no
half-graph.

## M6 — Embeddings, ranking, digest, why-ranked
**Build:** ONNX MiniLM embed interface (pinned model ID + artifact hash;
Python-venv fallback rule per CLAUDE.md if binding fails); background
enricher goroutine + retrieval_mode; sqlite-vec storage (+ brute-force
fallback); RRF k=60 + percentile + deterministic ties; both P0 profiles
with constants from constants.go; effective_P decay + suspension; mandatory
inclusion + omitted_mandatory_count; budget_chars accounting over the
COMPLETE payload; digest views from local view config (hard filters +
optional interest query, R§7); per-line `> [CAIRN] ` prefixing;
fetched/ manifest+body pair; `why_ranked` printing stored arithmetic;
`reindex --semantic` full path incl. model-migration invalidation.
**Accept:** golden-corpus retrieval tests (build testdata/corpus of ~200
messages with known-relevant sets; assert Success@5 on it); budget
compliance property test (never exceeds, includes metadata); why-ranked
output matches recomputed scores exactly; kill enricher mid-batch →
lexical_only served, reindex heals.

## M7 — Telemetry, gates harness, full fault matrix
**Build:** local telemetry.sqlite (never in event log): interactions with
ids/positions/budgets/outcomes, inferred=true flagging (R§10); outcome
commands bound to interaction_id; `cairn gates` report (engineering gates
computed; product gates template for human entry); emergency reserve
(64 MiB preallocated, operator-only release, R§11); complete every
remaining TESTING.md row incl. simulated power-cycle loss of unfsynced
state (use a tmpfs/loopback harness or fs shim that drops unsynced writes);
1M-event synthetic scorecard run.
**Accept:** entire fault matrix green in CI; `cairn gates` shows zero-loss,
provenance 100%, budget 100%, and P95 lexical visibility < 200 ms on the
dev machine; scorecard numbers recorded in PROGRESS.md.

## M8 — Dogfood package (exit to evaluation)
**Build:** `cairn setup-agent <name>` (creates view + config); a
DOGFOOD.md quickstart for the operator: wiring three agent surfaces
(Claude Code project A, project B, a chat-agent copy/paste view), the
30-handoff diary protocol, baseline collection, and how to run
`cairn gates` weekly; launchd plist for daemon autostart on macOS;
backup script (portable data only) + restore drill (verifies new-origin
behavior).
**Accept:** fresh-machine install from README in <10 minutes; restore
drill demonstrates portable-only restore creates a new origin; operator
evaluation can begin.

## M9 — Ingest (stub; post-P0)
**Build:** `cairn ingest` — scan/manifest/apply pipeline for importing
existing knowledge bases (llm-wiki style repos, docs trees); built post-M8
against a live mesh. Hooks already in place from P0: optional
`source_ref`/`relates_to` fields on message.publish
(build/schemas/p0-events.schema.json) and the `source_refs` projection
table (build/sql/projection.sql). No P0 milestone builds any ingest
behavior beyond schema acceptance and that table.

---

## Sequencing notes for Claude Code sessions

- M0+M1 are the foundation and the highest-risk code; give them the most
  care. Everything after M1 trusts the log.
- M1's fault-injection fs wrapper is reused by every later milestone — build
  it well once.
- If a session ends mid-milestone, PROGRESS.md must contain enough state to
  resume cold: last completed task, failing tests, open RULING-NEEDED items.
- Estimated shape (solo + Claude Code): M0–M1 ≈ 30% of total effort,
  M2–M5 ≈ 40%, M6–M8 ≈ 30%. Do not let M6 ranking polish steal time from
  M7 fault coverage — the zero-loss gate is the release blocker.
