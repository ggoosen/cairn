# Agent Mesh — v0.3 Implementation Specification

**Status:** Implementation-ready spec. Consolidates two review rounds (ChatGPT, Grok, Gemini × 2). The architecture is considered settled: round two produced boundary-hardening and constants, not reversals. This document is what gets built.
**Lineage:** v0.1 (concept) → v0.2 (architecture: event sourcing, retrieval contract, full-text replication, local maps) → v0.3 (this: identity separation, event schema, capability tiers, ranking profiles, P0 gates).
**Reading guide for reviewers (round 3):** §13 lists the open questions. §14 is the changelog with per-reviewer attribution. The spec deliberately describes the *target* schema; §12 defines the minimal P0 subset that ships first.

---

## 1. Product Definition

> **An agent asks for what it needs and receives a ranked, provenance-preserving answer within a hard context budget, while retaining the ability to fetch the original material deliberately.**

- **Substrate:** an append-only, signed, per-origin event log with peer replication, lazy binary artifacts, and derived local indexes.
- **Contract:** `digest`, `search`, `peek`, `fetch` — progressive disclosure; each rung deliberate and priced. Agents are never handed a raw inbox.
- **Everything else** (transport, replication, maintenance, salience) serves the contract. Self-organising knowledge ecology remains P4, gated on evidence.

## 2. System Overview and Trust Model

- One **daemon per machine**. The daemon owns the device identity, the event log, the index, and all interfaces. Agents are **local principals**, never network members.
- **v1 trust model: personal mesh = one trust domain.** Every admitted device replicates all canonical text. This is an explicit access-control decision, not an optimisation. Multi-human federation (namespaces, per-channel keys, key epochs) is out of scope until P4.
- Transport: Tailscale (P1) → iroh 1.x (P3). Transport authenticates endpoints; the daemon independently verifies mesh membership at the application layer.

## 3. Identity and Key Architecture

**The v0.2 bug this fixes:** device private keys and sequence state lived inside the portable folder. Copying the folder cloned the signer → two valid writers on one origin → an unresolvable fork. Identity is therefore separated from data.

### 3.1 Three-way separation

```
PORTABLE MESH DATA (copy/backup/sync freely)
├── events/            # signed event segments
├── objects/           # content-addressed blobs & text objects
├── exports/           # generated markdown
└── views/             # generated agent surfaces
    (may include: public device certs, revocation history, encrypted recovery metadata)

DEVICE-LOCAL SECURE STATE (never in backups; OS keychain / restricted dir)
├── device private key
├── device certificate
├── origin sequence state (generation, next_sequence)
└── local capability profiles & session handles

OPERATOR RECOVERY MATERIAL (offline / separate secure storage)
├── mesh root key (or Shamir recovery shares)
└── recovery instructions
```

### 3.2 Rules

- **A raw folder restore ALWAYS creates a new origin identity.** The daemon detects data-without-matching-local-identity and refuses to write under the old origin.
- **Device migration is a ceremony:** `mesh device migrate` = (1) enrol new device identity on target, (2) sync data, (3) revoke old device certificate (`device.revoke`), (4) preserve old origin log as history, (5) continue under new origin.
- **Origin generation:** each (device, enrolment) gets a generation number; sequence numbers are scoped to (origin_device_id, origin_generation). A restored backup that somehow writes anyway forks on a *dead generation* and is trivially quarantined.
- Root key signs device certificates; root rotation is a first-class event (`root.rotate`).

### 3.3 At-rest encryption (v0.2 contradiction fixed)

Event segments contain the full plaintext corpus; encrypting only `objects/` and the index was incoherent. **P0/P1 requirement: the entire mesh directory resides on an encrypted volume (FileVault/LUKS/BitLocker) — enforced by a startup check with an explicit operator override.** Payload-level encryption (mesh data key wrapped per device) is deferred to P4/multi-human. The spec claims application-level at-rest encryption only when segments are covered.

## 4. Canonical Event Log

### 4.1 Event envelope

```
schema_version, mesh_id
event_id, event_type
origin_device_id, origin_generation, origin_sequence, previous_origin_event_id
actor_principal_id, actor_task_id?, actor_agent_instance_id?
object_type, object_id
causation_event_id?, correlation_id?, parent_event_ids[]
wall_time, observed_time?
payload_schema, payload
signing_key_id, signature
```

Distinct identities are never conflated: the logical object (`object_id`), the revision (in payload), the event that created it (`event_id`), the content bytes (content hash in payload), the signer (`signing_key_id`), and the acting principal (`actor_*`).

### 4.2 Canonical serialization and signing

- Events are serialized with **RFC 8785 canonical JSON** (or deterministic CBOR — decide at implementation; canonical JSON preferred for inspectability).
- `canonical_bytes = canonicalise(event minus event_id minus signature)`
- `event_id = BLAKE3("agent-mesh-event-v1" || canonical_bytes)` — domain-separated.
- `signature = Sign(device_private_key, mesh_id || event_id)`

### 4.3 Physical segment format

JSONL is the *export* format, not the record format (interrupted appends leave partial lines).

- **Record:** length-prefix + canonical bytes + checksum; append = write-temp record, checksum, fsync, atomic commit.
- **Open segment:** current append target. **Sealed segment:** immutable, carries header {first_seq, last_seq, event_count, root_hash}. Replication reasons about events and ranges, never raw filenames.
- `mesh export --jsonl` produces human-inspectable JSONL from segments at will.

### 4.4 Event families (v0.3 canonical set)

```
Mesh & security:   mesh.genesis, device.add, device.revoke, device.rotate,
                   root.rotate, device.fork.resolve
Messages:          message.publish, message.revise_body, message.retract,
                   message.restore, message.reply
Organisation:      topic.create, topic.rename, topic.alias, topic.archive,
                   topic.link.add, topic.link.remove
Blobs/derivatives: blob.pin, blob.unpin,
                   derivative.publish, derivative.fail, derivative.invalidate
Subscriptions:     subscription.create, subscription.update (base-revision
                   optimistic), subscription.disable, subscription.delete
Signals:           signal.emit
```

Threads are **emergent**: the first publish carrying a new `thread_id` implicitly creates the thread; `message.reply` records the reference edge. `thread.*` lifecycle events are added only if usage shows pain (schema_version makes this non-breaking).

### 4.5 Stream classes (one log, four lifecycles)

| Class | Contents | Replication | Retention |
|---|---|---|---|
| Permanent knowledge | publish/revise/retract/restore/reply, topic links, explicit operator signals, blob refs & durability intents | All full nodes | Forever (cold-packed) |
| Permanent security log | genesis, device add/revoke/rotate, root rotate, fork resolutions | All nodes | Forever |
| Control state (compactable) | Durable subscriptions, durable mutes, pin policies | All full nodes | Compacted to current state |
| **Local telemetry (NOT events)** | Impressions, result positions, peeks, fetches, reformulations, latency, session-scoped subscriptions | **Never replicated, never signed** | Local TTL |

Behavioural salience (P2) is computed locally from telemetry; if cross-node salience is ever needed, compact signed **aggregate counters** replicate periodically — never raw impressions. Agent-session subscriptions are local by default; only explicitly durable operator/project subscriptions become events.

## 5. Message Model

### 5.1 Revision DAG

```
message_id               # stable logical message
revision_id              # immutable revision object
parent_revision_ids[]    # [] = initial publish; [one] = revision;
                         # [two] = merge (conflict resolution)
body_hash → content-addressed text object (full body per revision; no patches)
author_principal_id, created_by_event_id
```

- Full-body-per-revision (deduplicated by content addressing); patch chains are forbidden — replay must never depend on every prior patch being valid.
- Subscriptions and retrieval evaluate against **head revisions**. Demand/reference signals remain attached to the revision they targeted; the supersedes edge is part of the reference graph.

### 5.2 What is revisable (narrow, explicit)

- `message.revise_body` changes body only.
- Topics change via `topic.link.add/remove` — never inside a revision.
- **Sender priority is immutable testimony.** Later importance is an operator `signal.emit` or a pin — history is not rewritten.
- Tags: immutable descriptive claims in the publication; evolving categorisation uses topic links.
- Front-matter edits to exports are **decomposed on ingest**: body diff → `revise_body`; topic delta → link events; pin delta → pin events; attempted priority rewrite → rejected or converted to an operator signal. One edit never silently mutates multiple semantic dimensions.

### 5.3 Retraction

> Retraction targets the **logical message**, hides all revisions from default retrieval, preserves full history, is excluded from digests and search unless `include_retracted` is explicitly capability-gated, and cannot be undone by an ordinary revision. Restoration requires an operator-authorised `message.restore`.

Recipients who already fetched a retracted message see the retraction in thread context on next access.

### 5.4 Text classes and the inline limit

**"Text is small" is false for agents** (trace logs, DOM dumps, reasoning trees can reach hundreds of MB/day). Therefore:

- **Inline body limit: 64 KiB** (config ceiling 256 KiB). Larger text becomes a content-addressed **text object** with class:
  - `eager-searchable-text` — replicates eagerly to full nodes (universal search preserved), indexed, but does not bloat event records.
  - `ephemeral-text` — scratchpads, raw logs, intermediate reasoning: local TTL (default 7 days), gossiped only to actively connected peers, indexed locally, never counted as canonical knowledge.
  - `canonical-text` — conclusions, decisions, human-authored notes: replicates everywhere, forever.
- Senders declare class; daemon policy can downgrade suspicious bulk (e.g. >1 MB bodies default to ephemeral unless flagged).
- Decompression-bomb / recursive-archive protections on all ingested objects.

### 5.5 Concurrent-update semantics (observed-remove, no CRDT library)

- **Topic links:** `topic.link.add {link_id, message_id, topic_id}`; `topic.link.remove {removed_link_ids[]}` retracts only *observed* assertions — a concurrent new link survives. Human-added links carry a protected flag (auto-processes may not remove).
- **Pins:** `blob.pin {pin_id, principal_id, blob_hash, durability}`; `blob.unpin {pin_ids[]}`; a blob stays pinned while any active pin intent exists.
- **Subscriptions:** create/update/disable/delete with `base_revision_id` optimistic concurrency on update.

## 6. Replication

### 6.1 Node roles (honest contracts)

- **Full node:** complete event log + complete canonical/eager text corpus; full local search; eligible durability replica.
- **Thin node** (mobile/metered): recent text window + selected cached objects; search = local recent + remote query dependency; **no offline universal-search guarantee**; not counted toward durability unless it actually holds the object. A thin node is never advertised as a normal node.

Universal search formally means: *any full node searches all canonical + eager text locally; ephemeral text within its TTL/horizon.*

### 6.2 Reconciliation

- Per-origin: contiguous sequence + previous-event-id chain + signatures. Peers exchange highest-contiguous frontier, known gaps, sealed-segment root hashes, and request missing ranges. At-least-once delivery; idempotent ingest by event_id.
- Verify hash + signature **before** indexing.

### 6.3 Blob durability classes

`ephemeral` (origin only) / `normal` (≥2 nodes — default) / `important` (all operator-owned or designated archival nodes) / `pinned` (explicit). A durable send acknowledges replication state explicitly: `indexed → replicated k/n → durability_satisfied`.

### 6.4 Fork handling

Automatic: invalid signature → quarantine; failed hash/truncation → quarantine; missing range → request. **Non-automatic** (same origin+generation+sequence, different hashes, both validly signed = identity clone/equivocation):

`mesh doctor fork <origin>` shows common ancestor, per-branch unique events with human-readable summaries, which peers advertise each branch, and whether either branch contains security operations. Repair: freeze the origin → enrol the physical device under a new identity → operator chooses (A canonical | B canonical | preserve both as recovered histories) → useful events from the losing branch reissued under a recovery origin with `recovered_from_event_id` + `fork_resolution_id` → root-signed `device.fork.resolve` → revoke cloned certificate. **The losing branch is never silently deleted.**

## 7. Agent Interface

### 7.1 Operations

```
digest(budget, scope)      search(query, scope, k)     peek(msg_id)
fetch(msg_id|object_hash)  send(...)                   reply(msg_id, ...)
revise_body(msg_id, body)  retract(msg_id)             signal(msg_id, kind)
subscribe(...)/mute(...)   why_ranked(msg_id)          link(msg_id, topic)
```

Budgets: `budget_chars` (universal) or `budget_tokens` + named tokenizer; final truncation with the target tokenizer when known. Filesystem, CLI, MCP are thin adapters; nothing else is exposed.

### 7.2 Capability model

**Positive grants only** (no negative labels — deny/allow precedence ambiguity eliminated):

```
capability = {
  subject, actions[], resource_selectors[], constraints, expiry,
  issuer, capability_profile_version
}
actions:    digest.read, search.read_metadata, message.peek, message.fetch_body,
            blob.fetch, message.send, message.revise_own, message.retract_own,
            topic.link_own, subscription.manage_self, signal.emit,
            history.read, admin.device, admin.policy
selectors:  topic="project/x/*", sender=self, message_owner=self,
            blob_mime in [...], thread_id=...
constraints: expires_at, max_search_results, max_digest_chars, max_blob_bytes,
            max_send_body_bytes, allowed_recipient_topics, rate_limit,
            durability_ceiling
```

**Issuance (low-friction):** capability **profiles** + trusted launcher:

```
mesh run --profile coding-agent -- claude
```

launcher: creates task_id + agent_instance_id → mints a short-TTL, non-delegable session handle bound to the process/local endpoint → applies the profile → auto-revokes on exit/idle. MCP binds to a **Unix domain socket** (SO_PEERCRED identifies user; the launcher-bound handle identifies the *session*).

**Isolation assurance tiers (documented honestly):**

1. **Cooperative** — agents share an OS user: capabilities prevent accidents, not a malicious local agent.
2. **Process-contained** — daemon-launched, handle bound to process tree, validated per connection.
3. **Strong** — separate users/containers/microVMs; capability material is unstealable. (May be inherited from the sandboxing layer of the orchestrator rather than rebuilt here.)

P0 simplification: filesystem + CLI only under operator trust (tier 1); capability enforcement activates with the MCP adapter and any network sender (P1). Macaroons noted as the future path if attenuated delegation is needed; opaque daemon-side handles suffice for P0–P2.

### 7.3 Filesystem surface (context-bomb-proof, provenance-separated)

```
views/<agent>/
├── digest.md                       # ranked, budget-capped, regenerated
├── map.md                          # local navigation view
├── search-results/
├── fetched/
│   ├── <message-id>.manifest.json  # provenance, trust=untrusted, hashes, revision ids
│   └── <message-id>.body.md        # ONLY the retrieved body
├── conflicts/<message-id>/         # BASE.md, CURRENT.md, OPERATOR_EDIT.md, RESOLVE.md
└── outbox/                         # drop to send; atomic ingest; receipt file returned
```

- Bodies appear in `fetched/` only after explicit fetch. Manifest and body are separate files: authoritative metadata and retrieved content never share a document.
- Digest/map lines that quote mesh content prefix every quoted line with `> [MESH]` (per-line prefixing cannot be escaped by in-line content; block sentinels can).
- Exports carry front-matter: `message_id, revision_id, base_revision_id, body_hash, exported_at` — enabling three-way merge on ingest (§5.2): clean merge → automatic machine-merged revision (flagged); conflict → `conflicts/` materialisation; never a blocking prompt, never a silent overwrite, gossip never pauses.

### 7.4 Untrusted content: presentation vs security

Structural delimiting (JSON envelopes with `trust:"untrusted"` + provenance over MCP; manifest/body separation and per-line prefixes on the filesystem) is **presentation, not a security boundary** — no delimiter reliably prevents prompt injection inside a model context. The actual controls:

- Retrieved content can never expand an agent's capabilities.
- A message cannot invoke a daemon operation; sensitive actions pass policy checks independent of the model.
- Content-derived requests to send/delete/reveal/fetch-private require explicit confirmation or a narrowly pre-authorised workflow.
- Proposed actions carry `tainted_by_message_ids[]` where practical.
- Nothing is auto-executed because a model repeated instructions found in a fetched message.

**The capability system, not the delimiter, bounds injection impact.**

## 8. Ingestion and Processing

### 8.1 send(): the log is the durable queue

1. Validate capability + size/class. 2. Append + fsync the publish event. 3. Return `accepted_locally` **immediately — the agent never sees rejection or delay** (agents cannot back off; a "busy" error causes retry loops and hallucinated workarounds). 4. Index, embed, summarise, replicate asynchronously; expose state: `indexed → replicated k/n → durability_satisfied`. Derived work queues are reconstructable from events lacking projection state — no separate durable queue exists.

### 8.2 Degradation ladder (under load, in order)

1. Delay auto-link suggestions → 2. delay receiver summaries → 3. delay embeddings → 4. serve lexical-only search (flagged) → 5. delay normal blob replication → 6. reject oversized/low-priority **blobs** → 7. reject small text only on disk/quota exhaustion. Reserved emergency capacity for small high-priority text always exists. Maintenance lag degrades enrichment; it never becomes message loss.

### 8.3 Derivatives (blob → searchable text)

P1 (deterministic, cheap): plain/source/markdown, sanitised HTML, PDFs with text layers, common office text. P2 (heavy, opt-in): OCR (Tesseract-class), image captioning, audio transcription, multimodal summarisation. Derivative record: `{blob_hash, derivative_type, text_hash, extractor_name+version, generated_at, language?, confidence?, status, error?}` via `derivative.publish/fail/invalidate`. **All parsers sandboxed:** MIME sniffing (not filename trust), size/page limits, CPU/memory/timeouts, no network, no macro/script execution. Derivative text is untrusted and cryptographically tied to its source hash.

### 8.4 Summaries: topical-consistency check (renamed honestly)

Embedding distance detects a summary about the *wrong topic*; it cannot detect a topically-faithful summary that **reverses the conclusion** ("do not deploy" vs "deploy"). Therefore: sender summary = untrusted claim; receiver computes local extractive snippets (+ optional local abstractive summary) with provenance `{text, author, method/model, tokenizer, ts, source_hash}`; a visible disagreement marker when sender and local diverge; **high-salience digest entries include one or two supporting excerpts, never a summary alone.**

## 9. Retrieval and Ranking

### 9.1 Two profiles (one formula fails one of them)

All features normalised to [0,1] first: `R` relevance (rank-percentile-transformed RRF over BM25+vector, so magnitudes survive retriever changes), `S` bounded salience, `F = 2^(−age/half_life)` freshness, `I` operator intent, `N` novelty, `P` = declared_priority/3. Freshness is always a **bonus decaying to zero — never a subtractive penalty** (subtractive decay annihilates old canonical material). Each duplicate/thread-saturation penalty capped at **0.15**. Explicit recipients and pins are **mandatory-inclusion rules**, not score bonuses.

**P0 (relevance, freshness, priority only):**

```
search_score = 0.90·R + 0.07·F(half_life=90d) + 0.03·P
digest_score = 0.60·R_subscription + 0.30·F(half_life=72h) + 0.10·P
```

**P2 (full model):**

```
search = 0.75·R + 0.08·S + 0.04·F + 0.10·I + 0.03·N
digest = 0.45·R + 0.15·S + 0.20·F + 0.15·I + 0.05·N
```

Declared priority: hard cap on contribution; decays within 48–72 h unless validated by demand/reference/operator signal.

### 9.2 Salience inputs (P2)

- Demand: exposure-normalised smoothed posterior `fetch_rate = (fetches+α)/(impressions+α+β)`, **α=1, β=4** (prior mean 0.20), minimum **5 qualified impressions** before any negative judgment, **10% exploration quota** for new items. A fetch is weak evidence (agents fetch wrong results while investigating); re-fetch across separate operator tasks is strong.
- Demand clustered by principal hierarchy (operator → task → agent instance): one orchestration run = one demand cluster; prefetch = impression, not demand.
- Reference graph: replies, citations, onward attachment, supersedes edges; in-degree + optional one-hop.
- Operator signals: additive heavyweight term with slow decay (never multiplier locks); deduped per principal/item/kind/session; per-principal caps; auto-emitted low-weight signals on high-value actions (successful fetch, paste-into-context).

### 9.3 Calibration (inspectable, not learned)

Collect per retrieval episode: results shown + ranks, peek/fetch, reformulations, explicit `mesh found <id>` / `mesh not-found` / `mesh manual-workaround`. After ≥30–50 genuine episodes: offline replay of logged `why_ranked` arithmetic under weight grids (0.05 increments), optimising Success@5, reciprocal rank of the chosen result, reformulations-before-success; **hold out entire tasks**, not random queries; adopt a change only if it survives the holdout and remains explainable. Constants live in one commented config; `mesh rank-stats` shows current per-term contribution distributions.

### 9.4 `why_ranked` example output

```
msg_01J9…  rank 2 in digest (profile=digest-P0)
  R_subscription  0.81 × 0.60 = 0.486   (BM25 pct 0.77, vec pct 0.85, RRF→pct 0.81)
  F(72h)          0.42 × 0.30 = 0.126   (age 21.4h)
  P               0.67 × 0.10 = 0.067   (declared 2/3, within decay window)
  penalties       −0.00
  mandatory: no   pinned: no   explicit_recipient: no
  total 0.679
```

## 10. Security Summary

Threat model documented in-repo. Personal mesh = one trust domain (§2). Keys separated from data (§3). Full-volume at-rest encryption enforced P0/P1 (§3.3). Transport: Tailscale restrictive ACLs + Tailnet Lock consideration; iroh endpoint auth + application-layer membership (P3); self-hosted relay = patching duty. Capabilities positive-grant, tiered honestly (§7.2). Injection bounded by capabilities, not delimiters (§7.4). Revocation stops future access; it cannot un-disclose replicated data. Pairing = one-time expiring high-entropy invitation or PAKE.

## 11. Telemetry and P0 Gates

**Product telemetry (local-only, never events):** per interaction — `interaction_id, task_id, agent_surface, query_or_digest, result_ids+positions, budget_requested, payload_returned, peek_ids, fetch_ids, reformulation_count, timestamps, outcome ∈ {found, not_found, manual_workaround, abandoned}`. Outcome via one command: `mesh found <id> | mesh not-found | mesh manual-workaround`. Metric renamed honestly: **mesh-delivered retrieval payload per successful handoff** (total model context consumption is not observable without instrumenting agents — don't).

**P0 exit gates (≥30 genuine cross-session handoffs across ≥3 agent surfaces):**

| Gate | Threshold |
|---|---|
| First-query Success@5 | ≥ 70% |
| Median time-to-useful-context | < 60 s or ≥ 50% below copy-paste baseline |
| Manual-workaround rate | ≤ 25% |
| Hard-budget compliance | 100% |
| Provenance (fetched → source event + content hash) | 100% |
| Acknowledged-event loss under crash/fault injection | 0 |
| Send-ack → digest-visible latency (local) | P95 < 200 ms (enrichment may lag) |

**Engineering scorecard (synthetic 10k/100k/1M events):** send-ack latency, incremental lexical index latency, incremental embedding latency, search P50/P95, `reindex --lexical` (fast, product stays usable) vs `reindex --semantic` (slow enrichment rebuild), backup/restore, disk/memory, fault-injection recovery.

## 12. Phasing

**P0 — local product (build list):** event store (envelope §4.1, canonical bytes §4.2, segments §4.3) with **minimal event subset**: `mesh.genesis, device.add, message.publish, message.revise_body, message.retract, message.reply, topic.create, topic.link.add/remove, blob.pin/unpin, signal.emit`; content-addressed object store + text classes + inline limit; SQLite FTS5+vector projection + `reindex --lexical|--semantic`; CLI + filesystem surface (outbox atomic ingest with receipts, digest/fetched/conflicts views, export front-matter + 3-way merge); P0 ranking profiles; telemetry + gates (§11); encrypted-volume check; identity separation + `device migrate`. **Explicitly absent from P0:** networking, MCP, capabilities beyond tier-1, semantic subscriptions, salience, maps, maintenance worker, derivatives beyond plain text.

**P1 — trusted personal mesh:** Tailscale; signed membership; reconciliation (§6.2); full canonical/eager text replication; lazy blobs, durability default `normal(2)`; fork detection + `mesh doctor`; MCP adapter + capability enforcement (profiles, launcher, Unix socket); deterministic derivatives (sandboxed); semantic subscriptions (calibrated thresholds, hard filters first, push caps); receiver summary consistency checks.

**P2 — retrieval quality (driven by P0/P1 data):** full additive profiles + salience inputs + calibration loop; saved searches; local maps + rollups (**structural** topic/thread/pin rollups — deterministic and embedding-free; the embedding-clustered *semantic* map is P4, where it can be trained on real P2 usage/salience data); async maintenance worker + degradation ladder tuning; compaction views; heavy derivatives (opt-in).

**P3 — onboarding/transport:** iroh 1.x; one-time pairing invitations; relay selection/self-host diagnostics + patching story; thin-node role.

**P4 — only if evidence demands:** shared map reducers, **embedding-clustered self-organising / self-folding knowledge maps** (the semantic map — needs P2 usage/salience data before it can be good; the P2 map is the structural precursor), automated filing beyond suggestions, multi-human namespaces, payload-level encryption + key epochs, salience propagation, macaroon-style delegation.

## 13. Open Questions (round 3)

1. **Serialization choice:** RFC 8785 JSON vs deterministic CBOR — inspectability vs compactness/robustness. Is dual-format (CBOR canonical, JSON export) worth the complexity?
2. **Origin-generation ergonomics:** does generation-scoped sequencing fully close the restored-backup hole, including the case where device-local state is restored *alongside* the folder (full-disk clone)? Is a liveness beacon (peers remember last-seen (generation, seq) and alarm on regression) needed?
3. **Text-class misuse:** senders (agents) will mislabel bulk as canonical. Are daemon downgrade policies + size heuristics enough, or does canonical-text need an operator-side quota/review queue?
4. **Two-profile ranking boundary:** subscriptions are digest-shaped, but `explore()`-style traversal (P2) is search-shaped with a budget — does it need a third profile or parameterised blending?
5. **P0 gate validity:** are Success@5 ≥70% and workaround ≤25% the right bars for *continuing*, and what specifically is falsified if P0 misses them — the product thesis, or just the P0 ranking constants?
6. **Conflict-view ergonomics:** will operators actually resolve `conflicts/` directories, or does unresolved-conflict debt need its own visibility (digest line? `mesh doctor conflicts`)?
7. **Session-handle binding strength (tier 2):** what precisely binds a handle to a process tree across exec/fork on macOS vs Linux, and what does the check cost per call?
8. **Emergency capacity sizing:** the reserved capacity for small high-priority text under degradation — how is it sized and how is "high-priority" authenticated against a misbehaving agent declaring everything urgent?
9. **Thin-node search UX:** when a thin node's query needs remote help, what is the latency/privacy contract, and how is partiality surfaced to the agent within the budget?
10. **Minimal P0 event subset audit:** does anything in the P0 build list secretly require an event type deferred to P1 (e.g., does 3-way export merge need `message.restore`)?

## 14. Decisions Changelog (v0.2 → v0.3)

**Accepted — round 2:**
- **Identity/data separation, migrate ceremony, restore→new-origin, generation-scoped sequences** (ChatGPT r2 — the round's critical catch; both other reviewers missed it).
- **At-rest encryption must cover event segments** → full-volume requirement P0/P1 (ChatGPT r2, fixing a v0.2 internal contradiction).
- **Revision DAG** (stable message_id, immutable revision_id, parent ids, merge revisions) — synthesis of Gemini r2's supersede semantics with ChatGPT r2's stable-identity model; resolves Grok r2's revise/subscription-versioning catch. **Sender priority immutable testimony** (ChatGPT r2).
- **Narrow event types** (revise_body only; links/pins as observed-remove assertions; subscription optimistic updates) (ChatGPT r2).
- **Four stream classes; telemetry never becomes signed events; session subscriptions local by default** (ChatGPT r2).
- **Text durability classes + inline limit + eager-searchable text objects** (Gemini r2's "text is small is fatal for agents" + ChatGPT r2's mechanism).
- **Full/thin node roles** instead of silent retention windows (ChatGPT r2; composes with Grok r2's searchable-horizon).
- **send() always succeeds; log = durable queue; degradation ladder; enrichment lags, messages never lost** (Gemini r2's "backpressure crashes agents" + ChatGPT r2's ladder; overrules Grok r2's "reject in P0").
- **Positive-grant capabilities + profiles + trusted launcher + Unix socket + honest isolation tiers** (ChatGPT r2 tiers + Gemini r2 socket + Grok r2 tokens, composed).
- **Two ranking profiles (search/digest), normalised features, freshness-as-bonus-only, penalty cap 0.15, mandatory-inclusion rules** with concrete P0/P2 constants (ChatGPT r2; Grok r2's calibration loop retained with stricter protocol).
- **α=1, β=4, 5 qualified impressions, 10% exploration; fetch = weak evidence** (ChatGPT r2 refining Grok r2; rejects Gemini r2's α=1/β=2 as too hot).
- **Summary check renamed topical-consistency; excerpts accompany high-salience summaries** (ChatGPT r2's reversal counterexample).
- **Delimiting = presentation; capabilities = security; tainted_by tracking; manifest/body file separation; per-line `> [MESH]` prefix on fs views** (ChatGPT r2 + Gemini r2).
- **Fork = identity clone; mesh doctor flow; never silently delete the losing branch** (ChatGPT r2 completing Gemini r1's conflict instinct).
- **Deterministic derivatives pulled to P1, sandboxed parsers, derivative events** (ChatGPT r2 correcting v0.2's blob-search deferral).
- **Honest metrics: mesh-boundary observables only, outcome commands, falsifiable P0 gates, product/engineering scorecard split, lexical/semantic reindex split** (ChatGPT r2 + Gemini r2's three daemon metrics incl. the 200 ms latency budget).
- **Record framing + sealed segments; canonical serialization + domain-separated hashing; JSONL demoted to export** (ChatGPT r2).

**Rejected — round 2, with rationale:**
- *Subtractive exponential decay penalty* (Gemini r2): recreates the annihilation pathology; freshness is a bonus decaying to zero.
- *Laplace α=1/β=2, new items start at 0.5* (Gemini r2): new items would open above corpus-average demand and flood digests; exploration quota provides visibility instead.
- *"MCP JSON framing naturally solves injection"* (Gemini r2): framing survives transport but the string still enters model context; see §7.4.
- *"Reject sends under debt, promote to queue later"* (Grok r2): rejection is never safe against agent callers; superseded by always-accept + async enrichment.
- *"P0 handoff metrics impossible without spyware"* (Gemini r2): overstated — mesh-boundary payload metrics + baseline diary suffice; but the honest rename was adopted.

**Carried from round 1 (settled, do not re-litigate):** full-text replication for canonical/eager text; event sourcing with markdown as export; multi-topic links; local maps; async maintenance; additive ranking; principal-clustered demand; receiver-verified summaries; durability classes; capability-scoped local principals; prompt-injection boundary; iroh core 1.0 status + relay caveats.
