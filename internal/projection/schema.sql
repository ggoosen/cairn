-- Cairn P0 — SQLite projection DDL (rulings v0.3.1 §6)
-- The projection is DERIVED. Deleting this database and running
-- `cairn reindex` MUST reproduce identical query results from the log.
-- PRAGMAs set at open: journal_mode=WAL; synchronous=FULL; foreign_keys=ON.
-- Single writer: the daemon. Schema version is monotonic.

CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);  -- schema_version, embedding_model_id, cairn_id

-- Projection checkpoint: committed in the SAME transaction as projections.
CREATE TABLE checkpoint (
  origin_device_id TEXT NOT NULL,
  origin_generation INTEGER NOT NULL,
  last_applied_sequence INTEGER NOT NULL,
  last_applied_event_id TEXT NOT NULL,
  PRIMARY KEY (origin_device_id, origin_generation)
);

-- Authoritative mirror of applied events (envelope fields; payload as JSON).
CREATE TABLE events (
  event_id TEXT PRIMARY KEY,
  event_type TEXT NOT NULL,
  origin_device_id TEXT NOT NULL,
  origin_generation INTEGER NOT NULL,
  origin_sequence INTEGER NOT NULL,
  previous_event_id TEXT,
  actor_principal_id TEXT,
  actor_task_id TEXT,
  correlation_id TEXT,               -- outbox request_id; receipt idempotency key (rulings §8)
  wall_time TEXT NOT NULL,           -- RFC3339 string; never used for ordering
  payload_json TEXT NOT NULL,
  UNIQUE (origin_device_id, origin_generation, origin_sequence)
);
CREATE INDEX idx_events_type ON events(event_type);
CREATE INDEX idx_events_correlation ON events(correlation_id) WHERE correlation_id IS NOT NULL;

CREATE TABLE messages (
  message_id TEXT PRIMARY KEY,
  thread_id TEXT,
  reply_to_message_id TEXT,
  head_revision_id TEXT NOT NULL,
  declared_priority INTEGER NOT NULL,     -- immutable testimony
  text_class TEXT NOT NULL,
  sender_principal_id TEXT,
  created_event_id TEXT NOT NULL REFERENCES events(event_id),
  created_at TEXT NOT NULL,
  retracted INTEGER NOT NULL DEFAULT 0,   -- projection flag; history preserved
  retracted_event_id TEXT
);
CREATE INDEX idx_messages_thread ON messages(thread_id);
CREATE INDEX idx_messages_created ON messages(created_at);
-- D1: every vector query joins head_revision_id → revision, on both the vec0
-- and the brute-force path. Without this SQLite builds a transient automatic
-- index for that join on EVERY query, which is exactly the per-query O(corpus)
-- cost the vector index exists to remove.
CREATE INDEX idx_messages_head ON messages(head_revision_id);

CREATE TABLE revisions (
  revision_id TEXT PRIMARY KEY,
  message_id TEXT NOT NULL REFERENCES messages(message_id),
  body_hash TEXT NOT NULL,
  body_len INTEGER NOT NULL,
  body_mime TEXT NOT NULL,
  machine_merged INTEGER NOT NULL DEFAULT 0,
  created_event_id TEXT NOT NULL REFERENCES events(event_id),
  created_at TEXT NOT NULL
);
CREATE INDEX idx_revisions_message ON revisions(message_id);

CREATE TABLE revision_parents (
  revision_id TEXT NOT NULL REFERENCES revisions(revision_id),
  parent_revision_id TEXT NOT NULL,
  ord INTEGER NOT NULL,               -- 0 or 1; merge revisions have two rows
  PRIMARY KEY (revision_id, ord)
);

CREATE TABLE recipients (
  message_id TEXT NOT NULL REFERENCES messages(message_id),
  agent_view TEXT NOT NULL,           -- drives mandatory inclusion in digests
  PRIMARY KEY (message_id, agent_view)
);

CREATE TABLE topics (
  topic_id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  created_event_id TEXT NOT NULL
);

-- Observed-remove set: each assertion is a row; removal marks the assertion.
CREATE TABLE topic_links (
  link_id TEXT PRIMARY KEY,
  message_id TEXT NOT NULL REFERENCES messages(message_id),
  topic_id TEXT NOT NULL REFERENCES topics(topic_id),
  protected INTEGER NOT NULL DEFAULT 0,
  removed INTEGER NOT NULL DEFAULT 0,
  removed_event_id TEXT
);
CREATE INDEX idx_links_msg ON topic_links(message_id) WHERE removed = 0;
CREATE INDEX idx_links_topic ON topic_links(topic_id) WHERE removed = 0;

CREATE TABLE pins (
  pin_id TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL,
  object_hash TEXT NOT NULL,
  durability TEXT NOT NULL,
  removed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_pins_object ON pins(object_hash) WHERE removed = 0;

CREATE TABLE attachments (
  message_id TEXT NOT NULL REFERENCES messages(message_id),
  object_hash TEXT NOT NULL,
  byte_len INTEGER NOT NULL,
  mime TEXT NOT NULL,
  filename TEXT,
  durability TEXT NOT NULL DEFAULT 'normal',
  PRIMARY KEY (message_id, object_hash)
);

-- Ingest hook (M9 `cairn ingest`): source provenance of imported messages,
-- projected from message.publish payloads carrying source_ref. Rebuildable
-- from events like every other table; no P0 behavior reads it.
CREATE TABLE source_refs (
  path TEXT PRIMARY KEY,
  message_id TEXT NOT NULL REFERENCES messages(message_id)
);

CREATE TABLE signals (
  event_id TEXT PRIMARY KEY REFERENCES events(event_id),
  message_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  weight INTEGER,
  actor_principal_id TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_signals_msg ON signals(message_id);

-- Contentless FTS keyed by IMMUTABLE revision_id; bodies fed at index time.
-- Retraction/supersession filtered via joins, never by deleting FTS rows
-- needed for replay fidelity. Tokenizer per rulings: unicode61 + tokenchars.
CREATE VIRTUAL TABLE fts_revisions USING fts5(
  body,
  content='',
  tokenize="unicode61 tokenchars '_-#@'"
);
-- rowid mapping table (fts contentless requires managed rowids)
CREATE TABLE fts_map (
  rowid INTEGER PRIMARY KEY,
  revision_id TEXT NOT NULL UNIQUE
);

-- D14: fts5vocab companion in 'row' mode — (term, doc, cnt) per indexed term,
-- where `doc` is the number of documents containing the term. It is the D11
-- term-discrimination probe's document-frequency source: asking the index for
-- df, rather than counting matching rowids up to a LIMIT that is itself half
-- the corpus. Nothing is stored — fts5vocab is a VIEW over the FTS index's own
-- b-tree — so it costs no space, cannot drift from the index it reads, and
-- reports terms exactly as the tokenizer emitted them (folded, tokenchars
-- applied), which is why query terms are folded in Go before lookup.
CREATE VIRTUAL TABLE fts_revisions_vocab USING fts5vocab('fts_revisions', 'row');
-- CAPTURE C2 companion index over the SAME body text, sharing fts_map's
-- rowid so ONE insert feeds both indexes in one transaction. unicode61
-- splits on word boundaries and can never match INSIDE a token, which is
-- exactly what agents search for: a partial UUID, a camelCase symbol, an
-- error-message fragment. The two tokenizers fail in opposite directions
-- (trigram is blind to stemming/relevance, word FTS to substrings), so the
-- pair is complementary; word hits still rank first (LexicalTopK).
CREATE VIRTUAL TABLE fts_revisions_trigram USING fts5(
  body,
  content='',
  tokenize='trigram'
);

-- Vectors: one row per (revision, model). Never compare across models.
-- This table is the SOURCE OF TRUTH for embeddings and the brute-force
-- oracle's input; it is written whether or not sqlite-vec is available
-- (D1). The vec0 virtual table below is a derived INDEX over it.
CREATE TABLE vectors (
  revision_id TEXT NOT NULL,
  embedding_model_id TEXT NOT NULL,
  dim INTEGER NOT NULL,
  vec BLOB NOT NULL,                  -- float32 little-endian
  PRIMARY KEY (revision_id, embedding_model_id)
);

-- D1: rowid bridge to the sqlite-vec index. vec0 virtual tables are keyed by
-- INTEGER rowid, our vectors by revision_id, so one stable mapping lives here
-- (same shape as fts_map, and for the same reason). The vec0 table itself is
-- created LAZILY, in internal/projection/vec.go, because its dimension is not
-- known until the first vector arrives and because the extension may not be
-- present at all — when it is absent this table simply stays empty and the
-- brute-force scan over `vectors` answers, which is the sanctioned fallback
-- (rulings §7). Rebuilt from `vectors` whenever the two disagree, so the vec0
-- index is as derived as everything else here.
CREATE TABLE vec_map (
  rowid INTEGER PRIMARY KEY,
  revision_id TEXT NOT NULL UNIQUE
);

-- Enrichment state for retrieval_mode + reindex --semantic backfill.
CREATE TABLE enrichment (
  revision_id TEXT PRIMARY KEY,
  lexical_indexed INTEGER NOT NULL DEFAULT 0,
  embedded INTEGER NOT NULL DEFAULT 0,
  embed_error TEXT
);

-- Ranking inputs snapshot for why_ranked reproducibility (per interaction
-- result row; lives here not telemetry because why_ranked is a product op).
CREATE TABLE rank_explanations (
  interaction_id TEXT NOT NULL,
  message_id TEXT NOT NULL,
  profile TEXT NOT NULL,
  components_json TEXT NOT NULL,      -- {"R":..,"F":..,"P_eff":..,"weights":{...}} (values as decimal STRINGS)
  final_rank INTEGER NOT NULL,
  PRIMARY KEY (interaction_id, message_id)
);

-- Projection-layer quarantine (F1 ruling 3): events that fail projection are
-- PARKED here and the stream continues — the projector never stalls and the
-- log is never touched (parking is a projection concept only). Doctor treats
-- parked events as a failure condition. Rebuildable like everything else.
-- retryable (R49/FIX-J1): 1 = a MISSING intra-mesh reference (a FOREIGN KEY on
-- a not-yet-projected dependency — e.g. a topic.link.add replicated ahead of
-- its topic.create) that a LATER event may satisfy; the projector re-attempts
-- retryable parked events after each applied event / reconcile batch / reindex
-- and clears them when they project. 0 = terminal (genuine corruption: a parse
-- or schema failure no future event can heal). Doctor/gates treat retryable
-- parks within R49's grace window as informational, terminal parks as failures.
CREATE TABLE parked_events (
  event_id TEXT PRIMARY KEY,
  origin_device_id TEXT NOT NULL,
  origin_generation INTEGER NOT NULL,
  origin_sequence INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  error TEXT NOT NULL,
  parked_at TEXT NOT NULL,
  retryable INTEGER NOT NULL DEFAULT 0
);

-- NOTE: telemetry (interactions, impressions, outcomes) lives in a SEPARATE
-- database (.cairn/telemetry.sqlite) per rulings §10 / spec §4.5 stream
-- classes — never in the event log, never replicated.

-- Durable semantic subscriptions (P1 N3; RULINGS.md R24/R25/R26).
-- Rebuilt from subscription.* events; delivery history is telemetry-class
-- (local, never events) and lives in telemetry.sqlite.
CREATE TABLE subscriptions (
  subscription_id TEXT PRIMARY KEY,      -- UUIDv7
  owner_view TEXT NOT NULL,              -- digest view this sub feeds
  interest_query TEXT NOT NULL,          -- raw NL, embedded locally at use
  hard_topics TEXT NOT NULL DEFAULT '[]',-- JSON array of topic_ids (filter FIRST)
  threshold_mode TEXT NOT NULL,          -- top_n | percentile (relative only)
  top_n INTEGER NOT NULL,
  window_hours INTEGER NOT NULL,
  percentile INTEGER NOT NULL,
  push_cap_per_day INTEGER NOT NULL,
  revision INTEGER NOT NULL,             -- optimistic-concurrency counter
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  created_event_id TEXT NOT NULL
);
CREATE INDEX idx_subscriptions_view ON subscriptions(owner_view, disabled);

-- Deterministic derivatives (P1 N4; spec §8.3). Rebuilt from derivative.*
-- events; the extracted text lives in the object store keyed by text_hash.
CREATE TABLE derivatives (
  derivative_id TEXT PRIMARY KEY,        -- UUIDv7
  blob_hash TEXT NOT NULL,               -- source blob (attachment object)
  derivative_type TEXT NOT NULL,         -- plain|sanitized_html|text_layer|office_text
  text_hash TEXT,                        -- extracted-text object (NULL for failed)
  extractor TEXT NOT NULL,
  extractor_version TEXT NOT NULL,
  status TEXT NOT NULL,                  -- ok | failed
  error TEXT,
  invalidated INTEGER NOT NULL DEFAULT 0,
  generated_at TEXT NOT NULL,
  created_event_id TEXT NOT NULL
);
CREATE INDEX idx_derivatives_blob ON derivatives(blob_hash);

-- FTS over derivative text (contentless, managed rowids like fts_revisions)
CREATE VIRTUAL TABLE fts_derivatives USING fts5(
  body,
  content='',
  tokenize="unicode61 tokenchars '_-#@'"
);
CREATE TABLE fts_derivatives_map (
  rowid INTEGER PRIMARY KEY,
  derivative_id TEXT NOT NULL UNIQUE
);

-- D14: the derivatives index gets the same df source, against its OWN
-- document population (a term common in bodies may be rare in extracted
-- attachment text).
CREATE VIRTUAL TABLE fts_derivatives_vocab USING fts5vocab('fts_derivatives', 'row');

-- Receiver summary topical-consistency check (P1 N4; spec §8.4).
-- sender_summary is event-derived (untrusted claim); the check columns are
-- enrichment-class (recomputed by the enricher, like vectors).
CREATE TABLE message_summaries (
  message_id TEXT PRIMARY KEY,
  sender_summary TEXT NOT NULL,
  checked INTEGER NOT NULL DEFAULT 0,
  agree_bp INTEGER,                      -- cosine in basis points at check time
  disagree INTEGER NOT NULL DEFAULT 0,
  local_summary TEXT,                    -- receiver extractive summary (on disagreement)
  local_method TEXT,                     -- provenance: method + embedder model
  checked_at TEXT
);
