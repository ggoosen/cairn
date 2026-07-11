package projection

import (
	"fmt"
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

// SearchLexical runs an FTS5 MATCH over HEAD revisions. Retracted messages
// are excluded by default and included only with includeRetracted
// (capability-gated at the caller — spec §5.3).
func (p *Projection) SearchLexical(query string, k int, includeRetracted bool) ([]SearchResult, error) {
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
