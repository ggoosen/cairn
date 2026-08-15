# CAPTURE — zero-effort capture + ecosystem reach (work order, planned 2026-08-09)

Provenance: competitive review of Hermes agent's session-memory design
(NousResearch/hermes-agent — FTS5 session search shipped, hybrid vector
proposed in their issue #44075; optional `qmd` skill for local hybrid
retrieval over registered document collections). The review's conclusion:
Cairn is AHEAD on retrieval (hybrid RRF + budgets + explainable ranking
already shipped) but BEHIND on capture — Cairn only learns what an agent
deliberately `cairn send`s, so knowledge that nobody summarized dies in the
session transcript. This work order closes the capture gap without
compromising the curated digest surface, and picks up two cheap wins.

Precedence: RULINGS.md > docs/rulings-v0.3.1.md > docs/spec-v0.3.md > this
file. Nothing here touches §14 decisions. Milestones in order; each is
independently shippable; PROGRESS.md gets a section per milestone.

---

## C1 — End-of-session handoff convention (docs only, S)

The quality layer above automatic capture: the agent itself is the best
summarizer, and it is present at session end.

- Add to the CLAUDE.md agent block (and DOGFOOD's agent-instruction
  snippet): **before ending a session, publish ONE canonical handoff
  note** — decisions made (with reasons), unfinished work, surprises —
  via `cairn send --topic <project> --priority 2`.
- Keep the existing "signal, not noise" guidance; the handoff note is the
  session's single mandatory write, not a license to dump.

Acceptance: instruction blocks updated (CLAUDE.md, README one-liner,
DOGFOOD §agent-wiring); no code change.

## C2 — Trigram FTS companion index (projection-only, S)

Hermes runs word-level AND trigram FTS5 indexes; the trigram side is what
makes substring/identifier search work (`PeerAdd`, partial UUIDs, error
fragments). Cairn's unicode61 tokenizer misses exactly that class, and
embeddings are weak on exact identifiers — the two indexes fail in
opposite directions, so the pair is complementary, not redundant.

- Add `fts_revisions_trigram` (FTS5 `tokenize='trigram'`) alongside the
  existing index; populate in the same projection transaction.
- `LexicalTopK` becomes a two-index union (word hits rank first, trigram
  hits append after, deduplicated — same pattern as the existing
  derivative-hits union).
- Projection-only: bump `ProjectionSchemaVersion` (auto-rebuild on drift
  already handles migration); no event-schema impact.
- Constants (weights/caps, if any) in `internal/config/constants.go`.

Acceptance: substring query (e.g. a UUID fragment, a camelCase
identifier) that word-FTS misses returns the message; budget property
tests stay green; reindex byte-identical test extended to cover the new
table.

## C3 — Session-transcript ingest: the low-trust capture substrate (M/L)

The core milestone. A new **transcript source** on the existing M9 ingest
path (`cairn ingest scan/apply`) that ingests agent session transcripts
(first adapter: Claude Code JSONL session files; adapter interface open
for Hermes session JSONL and others) so unsummarized knowledge is
FINDABLE without ever polluting the curated surface.

Non-negotiable design constraints (these ARE the feature):

1. **Text class:** transcript chunks land as `eager-searchable` (or
   `ephemeral` when the operator prefers TTL); NEVER `canonical`. The
   existing downgrade policy and ceilings apply unchanged.
2. **Digest exclusion:** transcript-sourced messages are excluded from
   digest candidates by default (dedicated topic namespace
   `transcript/<source>` + view hard filters, or an explicit source flag
   in the projection — decide at build time, prefer the mechanism that
   keeps `DigestCandidates` SQL simple). They are PULL-ONLY via search;
   "agents are never handed a raw inbox" stays true.
3. **Provenance:** `source_ref` records source path + session id + chunk
   position, so a search hit says "raw transcript, session X" — never
   dressed up as a curated note. Untrusted-content quoting applies as
   everywhere (R18/R53).
4. **Privacy is opt-in per source directory.** Transcripts contain
   pasted secrets and tool output. `cairn ingest` must be pointed at a
   directory explicitly; add a redaction pass (recognizable token/key
   patterns masked at ingest, BEFORE the object store — a stored secret
   is durable and replicated) and document the residual risk plainly.
5. **Idempotent re-scan:** same session file re-ingested = no duplicate
   messages (content-addressing gives this for unchanged chunks; chunk
   boundaries must therefore be deterministic).

Chunking: by conversational turn-group with a size cap (constants in
config), not fixed windows — turns are the natural provenance unit.

Prerequisites this milestone drags forward (from the audit's deferred
list, in this order, only as the corpus actually grows):
- **G1 sqlite-vec** — transcript volume (10–100× messages) reaches the
  brute-force-cosine cliff far sooner than curated traffic.
- **G3 duplicate/thread-saturation penalties** — transcripts are highly
  repetitive; without penalties a scoped search drowns in near-identical
  chunks. (G3 remains its own reviewed task per WP-G notes — R47/R51
  reconciliation must move in lockstep.)

Acceptance: end-to-end — ingest a real Claude Code session dir; a fact
mentioned only mid-session is found by `cairn search` with transcript
provenance; the digest for every view is byte-unchanged before vs after
ingest; re-running ingest is a no-op; a seeded fake API key does not
appear in the object store; the fault matrix stays green (ingest rides
the ordinary publish path, so durability ordering is inherited).

## C4 — Memory-provider packaging for agent harnesses (S/M)

Strategic, not technical: Hermes (and OpenClaw-style harnesses) treat
memory as a pluggable provider surface, with community directories
listing providers. Cairn already speaks MCP — package it for that slot.

- A thin provider adapter/manifest per harness (Hermes plugin config
  wrapping `cairn mcp --view hermes --actor hermes`; document the
  capability profile it runs under — agent-standard, never full, R21).
- A short "Cairn as your agent's memory provider" doc page (install,
  what the harness gets: digest/search/thread/send + provenance +
  budgets).
- Submit listings (awesome-hermes-agent, Hermes Atlas memory-provider
  directory) once the doc exists.

Acceptance: a stock Hermes install wired to Cairn via the documented
config can search/send against a live mesh; doc merged; listings
submitted (external acceptance is out of our control — record the
submissions in PROGRESS).

---

## Non-goals (explicit, from the same review)

- **LLM reranking / LLM summarization in the daemon** (qmd-style): breaks
  the R47/R51 explainability contract — every ranking must reconcile to
  exact printed arithmetic. The agent-side handoff note (C1) is the
  sanctioned place for LLM distillation.
- **Query expansion models:** Cairn's shipped RRF hybrid already covers
  the different-terminology failure Hermes's issue #44075 describes.
  Revisit only on measured recall failures from the 30-handoff data.
- **Infinite history as an attention surface:** transcripts become a
  searchable substrate, never digest content. (Hermes's own community
  guidance — "curated memory, not infinite chat history" — independently
  validates the digest/budget stance; do not weaken it to make capture
  look richer.)

## Sequencing

C1 immediately (docs). C2 next (small, self-contained). C3 as its own
milestone with the privacy design reviewed before code (the redaction
pass and opt-in boundary deserve the same crossed-review treatment as
R56 got — capture is a trust-surface change). C4 any time after the
first tag; it needs no new daemon capability.
