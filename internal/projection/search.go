package projection

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ggoosen/cairn/internal/object"
)

// SearchResult is one lexical hit (head revisions only). Score is BM25
// (lower = better in SQLite's bm25()); ties break deterministically by
// message_id, then revision_id (rulings §7 determinism requirement).
type SearchResult struct {
	MessageID  string  `json:"message_id"`
	RevisionID string  `json:"revision_id"`
	BodyHash   string  `json:"body_hash"`
	TextClass  string  `json:"text_class"`
	Priority   int     `json:"declared_priority"`
	CreatedAt  string  `json:"created_at"`
	Retracted  bool    `json:"retracted"`
	Score      float64 `json:"score"`
}

// FTSQuery converts a raw user/agent query into a safe FTS5 MATCH
// expression: each whitespace-separated term is double-quoted (embedded
// quotes doubled), so FTS5 operator characters (- # @ : etc.) can never
// produce syntax errors or column filters. Semantics: implicit AND of
// phrase-terms, bm25-ranked — identical to bareword behavior for plain
// words.
func FTSQuery(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return `""`
	}
	quoted := make([]string, len(fields))
	for i, f := range fields {
		quoted[i] = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " ")
}

// SearchLexical runs an FTS5 MATCH over HEAD revisions. Retracted messages
// are excluded by default and included only with includeRetracted
// (capability-gated at the caller — spec §5.3).
func (p *Projection) SearchLexical(query string, k int, includeRetracted bool) ([]SearchResult, error) {
	query = FTSQuery(query)
	rows, err := p.db.Query(`
		SELECT m.message_id, r.revision_id, r.body_hash, m.text_class, m.declared_priority,
		       m.created_at, m.retracted, bm25(fts_revisions) AS score
		FROM fts_revisions
		JOIN fts_map map ON fts_revisions.rowid = map.rowid
		JOIN revisions r ON r.revision_id = map.revision_id
		JOIN messages m ON m.message_id = r.message_id AND m.head_revision_id = r.revision_id
		WHERE fts_revisions MATCH ? AND (m.retracted = 0 OR ?)
		ORDER BY score, m.message_id, r.revision_id
		LIMIT ?`, query, boolInt(includeRetracted), k)
	if err != nil {
		return nil, fmt.Errorf("lexical search: %w", err)
	}
	defer rows.Close()
	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		var retracted int
		if err := rows.Scan(&r.MessageID, &r.RevisionID, &r.BodyHash, &r.TextClass, &r.Priority,
			&r.CreatedAt, &retracted, &r.Score); err != nil {
			return nil, err
		}
		r.Retracted = retracted == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// VisibleLinks returns the live (observed-remove) topic link set for a
// message, ordered deterministically.
func (p *Projection) VisibleLinks(messageID string) ([]string, error) {
	rows, err := p.db.Query(`SELECT topic_id FROM topic_links WHERE message_id=? AND removed=0 ORDER BY topic_id, link_id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LiveLinkIDs returns the live link assertion ids for a message (observed-
// remove set membership), ordered by link_id.
func (p *Projection) LiveLinkIDs(messageID string) ([]string, error) {
	rows, err := p.db.Query(`SELECT link_id FROM topic_links WHERE message_id=? AND removed=0 ORDER BY link_id`, messageID)
	if err != nil {
		return nil, err
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

// ActivePins reports whether any active pin intent exists for an object
// (spec §5.5: pinned while ANY active pin exists).
func (p *Projection) ActivePins(objectHash string) (int, error) {
	var n int
	err := p.db.QueryRow(`SELECT count(*) FROM pins WHERE object_hash=? AND removed=0`, objectHash).Scan(&n)
	return n, err
}

// ObjectRefs feeds ephemeral-TTL housekeeping (M2's object.HousekeepEphemeral):
// every revision's body reference with its message text class and creation
// time. Pinned objects are excluded (an active pin keeps the object).
func (p *Projection) ObjectRefs() ([]object.Ref, error) {
	rows, err := p.db.Query(`
		SELECT r.body_hash, m.text_class, r.created_at
		FROM revisions r JOIN messages m ON m.message_id = r.message_id
		WHERE NOT EXISTS (SELECT 1 FROM pins pn WHERE pn.object_hash = r.body_hash AND pn.removed = 0)
		ORDER BY r.body_hash, r.revision_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []object.Ref
	for rows.Next() {
		var hash, class, created string
		if err := rows.Scan(&hash, &class, &created); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("unparseable created_at %q: %w", created, err)
		}
		out = append(out, object.Ref{Hash: hash, TextClass: class, CreatedAt: t})
	}
	return out, rows.Err()
}

// AppliedEvent is a projected event row (receipt regeneration).
type AppliedEvent struct {
	EventID   string
	EventType string
	Payload   []byte
}

// EventsByCorrelation returns the events created under one outbox
// request_id (correlation_id), in origin-sequence order — the idempotency
// lookup: a duplicate bundle regenerates its receipt from these (rulings §8).
func (p *Projection) EventsByCorrelation(correlationID string) ([]AppliedEvent, error) {
	rows, err := p.db.Query(`SELECT event_id, event_type, payload_json FROM events
		WHERE correlation_id=? ORDER BY origin_device_id, origin_generation, origin_sequence`, correlationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppliedEvent
	for rows.Next() {
		var e AppliedEvent
		var payload string
		if err := rows.Scan(&e.EventID, &e.EventType, &payload); err != nil {
			return nil, err
		}
		e.Payload = []byte(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MessageInfo is peek-level metadata for one logical message.
type MessageInfo struct {
	MessageID        string `json:"message_id"`
	ThreadID         string `json:"thread_id,omitempty"`
	ReplyToMessageID string `json:"reply_to_message_id,omitempty"`
	HeadRevisionID   string `json:"head_revision_id"`
	BodyHash         string `json:"body_hash"`
	BodyLen          int64  `json:"body_len"`
	BodyMime         string `json:"body_mime"`
	TextClass        string `json:"text_class"`
	Priority         int    `json:"declared_priority"`
	Sender           string `json:"sender,omitempty"`
	CreatedAt        string `json:"created_at"`
	CreatedEventID   string `json:"created_event_id"`
	Retracted        bool   `json:"retracted"`
}

// MessageInfo returns metadata for a message and its head revision.
func (p *Projection) MessageInfo(messageID string) (*MessageInfo, error) {
	var mi MessageInfo
	var thread, reply, sender sql.NullString
	var retracted int
	err := p.db.QueryRow(`
		SELECT m.message_id, m.thread_id, m.reply_to_message_id, m.head_revision_id,
		       r.body_hash, r.body_len, r.body_mime, m.text_class, m.declared_priority,
		       m.sender_principal_id, m.created_at, m.created_event_id, m.retracted
		FROM messages m JOIN revisions r ON r.revision_id = m.head_revision_id
		WHERE m.message_id = ?`, messageID).Scan(
		&mi.MessageID, &thread, &reply, &mi.HeadRevisionID,
		&mi.BodyHash, &mi.BodyLen, &mi.BodyMime, &mi.TextClass, &mi.Priority,
		&sender, &mi.CreatedAt, &mi.CreatedEventID, &retracted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("message %s not found", messageID)
	}
	if err != nil {
		return nil, err
	}
	mi.ThreadID, mi.ReplyToMessageID, mi.Sender = thread.String, reply.String, sender.String
	mi.Retracted = retracted == 1
	return &mi, nil
}

// RevisionInfo is one revision row (export-ingest base validation).
type RevisionInfo struct {
	RevisionID string
	MessageID  string
	BodyHash   string
	BodyLen    int64
	BodyMime   string
}

// RevisionInfo looks up a revision by id.
func (p *Projection) RevisionInfo(revisionID string) (*RevisionInfo, error) {
	var ri RevisionInfo
	err := p.db.QueryRow(`SELECT revision_id, message_id, body_hash, body_len, body_mime
		FROM revisions WHERE revision_id=?`, revisionID).Scan(
		&ri.RevisionID, &ri.MessageID, &ri.BodyHash, &ri.BodyLen, &ri.BodyMime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("revision %s not found", revisionID)
	}
	if err != nil {
		return nil, err
	}
	return &ri, nil
}
