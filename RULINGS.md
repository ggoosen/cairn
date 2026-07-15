# RULINGS.md — Binding Post-P0 Rulings

**Process rule (FIX-F5):** design-conversation rulings are implemented ONLY
after they land in this file.

**Precedence (corrected per FIX-F8.4, Codex AUDIT2-004):**
**RULINGS.md** > `docs/rulings-v0.3.1.md` > `docs/spec-v0.3.md` >
`build/TESTING.md` > CLAUDE.md > builder judgment. Newest rulings AMEND
older documents where they explicitly conflict — that is this file's
purpose. Every override of an older document is recorded here as an
explicit adjudication, never made silently.

Provenance: retroactive capture of the design-conversation rulings that
never reached the repo (the F5 process failure found by both audits), plus
every ruling in `docs/cairn-p0-fix-workorder.md` (adjudication of the two
FAIL audits of commit d0aca05).

---

## R1 — Root key: P0 storage, export-root, P1 ceremony

1. **P0 storage:** the mesh root key is generated at `cairn init` and stored
   DEVICE-LOCAL (0600), never inside the portable mesh directory. `init`
   prints an explicit offline-backup instruction. (Adjudicates the M0 open
   ruling; supersedes spec §3.1's offline-only placement for P0 because the
   P0 migrate ceremony must root-sign `device.add`/`device.revoke` offline.)
2. **`cairn identity export-root [--out <path>] [--remove-local]`:** writes
   the root key material for offline storage, VERIFIES the export by
   read-back before offering anything, prints storage/usage instructions,
   and OFFERS (interactive prompt, or the explicit flag — never forced in
   P0) to remove the device-local copy. The export path must be OUTSIDE the
   portable mesh directory (it must never travel with backups). Existing
   files are never overwritten. After removal, `cairn migrate` requires
   restoring the exported key and says so.
3. **P1 device-add ceremony:** admitting a second device in P1 is an
   operator ceremony that requires producing the root key (from the export
   or the device-local copy) to sign the new cert — never an automatic or
   network-negotiated act. (Recorded for P1 design; no P0 code.)

## R2 — Resolve-merge base semantics (M5 adjudication)

`cairn resolve` applies `RESOLVE.md` as the authoritative human resolution
**relative to the conflict-time head** (the CURRENT.md the operator saw).
The original export base is NOT replayed (it re-conflicts forever on the
resolved lines). If the head moved again during resolution, the resolution
is diff3-merged against the new head (base = conflict-time head): clean →
apply; conflict → the materialization is REFRESHED (CURRENT.md, RESOLVE.md
seed, manifest) and **RESOLVE.md content is never lost**. The resolution
event preserves the operator branch (OPERATOR_EDIT.md, parent = original
base) and the resolved head (parents = [branch, current head]), with
`machine_merged=false` (human merge).

## R3 — Enrichment timing (confirms rulings v0.3.1 §6)

Append + fsync + ack are synchronous; the lexical FTS insert is synchronous
(digest visibility inside the 200 ms gate); embeddings run on an in-process
background goroutine. A just-sent message may briefly be `lexical_only` —
degradation-ladder step 4, not an error. Senders NEVER wait on enrichment
and NEVER see rejection after acknowledgement.

## R4 — Topic resolution and pre-ack referential validation (FIX-F1)

1. `cairn send --topic <ref>` resolves each ref as topic_id, then topic
   name, against the projection head. Unresolved refs on the operator CLI
   path AUTO-CREATE: `topic.create` then `topic.link.add`, in that order,
   in the SAME request — all events durably appended before the single
   ack/receipt. Outbox callers and `cairn link` never auto-create:
   unresolved refs are rejected BEFORE ack with a clear error.
2. **General rule:** any event whose intra-mesh references (topic ids,
   message ids targeted by links/replies/signals/retractions, pin ids,
   link ids, pinned object hashes) cannot resolve against the projection
   head or an earlier event in the same request batch is rejected before
   acknowledgement. An event that cannot project is never acked. Pre-ack
   rejection is legal; post-ack rejection never is.
3. **Projector parking (defense in depth):** a historical event that
   nevertheless fails projection is parked in `parked_events(event_id,
   error, parked_at, …)` within the same projection transaction, logged
   loudly, and the stream CONTINUES — startup replay, live sends, and
   reindex share identical semantics. `reindex` completes with exit 0 plus
   a prominent parked report; it never aborts. Doctor treats parked events
   as a failure condition. The log is never rewritten — parking is a
   projection-layer concept only.

## R5 — Two-pass replay and mesh-level trust (FIX-F2)

1. Replay pass 1 scans ALL origins for security-log events (genesis,
   device.add, device.revoke) and establishes the mesh membership/key set
   (root signatures make certs verifiable independent of origin placement;
   implementation: shared chain verifier iterated to fixpoint). Pass 2
   verifies and projects each origin's stream against that seeded set. The
   permanent security log is ONE stream class (spec §4.5) regardless of
   which origin's segments carry it.
2. `cairn identity show` verifies MESH-level trust: the genesis wherever it
   lives, plus the current device's membership and revocation status. It
   works identically before and after migration.
3. **Revocation ordering:** device.revoke gates WRITES from its causal
   position onward; events validly signed before revocation remain valid on
   replay. Keys of revoked devices stay in the verification set.

## R6 — Doctor depth (FIX-F3)

`cairn doctor` fails loudly (nonzero exit) on ANY of: parked events;
projection checkpoint AHEAD of the log or an unverifiable origin; missing
or hash-invalid objects referenced by non-expired, non-ephemeral revisions/
attachments; unresolved cross-origin trust; any log-integrity problem.
Informational (never failures): expired ephemerals; an ABSENT projection
database (derived, rebuildable); a checkpoint BEHIND the log with zero
parked events (parking makes silent stalls impossible, so behind always
heals by replay — e.g. after an offline migrate appends security events).
`cairn gates`' zero-loss row cites this deepened doctor.

## R7 — Build integrity (FIX-F4)

A binary that compiles but cannot start is never acceptable. The untagged
build fails AT COMPILE TIME with an instructive error naming the fix
(`make build` / `go build -tags sqlite_fts5`). `make verify` asserts both
the instructive failure of the plain command and the green tagged suite,
and is the CI entry point.

## R8 — CLI contract (FIX-F5.3)

Unknown subcommands — and bare group commands — exit NONZERO. Help text may
accompany the error; exit 0 may not.

## R9 — Restore semantics (FIX-F6.1; refines spec §3.2)

A portable-only restore (data without device-local identity) refuses ALL
writes and says so, pointing at `cairn init --adopt`. Read-only operations
— doctor, search, digest, fetch, identity show — are PERMITTED against the
restored data (spec §3.2 mandates refusing writes; read access is
explicitly allowed).

## R10 — Ephemeral TTL ergonomics (FIX-F6.2)

`EphemeralTTL` and `HousekeepInterval` are portable-config tunables
(constants remain the defaults). The daemon runs one housekeeping sweep at
startup; `cairn housekeep` triggers one manually.

## R11 — Version string (FIX-F6.3)

The binary version derives from build info (VCS revision when available)
and reflects P0 status — never a hardcoded milestone string.

## R12 — Interpretations adjudicated during P0 (recorded for re-audit)

- **source_ref paths** are stored as `repo/relpath`, making the
  single-column `source_refs.path` primary key collision-free across repos.
- **Protected-link removal:** `topic.link.remove` from a non-operator
  principal skips protected links; operator removals always apply
  (spec §5.5 "auto-processes may not remove"). Revisit under P1
  capabilities.
- **Invalid signature in the local log:** recovery refuses to start
  (single-writer log ⇒ corruption); doctor surfaces it. Quarantine-and-
  continue semantics belong to P1 replication of foreign origins.
- **Adopt:** `cairn init --adopt` archives the old history read-only at
  `events-preadopt-<old-cairn-id>/` (never deleted), retains objects,
  drops stale derived state, and creates a new origin identity.

## R13 — TTL duration strings (FIX-F8.1; amends R10)

`ephemeral_ttl` and `housekeep_interval` accept duration strings — Go
durations plus a `d` suffix ("30s", "90m", "7d", "1d12h") — and are the
preferred form. The legacy integer keys (`ephemeral_ttl_hours`,
`housekeep_minutes`) remain valid for backward compatibility; setting both
forms for one knob, or an unparseable string, is a load-time config error
with a clear message. Sub-hour TTLs are legitimate (agent scratchpads) and
required for expiry auditability; the suite carries a 30-second end-to-end
expiry drill.

## R14 — Golden corpus reproducibility (FIX-F8.2)

The golden retrieval corpus ships as checked-in fixtures
(`testdata/corpus/{messages,queries}.json`), embedded into the binary with
a drift test. `cairn bench golden` loads them into a THROWAWAY mesh
(isolated device state; never the operator's) through the real daemon
paths and reports Success@5 and lexical-only top-10 against the P0 gates,
with per-query miss detail. The published claim must be reproducible from
the shipped binary without reading test code. The M6 acceptance test
consumes the same fixtures (single source of truth). This runner is the
P2 ranking-calibration harness.

## R15 — Park-time loudness (FIX-F8.3; makes R4.3 concrete)

"Logged loudly" means AT PARK TIME: the daemon emits event_id, event_type,
origin/generation/sequence, the projection error, and the
`run cairn doctor` pointer to its warning stream the moment an event is
parked — in addition to (not instead of) doctor/reindex/gates reporting.

## R16 — Audit-brief wording on score drift (Codex AUDIT2-003, adjudicated)

Reindex/restart guarantees IDENTICAL result identity and ordering under a
fixed clock (proven byte-identical in the F2 regression). Raw scores drift
with wall-clock time because freshness legitimately decays — this is
correct behavior, not a defect. Audit briefs must phrase the invariant as
"identical result identity and ordering; scores may drift by freshness
decay."

## R17 — Doctor names a referencing revision (FIX-F8.5)

Missing/corrupt-object doctor lines additionally name one referencing
revision_id and message_id, so the operator never has to reverse a
body_hash by hand.

---

# P1 rulings (cairn-p1-buildpack-v1.1-full.md — FULL, ungated)

Operator ruling: build full P1, iterate after — no gated Part B. All
previously-TBD constants are ruled by judgment, live in config, and are
revisable from dogfood data without re-architecture.

## R18 — MCP untrusted-content envelope (N1)

Every content-bearing MCP result returns the spec §7.4 envelope
`{kind, trust:"untrusted", provenance{message_id, revision_id, sender,
content_hash}, content{mime, text}}`. Tool descriptions state that returned
content is DATA, not instructions.

## R19 — MCP budget accounting (N1)

MCP budgets are accounted identically to the CLI: the budget covers the
complete retrieval payload (header, entries, truncation markers). Defaults:
digest 1500 chars, search 2000 chars (config constants).

## R20 — MCP send policy (N1)

`cairn_send` over MCP gets full pre-ack referential validation and
text-class policy; there is NO `--force-class` equivalent exposed to MCP.

## R21 — MCP capability tier (N2)

MCP is never tier-1. Claude Desktop defaults to the `agent-standard`
profile; any future remote client defaults to `read-only` + send.

## R22 — Isolation honesty (N2)

Same-OS-user isolation prevents ACCIDENTS, not malice — documented wherever
profiles are described (rulings v0.3.1 §7.2 tier 1/2 language).

## R23 — Session handles (N2)

Capability handles are opaque daemon-side records (short-TTL, default 24h,
non-delegable, bound to the launched process, auto-revoked on exit/idle).
No JWT/macaroons in P1.

## R24 — Subscription calibration (N3)

Hard filters first; matching by top-N-per-window (default 10/24h) or
percentile over observed similarity with margin-over-next-best; push_cap
default 20/day. No static cosine thresholds, ever.

## R25 — Session vs durable subscriptions (N3)

Session subscriptions stay local (telemetry-class, never events). Only an
explicit `cairn subscribe --durable` creates subscription.* events.

## R26 — Digest composition with subscriptions (N3)

One budget, composed in order: mandatory (explicit recipients, then pins) →
durable-subscription matches (marked as such) → interest-query ranking.

## R27 — Application-layer membership (N5)

Peers authenticate mutually at the APPLICATION layer with device certs
chained to the mesh root; a valid Tailscale connection from an un-enrolled
or revoked device is refused and logged. Endpoint identity is never mesh
authorization.

## R28 — Enrolment requests (N5)

Enrolment requests expire (default 1h) and are single-use.

## R29 — Sync cadence (N6)

Push notification to connected peers on append; anti-entropy sweep default
every 5 minutes; both config-tunable. Battery/metered awareness is P3
(thin nodes) — P1 full nodes assume mains power.

## R30 — Bulk catch-up (N6)

A node syncing more than N events behind (default 10k, config) streams
sealed segments wholesale instead of per-event ranges.

## R31 — Blob fetch integrity (N7)

Blob fetch verifies the hash before serving; cache-then-advertise — a node
that fetched a blob becomes a source for it.

## R32 — Durability in doctor (N7)

Deep doctor verifies durability targets are met for non-pending blobs and
reports pending ones informationally.

## R33 — Fork isolation (N8)

Fork handling (freeze + quarantine of the equivocating origin) never blocks
other origins' sync.

## R34 — Standalone-mesh adoption (cross-cutting)

A pre-existing standalone mesh (own genesis/root) cannot merge origin logs
into another mesh — different mesh_id + root is a different trust domain by
design. Adoption: enrol the machine as a FRESH device of the main mesh,
re-publish the standalone mesh's knowledge via export/re-send carrying
`source_ref` provenance, retire the standalone mesh. Ship as
`cairn adopt-standalone <path>` if time permits, else a documented shell
script.

## R35 — CAIRN_SESSION escape probe: env-strip returns to tier-1 (N2 drill)

Observed in the operator drill (2026-07-12): inside a `cairn run --profile
read-only` shell, `unset CAIRN_SESSION` followed by `cairn send` SUCCEEDS —
the process falls back to the handle-less local CLI path, which is operator
tier-1 by design.

**Ruling: this is the designed behavior, not a defect.** Confinement is
environment-cooperative: the daemon cannot distinguish same-OS-user local
processes except by the handle they present (R22 — accident prevention,
not malice prevention). The profile guardrail protects against an agent
accidentally issuing structural ops through its normal toolchain (which
inherits the env); it does not and cannot stop code that deliberately
strips its environment, because that code could equally dial the daemon
socket directly. Documentation (DOGFOOD.md §3c, session.go, run.go) states
this boundary explicitly.

Any stronger boundary — socket peer-credential binding, per-principal OS
users, mandatory handles for ALL clients — is a P3 consideration and would
change the tier-1 ergonomics ruling; it is out of P1 scope.

## R36 — Subscription calibration mechanism (N3, interprets R24)

R24 mandates relative calibration with no static cosine thresholds but
leaves the mechanism open. Ruling (constants config-revisable per the
buildpack preamble):

- "Observed similarity" is the per-subscription HISTORY of similarities
  seen across digest evaluations (last 1000, telemetry-class — local,
  never events, survives projection rebuilds) combined with the current
  candidate pool.
- **top_n mode:** a candidate qualifies when its similarity clears the
  observed noise floor — the lower quartile of the observed distribution —
  by `SubMarginMin` (0.15). This is the "margin over next-best" clause:
  matches must stand out from what the subscription typically sees.
- **percentile mode:** a candidate qualifies at or above the subscription's
  Pth percentile of the observed distribution (default P=90).
- Window allowance (top-N-per-window, default 10/24h) and the daily
  push_cap (default 20) TRUNCATE the qualifying set; they never widen it.
  Only entries actually included in the budget-capped digest consume
  allowance.
- Edge rulings: a single candidate with no history passes (no relative
  signal exists; hard filters and caps still govern); a uniform pool where
  nothing stands out surfaces NOTHING — a relative calibrator cannot
  certify it, and silence is the conservative failure mode.

## R37 — N5 implementation rulings (interprets R27/R28)

- **Ceremony steps are OFFLINE**: enroll/approve/join/revoke run with the
  daemon stopped (the daemon write lock is held for the append), matching
  `cairn migrate`'s posture. Membership changes therefore take effect at
  the next daemon start — a running listener's trust view is the
  recover-time snapshot.
- **Join-grant bootstrap trust**: until N6 replicates real segments, the
  new node authenticates peers from the grant's identity chain, verified
  from genesis by the SAME chain verifier the log uses. Nothing in the
  grant is trusted that the chain did not prove (the embedded cert is
  re-verified against the chain's root). N6 must replace this bootstrap
  with replicated segments and delete the crutch.
- **Single-use marker (R28)** is the enrolment request id recorded in the
  device.add payload — durable in the signed log, scanned at approve time;
  no side-channel state to lose.
- **Loopback listener binding** is a dev/test mode behind an explicit
  CAIRN_SYNC_ALLOW_LOOPBACK=1 acknowledgement; production accepts only
  100.64.0.0/10 / fd7a:115c:a1e0::/48 literals. 0.0.0.0/:: is always
  refused.
- **Handshake shape**: three-message mutual proof over fresh 32-byte
  nonces under the "cairn-sync-hello-v1" domain separator; signature
  binds {cairn_id, signer device, both nonces}. Both directions verify
  membership + revocation + key possession BEFORE any protocol byte.

## R38 — N6 reconciliation implementation rulings (interprets §6.2 / R29/R30/R37)

Reconciliation runs over the N5-authenticated peer connection with a
newline-delimited JSON protocol (frontier → get_range/push_records →
records/ack → done). The INITIATOR drives BOTH directions in one dial, so a
single reconcile fully converges both nodes.

- **Foreign-origin ingest reuses the public log.** Received records are
  hash+signature verified (mesh trust) BEFORE they are appended; the append
  is the SAME `log.Append` the local write path uses, so contiguity,
  chaining, framing, fsync, and sealing are enforced identically. Durable-log
  internals are untouched. Idempotency is by (origin, sequence): a record at
  or below the local frontier is a no-op (at-least-once → idempotent).
- **Frontier = highest-contiguous next-sequence per origin.** Because
  `log.Append` enforces contiguity, a node can only store a contiguous
  prefix; range transfer therefore always starts at the receiver's frontier.
  Non-contiguous "known gaps" (§6.2) cannot be durably held and are not
  tracked separately — a conservative, spec-consistent simplification.
- **Text-replication scope (N6 vs N7).** N6 replicates message.publish /
  message.reply bodies and revise_body revision bodies (the searchable text
  corpus). Attachments and derivative text are N7 (lazy blob fetch by hash +
  local re-derivation). Canonical + eager bodies ship on both PULL and PUSH
  (backfillable to full nodes); ephemeral bodies ship ONLY on a live PUSH to
  a currently-connected peer and are never backfilled via a PULL response.
- **Own-active-origin ingest is refused.** A peer presenting events for THIS
  node's active origin beyond what it already holds is a possible fork
  (equivocation) — refused and surfaced, never silently ingested. Live fork
  detection + repair is N8.
- **Cadence (R29).** Push-on-append kicks an immediate (debounced) sweep of
  every configured `sync_peers`; an anti-entropy timer sweeps every 5 min
  (both config-tunable). A far-behind receiver (> `SyncBulkCatchupThreshold`,
  default 10k) is caught up with segment-sized batches (R30 bulk streaming),
  logged as a bulk catch-up.
- **Bootstrap-trust fallback (extends R37).** A node with no local genesis
  (freshly joined) OR whose genesis-bearing foreign origin was lost falls
  back to grant-chain bootstrap trust — genesis-rooted and root-verified —
  until N6 re-replicates the missing segments; MeshTrust resumes control the
  moment the local chain resolves. Bootstrap trust is RETAINED as a
  resilience fallback rather than deleted. Marked `// RULING-NEEDED:` in
  daemon.recover for author confirmation (broader than R37's literal "delete
  the crutch"; safe because it never widens the trust root).
- **Known limitation (flagged for N9 hardening).** A replicated record whose
  signing device is not yet locally admitted (a device enrolled on a peer
  whose device.add has not yet replicated) is refused until the admitting
  origin replicates; it resolves across subsequent sweeps (fixpoint), exactly
  as MeshTrust converges across origins. Not exercised by the two-node
  acceptance (both devices are mutually known before sync).

## R39 — N7 blob replication + durability implementation (interprets §6.3 / R31/R32)

Blob (attachment) replication runs as a THIRD reconciliation phase after the
N6 event/text phase, over the same authenticated connection (blob_inv →
get_object / put_object → object / put_ack). It reuses the durable-log
internals not at all; blobs are content-addressed objects handled entirely by
the object store.

- **Durability class → replica target.** ephemeral = origin only (1, never
  replicated); normal = `DurabilityNormalMin` (2, default); important /
  pinned = all non-revoked member nodes (all operator nodes), computed at
  runtime. The class is a per-message field on message.publish applying to
  that message's attachment blobs; it is projected onto the attachments table
  (schema v5) so both nodes agree on targets after the event phase.
- **Full-node replication, not lazy-only.** P1 full nodes replicate every
  non-ephemeral target blob bidirectionally during reconcile: a node fetches
  every target blob it lacks that a peer advertises and pushes every target
  blob the peer lacks. This meets or exceeds every target ≤ member count and
  matches "full node = complete corpus" (§6.1). True lazy on-demand fetch is a
  thin-node (P3) concern; N7's proactive replication is what satisfies
  "durability 2/2 when B connects".
- **R31 verify-before-serve/store.** Every transferred blob is content-address
  verified before it is stored (`store.Put` recomputes the hash and refuses a
  colliding address; the receiver additionally asserts got == expected) and
  before it is served (`store.Get` re-verifies). A hash mismatch is rejected,
  never stored or advertised — so an interrupted or corrupted transfer never
  counts toward durability. Cache-then-advertise: a node that fetches a blob
  becomes a holder (its object store IS the advertisement; the serving peer
  records it as a holder on the same dial).
- **Holder registry (local, non-replicated).** Peer holdership is runtime
  knowledge (spec §4.5: not events, not replicated), persisted to
  `.cairn/durability.json` (derived/cache-class, rebuilt by re-advertisement
  if lost). SELF holdership is ALWAYS recomputed from the object store, never
  trusted from the file — a deleted or corrupt local blob is never miscounted.
  The registry uses an atomic-OVERWRITE write (temp → rename-over), distinct
  from `fsx.WriteFileAtomic`, which is the write-ONCE primitive for immutable
  objects/receipts and refuses to replace an existing file.
- **Ack-time replication is deterministic (receipt idempotency).** The send's
  `replication` acknowledgement is the ACK-TIME snapshot — the origin holds
  the blob (have=1), pending if the class needs more nodes — derived purely
  from the durability class, so a regenerated receipt is byte-identical (M4).
  The LIVE replica state (2/2 etc.) is surfaced separately by `cairn sync
  status`, the gates durability row, the digest `[replication-pending]`
  marker, and deep doctor.
- **R32 deep doctor.** For each non-ephemeral attachment blob: a present local
  copy is content-verified (a corrupt present copy is a PROBLEM); the replica
  state is reported — SATISFIED (target met) or pending (informational,
  awaiting peers). A MISSING attachment blob is NOT a problem (blobs replicate
  and no single node need hold every one), distinct from a missing message
  BODY object (which IS a problem — bodies are the searchable corpus).
- **Known limitation (flagged for N9).** A corrupt local blob blocks re-fetch
  (the object store's verify-on-collision refuses to overwrite a colliding
  address); repair requires removing the corrupt object first. Doctor flags
  it; automated repair is out of N7 scope.

## R40 — N8 fork detection + repair implementation (interprets §6.4 / R33)

Equivocation (same origin+generation+sequence, DIFFERENT event_id, both
validly signed — a full-disk device clone that then wrote divergent events)
is detected when two logs meet during reconciliation. Detection has three
signals, all indicating the same condition:

- **Frontier** (primary): both sides report the SAME next-sequence for an
  origin but DIFFERENT chain heads → the branches diverged at or before the
  frontier. The initiator fetches the peer's overlapping branch via the
  ordinary get_range path, compares event ids seq-by-seq to find the exact
  divergence (common ancestor), and quarantines the peer's divergent suffix.
- **Ingest, same coordinate** (backstop): an incoming verified event at a
  sequence we already hold whose event_id differs from ours.
- **Ingest, chain divergence** (backstop): a contiguous incoming event whose
  previous_origin_event_id does not match our chain head.

On any signal cairn: **freezes** the origin (a typed forkError unwinds only
that origin's reconcile; every OTHER origin keeps syncing — R33), **quarantines**
the divergent branch's raw record frames under `.cairn/quarantine/<origin>/`
(preserved forever — the losing branch is never silently deleted), records a
ForkRecord under `.cairn/forks/`, and logs LOUDLY. A frozen origin ingests
nothing until resolved; the durable log is never mutated by detection.

- **Own active origin.** We are the sole legitimate writer of our own origin,
  so ANY peer event that conflicts with what we hold, OR extends beyond our
  head, is equivocation (a device clone) — frozen + quarantined, never
  ingested. (This tightens the N6 conservative refusal into full N8 handling.)
- **Surface.** `cairn doctor fork [origin]` shows the common ancestor,
  per-branch events with types, the advertising peer, and whether security
  operations (device.*/genesis) appear on either branch. Deep doctor reports
  an unresolved fork as a PROBLEM and a resolved one informationally; the
  `cairn gates` "no unresolved forks" row FAILs while any fork is frozen.
- **Repair** (`cairn fork resolve`, OFFLINE like migrate/revoke): the operator
  chooses the canonical branch; the LOSING branch's useful (message) events are
  reissued under the operator's active origin — a recovery origin — each
  carrying `recovered_from_event_id` + `fork_resolution_id`; a ROOT-signed
  `device.fork.resolve` records the decision (normative in p1-events.schema);
  the fork is marked resolved. Both branches are preserved (the canonical one
  in the log, the losing one in quarantine).
- **Conservative scoping (RULING-NEEDED in code).** The full §6.4 ceremony also
  re-enrols the physical device under a new identity and revokes the cloned
  certificate. cairn's `fork resolve` does the branch decision + reissue +
  device.fork.resolve; revoking the cloned cert is the documented follow-up
  (`cairn device revoke`, or `cairn migrate` first for a self-clone) rather
  than automated inside resolve, because a self-clone revocation is refused by
  design (RevokeDevice: "use migrate"). Marked for author confirmation.
- **Reissue scope.** Only message.publish/reply events are reissued (the
  "useful events" — content). Structural events (links, pins, signals) on the
  losing branch are not auto-reissued; they remain in quarantine for manual
  recovery. A losing message whose body is neither inline nor locally present
  (a clone-only blob) is skipped with a note (its frame stays quarantined).

## R42 — Ephemeral bodies are never inlined (schema wins over §5)

`text_class: ephemeral` ⇒ `body_bytes` MUST be absent from the publish event.
Validation rejects an ephemeral publish carrying inline bytes **pre-ack**
(per the F1 general rule: rejection before acknowledgement, never after).
Ephemeral bodies live only as content-addressed objects, fetched on demand
from a connected holder, and are genuinely purged at TTL.

**Why:** rulings §5 permits inlining ≤64 KiB bodies inside `signing_bytes`
for recovery acceleration; R29 forbids ephemeral backfill. The publish event
replicates to all full nodes as chain data regardless of text class, so an
inline ≤64 KiB ephemeral body makes the ephemeral guarantee structurally
unenforceable — the content is searchable on every synced node from the inline
copy, and TTL deletion can never scrub it. The two rulings collided; the
schema constraint wins. The inline optimization remains available for
canonical / eager-searchable text classes only.

**Historical events** already carrying inline ephemeral bytes (this test mesh
has them): the projection MUST treat them as ephemeral-with-object-absent —
exclude the inline body from indexing/search on non-origin nodes — and doctor
treats them under R43. The immutable log is never rewritten; a migration note
in PROGRESS.md records the transition.

## R43 — A missing ephemeral object is informational on every node

A missing ephemeral object is **informational on every node**, not only the
origin. `cairn doctor` MUST NOT fail (exit 1) because an ephemeral body was
withheld (peer offline at send time), never fetched, or expired at TTL. It
reports the condition and exits 0. This corrects the prior behaviour where a
synced non-origin node's doctor failed forever with "referenced object missing
(class ephemeral)" until expiry — which broke the DoD gate "doctor reports
clean" on every synced non-origin node. A missing *canonical* body object
remains a PROBLEM; only ephemeral is downgraded to informational.

## R44 — Sync listener defaults to auto (tailnet-only), never silent

The sync listener defaults to **auto**: on startup the daemon detects a
tailnet interface and binds `<tailnet-ip>:9700`. If no tailnet interface
exists it binds nothing — **and says so** (see R45). `sync_listen` may still
pin an explicit address; `sync_listen = "off"` disables the listener
deliberately. The listener MUST NEVER bind `0.0.0.0`. `cairn sync status`
reports the listener state (address bound, or the reason none was).

**Why:** `sync_listen` previously had no default, was never set by `init`, had
no CLI flag (device-TOML hand-edit only), and when unset the daemon bound
nothing and logged nothing — a core subsystem silently declining to start.

## R45 — No core subsystem declines to start silently (general rule)

**Any core subsystem that declines to start MUST log it prominently at every
startup**, with the reason and the remedy. Silence is never an acceptable state
for a disabled core function. This is the general rule behind three observed
instances: the daemon that wasn't running with no CLI hint; the embedder
falling back to lexical-only silently; the sync listener binding nothing
silently. When implementing any startup path, audit it for other
silent-declines and give each the same prominent, remedy-bearing log line.

---

# N9 audit rulings (two audit reports of d451c2f — H1–H8 work order)

## R46 — Invariant sweeps (adopt before H1)

Both G-residuals found by the N9 audit are the same failure class: **the fix was
applied at the finding's location, not across the invariant's surface.** G1 gated
the publish/reply path and left revise/merge inlining ephemeral bodies; G3 gated
enroll/join and left `migrate` writing a device key ungated. Neither is a new bug
— both are the *original* bug, alive on a path nobody enumerated.

**Ruling:** when a fix enforces an invariant, it is not complete until **every
write path to that invariant has been enumerated and gated**, and the enumeration
is recorded in the commit message. Grep-and-list the whole surface; never
patch-the-repro. This applies to any invariant-enforcing fix, not only the two
that triggered it — H1 (ephemeral-body inlining) and H2 (key-material writes)
adopt it immediately, and every future invariant fix inherits it.

## R47 — A ranking profile's explanation must reconcile exactly (from H3)

Any ENABLED ranking profile's `why-ranked` output MUST print every scoring
component (R, S, F, I, N, penalties, and the mandatory/pin flags) with its weight
and its weight×value product, and those printed numbers MUST recompute to the
returned score **exactly** (to the stored decimal precision), under EVERY
profile. A profile whose printed explanation does not reconcile with its own
returned score is **not adoptable** — an unreconcilable explanation is precisely
the "black box" that spec §9 ("dumb, inspectable, no black box", five review
rounds) exists to forbid. The N9 audit reproduced the P2 profile printing
component lines summing to 0.79 against a returned 0.8255 with S/I/N never
printed; that is a correctness defect in the audit surface, not a cosmetic one.

**Regression shape (mandatory):** a test parses `why-ranked` output, recomputes
the score from the printed components and weights ALONE, and asserts exact
equality with the returned score — under P0 and P2 profiles, over a corpus with
salience/intent/novelty non-zero. What an auditor can only verify by hand becomes
a permanent test.

## R48 — Untrusted-input pre-flight guards before heavy extraction (from H5)

**Mesh attachments are untrusted by definition** — that is the premise of the
whole sandbox posture. Any derivative pipeline that runs a resource-heavy or
external extractor over attachment/mesh content MUST run pre-flight guards BEFORE
any extractor executes: a decompression-ratio limit, a declared-vs-actual
dimension check, a pixel-count ceiling, a page/frame ceiling, a file-size
ceiling, and content-SNIFFED MIME (never filename trust). Malformed, bomb, or
oversized input produces a clean `derivative.fail` with bounded memory — never an
OOM, never a hang. "Safe on trusted content only" is a self-cancelling condition
for a pipeline that consumes mesh content: the guards are what make the opt-in
safe to turn on at all.

---

# N9 Brief A run 2 fix rulings (live two-node, `ccdf1dc`) — J1–J4

## R49 — Parked events are retryable; referential dependencies self-heal (from J1)

F1 made `send --topic <new>` emit `topic.create` + `topic.link.add` atomically **on
the origin node** (R4.1). That atomicity is **local**: cross-node reconciliation
replicates events without guaranteeing the `topic.create` is projected before the
`topic.link.add` that references it. The live two-node run reproduced a
`topic.link.add` originating on NODE-B replicating to NODE-A and **parking on A's
reindex** with `FOREIGN KEY constraint failed` — the referenced topic did not yet
exist in A's projection. Doctor and gates correctly went red (R6). The message stayed
searchable, so it is a projection-**consistency** failure, not data loss — but an
acknowledged topic link was permanently unprojectable on the peer. The P0
topic-poisoning class (F1) is alive **across the network**; atomic-on-origin is not
atomic-across-the-mesh. This is a spec gap in reconciliation, not only a bug.

**Ruling:**

1. A projection failure caused by a **missing intra-mesh reference** (a FOREIGN KEY on
   a not-yet-projected `topic.create`, or any dependency that may still arrive on a
   later event) **parks as RETRYABLE**, not terminal. A parse error, a schema
   violation, or any failure that no future event can satisfy parks as **terminal**
   (genuine corruption).
2. When a later event that could satisfy a parked event's dependency is projected, the
   projector **re-attempts the retryable parked events** — a bounded retry sweep run
   after each reconcile batch, at the end of a reindex/replay, and after each live
   append. A retryable event that now projects clears from `parked_events`; one that
   still fails stays parked (retryable), to be swept again when the next event lands.
3. `doctor`/`gates` distinguish **retryable-pending** (informational within a grace
   window — the dependency may still arrive during active sync; NOT a zero-loss
   failure on its own) from **terminal** parked events (always a failure) and from
   retryable events that have exceeded the grace window (a dependency that never
   arrived — a failure). The grace window is a config constant
   (`ParkedRetryableGrace`) measured from `parked_at`; a transient ordering gap during
   active sync is not a red gate, but a dependency that never arrives eventually is.
4. **Defense at the source (preferred, cheap):** reconciliation applies an origin's
   events in **per-origin sequence order**, never by arrival order — a `topic.create`
   at sequence N is applied before the `topic.link.add` at N+1 because they are already
   in sequence order on the origin. R49.4 is the ordering defense; R49.1–3 are the
   robust self-heal for genuinely out-of-order or partial arrival (the adversarial case
   where the link is delivered/applied before the create).

**Regression shape (mandatory, CROSS-NODE):** two mesh nodes; node B does
`send --topic brandnew "…"`; the events replicate to node A and A reindexes; assert A
projects the link cleanly — OR parks it retryable and then **self-heals** once the
create is applied. Then reindex A → doctor A exit 0, gates zero-loss PASS. The
adversarial ordering case (the link applied before its create) is exercised explicitly
and must self-heal. A single-node stand-in does NOT satisfy this ruling.
