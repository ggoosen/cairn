# Agent Mesh — Design Brief v0.2

**Status:** Revised after three independent LLM reviews (ChatGPT, Grok, Gemini) of v0.1.
**What changed:** v0.1's two architectural bugs are fixed (universal-search/replication contradiction; editable-markdown vs immutability incoherence). The self-organising knowledge ecology (shared hierarchical map, stigmergic tithe, global salience propagation) is cut from v1 and deferred behind evidence. The product is now defined as the **retrieval contract**; the mesh is the delivery mechanism. See §9 for the full decisions changelog, including rejected recommendations and rationale.
**Ask of reviewers (round 2):** v0.1's weak points are resolved or deferred — please do not re-litigate them (see §9). Attack the *new* weakest joints: the event model, the capability model, the additive ranking constants, the P0 success metrics, and the open questions in §8.

---

## 1. The Problem

A person working with multiple AI agent sessions (across tools, machines, and networks) constantly produces information in one session that another session needs: decisions, research findings, files, images, code context, task state. Today this moves by manual copy-paste or file shuffling. This is:

- **Inefficient** — the human is the message bus.
- **Lossy** — context degrades in transit; provenance is lost.
- **Non-compounding** — the same knowledge is re-explained to each new session.

There is no lightweight, private, agent-agnostic way for agent sessions to send each other messages and artifacts, and for any session to *find* relevant prior communication when it needs it.

## 2. Product Definition

> **An agent asks for what it needs and receives a ranked, provenance-preserving answer within a hard context budget, while retaining the ability to fetch original material deliberately.**

That retrieval contract — budget-capped digests, hybrid search, progressive disclosure, inspectable ranking — is the product and the differentiator. Everything else (transport, replication, maintenance) exists to serve it. Formally:

- **Substrate:** an append-only personal message and knowledge log with peer replication and lazy binary artifacts.
- **Contract:** `digest`, `search`, `peek`, `fetch` — each rung deliberate and priced; agents are never forced to ingest a raw inbox.
- **Deferred:** self-organising knowledge maps, autonomous maintenance economies, and multi-human federation are P4 candidates, built only if P0–P2 usage data demands them.

## 3. Requirements

### Functional
1. **Send/receive** messages (text) and artifacts (files, images) between agent sessions on different machines/networks. Messages support **revision** and **retraction** (as new events; history preserved).
2. **Pub/sub with opt-in/opt-out** — subscriptions by topic, sender, priority floor; symmetric mutes.
3. **Semantic subscriptions** — standing natural-language interest queries, matched by embedding similarity with calibrated (not static) thresholds.
4. **Universal search (pull)** — any node can search the full **text** corpus locally, because all message text replicates to all trusted nodes (see §5.3). Subscriptions govern push; search governs pull. Binary artifacts are searchable via their extracted-text/summary derivatives.
5. **Ranking/salience** — digest views are ranked, not chronological; ranking is additive, bounded, and inspectable (`why-ranked` prints the arithmetic).
6. **Whole-library portability** — backup/move/inspect is trivial: the store is a folder; markdown **exports** are generated on demand and are always faithful to the log.

### Non-functional
7. **Context economy** — retrieval is budget-capped. Budgets are specified as `budget_chars` (universal) or `budget_tokens` + named tokenizer; final truncation uses the target tokenizer when known.
8. **Simplicity** — single binary, one operator deploys it; normie onboarding (P3) is a one-time, expiring pairing invitation over iroh. Note: iroh's public relays are rate-limited, so fully infra-free onboarding carries a soft operational dependency unless a relay is self-hosted.
9. **No mandatory MCP** — a filesystem surface allows any agent that can read/write files to participate.
10. **Distributed, no single point of failure** — any connected subset of nodes functions fully for text; blob availability follows durability class (§5.3).
11. **Private and secure** — E2E-encrypted transport, mutually authenticated devices, application-layer membership verification, at-rest encryption for index and blobs, documented threat model (§6).
12. **Selective persistence** — all **text** (envelopes + bodies) replicates to every trusted node; **binary blobs** replicate lazily by content hash according to durability class.

## 4. Prior Art (updated)

| System | Gives | Gap |
|---|---|---|
| MCP Agent Mail over Tailscale | Working agent mailbox today | Hub-shaped; chronological; MCP-only; whole-message delivery → context bombs |
| **iroh** (core **v1.0 stable**, June 2026; companion protocols blobs/gossip/docs assessed individually) | Dial-by-public-key QUIC, hole-punching + relay fallback, content-addressed blobs, gossip | No semantic layer; public relays rate-limited; endpoint auth ≠ application authorisation (app must decide who may connect) |
| Karpathy llm-wiki pattern | Flat md + index-with-summaries navigates large corpora cheaply; folder = export | Single-writer, no transport, no push semantics |
| Local RAG stacks | Semantic search | No messaging, no push, no budget-capped digest contract; infra-heavy |
| Syncthing / git as transport | Trivial folder sync | No delivery semantics, no lazy blobs, manual conflicts |

**Shared gap:** none offers the retrieval contract (§2). That layer is the differentiation; transport is deliberately commodity.

## 5. Architecture

### 5.1 Canonical layer: the immutable event log

Markdown is the **export and authoring format, not the synchronisation substrate**. The canonical store is an append-only log of signed, immutable events:

```
mesh/
├── events/
│   └── <origin-device-id>/
│       └── <segment>.jsonl        # packed segments, not per-message files
├── objects/
│   └── <blake3-hash>              # binary blobs, content-addressed
├── exports/                       # GENERATED markdown, regenerate at will
├── views/                         # GENERATED agent surfaces (§5.11)
└── .mesh/
    ├── index.sqlite               # DERIVED projection: FTS5 + vectors + state
    ├── vectors/
    └── node-identity/             # device key, membership certs
```

Event skeleton: `event_id, mesh_id, origin_device_id, origin_sequence, previous_event_hash, event_type, wall_ts, logical_ts, principal_id, object_hash?, authorisation_scope, schema_version, signature`.

Event types: `message.publish`, `message.revise`, `message.retract`, `topic.link`, `topic.unlink`, `blob.pin/unpin`, `subscription.set`, `signal.emit`, `member.add`, `member.revoke`.

Consequences:
- **Dual-write problem dissolved.** SQLite is a pure projection replayed from the log; corrupt or delete it and `mesh reindex` rebuilds. (SQLite's own docs warn separately-maintained FTS content/index can drift; projection-from-log plus periodic integrity checks and full-rebuild capability is the mitigation.)
- **Edits are events.** The filesystem `outbox/` is an *input*: the daemon ingests atomically (write-temp → rename, validation, size limits, quarantine for malformed input, receipt file with resulting message ID). A hand-edit to an exported file is interpreted as a `message.revise` authored by the operator principal (permissive local flavor); anything arriving over the network with a broken hash/signature is quarantined (strict flavor).
- **Retention without breaking replay:** compaction produces summary *views*; source events are never deleted, only packed into cold segments.

### 5.2 Replication and reconciliation

- **Full text replication.** Every trusted node receives all events including message bodies. Text is small at personal scale; this makes universal search real and local. (v0.1's envelope-only replication could not satisfy universal search — fixed.)
- **Lazy binaries with durability classes:** `ephemeral` (origin only) / `normal` (replicated to ≥2 nodes — the default) / `important` (all operator-owned or designated archival nodes) / `pinned` (explicit policy). A durable send acknowledges only when its replication target is met, or the UI shows "replication pending."
- **Reconciliation:** per-origin contiguous sequence + previous-event-hash chain + signature. Peers exchange highest-contiguous frontier, known gaps, periodic segment/Merkle roots, and request missing ranges. High-water marks alone are insufficient (gaps, restored-backup forks, corruption); hash-chain + gap detection covers these without a general CRDT. Delivery is at-least-once with idempotent ingest (event_id dedup).
- **Fork handling:** two events claiming the same (origin, sequence) with different hashes = fork alarm, surfaced to the operator; the mesh quarantines the shorter/unsigned side rather than guessing.

### 5.3 Identity and capabilities

Transport auth ≠ mesh authorisation. Tailscale ACLs are network-level; iroh authenticates endpoint keys but delegates admission to the application. The daemon therefore maintains:

- **Mesh identity** (mesh_id, operator root key), **device certificates** signed by the root, **revocation events** (`member.revoke`; revocation stops future access — it cannot un-disclose already-replicated data).
- **Agents are local principals, not network members.** The daemon owns the device key. Each agent session gets a scoped capability: e.g. `search-only`, `search+fetch`, `send:topic/*`, `no-blob-access`, `no-structural-ops`. Principal hierarchy: **operator → project/task → agent instance** (load-bearing for salience, §5.8).
- **Pairing** (P3): high-entropy, one-time, expiring invitation (or PAKE), never a reusable password.

### 5.4 Message model

- **Publish event** carries: sender principal, recipients/topic links, priority (0–3, bounded influence), tags, body (markdown, replicated), blob refs `{hash, size, mime, filename, extracted_text?}`, thread_id.
- **Summaries** are stored with provenance: `{text, author (sender|receiver|extractive-fallback), method/model, tokenizer, generated_ts, source_content_hash}`. Receiver verifies sender summaries by embedding distance to body; beyond threshold → local re-summarise and prefer local (§5.9).
- **All received text is untrusted content.** Every agent-facing surface delimits mesh content so a message body cannot silently become an instruction to the reading agent (prompt-injection boundary).

### 5.5 The agent-facing contract

```
digest(budget, scope=subscriptions)   # ranked rollup, hard budget — the default read path
search(query, scope=all|topic|sender, k)  # hybrid BM25+vector, RRF; returns envelopes+summaries
peek(msg_id)                          # envelope + summary
fetch(msg_id | blob_hash)             # full body / payload — deliberate
send(to|topic[], body, priority, tags, attachments[], durability)
revise(msg_id, body) / retract(msg_id)
signal(msg_id, kind)                  # operator-intent events
subscribe(...) / mute(...)
why_ranked(msg_id)                    # prints the exact ranking arithmetic
```

Filesystem, CLI, and MCP are thin adapters over these operations. Nothing else is exposed.

### 5.6 Knowledge organisation: links, not a tree

- **Stable topic IDs; multiple `topic.link`s per message.** A shed message links simultaneously to `roastery`, `shed-buildout`, `council`, `finance`. Adding a better link never requires moving anything; "misfiling" degrades to "missing link," which is additive to fix. Human-added links are protected from automatic removal.
- **Auto-linking is suggestion-tiered:** high confidence → attach; medium → attach top 2–3 candidates; low → leave unlinked in a visible suggestion queue. Auto-linking never blocks `send()`. The metric that matters is retrieval/subscription misses caused by missing links, not label accuracy.
- **Maps are local views, not shared state.** Each node derives its own navigation map (rollups, groupings) from replicated links + local embeddings. LLM summaries and embedding classifications are non-deterministic, so v0.1's "derived state eventually agrees" claim was wrong; making maps local renders divergence harmless and removes the split/merge convergence problem entirely (no coordinator, no lowest-node-ID election — the question is deleted, not answered).
- **Saved searches are virtual branches.** Explicit topic links + hybrid search + saved searches deliver the "join and orient" experience; a joining node syncs the log and derives its own map.

### 5.7 Maintenance: asynchronous debt, not a tithe

- Foreground operations **enqueue** maintenance debt (file suggestions, stale rollups, `/_loose` items); they never execute it. A background worker services the queue when idle, with strict CPU/time/token limits; heavy jobs are cancelable and idempotent (deterministic chore IDs; duplicate execution across partitions is harmless by design — no leasing).
- **Backpressure:** if debt exceeds a threshold, the daemon may reject non-critical *sends* until it catches up — throttling mess creation without ever stalling a read.
- Skippable under CPU/battery/latency pressure; disabled during rapid agent tool loops.

### 5.8 Salience and ranking (additive, bounded, inspectable)

**Eligibility gate, then additive rank** (v0.1's multiplicative form had annihilation and calibration pathologies):

```
eligible = explicit_recipient OR pinned OR relevance ≥ subscription_threshold

rank = a·relevance + b·bounded_salience + c·freshness + d·operator_intent + e·novelty
       − duplicate_penalty − thread_saturation_penalty
```

with per-thread caps, diversity (MMR), mandatory inclusion of explicit-recipient items, a small exploration quota for new items, and a hard cap on declared-priority contribution (carries cold-start, decays fast unless validated).

**Salience inputs** (weighted counters over logged events; no learned model):
- *Declared* (weak): sender priority, tags.
- *Demand* (strong): peek→fetch→blob depth, re-fetch across sessions — **exposure-normalised with a smoothed posterior**: `fetch_rate = (fetches+α)/(impressions+α+β)`, engagement weight ramps in only after `minimum_impressions`, and new items get an exploration bonus until then. No negative judgment before qualified impressions.
- *Demand is clustered by principal hierarchy:* three agents in one orchestration run fetching the same context = one demand cluster; repeated retrieval across separate operator tasks = meaningful; automatic prefetch = impression, not demand.
- *Reference graph* (strongest): replies, citations, onward blob attachment; in-degree with optional one-hop propagation.
- *Operator signals* (highest intent, additive with slow decay — **not** multiplier locks): emitted via `signal()`; deduplicated per principal/item/kind/session, per-principal caps, agent trust weights, orchestration-run dedup, negative signals weighted strongly. High-value actions (successful fetch, paste-into-context) auto-emit low-weight signals so sparsity is solved by ergonomics, not exhortation. Salience becomes per-principal if a second human ever joins.

**Embeddings:** local ONNX/Ollama model; start MiniLM-class, upgrade path to nomic-embed-text / BGE / mxbai-embed-large (all local, few hundred MB) if technical-content retrieval underperforms. Semantic subscription thresholds are calibrated per subscription: hard filters first, then percentile/top-N-per-window over the observed similarity distribution, margin over next-best branch, positive/negative feedback corrections, and a max-pushes-per-period cap. The raw natural-language query is stored (not just its vector) so any node can re-embed with its local model.

### 5.9 Interfaces (context-bomb-proof)

```
views/<agent>/
├── digest.md          # ranked, budget-capped — regenerated
├── map.md             # local navigation view
├── search-results/    # materialised on request
├── fetched/           # bodies appear here ONLY after explicit fetch
└── outbox/            # drop files to send; daemon ingests atomically
```

The filesystem surface never contains every message body — an agent that globs the directory ingests only what was deliberately fetched. CLI: `mesh send|digest|search|fetch|why-ranked|reindex`. MCP server exposes §5.5 verbatim with capability scoping.

## 6. Security Model

- **v1 trust model: personal mesh = one trust domain.** Every admitted device sees all text. Full-text replication *is* an access-control decision, stated explicitly. No per-topic ACLs in v1; a second human means authorised namespaces / per-channel content keys / key epochs / signed membership — designed later, deliberately (see §9 deferrals).
- At-rest: OS-level or file-level encryption for `objects/` and especially `.mesh/index.sqlite` (plaintext derivatives live there).
- Transport: Tailscale with restrictive ACLs (+ consider Tailnet Lock against control-plane key insertion); iroh later. Application-layer membership verified regardless of transport.
- Self-hosted relays are a patching responsibility (iroh shipped a relay DoS fix in 1.0.2) — noted in the P3 operational docs.
- Prompt-injection boundary per §5.4: mesh content is delimited untrusted data on every surface.

## 7. Phasing

**P0 — Useful local product (single machine).** Immutable event store, blob store, SQLite FTS+vector projection, CLI, filesystem outbox + digest/fetch views, `search/digest/send/fetch/revise/retract`, explicit topics + pins. **No map, no maintenance economy, no behavioural salience** — ranking is relevance + recency + declared priority only.
**P0 success metrics (measured against today's copy-paste workflow):** context tokens consumed per cross-session handoff; time-to-find relevant prior work; % of handoffs that stop involving manual copy-paste. Benchmarks at 10k/100k/1M messages: cold rebuild time, incremental index latency, search P95, backup/restore, disk/memory.

**P1 — Trusted personal mesh.** Tailscale transport, signed device membership, per-origin sequence/hash chains + gap reconciliation, full text replication, lazy blobs with default replication factor 2, MCP adapter, semantic subscriptions.

**P2 — Retrieval quality (driven by P0/P1 usage data).** Receiver summaries + distance-check verification, calibrated subscriptions, demand + reference salience signals, full additive ranking + `why-ranked`, saved searches, local maps and rollups, compaction views, async maintenance worker + backpressure.

**P3 — Onboarding & transport portability.** iroh 1.x, one-time pairing invitations, relay selection/self-host diagnostics, update/patching mechanism.

**P4 — Only if evidence demands.** Shared map operations (deterministic reducers over logged ops), automated filing beyond suggestions, split/merge proposals, multi-human namespaces, content-key rotation, salience propagation.

## 8. Open Questions (round 2 — attack these)

1. **Event schema completeness:** does the event-type set cover real workflows, or will `message.revise` semantics (what exactly is revisable — body only? links? priority?) and thread modeling need rework once agents use it in anger?
2. **Capability granularity:** is the proposed capability set (search-only … no-structural-ops) the right shape, and how are capabilities issued/rotated for ephemeral agent sessions without operator friction on every session start?
3. **Ranking constants:** the additive weights (a–e), decay rates, α/β priors, and exploration quota are unspecified. What's a principled initial setting, and what's the cheapest calibration loop that keeps the model inspectable?
4. **Full-text replication ceiling:** at what corpus size / node count does replicate-all-text break (mobile nodes? metered links?), and is the answer per-device retention windows over the same log?
5. **Fork recovery UX:** on a detected origin fork (restored backup), what's the operator's actual repair path? Quarantine-and-ask is stated; is there a safe auto-heal?
6. **Backpressure correctness:** rejecting sends under maintenance debt could drop important messages during bursts. Should backpressure queue-and-delay rather than reject, and where does that queue live?
7. **Extracted-text pipeline for blobs:** searchable derivatives of PDFs/images (OCR? captioning?) — in-scope for P1, or does blob search wait for P2? What's the local-model cost?
8. **P0 metric validity:** are the proposed success metrics actually measurable without instrumenting the agent tools themselves? What's the minimal telemetry that doesn't become its own project?
9. **Untrusted-content delimiting:** what concrete delimiting scheme reliably survives markdown rendering and agent tool-call round-trips (fenced blocks? sentinel tags? structural JSON)?
10. **Revision UX for exports:** operator hand-edits an exported file that has since been revised by an agent — the daemon sees a conflict between edit-base and head. Merge, fork-as-new-revision, or prompt?

## 9. Decisions Changelog (v0.1 → v0.2)

**Accepted — architectural:**
- Universal search vs envelope-only replication contradiction → **replicate all text, lazy binaries** (ChatGPT; the other two reviewers missed this bug).
- Editable-markdown canon vs immutability → **immutable signed event log; markdown = export; outbox = input; edits = revision events; tombstones for retraction** (ChatGPT; Gemini converged via the CRDT question).
- Hierarchical single-assignment tree → **stable topic IDs + multi-topic links + saved searches** (ChatGPT; Grok and Gemini converged via "flat tags first").
- Shared convergent knowledge map → **local map views; replicate only explicit links** (ChatGPT). Deletes the fold-oscillation problem rather than solving it.
- Synchronous tithe → **async debt queue + idle worker + limits + backpressure** (all three; backpressure from Gemini).
- Multiplicative ranking → **eligibility gate + bounded additive rank** (ChatGPT).
- Raw fetch-rate exposure normalisation → **smoothed posterior + exploration period** (all three, near-identical formulas).
- Agent network membership → **daemon owns device key; agents are capability-scoped local principals** (ChatGPT).
- Distinct-agent demand → **principal hierarchy (operator/task/instance) with demand clustering** (ChatGPT).
- Sender summaries trusted → **receiver verification by embedding distance, local re-summary on mismatch, provenance stored** (Gemini's distance-check mechanism; ChatGPT/Grok's always-re-summarise relaxed to on-mismatch).
- High-water marks → **sequence + hash-chain + signature + gap detection + Merkle segment roots** (ChatGPT).
- Blob availability → **durability classes with default replication factor 2** (ChatGPT; Grok/Gemini converged on pinning).
- Token budgets underspecified → **budget_chars or budget_tokens+tokenizer; summary provenance records** (ChatGPT).
- Inbox-as-directory context bomb → **views expose digest + explicitly-fetched only** (ChatGPT).
- Signal sparsity → **auto-emit low-weight signals on high-value actions; per-principal dedup and caps** (Grok + ChatGPT).
- Prompt-injection: mesh content = **delimited untrusted data** on all surfaces (ChatGPT).
- iroh status corrected: **core 1.0 stable (June 2026)**; public relays rate-limited; relay self-hosting = patching duty (ChatGPT).
- Embedding upgrade path: **nomic-embed / BGE / mxbai via Ollama/ONNX** (Grok).
- Phasing: **P0 local product first, measured ruthlessly before networking** (all three).

**Rejected — with rationale:**
- *Lowest-node-ID split rights* (Gemini) and *temporary coordinator on reconnect* (Grok): both reintroduce leader election into a leaderless design; local maps remove the coordination question entirely.
- *Operator signal as salience multiplier-lock* (Gemini): reintroduces multiplicative pathology (stale "important" dominates forever); operator intent is the heavyweight additive term with slow decay, plus separate explicit pins.
- *Markdown-canonical with watcher + eventual consistency* (Grok): incompatible with replayability and signed replication; superseded by event sourcing. The permissive edit-as-revision-event ergonomic (Gemini) is retained for local operator edits only.
- *Defer semantic subscriptions to late phases*: retained at P1 — they are core product (push economy), not research.

**Deferred to P4 (not abandoned — the event-sourced foundation makes them addable without migration):** shared map reducers, stigmergic maintenance economy, salience propagation, automated filing beyond suggestions, multi-human namespaces/key rotation.
