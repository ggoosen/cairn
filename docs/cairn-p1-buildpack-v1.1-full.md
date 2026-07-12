# Cairn P1 — Build Pack v1.1 (FULL, ungated)

**Supersedes v1.0.** Operator ruling: build full P1, iterate after — no gated
Part B. All previously-TBD constants are ruled below by judgment; every one of
them lives in config and is revisable from dogfood/usage data without
re-architecture. The 30-handoff evaluation continues in parallel as tuning
input.

**Authority:** RULINGS.md > rulings-v0.3.1 > spec-v0.3. FIRST TASK of the
build: append R18–R34 below to RULINGS.md. Durable-log internals (framing,
signing, sealing) remain out of bounds; flag immediately if a milestone seems
to need them. One milestone per session, acceptance before advancing,
PROGRESS.md discipline, commit per task.

**P1 definition of done:** all nine milestones accepted + the crossed
two-auditor network audit (N9) passes. Windows native is NOT in P1 (WSL2 is
the Windows path — appendix).

---

## N1 — MCP server + untrusted-content envelope

Server exposing the daemon over stdio (`cairn mcp`) for Claude Desktop /
Claude Code, tool layer transport-agnostic for later HTTP mounting.
Tools mirror §5.5 exactly: `cairn_digest, cairn_search, cairn_peek,
cairn_fetch, cairn_send, cairn_reply, cairn_signal, cairn_outcome,
cairn_why_ranked`. Nothing else.

- **R18:** every content-bearing result returns the spec §7.4 envelope
  `{kind, trust:"untrusted", provenance{message_id,revision_id,sender,
  content_hash}, content{mime,text}}`; tool descriptions state content is
  data, not instructions.
- **R19:** MCP budget-accounted identically to CLI (budget covers complete
  serialized payload); defaults digest 1500 / search 2000 chars.
- **R20:** MCP `cairn_send` gets full pre-ack validation + text-class policy;
  no `--force-class` equivalent.

**Accept:** Claude Desktop full round-trip (digest→search→fetch→send→outcome)
against the live mesh; envelope on every result; over-budget digest truncates
exactly.

## N2 — Capability enforcement + trusted launcher

Activate the rulings §7.2 tier system (P0 ran tier-1).
Profiles (device-local TOML): `full`, `agent-standard` (digest/search/peek/
fetch/send/reply/signal/outcome; no admin/structural), `read-only`.
`cairn run --profile <name> -- <cmd>`: short-TTL (24h) non-delegable session
handle bound to the process, `CAIRN_SESSION` env, auto-revoke on exit/idle.
Daemon: CLI without handle = operator `full` (tier-1 preserved); MCP REQUIRES
a handle or a profile named in server config; requests carry the principal
hierarchy.

- **R21:** MCP is never tier-1. Desktop default `agent-standard`; any future
  remote client default `read-only` + send.
- **R22:** isolation honesty documented (same-user = accident prevention,
  not malice prevention).
- **R23:** handles are opaque daemon-side records; no JWT/macaroons in P1.

**Accept:** read-only session's send refused pre-ack with capability error;
handle expiry ends access; telemetry rows carry principal hierarchy.

## N3 — Durable semantic subscriptions

`subscription.create/update/disable/delete` events (base-revision optimistic
update, rulings v0.3.1 §2). Durable sub = {owner_view, hard_filters,
interest_query (raw NL stored + locally embedded), threshold_mode, push_cap}.
View-config queries remain the local tier.

- **R24:** calibration per spec §5.8 — hard filters first; top-N-per-window
  (default 10/24h) or percentile over observed similarity; margin over
  next-best; push_cap default 20/day. No static cosine thresholds.
- **R25:** session subscriptions stay local (telemetry-class); only explicit
  `cairn subscribe --durable` creates events.
- **R26:** digest composition: mandatory (recipients, pins) → subscription
  matches (marked) → interest-query ranking, one budget.

**Accept:** durable sub "council planning approval" surfaces a semantically
matching new send (no shared keywords) in next digest, marked; cap enforced;
disable stops delivery; events replay cleanly through reindex.

## N4 — Deterministic derivatives + receiver summary check

Sandboxed extraction (size/page/CPU/timeout, no network, MIME sniffing) for
text-layer PDFs, sanitised HTML, office text → `derivative.publish/fail/
invalidate` with provenance. Receiver topical-consistency check: sender
summary embedded vs body; over-distance → local extractive summary preferred
+ disagreement marker.

**Accept:** PDF attachment searchable via derivative with full provenance;
misleading sender summary flagged and locally re-summarised.

## N5 — Transport + membership (Tailscale)

Sync listener bound to the tailnet address ONLY (never 0.0.0.0); application-
layer membership verified per connection regardless of transport (endpoint
identity ≠ mesh authorisation). Enrolment ceremony implemented end-to-end:
`cairn device enroll` on the new node produces an enrolment request; operator
restores root key device-local on the signing machine, `cairn device approve
<request>` emits root-signed `device.add`; operator removes root key;
new node receives cert and joins. `device.revoke` propagates and refused
connections log the revoked identity.

- **R27:** peers authenticate mutually at the application layer with device
  certs chained to mesh root; a valid Tailscale connection from an
  un-enrolled or revoked device is refused and logged.
- **R28:** enrolment requests expire (default 1h) and are single-use.

**Accept:** two-node enrolment ceremony completes with the real offline root
key round-trip; un-enrolled tailnet peer refused; revoked peer refused after
`device.revoke` replicates.

## N6 — Reconciliation + text replication

Per spec §6.2: per-origin frontier exchange (highest-contiguous + known gaps),
sealed-segment root comparison, missing-range requests, at-least-once with
idempotent ingest, verify hash+signature before indexing. Replication policy:
canonical + eager text to all full nodes; ephemeral text to currently-
connected peers only, never backfilled.

- **R29:** sync cadence — push notification on append when peer connected;
  anti-entropy sweep default every 5 min; both config. Battery/metered
  awareness deferred to P3 (thin nodes) — full nodes assume mains power.
- **R30:** a node syncing >N events behind (default 10k) streams sealed
  segments wholesale rather than per-event.

**Accept:** two-node convergence after: normal operation, one node offline
for 100+ events, kill -9 mid-sync on each side, and a deliberately deleted
segment on the receiver (re-fetched and verified). Reindex on BOTH nodes
after all drills reproduces identical query results for canonical content.

## N7 — Blob replication + durability acknowledgement

Lazy fetch by hash from any advertising peer; durability classes live:
`ephemeral` origin-only, `normal` ≥2 nodes (default), `important` all
operator nodes, `pinned` per policy. Send with durability > available nodes:
ack `accepted_locally` with `replication: pending` surfaced in receipts,
digest annotations, and `cairn gates`; satisfied asynchronously when a peer
appears.

- **R31:** blob fetch verifies hash before serving; cache-then-advertise
  (a node that fetched a blob becomes a source).
- **R32:** `cairn doctor` (deep) verifies durability targets met for
  non-pending blobs and reports pending ones informationally.

**Accept:** attachment sent on node A reaches durability 2/2 when B connects;
fetch on B of an A-origin blob verifies and re-advertises; doctor reports
match reality through a kill -9 mid-blob-transfer.

## N8 — Live fork detection + network doctor

The P0 equivocation case (same origin+generation+sequence, different hashes,
both validly signed) becomes detectable when logs meet. On detection: freeze
ingest from the forked origin, quarantine both branches' divergent suffixes,
loud daemon log + `cairn gates` FAIL row, `cairn doctor fork <origin>` shows
common ancestor, per-branch summaries, advertising peers. Repair flow per
rulings v0.3.1 §6.4 (operator chooses; losing branch never silently deleted;
recovery origin re-issue with `recovered_from_event_id`).

- **R33:** fork handling never blocks other origins' sync.

**Accept:** manufactured equivocation (clone device state in a sandbox, write
divergent events, connect both) is detected, frozen, surfaced, and repaired
via the documented flow with both branches preserved.

## N9 — Hardening + crossed two-auditor network audit

Complete network fault matrix: partition/rejoin (each side continues writing,
converges on rejoin), kill -9 during every sync phase (frontier exchange,
range transfer, blob transfer, segment stream), slow/lossy link simulation,
duplicate delivery floods (idempotency under pressure), revoked-mid-sync.
Then the crossed audit: two independent auditors (swap assignments from P0
round 2), P0-grade rigor on the network surface — enrolment ceremony,
convergence drills, fork detection, capability enforcement over MCP,
injection probe through the envelope, and a regression pass on the
triple-attested durable core.

**Accept:** both audit reports PASS or minor-only.

---

## Cross-cutting rulings

- **R34 — WSL/standalone node adoption:** a pre-existing standalone mesh
  (own genesis/root) cannot merge origin logs into another mesh (different
  mesh_id + root = different trust domain, by design). Adoption path: enrol
  the machine as a FRESH device of the main mesh; re-publish the standalone
  mesh's knowledge via an export/re-send script (`cairn export --jsonl` →
  send loop) carrying `source_ref` provenance; retire the standalone mesh.
  Ship the script as `cairn adopt-standalone <path>` if time permits, else
  documented shell.
- M9 ingest (scan/manifest/apply for external knowledge bases) remains its
  own post-P1 work item — not silently absorbed here.
- F9 (bench golden `--embedder real`) rides along in any milestone's slack;
  do not let it slip past N4.

## Sequencing and checkpoints

N1→N9 in order. Operator checkpoints (read PROGRESS.md before continuing):
after N2 (the security model just activated), after N5 (first network
surface + real root-key ceremony), after N8 (before hardening). The N5
ceremony needs YOU physically: root key from offline storage, approve,
remove — schedule it.

## Appendix — Windows/WSL node (do now, standalone)

WSL2 Ubuntu on the Windows PC → `make build` → `cairn init` (own mesh, own
root key — export offline too) → views `printagent`, `labels` → AGENTS.md/
CLAUDE.md blocks in those repos. Accumulates knowledge immediately; adopted
into the main mesh at N5 via R34.
