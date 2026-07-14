# Cairn P1 — Pre-N9 Live Network Verification

## Context for the verifying agent

You are an independent verification agent. Cairn's P1 milestones N5–N8
(Tailscale transport, enrolment, replication, blob durability, fork detection)
were built and passed their acceptance suites **in the test harness**. They
have **never run on real two-machine infrastructure**. Your job is to prove or
refute that they work live, across two actual nodes on a real tailnet, BEFORE
the formal N9 audit. Harness green is not evidence the network works — that is
exactly what you are here to check.

Rules of engagement:
- Execute against live binaries on real machines. Never trust PROGRESS.md /
  builder claims — treat them as hypotheses.
- Do not fix anything. Record findings (command, expected + spec/ruling
  reference, actual, exit codes) and continue.
- Two nodes: NODE-A = the operator's Mac (mesh origin, tailnet 100.71.53.50),
  NODE-B = the second machine (WSL2 Ubuntu on the Windows PC, or a second Mac).
  The operator drives cross-machine steps and the physical root-key ceremony;
  you specify each command and interpret each result.
- Authority: RULINGS.md > docs/rulings-v0.3.1 > docs/spec-v0.3. Record the
  audited commit SHA on both nodes (must match).
- Produce `PRE-N9-REPORT-<yourname>.md` (template at bottom).

---

## Phase 0 — Substrate reality check (the thing the harness faked)

On NODE-A:
1. `tailscale ip -4` → records a 100.x address. On NODE-B likewise; the two
   must differ and both must be reachable (`tailscale ping <other>`).
2. Start the daemon on NODE-A capturing output:
   `pkill -f "cairn daemon"; cairn daemon 2>&1 | tee /tmp/cairn-a.log &` then
   `sleep 3`.
3. **THE N5 CHECKPOINT FINDING** — verify it's closed:
   `lsof -iTCP -sTCP:LISTEN -P | grep cairn`
   - Expected (R27, spec §7): a listener bound to the **tailnet address**
     (100.71.53.50:<port>), NOT 0.0.0.0 or *:<port>.
   - A bind on 0.0.0.0 / *: = **BLOCKER** (LAN-exposed; violates tailnet-only).
   - No listener AND no explanatory log line in /tmp/cairn-a.log = **BLOCKER**
     (this was the open N5 finding; the milestone doesn't pass until the bind
     is real or its absence is explicitly logged and justified).
   - No listener but a clear "deferred until enrolled peers" log line = NOTE,
     proceed (the listener must then appear after Phase 2).
4. Confirm the daemon survives independently of your shell (the launchd/
   service install, or note its absence as a MAJOR ergonomics finding —
   `cairn daemon --install` / F10 was flagged).

## Phase 1 — Second node exists and is initialized

On NODE-B:
1. Tailscale installed and `up` on the SAME tailnet account; `tailscale ip -4`
   returns a distinct 100.x; `tailscale ping <NODE-A>` succeeds.
2. Cairn built from the SAME commit as NODE-A (`cairn --version` matches).
3. NODE-B has its own mesh init OR is a bare pre-enrolment node per the
   enrolment design — follow RULINGS R37/N5 mechanics; record which model the
   build actually implements (fresh-init-then-enrol vs bare-enrol).

## Phase 2 — Enrolment ceremony, live (R27, R28, R37) — operator-driven

This is the first real test of the offline-root-key posture. The operator
performs the key handling; you verify each artifact.
1. On NODE-B: `cairn device enroll` → produces an enrolment request. Verify it
   is single-use and carries a 1h expiry (R28).
2. Operator restores the root key to NODE-A device-local storage from offline
   backup.
3. On NODE-A: `cairn device approve <request>` → emits a **root-signed**
   `device.add`. Verify the signature chains to the mesh root; verify the
   event appears in the log.
4. Operator removes the root key from NODE-A device-local again.
5. NODE-B receives its cert and joins. `cairn identity show` on NODE-B is
   green and shows its device under the mesh.
6. **Negative test (R27):** BEFORE enrolment (or with a third un-enrolled
   tailnet device), attempt a sync connection to NODE-A → must be **refused
   and logged** with the rejected identity. An un-enrolled tailnet peer that
   is NOT refused = **BLOCKER**.
7. Re-run Phase 0.3 `lsof` on NODE-A if the listener was deferred — it must
   now be bound.

## Phase 3 — Text convergence, live (N6: R29, R30, spec §6.2)

1. `cairn send` a distinctive message on NODE-A → within the anti-entropy
   window (R29 default 5 min; force with any push mechanism if one exists),
   `cairn search` on NODE-B returns it, content-identical (message_id,
   revision_id, body_hash match).
2. Reverse direction (send on B, find on A).
3. **Offline divergence + rejoin:** stop NODE-B's daemon; send 100+ messages
   on NODE-A; restart NODE-B → it catches up; verify count and that catch-up
   used sealed-segment streaming if >10k (R30 — here just confirm convergence).
4. **Kill mid-sync:** during a catch-up, `kill -9` the receiver; restart →
   converges, no duplicates, no chain gaps (per-event reconciliation over
   segment bytes). Then `kill -9` the sender mid-sync; same check.
5. **Reindex on BOTH nodes** after all drills → identical query results for
   canonical content (freshness-driven score drift allowed; hit identity and
   ordering not).
6. Ephemeral text (R29): a message sent while NODE-B is offline must NOT be
   backfilled to it on rejoin (ephemeral gossips to connected peers only).
   Verify it's absent on B and present on A.

## Phase 4 — Blob durability, live (N7: R31, R32)

1. `cairn send --attach <file>` on NODE-A with durability `normal` (2).
   Immediately: receipt/digest shows `replication: pending` (only 1 node has
   it). When NODE-B connects/syncs, durability reaches 2/2 and the annotation
   clears (R32). Verify via `cairn gates` and deep `cairn doctor`.
2. On NODE-B: `cairn fetch <blob-hash>` (origin A) → hash-verifies before
   serving (R31); NODE-B then advertises it (cache-then-advertise) — confirm A
   can subsequently fetch it FROM B (kill A's copy or check peer advertisement).
3. **Kill -9 mid-blob-transfer** → no partial/corrupt object served; retry
   completes; doctor reports match reality.
4. Deep doctor durability line (R32): reports met targets for non-pending
   blobs, pending ones informationally.

## Phase 5 — Fork detection, live (N8: §6.4, R33) — optional but valuable

Manufacture equivocation on the real tailnet:
1. Clone NODE-A's device state into a sandbox dir; from the clone, write
   divergent events at coordinates that already exist (same origin+gen+seq,
   different content, both validly signed by the cloned key).
2. Connect both to NODE-B (or each other). Expected: detection when logs meet
   (frontier/ingest signals), the forked origin **frozen** while OTHER origins
   keep syncing (R33 — verify a second origin still converges during the
   freeze), divergent frames quarantined under `.cairn/quarantine/`, loud log,
   `cairn gates` FAIL row, `cairn doctor fork <origin>` showing common ancestor
   + per-branch events + advertising peer.
3. Do NOT run the repair ceremony unless the operator wants to rehearse R41
   (repair now bundles device.fork.resolve + reissue + cloned-cert revoke in
   one root-key session). If run: both branches preserved, losing content
   reissued under a recovery origin, cloned cert revoked.

## Phase 6 — Report

`PRE-N9-REPORT-<yourname>.md`:
```
# Cairn Pre-N9 Live Network Verification — <yourname> — <date>
Nodes: A=<sha>@<tailnet-ip>  B=<sha>@<tailnet-ip>
Verdict: READY FOR N9 / NOT READY (blockers) / READY WITH FINDINGS

## Substrate & listener (Phase 0) — the N5 checkpoint status
## Per-phase results table: phase | expected | actual | pass/fail | evidence
## Findings (severity-ordered): BLOCKER/MAJOR/MINOR/NOTE, with repro
## Rulings/spec deviations observed
## Recommendation: proceed to the formal N9 crossed audit? y/n + why
```
Severity: BLOCKER = LAN-exposed listener, un-enrolled peer accepted, acked-
event loss/duplication across nodes, corrupt blob served, fork undetected when
logs meet. One BLOCKER = NOT READY.

Terse, evidence-first. If a phase can't run because a prerequisite failed
(e.g. no second node), say so and mark downstream phases blocked — do not
simulate.
```
