package projection

// Subscription queries (P1 N3). Subscriptions are event-derived; delivery
// history is telemetry-class and lives in telemetry.sqlite (R25).

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SubscriptionRow is one durable subscription.
type SubscriptionRow struct {
	SubscriptionID string   `json:"subscription_id"`
	OwnerView      string   `json:"owner_view"`
	InterestQuery  string   `json:"interest_query"`
	HardTopics     []string `json:"hard_topics"` // topic_ids; filter FIRST (R24)
	ThresholdMode  string   `json:"threshold_mode"`
	TopN           int      `json:"top_n"`
	WindowHours    int      `json:"window_hours"`
	Percentile     int      `json:"percentile"`
	PushCapPerDay  int      `json:"push_cap_per_day"`
	Revision       int      `json:"revision"`
	Disabled       bool     `json:"disabled"`
	CreatedAt      string   `json:"created_at"`
}

func scanSub(scan func(...any) error) (*SubscriptionRow, error) {
	var s SubscriptionRow
	var topics string
	var disabled int
	if err := scan(&s.SubscriptionID, &s.OwnerView, &s.InterestQuery, &topics, &s.ThresholdMode,
		&s.TopN, &s.WindowHours, &s.Percentile, &s.PushCapPerDay, &s.Revision, &disabled, &s.CreatedAt); err != nil {
		return nil, err
	}
	s.Disabled = disabled == 1
	if err := json.Unmarshal([]byte(topics), &s.HardTopics); err != nil {
		return nil, err
	}
	return &s, nil
}

const subCols = `subscription_id, owner_view, interest_query, hard_topics, threshold_mode,
	top_n, window_hours, percentile, push_cap_per_day, revision, disabled, created_at`

// SubscriptionByID returns one subscription (disabled included).
func (p *Projection) SubscriptionByID(id string) (*SubscriptionRow, error) {
	row := p.db.QueryRow(`SELECT `+subCols+` FROM subscriptions WHERE subscription_id=?`, id)
	s, err := scanSub(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("subscription %s not found", id)
	}
	return s, err
}

// Subscriptions lists subscriptions; view "" lists all views.
func (p *Projection) Subscriptions(view string, activeOnly bool) ([]SubscriptionRow, error) {
	q := `SELECT ` + subCols + ` FROM subscriptions`
	var conds []string
	var args []any
	if view != "" {
		conds = append(conds, "owner_view=?")
		args = append(args, view)
	}
	if activeOnly {
		conds = append(conds, "disabled=0")
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY created_at, subscription_id"
	rows, err := p.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubscriptionRow
	for rows.Next() {
		s, err := scanSub(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// MessagesInTopicIDs returns non-retracted head messages linked to ANY of
// the topic ids (a subscription's hard filter). Empty ids = all messages.
func (p *Projection) MessagesInTopicIDs(topicIDs []string) (map[string]bool, error) {
	var rows *sql.Rows
	var err error
	if len(topicIDs) == 0 {
		rows, err = p.db.Query(`SELECT message_id FROM messages WHERE retracted=0`)
	} else {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(topicIDs)), ",")
		args := make([]any, len(topicIDs))
		for i, id := range topicIDs {
			args[i] = id
		}
		rows, err = p.db.Query(`
			SELECT DISTINCT m.message_id FROM messages m
			JOIN topic_links l ON l.message_id = m.message_id AND l.removed = 0
			WHERE m.retracted = 0 AND l.topic_id IN (`+placeholders+`)`, args...)
	}
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

// LatestTopicMessage returns the message_id of the most-recent non-retracted
// head message linked to the named topic (R56 onboarding-record lookup), or ""
// if the topic does not exist or has no messages. Recency is created_at then
// message_id (a stable tiebreak), matching the digest's ordering convention.
func (p *Projection) LatestTopicMessage(topicName string) (string, error) {
	topicID, err := p.TopicIDByName(topicName)
	if err != nil || topicID == "" {
		return "", err // absent topic is not an error: no record yet
	}
	var id string
	err = p.db.QueryRow(`
		SELECT m.message_id FROM messages m
		JOIN topic_links l ON l.message_id = m.message_id AND l.removed = 0
		WHERE m.retracted = 0 AND l.topic_id = ?
		ORDER BY m.created_at DESC, m.message_id DESC LIMIT 1`, topicID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// TopicExists reports whether a topic_id exists (pre-ack validation).
func (p *Projection) TopicExists(topicID string) (bool, error) {
	var n int
	if err := p.db.QueryRow(`SELECT count(*) FROM topics WHERE topic_id=?`, topicID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
