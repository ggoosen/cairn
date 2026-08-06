package projection

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// Rank-support queries (M6). All ordering is deterministic.

// LexicalTopK returns message IDs of HEAD-revision FTS matches in bm25
// order (ties by message_id) — the lexical candidate list for RRF fusion.
func (p *Projection) LexicalTopK(query string, k int, includeRetracted bool) ([]string, error) {
	raw := query
	query = FTSQuery(query)
	rows, err := p.db.Query(`
		SELECT m.message_id
		FROM fts_revisions
		JOIN fts_map map ON fts_revisions.rowid = map.rowid
		JOIN revisions r ON r.revision_id = map.revision_id
		JOIN messages m ON m.message_id = r.message_id AND m.head_revision_id = r.revision_id
		WHERE fts_revisions MATCH ? AND (m.retracted = 0 OR ?)
		ORDER BY bm25(fts_revisions), m.message_id
		LIMIT ?`, query, boolInt(includeRetracted), k)
	if err != nil {
		return nil, fmt.Errorf("lexical candidates: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// N4: attachments are searchable via their derivatives — union hits from
	// derivative text (body hits rank first; derivative hits append after,
	// deduplicated). Provenance stays inspectable via `cairn derivative list`.
	derivHits, err := p.DerivativeMessageHits(raw, k, includeRetracted)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, id := range out {
		seen[id] = true
	}
	for _, id := range derivHits {
		if !seen[id] && len(out) < k {
			out = append(out, id)
		}
	}
	return out, nil
}

// VecBlob encodes float32 little-endian (the DDL's vectors.vec format).
func VecBlob(v []float32) []byte {
	out := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(x))
	}
	return out
}

func blobVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// InsertVector stores one revision embedding and marks enrichment done —
// one transaction, idempotent (INSERT OR REPLACE by PK). Also pins the
// projection-wide embedding model id in meta.
func (p *Projection) InsertVector(revisionID, modelID string, vec []float32) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR REPLACE INTO vectors(revision_id, embedding_model_id, dim, vec) VALUES (?,?,?,?)`,
		revisionID, modelID, len(vec), VecBlob(vec)); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO enrichment(revision_id, lexical_indexed, embedded)
			VALUES (?,1,1)
			ON CONFLICT(revision_id) DO UPDATE SET embedded=1, embed_error=NULL`, revisionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('embedding_model_id', ?)`, modelID); err != nil {
		return err
	}
	return tx.Commit()
}

// EmbeddingModelID returns the pinned model of the stored vectors ("" if none).
func (p *Projection) EmbeddingModelID() (string, error) {
	var v string
	err := p.db.QueryRow(`SELECT value FROM meta WHERE key='embedding_model_id'`).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// InvalidateVectors deletes ALL vectors and resets enrichment (model
// migration: invalidate + reindex --semantic, rulings §7).
func (p *Projection) InvalidateVectors() error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM vectors`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE enrichment SET embedded=0`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM meta WHERE key='embedding_model_id'`); err != nil {
		return err
	}
	return tx.Commit()
}

// PendingEmbedding is one revision awaiting a vector.
type PendingEmbedding struct {
	RevisionID string
	BodyHash   string
}

// PendingEmbeddings lists revisions without vectors, oldest first.
func (p *Projection) PendingEmbeddings(limit int) ([]PendingEmbedding, error) {
	rows, err := p.db.Query(`
		SELECT r.revision_id, r.body_hash FROM revisions r
		LEFT JOIN enrichment e ON e.revision_id = r.revision_id
		WHERE COALESCE(e.embedded, 0) = 0
		ORDER BY r.created_at, r.revision_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingEmbedding
	for rows.Next() {
		var pe PendingEmbedding
		if err := rows.Scan(&pe.RevisionID, &pe.BodyHash); err != nil {
			return nil, err
		}
		out = append(out, pe)
	}
	return out, rows.Err()
}

// CountPendingEmbeddings returns how many revisions are indexed but not yet
// embedded — the maintenance worker's primary "debt" signal (P2-1, spec §8.2).
func (p *Projection) CountPendingEmbeddings() (int, error) {
	var n int
	err := p.db.QueryRow(`
		SELECT count(*) FROM revisions r
		LEFT JOIN enrichment e ON e.revision_id = r.revision_id
		WHERE COALESCE(e.embedded, 0) = 0`).Scan(&n)
	return n, err
}

// ReferenceInDegree returns each message's reference-graph in-degree for P2
// salience (spec §9.2: "replies, citations, onward attachment, supersedes
// edges"). It sums the structurally-projected inbound edges from OTHER,
// non-retracted messages (P2H5 — was replies-only, which only reflected
// threading):
//
//   - REPLIES: a non-retracted message whose reply_to_message_id points at the
//     target. Counted at message_id granularity, so a reply to a since-
//     SUPERSEDED revision still counts for its message — this is how the
//     supersedes edge is honored (§9.2 / spec line 135, "the supersedes edge is
//     part of the reference graph"): references survive revision, they are not
//     re-homed or lost.
//   - ONWARD ATTACHMENT: a message that (re-)attaches a blob first introduced by
//     another message references that origin message. For each object_hash, the
//     earliest non-retracted attacher is the origin; every later distinct
//     attacher contributes +1 to the origin's in-degree.
//
// CITATIONS in later message bodies (the remaining §9.2 edge type) have no
// structured edge in the P0/P2 event set — a citation is free text inside a
// body, and detecting it means scanning every body for message-id references,
// an O(corpus²) pass on a hot ranking path. It is DEFERRED to a future indexed
// citation extractor (or an explicit citation event); see PROGRESS P2H5.
func (p *Projection) ReferenceInDegree() (map[string]int, error) {
	out := map[string]int{}

	// reply edges
	replies, err := p.db.Query(`
		SELECT reply_to_message_id, count(*) FROM messages
		WHERE reply_to_message_id IS NOT NULL AND retracted=0
		GROUP BY reply_to_message_id`)
	if err != nil {
		return nil, err
	}
	defer replies.Close()
	for replies.Next() {
		var id string
		var n int
		if err := replies.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] += n
	}
	if err := replies.Err(); err != nil {
		return nil, err
	}

	// onward-attachment edges: the earliest non-retracted attacher of each blob
	// is the origin; every later DISTINCT attacher is one onward reference to it.
	onward, err := p.db.Query(`
		SELECT origin_msg, count(*) FROM (
			SELECT a.message_id AS attacher,
				(SELECT a2.message_id FROM attachments a2
					JOIN messages m2 ON m2.message_id = a2.message_id
					WHERE a2.object_hash = a.object_hash AND m2.retracted = 0
					ORDER BY m2.created_at ASC, m2.message_id ASC
					LIMIT 1) AS origin_msg
			FROM attachments a
			JOIN messages m ON m.message_id = a.message_id
			WHERE m.retracted = 0
		)
		WHERE origin_msg IS NOT NULL AND attacher <> origin_msg
		GROUP BY origin_msg`)
	if err != nil {
		return nil, err
	}
	defer onward.Close()
	for onward.Next() {
		var id string
		var n int
		if err := onward.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] += n
	}
	return out, onward.Err()
}

// OperatorSignalWeight returns each message's summed operator-signal weight
// (P2 salience §9.2 operator-signals term; NULL weight counts as 1). NOTE: this
// is the RAW sum — the salience path no longer uses it (FIX-H4 replaced it with
// SignalObservations + rank.EffectiveSignalWeights, which dedup/decay/trust/cap
// before saturating). Retained for diagnostics.
func (p *Projection) OperatorSignalWeight() (map[string]int, error) {
	out := map[string]int{}
	rows, err := p.db.Query(`SELECT message_id, COALESCE(sum(COALESCE(weight,1)),0) FROM signals GROUP BY message_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// SignalRow is one raw operator signal for the FIX-H4 anti-gaming layer.
type SignalRow struct {
	MessageID string
	Principal string
	Kind      string
	Weight    int
	CreatedAt string // RFC 3339 UTC
}

// SignalObservations returns every operator signal, un-aggregated, so the
// salience layer can dedup per (principal, message, kind), slow-decay by age,
// trust-weight, and per-principal-day cap (spec §9.2). An empty actor_principal_id
// is attributed to the operator (the P0/local-CLI tier-1 default).
func (p *Projection) SignalObservations() ([]SignalRow, error) {
	rows, err := p.db.Query(`
		SELECT message_id, COALESCE(NULLIF(actor_principal_id,''),'operator'),
		       kind, COALESCE(weight,1), created_at
		FROM signals`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SignalRow
	for rows.Next() {
		var r SignalRow
		if err := rows.Scan(&r.MessageID, &r.Principal, &r.Kind, &r.Weight, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HeadVectors returns message_id → head-revision vector for one model
// (brute-force cosine happens in the caller; P0 candidate sets ≪ 5k —
// rulings §7 sanctions the fallback).
func (p *Projection) HeadVectors(modelID string, includeRetracted bool) (map[string][]float32, error) {
	rows, err := p.db.Query(`
		SELECT m.message_id, v.vec
		FROM vectors v
		JOIN messages m ON m.head_revision_id = v.revision_id
		WHERE v.embedding_model_id = ? AND (m.retracted = 0 OR ?)`, modelID, boolInt(includeRetracted))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]float32{}
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		out[id] = blobVec(blob)
	}
	return out, rows.Err()
}

// RankRow carries everything the ranker needs for one message.
type RankRow struct {
	MessageID      string
	HeadRevisionID string
	BodyHash       string
	TextClass      string
	Priority       int
	CreatedAt      string // head revision created_at
	CreatedEventID string
	Recipient      bool // explicit recipient of the requesting agent view
	PinActive      bool // active pin on the head body object
	PriorityConf   bool // signal.emit(priority_confirm) exists
}

// RankRows fetches rank inputs for a set of message IDs (agentView drives
// the recipient flag; pass "" to skip).
func (p *Projection) RankRows(messageIDs []string, agentView string) (map[string]RankRow, error) {
	out := map[string]RankRow{}
	if len(messageIDs) == 0 {
		return out, nil
	}
	placeholders := strings.Repeat("?,", len(messageIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(messageIDs)+1)
	args = append(args, agentView)
	for _, id := range messageIDs {
		args = append(args, id)
	}
	rows, err := p.db.Query(`
		SELECT m.message_id, m.head_revision_id, r.body_hash, m.text_class, m.declared_priority,
		       r.created_at, m.created_event_id,
		       EXISTS(SELECT 1 FROM recipients rc WHERE rc.message_id = m.message_id AND rc.agent_view = ?),
		       EXISTS(SELECT 1 FROM pins pn WHERE pn.object_hash = r.body_hash AND pn.removed = 0),
		       EXISTS(SELECT 1 FROM signals s WHERE s.message_id = m.message_id AND s.kind = 'priority_confirm')
		FROM messages m JOIN revisions r ON r.revision_id = m.head_revision_id
		WHERE m.message_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rr RankRow
		var recip, pin, pc int
		if err := rows.Scan(&rr.MessageID, &rr.HeadRevisionID, &rr.BodyHash, &rr.TextClass, &rr.Priority,
			&rr.CreatedAt, &rr.CreatedEventID, &recip, &pin, &pc); err != nil {
			return nil, err
		}
		rr.Recipient, rr.PinActive, rr.PriorityConf = recip == 1, pin == 1, pc == 1
		out[rr.MessageID] = rr
	}
	return out, rows.Err()
}

// TopicInfo is one row of the topic browse (RETR-D5): before this the only
// way to see the taxonomy was reading views/<agent>/map.md off disk, so
// send --topic typos silently proliferated unfindable topics.
type TopicInfo struct {
	TopicID  string `json:"topic_id"`
	Name     string `json:"name"`
	Messages int    `json:"messages"` // live (non-removed) linked messages
}

// TopicList returns every topic with its live link count, by name.
func (p *Projection) TopicList() ([]TopicInfo, error) {
	rows, err := p.db.Query(`
		SELECT t.topic_id, t.name,
		       (SELECT count(DISTINCT l.message_id) FROM topic_links l
		        WHERE l.topic_id = t.topic_id AND l.removed = 0)
		FROM topics t ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopicInfo
	for rows.Next() {
		var ti TopicInfo
		if err := rows.Scan(&ti.TopicID, &ti.Name, &ti.Messages); err != nil {
			return nil, err
		}
		out = append(out, ti)
	}
	return out, rows.Err()
}

// ThreadMessage is one message of a thread expansion (RETR-D4).
type ThreadMessage struct {
	MessageID string
	ReplyTo   string
	Sender    string
	CreatedAt string
	BodyHash  string
}

// ThreadMessages returns a thread's non-retracted messages in wall order
// (idx_messages_thread exists since P0). The thread id IS the root
// message's id, and the root itself carries no thread_id (the thread
// emerges at the first reply) — so the root is matched by message_id.
// An unknown thread returns empty.
func (p *Projection) ThreadMessages(threadID string) ([]ThreadMessage, error) {
	rows, err := p.db.Query(`
		SELECT m.message_id, COALESCE(m.reply_to_message_id,''), COALESCE(m.sender_principal_id,''),
		       m.created_at, r.body_hash
		FROM messages m JOIN revisions r ON r.revision_id = m.head_revision_id
		WHERE (m.thread_id = ? OR m.message_id = ?) AND m.retracted = 0
		ORDER BY m.created_at, m.message_id`, threadID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadMessage
	for rows.Next() {
		var tm ThreadMessage
		if err := rows.Scan(&tm.MessageID, &tm.ReplyTo, &tm.Sender, &tm.CreatedAt, &tm.BodyHash); err != nil {
			return nil, err
		}
		out = append(out, tm)
	}
	return out, rows.Err()
}

// ScopeMessageIDs returns the message IDs matching a search scope
// (RETR-D3, spec §7.1 search(query, scope, k)): any of topicNames (by
// name, like DigestCandidates), and/or a sender principal, and/or one
// thread. Returns nil (not empty) when no scope is set — callers treat
// nil as "no filtering".
func (p *Projection) ScopeMessageIDs(topicNames []string, sender, threadID string) (map[string]bool, error) {
	if len(topicNames) == 0 && sender == "" && threadID == "" {
		return nil, nil
	}
	q := `SELECT DISTINCT m.message_id FROM messages m`
	var args []any
	if len(topicNames) > 0 {
		// hard filter FIRST, resolving names — a nonexistent topic is a
		// typed refusal, not a silent empty result (TopicIDByName maps
		// missing to ("", nil), so check the id)
		for _, n := range topicNames {
			id, err := p.TopicIDByName(n)
			if err != nil {
				return nil, fmt.Errorf("scope topic %q: %w", n, err)
			}
			if id == "" {
				return nil, fmt.Errorf("scope topic %q does not exist (scopes never auto-create)", n)
			}
		}
		placeholders := strings.Repeat("?,", len(topicNames))
		q += ` JOIN topic_links l ON l.message_id = m.message_id AND l.removed = 0
		       JOIN topics t ON t.topic_id = l.topic_id AND t.name IN (` + placeholders[:len(placeholders)-1] + `)`
		for _, n := range topicNames {
			args = append(args, n)
		}
	}
	q += ` WHERE 1=1`
	if sender != "" {
		q += ` AND m.sender_principal_id = ?`
		args = append(args, sender)
	}
	if threadID != "" {
		q += ` AND m.thread_id = ?`
		args = append(args, threadID)
	}
	rows, err := p.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ResultMetaRow carries the human-readable context for one retrieval hit
// (RETR-D1/D2): who said it, in which thread, under which topics. Without
// it every result was opaque UUIDs — the agent burned a fetch per hit just
// to learn what it was.
type ResultMetaRow struct {
	Sender   string
	ThreadID string
	Topics   []string
}

// ResultMeta batch-fetches sender/thread/topics for a set of message IDs.
func (p *Projection) ResultMeta(messageIDs []string) (map[string]ResultMetaRow, error) {
	out := map[string]ResultMetaRow{}
	if len(messageIDs) == 0 {
		return out, nil
	}
	placeholders := strings.Repeat("?,", len(messageIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(messageIDs))
	for i, id := range messageIDs {
		args[i] = id
	}
	rows, err := p.db.Query(`
		SELECT message_id, COALESCE(sender_principal_id,''), COALESCE(thread_id,'')
		FROM messages WHERE message_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var m ResultMetaRow
		if err := rows.Scan(&id, &m.Sender, &m.ThreadID); err != nil {
			return nil, err
		}
		out[id] = m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	trows, err := p.db.Query(`
		SELECT l.message_id, t.name
		FROM topic_links l JOIN topics t ON t.topic_id = l.topic_id
		WHERE l.removed = 0 AND l.message_id IN (`+placeholders+`)
		ORDER BY t.name`, args...)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var id, name string
		if err := trows.Scan(&id, &name); err != nil {
			return nil, err
		}
		m := out[id]
		m.Topics = append(m.Topics, name)
		out[id] = m
	}
	return out, trows.Err()
}

// DigestCandidates lists non-retracted message IDs passing the view's hard
// topic filter (topic NAMES; empty filter = all), deterministically ordered.
func (p *Projection) DigestCandidates(topicNames []string) ([]string, error) {
	if len(topicNames) == 0 {
		rows, err := p.db.Query(`SELECT message_id FROM messages WHERE retracted = 0 ORDER BY created_at DESC, message_id`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanIDs(rows)
	}
	placeholders := strings.Repeat("?,", len(topicNames))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(topicNames))
	for i, n := range topicNames {
		args[i] = n
	}
	rows, err := p.db.Query(`
		SELECT DISTINCT m.message_id FROM messages m
		JOIN topic_links l ON l.message_id = m.message_id AND l.removed = 0
		JOIN topics t ON t.topic_id = l.topic_id
		WHERE m.retracted = 0 AND t.name IN (`+placeholders+`)
		ORDER BY m.message_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIDs(rows)
}

// MandatoryDigestIDs: explicit recipients of the view + actively pinned
// messages (mandatory-inclusion classes, rulings §7).
func (p *Projection) MandatoryDigestIDs(agentView string) (recipients, pinned []string, err error) {
	rows, err := p.db.Query(`SELECT rc.message_id FROM recipients rc
		JOIN messages m ON m.message_id = rc.message_id
		WHERE rc.agent_view = ? AND m.retracted = 0 ORDER BY m.created_at DESC, m.message_id`, agentView)
	if err != nil {
		return nil, nil, err
	}
	recipients, err = scanIDs(rows)
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	rows, err = p.db.Query(`SELECT DISTINCT m.message_id FROM messages m
		JOIN revisions r ON r.revision_id = m.head_revision_id
		JOIN pins pn ON pn.object_hash = r.body_hash AND pn.removed = 0
		WHERE m.retracted = 0 ORDER BY m.created_at DESC, m.message_id`)
	if err != nil {
		return nil, nil, err
	}
	pinned, err = scanIDs(rows)
	rows.Close()
	return recipients, pinned, err
}

func scanIDs(rows *sql.Rows) ([]string, error) {
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SaveExplanations stores the why_ranked inputs for one interaction —
// values as decimal STRINGS (no floats in durable payloads; this is a
// local projection table, but the string rule keeps arithmetic replayable).
func (p *Projection) SaveExplanations(interactionID, profile string, rows []ExplanationRow) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range rows {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO rank_explanations(interaction_id, message_id, profile, components_json, final_rank)
				VALUES (?,?,?,?,?)`, interactionID, r.MessageID, profile, r.ComponentsJSON, r.FinalRank); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ExplanationRow is one stored why_ranked record.
type ExplanationRow struct {
	MessageID      string
	ComponentsJSON string
	FinalRank      int
}

// CompactionStats reports the corpus reduced to its CURRENT effective state
// (spec §7 "compacted to current state"): how many events/revisions/assertions
// collapse into how few live entities (P2-6).
type CompactionStats struct {
	TotalEvents         int
	TotalRevisions      int
	LiveMessages        int // non-retracted messages (one head revision each)
	RetractedMessages   int
	SupersededRevisions int // revisions no longer any message's head
	ActiveTopicLinks    int
	RemovedTopicLinks   int
	ActivePins          int
	ActiveSubscriptions int
}

// Compaction computes the current-state compaction stats.
func (p *Projection) Compaction() (CompactionStats, error) {
	var c CompactionStats
	q := func(sql string, dst *int) error { return p.db.QueryRow(sql).Scan(dst) }
	for _, pair := range []struct {
		sql string
		dst *int
	}{
		{`SELECT count(*) FROM events`, &c.TotalEvents},
		{`SELECT count(*) FROM revisions`, &c.TotalRevisions},
		{`SELECT count(*) FROM messages WHERE retracted=0`, &c.LiveMessages},
		{`SELECT count(*) FROM messages WHERE retracted=1`, &c.RetractedMessages},
		{`SELECT count(*) FROM revisions r WHERE r.revision_id NOT IN (SELECT head_revision_id FROM messages)`, &c.SupersededRevisions},
		{`SELECT count(*) FROM topic_links WHERE removed=0`, &c.ActiveTopicLinks},
		{`SELECT count(*) FROM topic_links WHERE removed=1`, &c.RemovedTopicLinks},
		{`SELECT count(DISTINCT object_hash) FROM pins WHERE removed=0`, &c.ActivePins},
		{`SELECT count(*) FROM subscriptions WHERE disabled=0`, &c.ActiveSubscriptions},
	} {
		if err := q(pair.sql, pair.dst); err != nil {
			return c, err
		}
	}
	return c, nil
}

// NavTopic / NavThread summarise the corpus for the local navigation map
// (P2-5). All counts exclude retracted/removed rows.
type NavTopic struct {
	Name  string
	Count int
}
type NavThread struct {
	RootID     string
	ReplyCount int
}

// NavMap is the aggregated navigation/rollup snapshot for map.md (P2-5).
type NavMap struct {
	TotalMessages int
	TotalTopics   int
	PinnedObjects int
	Topics        []NavTopic  // by message count desc, then name
	Threads       []NavThread // by reply count desc, then root id
}

// NavMap computes the local map + rollup aggregation from the projection.
func (p *Projection) NavMap(topThreads int) (NavMap, error) {
	var m NavMap
	if err := p.db.QueryRow(`SELECT count(*) FROM messages WHERE retracted=0`).Scan(&m.TotalMessages); err != nil {
		return m, err
	}
	if err := p.db.QueryRow(`SELECT count(*) FROM topics`).Scan(&m.TotalTopics); err != nil {
		return m, err
	}
	if err := p.db.QueryRow(`SELECT count(DISTINCT object_hash) FROM pins WHERE removed=0`).Scan(&m.PinnedObjects); err != nil {
		return m, err
	}
	trows, err := p.db.Query(`
		SELECT t.name, count(DISTINCT l.message_id)
		FROM topics t LEFT JOIN topic_links l
		  ON l.topic_id=t.topic_id AND l.removed=0
		GROUP BY t.topic_id ORDER BY count(DISTINCT l.message_id) DESC, t.name`)
	if err != nil {
		return m, err
	}
	for trows.Next() {
		var nt NavTopic
		if err := trows.Scan(&nt.Name, &nt.Count); err != nil {
			trows.Close()
			return m, err
		}
		m.Topics = append(m.Topics, nt)
	}
	trows.Close()
	hrows, err := p.db.Query(`
		SELECT reply_to_message_id, count(*) FROM messages
		WHERE reply_to_message_id IS NOT NULL AND retracted=0
		GROUP BY reply_to_message_id
		ORDER BY count(*) DESC, reply_to_message_id LIMIT ?`, topThreads)
	if err != nil {
		return m, err
	}
	defer hrows.Close()
	for hrows.Next() {
		var th NavThread
		if err := hrows.Scan(&th.RootID, &th.ReplyCount); err != nil {
			return m, err
		}
		m.Threads = append(m.Threads, th)
	}
	return m, hrows.Err()
}

// ExplanationsForInteraction returns every stored why_ranked record for one
// interaction (P2-3b calibration replay reads the whole candidate set).
func (p *Projection) ExplanationsForInteraction(interactionID string) ([]ExplanationRow, error) {
	rows, err := p.db.Query(`SELECT message_id, components_json, final_rank FROM rank_explanations
		WHERE interaction_id=? ORDER BY final_rank`, interactionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExplanationRow
	for rows.Next() {
		var r ExplanationRow
		if err := rows.Scan(&r.MessageID, &r.ComponentsJSON, &r.FinalRank); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Explanation fetches a stored why_ranked record.
func (p *Projection) Explanation(interactionID, messageID string) (profile, componentsJSON string, finalRank int, err error) {
	err = p.db.QueryRow(`SELECT profile, components_json, final_rank FROM rank_explanations
		WHERE interaction_id=? AND message_id=?`, interactionID, messageID).
		Scan(&profile, &componentsJSON, &finalRank)
	if err == sql.ErrNoRows {
		return "", "", 0, fmt.Errorf("no stored ranking for interaction %s message %s", interactionID, messageID)
	}
	return
}
