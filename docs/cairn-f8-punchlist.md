# Cairn P0 — F8 Punch List (Round-2 Audit Residue)

**Status:** P0 passed round-2 re-audit (Claude: PASS; Codex: conditional pass, conditions adjudicated below). Nothing here gates the dogfood evaluation — fix during it, one session, commit per item as `FIX-F8.<n>`.

## F8.1 — TTL duration strings (Codex AUDIT2-001)
`ephemeral_ttl` and `housekeep_interval` accept Go duration strings ("30s", "90m", "7d" — extend parsing for d) instead of integer hours/minutes. Keep backward-compat with the existing integer keys or migrate config with a clear message. Rationale: the expiry workflow is proven (Claude live-verified at 1h) but sub-hour TTLs are both legitimately useful (agent scratchpads) and required for auditability without waiting an hour. Regression: end-to-end expiry drill at TTL=30s in the suite.

## F8.2 — Golden corpus live runner (Codex AUDIT2-002)
Ship `testdata/corpus/` as checked-in fixtures and `cairn bench golden`: loads the corpus into a temp mesh via the real binary, runs the 30 queries, reports Success@5 and lexical-only top-10. The claim 0.97/0.63 must be reproducible without reading test code. Side benefit: this becomes the P2 ranking-calibration harness.

## F8.3 — Log loudly at park time (Claude NEW-01)
Ruling R4.3 says "logged loudly"; the daemon currently parks silently (loudness only via doctor/reindex/gates). Add a daemon log/stderr line at the park branch: event_id, event_type, origin/seq, error, and "run cairn doctor". One-liner plus test assertion in TestF1UnprojectableEventIsParkedNotFatal.

## F8.4 — RULINGS.md precedence text (Codex AUDIT2-004)
Correct the self-stated precedence to: **RULINGS.md > docs/rulings-v0.3.1.md > docs/spec-v0.3.md > build/TESTING.md** — newest rulings amend older documents where they explicitly conflict; that is the file's purpose. Also add this F8 list's rulings to RULINGS.md.

## F8.5 — Doctor names the referencing revision (Claude NEW-02, optional nit)
Missing/corrupt-object problem lines additionally name one referencing revision_id (and message_id) to save the operator a body_hash lookup. Low priority.

## Adjudications recorded (no action)
- Codex AUDIT2-003 (score drift after reindex/restart): brief wording error, not a defect — freshness legitimately decays with wall-clock time; hit set/order/identity were stable, and TestF2MigratedMeshReindexes proves byte-identical results under a fixed clock. Future audit briefs will say "identical result identity and ordering; scores may drift by freshness decay."
- Codex round-1 AUDIT-002 (plain daemon exits on fts5): same root as AUDIT-001; observed fixed implicitly by round 2 running entirely on a make-built daemon. Closed.
- Codex AUDIT2-001 partially dissolved by Claude's live 1-hour expiry verification: workflow proven; only granularity remains (F8.1).
