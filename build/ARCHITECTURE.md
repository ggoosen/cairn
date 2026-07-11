# Agent Mesh P0 — Implementation Architecture (Condensed)

One resident, single-writer daemon per machine. Agents interact via the
filesystem (outbox drops, generated views) and the `mesh` CLI (which talks to
the daemon over a local unix socket). Everything an agent sends becomes a
signed, immutable event in an append-only log. SQLite is a rebuildable
projection of that log. Retrieval is budget-capped and rank-explained.

```
        agent session                    operator
      │ drop .md / bundle │            │ mesh CLI │
      ▼                   ▼            ▼          ▼
┌─────────────────────────────────────────────────────────┐
│                    mesh daemon (Go)                      │
│                                                          │
│  outbox ingester ──► validator ──► EVENT LOG (append,    │
│   (atomic bundles,     (schema,      fsync, ack) ────────┼──► receipt
│    idempotent)          capability*)      │              │
│                                           ▼              │
│                              sync: FTS projection        │
│                              async: embeddings (goroutine)│
│                                           │              │
│  ┌────────────┐   ┌─────────────┐   ┌────▼───────────┐  │
│  │ object     │   │ identity    │   │ SQLite         │  │
│  │ store      │   │ (device-    │   │ projection     │  │
│  │ (BLAKE3,   │   │  local keys)│   │ FTS5 + vec +   │  │
│  │  TTL GC)   │   │             │   │ checkpoint     │  │
│  └────────────┘   └─────────────┘   └────┬───────────┘  │
│                                          ▼              │
│                    view generator: digest.md, exports/,  │
│                    fetched/ (manifest+body), conflicts/  │
└─────────────────────────────────────────────────────────┘
   * P0 = tier-1 trust; capability checks activate in P1
```

## On-disk layout

```
PORTABLE (the mesh directory, default ~/mesh — backup/copy freely)
├── events/<origin>/<generation>/seg_*.seg     # framed, signed records
├── objects/<xx>/<blake3-hex>                  # bodies + blobs, immutable
├── exports/                                   # generated markdown (front-matter)
└── views/<agent-id>/{digest.md, search-results/, fetched/, conflicts/, outbox/}

DEVICE-LOCAL ($XDG_DATA_HOME/agent-mesh/<mesh_id>/device/ or macOS equivalent)
├── device.key (0600 / keychain)   ├── device.cert
├── seq_state.json (cache only)    └── config-device.toml (incl. --allow-unencrypted flag)

DERIVED (inside mesh dir, rebuildable, excluded from backup by default)
└── .mesh/{index.sqlite, vectors, telemetry.sqlite}
```

## The write path (the most important 30 lines in the system)

1. Ingest request (outbox bundle rename-detected, or CLI over socket).
2. Validate: schema, size, text-class policy (>1 MiB canonical/eager →
   downgrade to ephemeral), retracted-target rejection.
3. Persist body/blob objects: temp → fsync → rename → **fsync dir**.
4. Build event: payload → envelope → canonical signing_bytes → event_id =
   BLAKE3("agent-mesh-event-v1" || signing_bytes) → Ed25519 sign.
5. Frame record_bytes: magic+version | u64-LE length | bytes | CRC32C.
6. Append to open segment; **fdatasync**.
7. **Acknowledge** (write receipt for outbox; return for CLI). Never before.
8. Synchronous: apply projection (SQLite tx includes checkpoint row) + FTS
   insert → message is digest-visible (lexical) → regenerate affected views.
9. Background goroutine: embed body (MiniLM ONNX), insert vector, mark
   retrieval_mode full. Failure = stays lexical_only; reindex --semantic heals.

Recovery on startup: scan segments per (origin, generation); validate frames
in order; truncate only an invalid/incomplete TRAILING frame; verify chain
(prev_event_id) and signatures; rebuild seq from log (cache never trusted if
higher); replay any events past the projection checkpoint.

## Event model in brief

- Envelope per spec §4.1; payloads per `build/schemas/p0-events.schema.json`.
- P0 types: mesh.genesis, device.add, device.revoke, message.publish,
  message.revise_body, message.retract, message.reply, topic.create,
  topic.link.add, topic.link.remove, blob.pin, blob.unpin, signal.emit.
- Revision DAG: message_id stable; revisions immutable; merge revisions have
  two parents; one revise_body event may create TWO revision objects
  (operator branch + merged) — see rulings §8.
- Every body is a content-addressed object; ≤64 KiB may also inline in the
  event. Text classes: canonical / eager-searchable / ephemeral(7d TTL →
  typed content_expired on fetch).
- Threads are emergent from thread_id; reply is an atomic publish variant.
- Links/pins are observed-remove assertion sets (remove by assertion ID).
- Sender priority is immutable testimony; importance changes are signals/pins.

## Retrieval in brief

- search(query, scope, k, budget_chars): FTS top-100 + vector top-100 → RRF
  k=60 → percentile over union → P0 search profile
  `0.90·R + 0.07·F(90d) + 0.03·effective_P` → budget-truncate (budget covers
  ENTIRE payload incl. metadata) → return with interaction_id.
- digest(budget_chars, view): candidates pass the view's local config hard
  filters; R = relevance vs the view's interest query (no query → R=1.0);
  digest profile `0.60·R + 0.30·F(72h) + 0.10·effective_P`; mandatory items
  (explicit recipients, then pins) inserted first and budget-counted;
  effective_P = (priority/3)·2^(−age/60h), suspended by pin/priority_confirm.
- why_ranked(msg): prints the exact arithmetic from stored scoring inputs.
- fetch materialises views/<agent>/fetched/{id.manifest.json, id.body.md};
  body file contains ONLY the body; digest lines quoting mesh content are
  prefixed per-line with `> [MESH] `.

## Non-goals in P0 (do not build)

Networking/replication, MCP server, capability tokens beyond tier-1 trust,
semantic subscriptions as events, salience beyond declared priority,
knowledge maps, maintenance worker, derivatives beyond native text,
message.restore, tokenizer-based budgets, Windows.
