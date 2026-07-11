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
	return out, rows.Err()
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
