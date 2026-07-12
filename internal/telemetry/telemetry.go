// Package telemetry is the LOCAL interaction log (spec §4.5 stream classes,
// rulings §10): a SEPARATE SQLite database under .cairn/ — never events,
// never signed, never replicated. Records per-interaction ids, positions,
// budgets, payload sizes, and outcomes; missing attribution is daemon-
// generated and flagged inferred=true.
package telemetry

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/ggoosen/cairn/internal/config"
)

const schema = `
CREATE TABLE IF NOT EXISTS interactions (
  interaction_id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,                 -- search | digest
  task_id TEXT,
  agent_surface TEXT,
  agent_instance_id TEXT,
  inferred INTEGER NOT NULL DEFAULT 0,
  query TEXT,
  budget_requested INTEGER,
  payload_chars INTEGER NOT NULL,
  result_count INTEGER NOT NULL,
  retrieval_mode TEXT,
  created_at TEXT NOT NULL,
  outcome TEXT,                       -- found | not_found | manual_workaround
  outcome_message_id TEXT,
  outcome_at TEXT
);
CREATE TABLE IF NOT EXISTS impressions (
  interaction_id TEXT NOT NULL,
  message_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  PRIMARY KEY (interaction_id, message_id)
);
CREATE TABLE IF NOT EXISTS latencies (
  kind TEXT NOT NULL,                 -- ack_to_lexical_visible
  micros INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
`

// Store is the telemetry database.
type Store struct{ db *sql.DB }

// Path: .cairn/telemetry.sqlite (derived-state territory; never replicated).
func Path(portableDir string) string {
	return filepath.Join(portableDir, config.DerivedDirName, "telemetry.sqlite")
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=2000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Interaction is one retrieval episode.
type Interaction struct {
	InteractionID   string
	Kind            string
	TaskID          string
	AgentSurface    string
	AgentInstanceID string
	Inferred        bool
	Query           string
	BudgetRequested int
	PayloadChars    int
	ResultCount     int
	RetrievalMode   string
	CreatedAt       time.Time
	ResultIDs       []string // in position order
}

// Record stores one interaction with its impressions.
func (s *Store) Record(it Interaction) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	inferred := 0
	if it.Inferred {
		inferred = 1
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO interactions
		(interaction_id, kind, task_id, agent_surface, agent_instance_id, inferred, query,
		 budget_requested, payload_chars, result_count, retrieval_mode, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		it.InteractionID, it.Kind, nz(it.TaskID), nz(it.AgentSurface), nz(it.AgentInstanceID), inferred,
		nz(it.Query), it.BudgetRequested, it.PayloadChars, it.ResultCount, it.RetrievalMode,
		it.CreatedAt.UTC().Format(config.WallTimeFormat)); err != nil {
		return err
	}
	for i, id := range it.ResultIDs {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO impressions(interaction_id, message_id, position) VALUES (?,?,?)`,
			it.InteractionID, id, i+1); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecordOutcome binds an outcome to an interaction_id (rulings §10: outcome
// commands REQUIRE the interaction id).
func (s *Store) RecordOutcome(interactionID, outcome, messageID string, at time.Time) error {
	switch outcome {
	case "found", "not_found", "manual_workaround":
	default:
		return fmt.Errorf("unknown outcome %q", outcome)
	}
	res, err := s.db.Exec(`UPDATE interactions SET outcome=?, outcome_message_id=?, outcome_at=? WHERE interaction_id=?`,
		outcome, nz(messageID), at.UTC().Format(config.WallTimeFormat), interactionID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("interaction %s not found (outcomes bind to interaction_id)", interactionID)
	}
	return nil
}

// RecordLatency stores one ack→lexical-visible sample.
func (s *Store) RecordLatency(kind string, d time.Duration, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO latencies(kind, micros, created_at) VALUES (?,?,?)`,
		kind, d.Microseconds(), at.UTC().Format(config.WallTimeFormat))
	return err
}

// GateStats aggregates for `cairn gates`.
type GateStats struct {
	Interactions      int
	BudgetedCount     int
	BudgetViolations  int
	OutcomeFound      int
	OutcomeNotFound   int
	OutcomeWorkaround int
	LatencySamples    int
	LatencyP95Micros  int64
}

func (s *Store) Gates() (*GateStats, error) {
	g := &GateStats{}
	if err := s.db.QueryRow(`SELECT count(*) FROM interactions`).Scan(&g.Interactions); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM interactions WHERE budget_requested > 0`).Scan(&g.BudgetedCount); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM interactions WHERE budget_requested > 0 AND payload_chars > budget_requested`).Scan(&g.BudgetViolations); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM interactions WHERE outcome='found'`).Scan(&g.OutcomeFound); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM interactions WHERE outcome='not_found'`).Scan(&g.OutcomeNotFound); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM interactions WHERE outcome='manual_workaround'`).Scan(&g.OutcomeWorkaround); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM latencies WHERE kind='ack_to_lexical_visible'`).Scan(&g.LatencySamples); err != nil {
		return nil, err
	}
	if g.LatencySamples > 0 {
		off := (g.LatencySamples * 95) / 100
		if off >= g.LatencySamples {
			off = g.LatencySamples - 1
		}
		err := s.db.QueryRow(`SELECT micros FROM latencies WHERE kind='ack_to_lexical_visible'
			ORDER BY micros LIMIT 1 OFFSET ?`, off).Scan(&g.LatencyP95Micros)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return g, nil
}

func nz(s string) any {
	if s == "" {
		return nil
	}
	return s
}
