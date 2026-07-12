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

-- Vectors: one row per (revision, model). Never compare across models.
-- If sqlite-vec is available this becomes a vec0 virtual table; otherwise
-- this plain table + in-process brute-force cosine (<5k candidates).
CREATE TABLE vectors (
  revision_id TEXT NOT NULL,
  embedding_model_id TEXT NOT NULL,
  dim INTEGER NOT NULL,
  vec BLOB NOT NULL,                  -- float32 little-endian
  PRIMARY KEY (revision_id, embedding_model_id)
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
CREATE TABLE parked_events (
  event_id TEXT PRIMARY KEY,
  origin_device_id TEXT NOT NULL,
  origin_generation INTEGER NOT NULL,
  origin_sequence INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  error TEXT NOT NULL,
  parked_at TEXT NOT NULL
);

-- NOTE: telemetry (interactions, impressions, outcomes) lives in a SEPARATE
-- database (.cairn/telemetry.sqlite) per rulings §10 / spec §4.5 stream
-- classes — never in the event log, never replicated.
