// Package projection implements the SQLite projection of the event log
// (rulings §6): WAL, synchronous=FULL, foreign keys on, ONE writer (the
// daemon). The projection is DERIVED — deleting the database and replaying
// the log MUST reproduce identical query results. The checkpoint row commits
// in the SAME transaction as each applied event, so an event is either fully
// projected and checkpointed or untouched (crash row 9: mid-projection
// crash resumes idempotently).
//
// Build note: requires the `sqlite_fts5` build tag (see Makefile/CLAUDE.md).
package projection

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
)

//go:embed schema.sql
var schemaSQL string

// ErrSchemaVersion: the on-disk projection predates this build. The
// projection is derived — callers rebuild it (delete + replay/reindex).
var ErrSchemaVersion = errors.New("projection schema version mismatch")

// BodyFetch resolves a body_hash to indexable text. ok=false means the body
// is unavailable (e.g. expired ephemeral) — the revision is simply not
// lexically indexed; never an error (TESTING.md: expiry racing reindex must
// not crash).
type BodyFetch func(hash string) (text []byte, ok bool)

// Projection is the open SQLite database.
type Projection struct {
	db        *sql.DB
	path      string
	bodyFetch BodyFetch
}

// Open opens (creating if necessary) the projection database.
func Open(path string, bodyFetch BodyFetch) (*Projection, error) {
	if bodyFetch == nil {
		bodyFetch = func(string) ([]byte, bool) { return nil, false }
	}
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; serialized access
	p := &Projection{db: db, path: path, bodyFetch: bodyFetch}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='meta'`).Scan(&n); err != nil {
		db.Close()
		return nil, err
	}
	if n == 0 {
		if _, err := db.Exec(schemaSQL); err != nil {
			db.Close()
			return nil, fmt.Errorf("creating schema: %w", err)
		}
		if _, err := db.Exec(`INSERT INTO meta(key, value) VALUES ('schema_version', ?)`,
			fmt.Sprintf("%d", config.ProjectionSchemaVersion)); err != nil {
			db.Close()
			return nil, err
		}
	} else {
		var v string
		if err := db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil {
			db.Close()
			return nil, fmt.Errorf("reading schema_version: %w", err)
		}
		if v != fmt.Sprintf("%d", config.ProjectionSchemaVersion) {
			db.Close()
			return nil, fmt.Errorf("%w: found %s, want %d", ErrSchemaVersion, v, config.ProjectionSchemaVersion)
		}
	}
	return p, nil
}

func (p *Projection) Close() error { return p.db.Close() }

// Checkpoint returns the last applied sequence for an origin (0 if none).
func (p *Projection) Checkpoint(originDeviceID string, generation int) (int64, error) {
	var seq int64
	err := p.db.QueryRow(`SELECT last_applied_sequence FROM checkpoint WHERE origin_device_id=? AND origin_generation=?`,
		originDeviceID, generation).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

// Apply projects one verified event. Idempotent: events at or below the
// origin checkpoint are skipped; the checkpoint advances in the same
// transaction as the projection writes (rulings §6).
func (p *Projection) Apply(env *event.Envelope, _ []byte) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var last int64
	err = tx.QueryRow(`SELECT last_applied_sequence FROM checkpoint WHERE origin_device_id=? AND origin_generation=?`,
		env.OriginDeviceID, env.OriginGeneration).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if env.OriginSequence <= last {
		return nil // already applied
	}
	if env.OriginSequence != last+1 {
		return fmt.Errorf("projection gap: origin %s/%d at %d, event %d arrived",
			env.OriginDeviceID, env.OriginGeneration, last, env.OriginSequence)
	}

	if _, err := tx.Exec(`INSERT INTO events(event_id, event_type, origin_device_id, origin_generation, origin_sequence,
			previous_event_id, actor_principal_id, actor_task_id, correlation_id, wall_time, payload_json)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		env.EventID, env.EventType, env.OriginDeviceID, env.OriginGeneration, env.OriginSequence,
		nullable(env.PreviousOriginEventID), nullable(env.ActorPrincipalID), nullable(env.ActorTaskID),
		nullable(env.CorrelationID), env.WallTime, string(env.Payload)); err != nil {
		return fmt.Errorf("events insert for %s: %w", env.EventID, err)
	}

	// F1 ruling 3 (defense in depth): a payload that cannot project is
	// PARKED — quarantined in the same transaction — and the stream
	// continues. The projector never stalls; the log is never touched.
	if _, err := tx.Exec(`SAVEPOINT payload`); err != nil {
		return err
	}
	if perr := p.applyPayload(tx, env); perr != nil {
		if _, err := tx.Exec(`ROLLBACK TO payload`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO parked_events
				(event_id, origin_device_id, origin_generation, origin_sequence, event_type, error, parked_at)
				VALUES (?,?,?,?,?,?,?)`,
			env.EventID, env.OriginDeviceID, env.OriginGeneration, env.OriginSequence,
			env.EventType, perr.Error(), time.Now().UTC().Format(config.WallTimeFormat)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`RELEASE payload`); err != nil {
		return err
	}

	if _, err := tx.Exec(`INSERT INTO checkpoint(origin_device_id, origin_generation, last_applied_sequence, last_applied_event_id)
			VALUES (?,?,?,?)
			ON CONFLICT(origin_device_id, origin_generation) DO UPDATE
			SET last_applied_sequence=excluded.last_applied_sequence, last_applied_event_id=excluded.last_applied_event_id`,
		env.OriginDeviceID, env.OriginGeneration, env.OriginSequence, env.EventID); err != nil {
		return err
	}
	return tx.Commit()
}

// --- payload projection -----------------------------------------------------

type attachmentPayload struct {
	ObjectHash string `json:"object_hash"`
	ByteLen    int64  `json:"byte_len"`
	Mime       string `json:"mime"`
	Filename   string `json:"filename"`
}

type sourceRefPayload struct {
	Path        string `json:"path"`
	Repo        string `json:"repo"`
	ContentHash string `json:"content_hash"`
	ImportedAt  string `json:"imported_at"`
}

type publishPayload struct {
	MessageID        string              `json:"message_id"`
	RevisionID       string              `json:"revision_id"`
	BodyHash         string              `json:"body_hash"`
	BodyBytes        string              `json:"body_bytes"`
	BodyLen          int64               `json:"body_len"`
	BodyMime         string              `json:"body_mime"`
	TextClass        string              `json:"text_class"`
	DeclaredPriority int                 `json:"declared_priority"`
	ThreadID         string              `json:"thread_id"`
	ReplyToMessageID string              `json:"reply_to_message_id"`
	Recipients       []string            `json:"recipients"`
	Attachments      []attachmentPayload `json:"attachments"`
	SourceRef        *sourceRefPayload   `json:"source_ref"`
}

type revisionPayload struct {
	RevisionID        string   `json:"revision_id"`
	ParentRevisionIDs []string `json:"parent_revision_ids"`
	BodyHash          string   `json:"body_hash"`
	BodyBytes         string   `json:"body_bytes"`
	BodyLen           int64    `json:"body_len"`
	MachineMerged     bool     `json:"machine_merged"`
}

func (p *Projection) applyPayload(tx *sql.Tx, env *event.Envelope) error {
	switch env.EventType {
	case "message.publish", "message.reply":
		var pl publishPayload
		if err := json.Unmarshal(env.Payload, &pl); err != nil {
			return err
		}
		if pl.BodyMime == "" {
			pl.BodyMime = "text/markdown"
		}
		if _, err := tx.Exec(`INSERT INTO messages(message_id, thread_id, reply_to_message_id, head_revision_id,
				declared_priority, text_class, sender_principal_id, created_event_id, created_at)
				VALUES (?,?,?,?,?,?,?,?,?)`,
			pl.MessageID, nullable(pl.ThreadID), nullable(pl.ReplyToMessageID), pl.RevisionID,
			pl.DeclaredPriority, pl.TextClass, nullable(env.ActorPrincipalID), env.EventID, env.WallTime); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO revisions(revision_id, message_id, body_hash, body_len, body_mime, machine_merged, created_event_id, created_at)
				VALUES (?,?,?,?,?,0,?,?)`,
			pl.RevisionID, pl.MessageID, pl.BodyHash, pl.BodyLen, pl.BodyMime, env.EventID, env.WallTime); err != nil {
			return err
		}
		for _, r := range pl.Recipients {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO recipients(message_id, agent_view) VALUES (?,?)`, pl.MessageID, r); err != nil {
				return err
			}
		}
		for _, a := range pl.Attachments {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO attachments(message_id, object_hash, byte_len, mime, filename)
					VALUES (?,?,?,?,?)`, pl.MessageID, a.ObjectHash, a.ByteLen, a.Mime, nullable(a.Filename)); err != nil {
				return err
			}
		}
		if pl.SourceRef != nil {
			// M9 ingest hook: rebuildable provenance row; replay-deterministic
			// (last event wins on path collisions).
			if _, err := tx.Exec(`INSERT OR REPLACE INTO source_refs(path, message_id) VALUES (?,?)`,
				pl.SourceRef.Path, pl.MessageID); err != nil {
				return err
			}
		}
		return p.indexRevision(tx, pl.RevisionID, pl.BodyHash, pl.BodyBytes)

	case "message.revise_body":
		var pl struct {
			MessageID string            `json:"message_id"`
			Revisions []revisionPayload `json:"revisions"`
		}
		if err := json.Unmarshal(env.Payload, &pl); err != nil {
			return err
		}
		if len(pl.Revisions) == 0 {
			return errors.New("revise_body with no revisions")
		}
		var mime string
		if err := tx.QueryRow(`SELECT r.body_mime FROM revisions r JOIN messages m ON m.head_revision_id = r.revision_id AND m.message_id=?`,
			pl.MessageID).Scan(&mime); err != nil {
			return fmt.Errorf("revise_body for unknown message %s: %w", pl.MessageID, err)
		}
		for _, rev := range pl.Revisions {
			if _, err := tx.Exec(`INSERT INTO revisions(revision_id, message_id, body_hash, body_len, body_mime, machine_merged, created_event_id, created_at)
					VALUES (?,?,?,?,?,?,?,?)`,
				rev.RevisionID, pl.MessageID, rev.BodyHash, rev.BodyLen, mime, boolInt(rev.MachineMerged), env.EventID, env.WallTime); err != nil {
				return err
			}
			for ord, parent := range rev.ParentRevisionIDs {
				if _, err := tx.Exec(`INSERT INTO revision_parents(revision_id, parent_revision_id, ord) VALUES (?,?,?)`,
					rev.RevisionID, parent, ord); err != nil {
					return err
				}
			}
			if err := p.indexRevision(tx, rev.RevisionID, rev.BodyHash, rev.BodyBytes); err != nil {
				return err
			}
		}
		head := pl.Revisions[len(pl.Revisions)-1].RevisionID
		_, err := tx.Exec(`UPDATE messages SET head_revision_id=? WHERE message_id=?`, head, pl.MessageID)
		return err

	case "message.retract":
		var pl struct {
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(env.Payload, &pl); err != nil {
			return err
		}
		// projection FLAG — history preserved, FTS rows untouched (rulings §6)
		_, err := tx.Exec(`UPDATE messages SET retracted=1, retracted_event_id=? WHERE message_id=?`, env.EventID, pl.MessageID)
		return err

	case "topic.create":
		var pl struct {
			TopicID string `json:"topic_id"`
			Name    string `json:"name"`
		}
		if err := json.Unmarshal(env.Payload, &pl); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO topics(topic_id, name, created_event_id) VALUES (?,?,?)`,
			pl.TopicID, pl.Name, env.EventID)
		return err

	case "topic.link.add":
		var pl struct {
			LinkID    string `json:"link_id"`
			MessageID string `json:"message_id"`
			TopicID   string `json:"topic_id"`
			Protected bool   `json:"protected"`
		}
		if err := json.Unmarshal(env.Payload, &pl); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO topic_links(link_id, message_id, topic_id, protected) VALUES (?,?,?,?)`,
			pl.LinkID, pl.MessageID, pl.TopicID, boolInt(pl.Protected))
		return err

	case "topic.link.remove":
		var pl struct {
			RemovedLinkIDs []string `json:"removed_link_ids"`
		}
		if err := json.Unmarshal(env.Payload, &pl); err != nil {
			return err
		}
		for _, id := range pl.RemovedLinkIDs {
			// Observed-remove: only the listed assertions die; concurrent adds
			// (different link_id) survive. Protected links survive AUTOMATIC
			// removal — spec §5.5 "auto-processes may not remove"; the
			// operator principal may (conservative reading, noted in
			// PROGRESS.md).
			if env.ActorPrincipalID == "operator" {
				if _, err := tx.Exec(`UPDATE topic_links SET removed=1, removed_event_id=? WHERE link_id=?`, env.EventID, id); err != nil {
					return err
				}
			} else {
				if _, err := tx.Exec(`UPDATE topic_links SET removed=1, removed_event_id=? WHERE link_id=? AND protected=0`, env.EventID, id); err != nil {
					return err
				}
			}
		}
		return nil

	case "blob.pin":
		var pl struct {
			PinID       string `json:"pin_id"`
			PrincipalID string `json:"principal_id"`
			ObjectHash  string `json:"object_hash"`
			Durability  string `json:"durability"`
		}
		if err := json.Unmarshal(env.Payload, &pl); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO pins(pin_id, principal_id, object_hash, durability) VALUES (?,?,?,?)`,
			pl.PinID, pl.PrincipalID, pl.ObjectHash, pl.Durability)
		return err

	case "blob.unpin":
		var pl struct {
			PinIDs []string `json:"pin_ids"`
		}
		if err := json.Unmarshal(env.Payload, &pl); err != nil {
			return err
		}
		for _, id := range pl.PinIDs {
			if _, err := tx.Exec(`UPDATE pins SET removed=1 WHERE pin_id=?`, id); err != nil {
				return err
			}
		}
		return nil

	case "signal.emit":
		var pl struct {
			MessageID string `json:"message_id"`
			Kind      string `json:"kind"`
			Weight    *int64 `json:"weight"`
		}
		if err := json.Unmarshal(env.Payload, &pl); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO signals(event_id, message_id, kind, weight, actor_principal_id, created_at)
				VALUES (?,?,?,?,?,?)`,
			env.EventID, pl.MessageID, pl.Kind, pl.Weight, nullable(env.ActorPrincipalID), env.WallTime)
		return err

	case "cairn.genesis":
		var pl struct {
			CairnID string `json:"cairn_id"`
		}
		if err := json.Unmarshal(env.Payload, &pl); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('cairn_id', ?)`, pl.CairnID)
		return err

	case "device.add", "device.revoke":
		return nil // security events live in the events table; keys are the chain verifier's concern

	default:
		// Unknown event types are preserved in the events table (already
		// inserted) and otherwise ignored by this schema version (rulings §2).
		return nil
	}
}

// indexRevision inserts the revision body into the contentless FTS index
// (synchronous lexical enrichment — rulings §6) and records enrichment
// state. A missing body (expired ephemeral) is recorded, never fatal.
func (p *Projection) indexRevision(tx *sql.Tx, revisionID, bodyHash, inline string) error {
	body := []byte(inline)
	if len(body) == 0 {
		var ok bool
		body, ok = p.bodyFetch(bodyHash)
		if !ok {
			_, err := tx.Exec(`INSERT OR REPLACE INTO enrichment(revision_id, lexical_indexed, embedded) VALUES (?,0,0)`, revisionID)
			return err
		}
	}
	res, err := tx.Exec(`INSERT INTO fts_map(revision_id) VALUES (?)`, revisionID)
	if err != nil {
		return err
	}
	rowid, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO fts_revisions(rowid, body) VALUES (?,?)`, rowid, string(body)); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR REPLACE INTO enrichment(revision_id, lexical_indexed, embedded) VALUES (?,1,0)`, revisionID)
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
