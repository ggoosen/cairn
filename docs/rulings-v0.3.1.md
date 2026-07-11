# Agent Mesh v0.3.1 — P0 Build Rulings (Errata to v0.3)

**Status:** Binding rulings. Read alongside v0.3; where they conflict, this document wins. Produced from three independent buildability audits (ChatGPT, Grok, Gemini) against the §12 P0 scope. All three audits returned no-go pending author rulings; this document is the complete ruling set. **P0 is go.**

---

## 1. Cryptography and Serialization

- **Signatures:** Ed25519 for root and device keys. (Unanimous.)
- **Content addressing:** BLAKE3 for all hashes — event IDs, object IDs, segment roots, key IDs (BLAKE3 of canonical public-key bytes).
- **Canonical serialization: RFC 8785 canonical JSON only.** No CBOR, no dual format. **Floats are forbidden in all event payloads** — integers and strings only; timestamps are RFC 3339 UTC strings with fixed microsecond precision; priority is an integer. This removes the float-normalization signature-fork risk that motivated CBOR while keeping direct inspectability.
- **Naming disambiguation (fixes v0.3 §4.2/§4.3 ambiguity):**
  - `signing_bytes` = canonical event *excluding* `event_id` and `signature`.
  - `record_bytes` = complete canonical event *including* both.
  - `event_id = BLAKE3("agent-mesh-event-v1" || signing_bytes)`; `signature = Sign(device_key, mesh_id || event_id)`; segments store `record_bytes`.
- **Wall time never participates in ordering.** Ordering is (origin, generation, sequence).

## 2. P0 Event Payload Schemas (normative)

Versioned JSON Schemas ship in-repo for every P0 event type. Starter set (Grok's schema + ChatGPT's reply semantics):

- `mesh.genesis`: `{mesh_id, root_pubkey, initial_device_cert}` — root-signed initial device certificate embedded; event dual-signed by that device.
- `device.add`: `{device_id, pubkey, generation, cert}` — cert is root-signed; submitted by an already-admitted device.
- `device.revoke`: `{device_id, reason?}` — **added to the P0 subset** (required by `device migrate`; hidden dependency confirmed by audit).
- `message.publish`: `{message_id, revision_id, body_hash, body_bytes?, body_len, body_mime, text_class, declared_priority, thread_id?, reply_to_message_id?, reply_to_revision_id?, recipients?[]}`
- `message.reply`: **an atomic publish variant** — creates a new logical message + initial revision, recording `reply_to_message_id`, `reply_to_revision_id`, `thread_id`. Initial topic links remain separate events.
- `message.revise_body`: `{message_id, new_revision_id, parent_revision_ids[], body_hash, body_bytes?, body_len}` — may create **two revision objects in one payload** for merges (see §8).
- `message.retract`: `{message_id, reason?}`
- `topic.create`: `{topic_id, name}`; `topic.link.add`: `{link_id, message_id, topic_id, protected?}`; `topic.link.remove`: `{removed_link_ids[]}` (observed-remove).
- `blob.pin`: `{pin_id, principal_id, object_hash, durability}`; `blob.unpin`: `{pin_ids[]}`.
- `signal.emit`: `{message_id, kind, weight?}` — P0 kinds: `important`, `ignore`, `priority_confirm`, `found`, `not_found`.
- **Unknown payload fields:** preserved in canonical storage (they are signed); projection ignores fields not in the schema version.
- **ID formats:** logical IDs (message, revision, topic, link, pin, request, interaction) = **UUIDv7**. Event/content IDs = BLAKE3 (hex or lowercase base32, pick one repo-wide). Caller may supply logical IDs (idempotency); daemon generates when absent.

## 3. Durability and the Acknowledgement Boundary

The zero-loss gate is defined by this ordering (ChatGPT ruling, accepted verbatim):

1. Persist referenced body/blob objects first: write-temp → fsync file → rename → **fsync directory**.
2. Append frame to open segment: fixed magic+version, **u64 little-endian length**, `record_bytes`, **CRC32C** checksum (framing detects corruption; signatures provide cryptographic integrity).
3. `fdatasync` the segment.
4. **Only then acknowledge.** Acknowledgement is never issued after any failed durability operation; validation and terminal storage failures reject *before* ack (this is the resolution of the §8.1/§8.2 "never rejects vs rejects" contradiction: never reject an acknowledged event).
5. Sequence-state files are caches; **the verified log is authoritative** — recovery derives next_sequence from the log and never trusts a higher cached value.
6. Recovery validates frames in order and truncates only an incomplete or checksum-invalid *trailing* frame.
7. Segments: one directory per (origin, generation); seal at **64 MiB or 10,000 events**; sealed header {first_seq, last_seq, event_count, root_hash = BLAKE3 over ordered event IDs}; crash-safe seal = either the open segment remains valid or exactly one valid sealed segment exists.
8. Object paths: `objects/<first2>/<rest>`; hash raw uncompressed bytes; never overwrite — verify hash+length on collision.

## 4. Identity Bootstrap and Migration (P0, offline)

- Genesis establishes root + first device (§2 above). `device.add` requires root-signed cert.
- **P0 `device migrate` is an offline ceremony:** enrol new identity on target → manual data copy → root-signed `device.revoke` for old cert durably appended → old origin read-only. Crash between add and revoke: both devices valid; rerun completes revoke safely.
- Portable-data-only restore ⇒ new origin identity (unchanged from v0.3).
- **Full-disk clone (keys + data copied together) is outside P0's prevention guarantee** — it is equivocation, detected when logs meet under P1 replication. Prefer OS-keystore non-exportable keys where available; do not claim file permissions prevent cloning. No liveness beacon in P0 (requires peers).

## 5. Body/Object Storage Model (resolves the three-way contradiction)

- **Every revision body is a content-addressed object.** Events always carry `body_hash, body_len, body_mime, text_class`.
- Bodies **≤ 64 KiB may additionally be inlined** in the event (`body_bytes`) for inspectability and recovery acceleration; the object remains authoritative.
- Hashes are over uncompressed UTF-8 bytes; compression is a storage detail.
- Expired ephemeral text: publish event survives; object is deleted; search projection drops it; `fetch` returns a typed `content_expired` result.
- The v0.3 §3.3 sentence "event segments contain the full plaintext corpus" is **struck as stale**; the encryption requirement applies to the entire mesh directory (segments, objects, index) regardless of where bytes live.
- **Text-class policy (P0):** bodies > 1 MiB declared canonical/eager are auto-downgraded to ephemeral; only an explicit operator CLI override forces canonical/eager; outbox callers cannot invoke the override; configurable daily canonical-byte and per-message ceilings; no review queue.

## 6. Daemon and Projection Model

- **P0 includes one resident, single-writer daemon** (OS file lock + local IPC ownership; never PID files alone). It owns appends, object commits, SQLite projection, outbox ingestion, view regeneration, ephemeral-TTL housekeeping. CLI mutations go through the daemon or fail; they never append independently.
- "Maintenance worker absent from P0" (§12) means the **P2 prioritised maintenance subsystem** — not the basic in-process projector/housekeeping loop, which P0 requires.
- **Enrichment timing (final ruling, overrides both auditors' block-the-ingest instinct):** append+fsync+ack synchronous; **lexical FTS insert synchronous** (sub-ms; makes the message digest-visible, satisfying the 200 ms gate); **embedding on an in-process background thread**. A just-sent message is briefly `retrieval_mode="lexical_only"` — degradation-ladder step 4 behaving normally. `reindex --semantic` is the bulk backfill.
- SQLite: WAL, `synchronous=FULL`, FKs on, one writer. Authoritative `events` table + normalized `messages/revisions/topic_links/pins` + contentless FTS5 keyed by immutable `revision_id` + separate current-head table + `messages_vec`. **Projection checkpoint (last applied event_id + per-origin sequence) commits in the same transaction as the projection.** Retraction = projection flag, never FTS deletion needed for replay. Reindex builds new tables beside current and atomically swaps. Monotonic integer schema version; migrations replay-safe, backed by full reindex.
- FTS5 tokenizer: `unicode61` with tokenchars `_ - # @`.

## 7. Retrieval Contract (P0 executables)

- **Embedding:** `all-MiniLM-L6-v2`, 384-d float32, pinned by model ID **and artifact hash**, ONNX in-process, cosine distance. `embedding_model_id` stored per vector; vectors from different models never compared; model migration = invalidate + `reindex --semantic`.
- **Vector store:** sqlite-vec pinned extension; brute-force cosine fallback for candidate sets < 5k.
- **Fusion:** top-100 from FTS + top-100 from vector; RRF k=60; percentile over the union; ties broken deterministically by event_id. No compatible embedding ⇒ BM25 percentile alone + `retrieval_mode="lexical_only"`.
- **Digest without subscriptions:** each `views/<agent>/` has **local, non-event view config**: hard topic filters + optional natural-language interest query. Candidates pass hard filters; `R_subscription` = the standard hybrid relevance against the interest query; **no query ⇒ R=1.0 for all candidates** (digest degrades to freshness+priority ordering). This is local configuration, not a durable subscription.
- **Priority decay (executable):** `effective_P = (declared_priority/3) × 2^(−age/60h)`. An active pin or `signal.emit(kind="priority_confirm")` suspends decay. Demand/reference validation does not exist in P0.
- **Mandatory inclusion:** inserted before scored items — explicit recipients first, then active pins, then scored. Mandatory items consume budget; overflow drops oldest-first and reports `omitted_mandatory_count`.
- **Budgets:** every retrieval op takes exactly one budget parameter. **P0 = `budget_chars` only** (Unicode scalar values); `budget_tokens` returns `unsupported_in_P0` (no bundled tokenizers). The budget covers the **complete returned payload** — metadata, prefixes, truncation markers included. Search may return fewer than k to comply. Ranking ties: mandatory class → score → wall time → event_id.

## 8. Outbox and Merge Contract

- **Canonical outbox request = atomic directory bundle:** `request.json` (+ optional body files), written under a temp name, atomically renamed to `<request_id>.ready`. `request_id` (UUIDv7) is mandatory and is the **idempotency key forever** — reprocessing returns the original event IDs. Receipt `<request_id>.receipt.json` is written **only after event durability**; crash during receipt write ⇒ retry regenerates the identical receipt. Invalid bundles → `rejected/` with structured error; no ack.
- **Convenience shorthand:** a bare `<name>.md` (optional YAML front-matter `{action, message_id?, base_revision_id?, reply_to_message_id?, text_class?, declared_priority?, topic_ids?}`) atomically renamed into the outbox = simple publish/reply; the daemon wraps it into a bundle internally (request_id derived from a daemon-assigned UUIDv7 recorded in the receipt). No front-matter ⇒ plain publish.
- **Export front-matter:** `message_id, revision_id, base_revision_id, body_hash, exported_at` — **read-only in P0**. Any front-matter mutation is rejected. Topic/pin decomposition from exports moves to P1.
- **Merge (body-only, P0):** UTF-8, LF-normalised, line-based **diff3** (pinned implementation; `git merge-file` acceptable if vendored behavior is pinned). Exit 0 = clean; else conflict.
  - Clean, head unchanged since export ⇒ one normal revision.
  - Clean, head moved ⇒ one `revise_body` event creating **two revision objects**: operator-branch revision (parent = `base_revision_id`) and merged revision (parents = [operator-branch, current head]); both record the same `created_by_event_id`; merge flagged machine-merged.
  - Conflict ⇒ `conflicts/<message_id>/{BASE.md, CURRENT.md, OPERATOR_EDIT.md, RESOLVE.md}`; operator edits RESOLVE.md; `mesh resolve <message_id>` runs the same merge path against then-current head. Never a blocking prompt; never a silent overwrite.
  - **Editing a retracted message is rejected** with an error receipt ("retracted; restore requires explicit operator action"). `message.restore` stays out of P0.
  - Crash during merge: either no revision or the complete merge event — never half a revision graph.
- Agent view directory names are daemon-generated safe IDs; display names stored separately.

## 9. Platform and Encryption

- **P0 primary platform: macOS arm64** (operator's daily environment; encryption check = `fdesetup status`). **Linux x86-64/arm64: best-effort** (lsblk/dm-crypt detection). Windows: out of scope for P0.
- Unknown/indeterminate encryption status **fails closed**. Override: `--allow-unencrypted` — persistent operator flag stored in **device-local state** (never portable data), surfaced with a warning on every startup.
- Backup semantics: portable data only by default; device-local identity requires a separate explicit procedure.
- Config: versioned TOML, split portable mesh config vs device-local config. Exports: UTF-8 no BOM, LF, strict YAML field whitelist.

## 10. Telemetry and Gate Interpretation

- Every CLI request / outbox bundle accepts `task_id, agent_surface, agent_instance_id?`; daemon generates missing values flagged `inferred=true`. Every search/digest returns an `interaction_id`. **Outcome commands (`mesh found <id> | not-found | manual-workaround`) require the interaction_id**; "latest" shortcuts are convenience and excluded from gate calculations.
- "Three agent surfaces" (P0) = three distinct named `views/<agent>/` consumers, not transport adapters.
- **Gate interpretation:** engineering gates (zero acknowledged loss, 100% provenance, 100% budget compliance, 200 ms P95 lexical visibility) are **release blockers**. Product gates (Success@5, time-to-context, workaround rate) missing initially falsifies the **P0 ranking/view configuration**; the thesis is challenged only if targets remain unmet after **one documented tuning pass on held-out tasks**.
- Gates table gains an `automated | human-measured` column: time-to-context and copy-paste baseline are human-measured (diary protocol), not CI.
- `mesh doctor` ships in P0: walks segments, verifies hashes/signatures/chains, reports projection drift; `mesh doctor conflicts` lists unresolved conflict debt; digests may show a single operational notice with the unresolved count (never conflict content).

## 11. Emergency Reserve

Preallocated **64 MiB emergency file per mesh**. Ordinary sends cannot consume it. Only an explicit interactive operator command releases it, for one small text event ≤ 64 KiB. **Declared message priority is never authorisation to use the reserve.** Fault tests cover reserve exhaustion by ordinary and operator-authorised sends.

## 12. Crash/Fault Matrix (canonical — ChatGPT superset adopted)

Invariant after every acknowledged mutation: *after restart, the event exists exactly once by event_id, the origin chain is contiguous, every referenced durable object is fetchable and hash-valid, and all projections are reconstructible from the log.*

Crash points (each with required outcome per the audit table): object temp-write; post-fsync pre-rename; post-rename pre-dir-fsync; mid-frame append; post-append pre-fdatasync; post-fdatasync pre-ack; post-ack; sequence-state update; mid-projection transaction; post-projection pre-view-swap; mid-seal; mid-receipt; mid-merge; migration between add and revoke; post-revoke.

Injected failures: ENOSPC on every write path; EIO/short-write/fsync-failure; SIGKILL **and simulated power-cycle loss of unfsynced state** (SIGKILL alone is insufficient for the durability claim); truncated/corrupt/bit-flipped frames; hash-valid frame with invalid signature; missing referenced object; corrupt/deleted SQLite + full replay; concurrent CLI senders; duplicate bundle + duplicate ingest; clock rollback/extreme-future timestamps; stale export edit racing one and multiple revisions; retraction racing export ingest; ephemeral expiry racing fetch/reindex; restored portable data without device-local identity; sequence cache behind/ahead of log; encrypted/unencrypted/indeterminate volumes; reserve exhaustion. Property tests: observed-remove link/pin concurrency; retraction visibility; merge clean/conflict paths.

## 13. Corrections to v0.3 Text

- §3.3: strike "event segments contain the full plaintext corpus"; encryption requirement rewritten per §5 above.
- §4.2/§4.3: adopt `signing_bytes`/`record_bytes` terminology.
- §5.2: export decomposition (topics/pins) deferred to P1; P0 exports are body-editable only.
- §7.3: **remove `map.md` from the P0 views listing** (maps are P2).
- §8.1/§8.2: rejection clarified — never after acknowledgement.
- §12: add `device.revoke` to the P0 event subset; note the resident daemon + in-process projector is P0 scope.
- Gates table: add automated/human column.

## 14. Adjudication Log

- **ChatGPT audit:** deepest — durability boundary (§3), migration→revoke hidden dependency, merge second-branch representation, digest R_subscription source, executable priority decay, emergency reserve, gate interpretation, canonical crash matrix. All accepted. Platform recommendation (Linux) overruled for macOS-primary on operator-environment grounds.
- **Grok audit:** payload starter schemas (adopted, extended with reply semantics), segment format (adopted; checksum changed to CRC32C), outbox single-file format (demoted to convenience shorthand under ChatGPT's bundle), sqlite-vec + fallback, FTS tokenchars, relational-extraction schema, RRF k=60 + percentile. ULID overruled for UUIDv7.
- **Gemini audit:** Ed25519, MiniLM, diff3, BLAKE3-everywhere (all confirmed unanimous). CBOR recommendation reversed by the no-floats rule (and by its own author's round-2 JSON ruling). "Block the ingest loop" resolution overruled (agents never wait); "add message.restore" overruled (reject edits to retracted). Crash matrix subsumed by ChatGPT's superset. H2 tokenizer concern resolved as budget_chars-only P0.
- **Unresolved items:** none. All three auditors' shortest-path-to-go lists are fully answered above.
