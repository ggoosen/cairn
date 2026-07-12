package projection

// Derivative + summary-check queries (P1 N4).

import (
	"database/sql"
	"errors"
	"fmt"
)

// DerivativeRow is one derivative record (spec §8.3 provenance).
type DerivativeRow struct {
	DerivativeID   string `json:"derivative_id"`
	BlobHash       string `json:"blob_hash"`
	DerivativeType string `json:"derivative_type"`
	TextHash       string `json:"text_hash,omitempty"`
	Extractor      string `json:"extractor"`
	Version        string `json:"extractor_version"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	Invalidated    bool   `json:"invalidated"`
	GeneratedAt    string `json:"generated_at"`
}

const derivCols = `derivative_id, blob_hash, derivative_type, COALESCE(text_hash,''),
	extractor, extractor_version, status, COALESCE(error,''), invalidated, generated_at`

func scanDeriv(scan func(...any) error) (*DerivativeRow, error) {
	var d DerivativeRow
	var inv int
	if err := scan(&d.DerivativeID, &d.BlobHash, &d.DerivativeType, &d.TextHash,
		&d.Extractor, &d.Version, &d.Status, &d.Error, &inv, &d.GeneratedAt); err != nil {
		return nil, err
	}
	d.Invalidated = inv == 1
	return &d, nil
}

// DerivativesForMessage lists derivative records for a message's attachments.
func (p *Projection) DerivativesForMessage(messageID string) ([]DerivativeRow, error) {
	rows, err := p.db.Query(`
		SELECT `+derivCols+` FROM derivatives
		WHERE blob_hash IN (SELECT object_hash FROM attachments WHERE message_id=?)
		ORDER BY generated_at, derivative_id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DerivativeRow
	for rows.Next() {
		d, err := scanDeriv(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// DerivativeByID returns one derivative record.
func (p *Projection) DerivativeByID(id string) (*DerivativeRow, error) {
	row := p.db.QueryRow(`SELECT `+derivCols+` FROM derivatives WHERE derivative_id=?`, id)
	d, err := scanDeriv(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("derivative %s not found", id)
	}
	return d, err
}

// AttachmentBlob is one attachment awaiting derivation.
type AttachmentBlob struct {
	ObjectHash string
	MessageID  string
	Mime       string
	Filename   string
}

// AttachmentsNeedingDerivative returns attachment blobs with NO derivative
// record (neither ok nor failed) — the enricher's work queue, reconstructed
// from state (spec §8.1: no separate durable queue exists).
func (p *Projection) AttachmentsNeedingDerivative(limit int) ([]AttachmentBlob, error) {
	rows, err := p.db.Query(`
		SELECT a.object_hash, a.message_id, a.mime, COALESCE(a.filename,'')
		FROM attachments a
		WHERE NOT EXISTS (SELECT 1 FROM derivatives d WHERE d.blob_hash = a.object_hash AND d.invalidated = 0)
		ORDER BY a.message_id, a.object_hash LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AttachmentBlob
	for rows.Next() {
		var b AttachmentBlob
		if err := rows.Scan(&b.ObjectHash, &b.MessageID, &b.Mime, &b.Filename); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DurableBlob is one attachment blob with its N7 durability class.
type DurableBlob struct {
	ObjectHash string
	MessageID  string
	Durability string
	ByteLen    int64
}

// AttachmentBlobs lists every distinct attachment blob with its durability
// class (N7 replication + doctor). One row per (blob, class); a blob shared by
// two messages with different classes yields the STRONGER class is left to the
// caller — here each (message, blob) pair is returned.
func (p *Projection) AttachmentBlobs() ([]DurableBlob, error) {
	rows, err := p.db.Query(`
		SELECT object_hash, message_id, durability, byte_len
		FROM attachments ORDER BY object_hash, message_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DurableBlob
	for rows.Next() {
		var b DurableBlob
		if err := rows.Scan(&b.ObjectHash, &b.MessageID, &b.Durability, &b.ByteLen); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DerivativeMessageHits searches derivative text and maps hits back to the
// owning messages (attachment join). Provenance flows: message → attachment
// blob_hash → derivative (extractor+version) → text_hash.
func (p *Projection) DerivativeMessageHits(query string, k int, includeRetracted bool) ([]string, error) {
	query = FTSQuery(query)
	rows, err := p.db.Query(`
		SELECT DISTINCT m.message_id
		FROM fts_derivatives
		JOIN fts_derivatives_map map ON fts_derivatives.rowid = map.rowid
		JOIN derivatives d ON d.derivative_id = map.derivative_id AND d.invalidated = 0 AND d.status = 'ok'
		JOIN attachments a ON a.object_hash = d.blob_hash
		JOIN messages m ON m.message_id = a.message_id
		WHERE fts_derivatives MATCH ? AND (m.retracted = 0 OR ?)
		ORDER BY m.message_id LIMIT ?`, query, boolInt(includeRetracted), k)
	if err != nil {
		return nil, fmt.Errorf("derivative search: %w", err)
	}
	defer rows.Close()
	return scanIDs(rows)
}

// --- receiver summary check (spec §8.4) --------------------------------------

// SummaryRow is the sender claim plus the receiver's local verdict.
type SummaryRow struct {
	MessageID     string `json:"message_id"`
	SenderSummary string `json:"sender_summary"`
	Checked       bool   `json:"checked"`
	AgreeBP       int    `json:"agree_bp,omitempty"` // cosine, basis points
	Disagree      bool   `json:"disagree"`
	LocalSummary  string `json:"local_summary,omitempty"`
	LocalMethod   string `json:"local_method,omitempty"`
	CheckedAt     string `json:"checked_at,omitempty"`
}

// SummaryForMessage returns the summary record (nil if the message carried
// no sender summary).
func (p *Projection) SummaryForMessage(messageID string) (*SummaryRow, error) {
	var s SummaryRow
	var checked, disagree int
	var agree sql.NullInt64
	var local, method, at sql.NullString
	err := p.db.QueryRow(`SELECT message_id, sender_summary, checked, agree_bp, disagree,
			local_summary, local_method, checked_at FROM message_summaries WHERE message_id=?`,
		messageID).Scan(&s.MessageID, &s.SenderSummary, &checked, &agree, &disagree, &local, &method, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.Checked, s.Disagree = checked == 1, disagree == 1
	s.AgreeBP, s.LocalSummary, s.LocalMethod, s.CheckedAt = int(agree.Int64), local.String, method.String, at.String
	return &s, nil
}

// SummariesNeedingCheck lists messages whose sender summary has not been
// verified yet (enrichment work queue, reconstructed from state).
func (p *Projection) SummariesNeedingCheck(limit int) ([]string, error) {
	rows, err := p.db.Query(`SELECT message_id FROM message_summaries WHERE checked=0 ORDER BY message_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIDs(rows)
}

// RecordSummaryCheck stores the receiver's verdict (enrichment-class state,
// recomputed after reindex --semantic style rebuilds).
func (p *Projection) RecordSummaryCheck(messageID string, agreeBP int, disagree bool, localSummary, method, at string) error {
	_, err := p.db.Exec(`UPDATE message_summaries
		SET checked=1, agree_bp=?, disagree=?, local_summary=?, local_method=?, checked_at=?
		WHERE message_id=?`,
		agreeBP, boolInt(disagree), nullable(localSummary), nullable(method), at, messageID)
	return err
}

// DisputedSummaries returns message ids whose sender summary disagrees with
// the body (digest marker input).
func (p *Projection) DisputedSummaries() (map[string]bool, error) {
	rows, err := p.db.Query(`SELECT message_id FROM message_summaries WHERE disagree=1`)
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
