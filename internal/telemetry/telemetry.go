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
  principal TEXT,                     -- N2 capability principal hierarchy
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
CREATE TABLE IF NOT EXISTS sub_observations (
  subscription_id TEXT NOT NULL,      -- N3 (R24): observed similarity history
  sim_bp INTEGER NOT NULL,            -- cosine in basis points (integers only)
  observed_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sub_deliveries (
  subscription_id TEXT NOT NULL,      -- N3: delivery history is telemetry-class (R25)
  message_id TEXT NOT NULL,
  delivered_at TEXT NOT NULL,
  PRIMARY KEY (subscription_id, message_id)
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
	// N2 migration: pre-existing local telemetry DBs lack the principal
	// column (telemetry is cache-class; additive ALTER is safe).
	var hasPrincipal bool
	rows, err := db.Query("PRAGMA table_info(interactions)")
	if err != nil {
		db.Close()
		return nil, err
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			db.Close()
			return nil, err
		}
		if name == "principal" {
			hasPrincipal = true
		}
	}
	rows.Close()
	if !hasPrincipal {
		if _, err := db.Exec("ALTER TABLE interactions ADD COLUMN principal TEXT"); err != nil {
			db.Close()
			return nil, err
		}
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
	Principal       string // N2 capability principal hierarchy
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
		 budget_requested, payload_chars, result_count, retrieval_mode, principal, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		it.InteractionID, it.Kind, nz(it.TaskID), nz(it.AgentSurface), nz(it.AgentInstanceID), inferred,
		nz(it.Query), it.BudgetRequested, it.PayloadChars, it.ResultCount, it.RetrievalMode,
		nz(it.Principal), it.CreatedAt.UTC().Format(config.WallTimeFormat)); err != nil {
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

// Principal returns the recorded principal hierarchy for one interaction.
func (s *Store) Principal(interactionID string) (string, error) {
	var p sql.NullString
	err := s.db.QueryRow(`SELECT principal FROM interactions WHERE interaction_id=?`, interactionID).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("interaction %s not found", interactionID)
	}
	if err != nil {
		return "", err
	}
	return p.String, nil
}

// Demand is the P2 salience demand signal (spec §9.2) per message: how many
// times it was shown (impressions) vs how many DISTINCT operator tasks found it
// (fetches — re-find across separate tasks is strong evidence; one orchestration
// run is one demand cluster). Both maps are keyed by message_id.
type Demand struct {
	Impressions map[string]int
	Fetches     map[string]int
}

// DemandByMessage aggregates the local interaction log into the demand signal.
func (s *Store) DemandByMessage() (Demand, error) {
	d := Demand{Impressions: map[string]int{}, Fetches: map[string]int{}}
	rows, err := s.db.Query(`SELECT message_id, count(*) FROM impressions GROUP BY message_id`)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return d, err
		}
		d.Impressions[id] = n
	}
	rows.Close()
	// a "fetch" = a `found` outcome; strength is DISTINCT operator tasks, so an
	// item re-found across separate tasks counts once per task.
	frows, err := s.db.Query(`
		SELECT outcome_message_id, count(DISTINCT COALESCE(NULLIF(task_id,''), interaction_id))
		FROM interactions
		WHERE outcome='found' AND outcome_message_id IS NOT NULL AND outcome_message_id<>''
		GROUP BY outcome_message_id`)
	if err != nil {
		return d, err
	}
	defer frows.Close()
	for frows.Next() {
		var id string
		var n int
		if err := frows.Scan(&id, &n); err != nil {
			return d, err
		}
		d.Fetches[id] = n
	}
	return d, frows.Err()
}

// FoundEpisode is one calibration episode's telemetry side (P2-3b): an
// interaction whose operator outcome was `found`, with the task it belonged to
// and the message they found useful.
type FoundEpisode struct {
	InteractionID  string
	TaskID         string
	FoundMessageID string
}

// AllInteractionIDs lists every recorded interaction id (P2-3b rank-stats walks
// them to aggregate per-term contribution distributions).
func (s *Store) AllInteractionIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT interaction_id FROM interactions`)
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

// FoundEpisodes lists interactions with a `found` outcome (the genuine
// retrieval episodes calibration replays, spec §9.3).
func (s *Store) FoundEpisodes() ([]FoundEpisode, error) {
	rows, err := s.db.Query(`
		SELECT interaction_id, COALESCE(task_id,''), outcome_message_id
		FROM interactions
		WHERE outcome='found' AND outcome_message_id IS NOT NULL AND outcome_message_id<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FoundEpisode
	for rows.Next() {
		var e FoundEpisode
		if err := rows.Scan(&e.InteractionID, &e.TaskID, &e.FoundMessageID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
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

// --- durable-subscription delivery tracking (N3; R24 window/cap math) -------
// Local-only: caps and windows survive projection rebuilds but are never
// replicated — each node meters its own digest deliveries.

// RecordSubDelivery marks one message as delivered for a subscription.
func (s *Store) RecordSubDelivery(subscriptionID, messageID string, at time.Time) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO sub_deliveries(subscription_id, message_id, delivered_at) VALUES (?,?,?)`,
		subscriptionID, messageID, at.UTC().Format(config.WallTimeFormat))
	return err
}

// SubDelivered returns every message already delivered for a subscription.
func (s *Store) SubDelivered(subscriptionID string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT message_id FROM sub_deliveries WHERE subscription_id=?`, subscriptionID)
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

// SubDeliveredSince counts deliveries at or after the given instant
// (window allowance and daily push-cap math).
func (s *Store) SubDeliveredSince(subscriptionID string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM sub_deliveries WHERE subscription_id=? AND delivered_at >= ?`,
		subscriptionID, since.UTC().Format(config.WallTimeFormat)).Scan(&n)
	return n, err
}

// RecordSubObservations appends the similarities a subscription observed in
// one digest evaluation (basis points). The observed distribution is the
// R24 calibration reference — relative, never a static threshold.
func (s *Store) RecordSubObservations(subscriptionID string, simsBP []int, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := at.UTC().Format(config.WallTimeFormat)
	for _, bp := range simsBP {
		if _, err := tx.Exec(`INSERT INTO sub_observations(subscription_id, sim_bp, observed_at) VALUES (?,?,?)`,
			subscriptionID, bp, ts); err != nil {
			return err
		}
	}
	// bound history: keep the most recent 1000 observations per subscription
	if _, err := tx.Exec(`DELETE FROM sub_observations WHERE subscription_id=? AND rowid NOT IN
		(SELECT rowid FROM sub_observations WHERE subscription_id=? ORDER BY observed_at DESC, rowid DESC LIMIT 1000)`,
		subscriptionID, subscriptionID); err != nil {
		return err
	}
	return tx.Commit()
}

// SubObservedSims returns the recorded similarity history (as float64).
func (s *Store) SubObservedSims(subscriptionID string) ([]float64, error) {
	rows, err := s.db.Query(`SELECT sim_bp FROM sub_observations WHERE subscription_id=?`, subscriptionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var bp int
		if err := rows.Scan(&bp); err != nil {
			return nil, err
		}
		out = append(out, float64(bp)/10000)
	}
	return out, rows.Err()
}
