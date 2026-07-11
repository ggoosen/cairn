# Cairn P0 — Test Plan (Crash/Fault Matrix)

Core invariant (assert after EVERY scenario):
> After restart, every acknowledged event exists exactly once by event_id,
> each origin chain is contiguous and signature-valid, every referenced
> durable object is fetchable and hash-valid, and all projections are
> reconstructible from the log (reindex produces identical query results).

Harness requirements:
- Fault-injecting filesystem wrapper (interface over os calls) able to:
  fail/short-write at call N, drop unsynced writes on "power cycle",
  return ENOSPC/EIO on demand. Built in M1, reused everywhere.
- Kill modes: SIGKILL, and simulated power-cycle (unsynced-state loss).
  SIGKILL alone does NOT satisfy the durability claim.

## 1. Crash points (each × SIGKILL and × power-sim)

| # | Crash point | Required outcome |
|---|---|---|
| 1 | During object temp write | No event; temp removable |
| 2 | After object fsync, before rename | No event; orphan temp removable |
| 3 | After rename, before dir fsync | No ack; recovery may keep or drop orphan |
| 4 | Mid frame append | No ack; trailing frame truncated on recovery |
| 5 | After append, before fdatasync | No ack; event may or may not survive |
| 6 | After fdatasync, before ack | Event may survive; retry dedups by request_id |
| 7 | Immediately after ack | Event AND objects must survive |
| 8 | During seq-state cache update | Log wins; next seq reconstructed |
| 9 | Mid SQLite projection tx | Event survives; projection resumes idempotently |
| 10 | After projection, before view swap | View regenerates; no partial file ever exposed |
| 11 | During segment seal | Open segment valid OR one valid sealed segment |
| 12 | During receipt write | Event survives; retry regenerates identical receipt |
| 13 | During clean merge | No revision OR complete 2-revision merge event |
| 14 | Migrate: after device.add, before revoke | Both devices valid; rerun completes revoke |
| 15 | Migrate: after revoke | Old origin read-only; new device usable |

## 2. Injected failures

- ENOSPC on: object write, segment append, SQLite WAL, view generation,
  receipt write. (Also: emergency-reserve behavior — ordinary send cannot
  consume reserve; operator release path works once, ≤64 KiB.)
- EIO, short writes, explicit fsync failure → never ack; clear error.
- Truncated frame / CRC-corrupt frame / bit-flipped canonical bytes →
  doctor detects; recovery truncates only trailing invalid frame; interior
  corruption = hard error report, never silent skip.
- Hash-valid frame with INVALID signature → quarantined, surfaced.
- Missing referenced object → typed error on fetch; doctor reports.
- Corrupt/deleted index.sqlite → full replay reproduces identical results.
- Duplicate outbox bundle; duplicate event ingest → idempotent.
- Concurrent CLI senders (10 parallel) → all acked events present, seq
  contiguous, no interleaving corruption.
- Clock rollback and extreme-future timestamps → ordering unaffected
  (ordering is origin/generation/sequence, never wall time).
- Stale export edit racing 1 revision; racing N revisions; racing a
  retraction → per rulings §8 (merge / conflict / rejection), never data loss.
- Ephemeral expiry racing fetch and racing reindex → content_expired typed
  result; no crash; event intact.
- Restored portable data WITHOUT device-local identity → daemon refuses to
  write under old origin; `cairn init --adopt` path creates new origin.
- seq cache BEHIND log (fine, reconstructed) and AHEAD of log (log wins,
  cache reset, warning logged).
- Volume states: encrypted / unencrypted / indeterminate → start / refuse /
  refuse; `--allow-unencrypted` persists device-local and warns each start.

## 3. Property tests

- Observed-remove links/pins: arbitrary interleavings of add/remove
  assertions converge; remove never kills an unobserved concurrent add;
  protected links survive automatic removal.
- Retraction: retracted messages absent from search/digest by default,
  present with capability-gated include_retracted, history replayable.
- Budget compliance: for random corpora and budgets, returned payload
  (metadata + prefixes + truncation markers included) NEVER exceeds
  budget_chars; mandatory overflow reports omitted_mandatory_count.
- Canonical JSON: serialize→parse→serialize is byte-stable; payloads with
  floats are rejected at validation.
- why_ranked arithmetic == independently recomputed score, always.

## 4. Retrieval quality (CI proxy for product gates)

- testdata/corpus: ~200 messages across 4 synthetic "projects" with a
  known-relevant answer set for ~30 queries.
- Assert Success@5 ≥ 70% on the golden corpus with default constants
  (this validates configuration, not the thesis — see rulings §10).
- lexical_only mode: same queries with embeddings disabled must still
  return the known-relevant item in top-10 for ≥ 60%.

## 5. Engineering scorecard (record in PROGRESS.md, dev machine)

At 10k / 100k / 1M synthetic events: cold recovery time; reindex --lexical;
reindex --semantic; send-ack P50/P95; ack→lexical-digest-visible P95
(< 200 ms gate at 100k); search P50/P95; backup size; restore time;
daemon RSS.

## 6. Human-measured (NOT CI — protocol in DOGFOOD.md)

30 genuine cross-session handoffs across 3 named agent views; diary
baseline of copy-paste workflow; Success@5, time-to-useful-context,
manual-workaround rate via `cairn found/not-found/manual-workaround`
bound to interaction_ids; weekly `cairn gates` report.
