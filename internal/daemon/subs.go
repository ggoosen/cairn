package daemon

// Durable semantic subscriptions (P1 N3).
//
//   - R24: hard filters first; relative calibration (top-N-per-window or
//     percentile over observed similarity) with a margin over next-best at
//     the boundary; push_cap default 20/day; NO static cosine thresholds.
//   - R25: only the explicit durable path creates subscription.* events;
//     the session tier is the local view.json (never events); delivery
//     history is telemetry-class.
//   - R26: digest composition — mandatory (recipients, pins) → subscription
//     matches (marked) → interest-query ranking, ONE budget.
//
// All writes are pre-ack validated (R4): stale base revisions, unknown
// subscriptions, unknown topics, and bad parameters reject before any
// event is appended.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/rank"
)

// SubscribeRequest creates one durable subscription.
type SubscribeRequest struct {
	OwnerView     string   `json:"owner_view"`
	InterestQuery string   `json:"interest_query"`
	Topics        []string `json:"topics,omitempty"` // topic ids or names; must exist (no auto-create)
	ThresholdMode string   `json:"threshold_mode,omitempty"`
	TopN          int      `json:"top_n,omitempty"`
	WindowHours   int      `json:"window_hours,omitempty"`
	Percentile    int      `json:"percentile,omitempty"`
	PushCapPerDay int      `json:"push_cap_per_day,omitempty"`
	Actor         string   `json:"actor,omitempty"`
}

// SubscribeResult acknowledges a durable subscription mutation.
type SubscribeResult struct {
	SubscriptionID string `json:"subscription_id"`
	EventID        string `json:"event_id"`
}

func validViewName(v string) bool {
	return v != "" && !strings.ContainsAny(v, "/\\") && !strings.Contains(v, "..")
}

// resolveSubTopics maps each entry (topic id or name) to a topic_id,
// rejecting unresolved entries BEFORE ack. Never auto-creates.
func (d *Daemon) resolveSubTopics(entries []string) ([]string, error) {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		ok, err := d.proj.TopicExists(e)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, e)
			continue
		}
		id, err := d.proj.TopicIDByName(e)
		if err != nil || id == "" {
			return nil, fmt.Errorf("rejected before ack: topic %q does not exist (subscriptions never auto-create topics)", e)
		}
		out = append(out, id)
	}
	return out, nil
}

// SubscribeDurable appends subscription.create after full pre-ack validation.
func (d *Daemon) SubscribeDurable(req SubscribeRequest) (*SubscribeResult, error) {
	if !validViewName(req.OwnerView) {
		return nil, fmt.Errorf("invalid owner view %q", req.OwnerView)
	}
	if strings.TrimSpace(req.InterestQuery) == "" {
		return nil, errors.New("interest_query is required (raw natural language)")
	}
	switch req.ThresholdMode {
	case "":
		req.ThresholdMode = "top_n"
	case "top_n", "percentile":
	default:
		return nil, fmt.Errorf("threshold_mode %q is not top_n|percentile", req.ThresholdMode)
	}
	if req.TopN <= 0 {
		req.TopN = config.SubTopNDefault
	}
	if req.WindowHours <= 0 {
		req.WindowHours = config.SubWindowHoursDefault
	}
	if req.Percentile <= 0 {
		req.Percentile = config.SubPercentileDefault
	}
	if req.Percentile >= 100 {
		return nil, fmt.Errorf("percentile %d must be 1-99", req.Percentile)
	}
	if req.PushCapPerDay <= 0 {
		req.PushCapPerDay = config.SubPushCapDefault
	}
	topicIDs, err := d.resolveSubTopics(req.Topics)
	if err != nil {
		return nil, err
	}

	subID := d.newUUID()
	payload := map[string]any{
		"subscription_id":  subID,
		"owner_view":       req.OwnerView,
		"interest_query":   req.InterestQuery,
		"threshold_mode":   req.ThresholdMode,
		"top_n":            req.TopN,
		"window_hours":     req.WindowHours,
		"percentile":       req.Percentile,
		"push_cap_per_day": req.PushCapPerDay,
	}
	if len(topicIDs) > 0 {
		payload["hard_topics"] = topicIDs
	}
	eventID, err := d.SimpleEvent("subscription.create", "subscription", subID, payload, PublishRequest{Actor: req.Actor})
	if err != nil {
		return nil, err
	}
	return &SubscribeResult{SubscriptionID: subID, EventID: eventID}, nil
}

// SubUpdateRequest changes fields of one subscription. Nil fields are
// unchanged; BaseRevision must match the current revision (optimistic
// concurrency, rejected pre-ack when stale).
type SubUpdateRequest struct {
	SubscriptionID string   `json:"subscription_id"`
	BaseRevision   int      `json:"base_revision"`
	InterestQuery  *string  `json:"interest_query,omitempty"`
	Topics         []string `json:"topics,omitempty"` // nil = unchanged; replaces in full
	ThresholdMode  *string  `json:"threshold_mode,omitempty"`
	TopN           *int     `json:"top_n,omitempty"`
	WindowHours    *int     `json:"window_hours,omitempty"`
	Percentile     *int     `json:"percentile,omitempty"`
	PushCapPerDay  *int     `json:"push_cap_per_day,omitempty"`
	Enable         bool     `json:"enable,omitempty"` // re-enable a disabled subscription
	Actor          string   `json:"actor,omitempty"`
}

// SubscriptionUpdate appends subscription.update after pre-ack validation.
func (d *Daemon) SubscriptionUpdate(req SubUpdateRequest) (*SubscribeResult, error) {
	cur, err := d.proj.SubscriptionByID(req.SubscriptionID)
	if err != nil {
		return nil, fmt.Errorf("rejected before ack: %w", err)
	}
	if cur.Revision != req.BaseRevision {
		return nil, fmt.Errorf("rejected before ack: base_revision %d is stale (current revision %d)",
			req.BaseRevision, cur.Revision)
	}
	payload := map[string]any{
		"subscription_id": req.SubscriptionID,
		"base_revision":   req.BaseRevision,
	}
	if req.InterestQuery != nil {
		if strings.TrimSpace(*req.InterestQuery) == "" {
			return nil, errors.New("interest_query cannot be emptied")
		}
		payload["interest_query"] = *req.InterestQuery
	}
	if req.Topics != nil {
		ids, err := d.resolveSubTopics(req.Topics)
		if err != nil {
			return nil, err
		}
		payload["hard_topics"] = ids
	}
	if req.ThresholdMode != nil {
		if *req.ThresholdMode != "top_n" && *req.ThresholdMode != "percentile" {
			return nil, fmt.Errorf("threshold_mode %q is not top_n|percentile", *req.ThresholdMode)
		}
		payload["threshold_mode"] = *req.ThresholdMode
	}
	if req.TopN != nil {
		if *req.TopN <= 0 {
			return nil, errors.New("top_n must be positive")
		}
		payload["top_n"] = *req.TopN
	}
	if req.WindowHours != nil {
		if *req.WindowHours <= 0 {
			return nil, errors.New("window_hours must be positive")
		}
		payload["window_hours"] = *req.WindowHours
	}
	if req.Percentile != nil {
		if *req.Percentile <= 0 || *req.Percentile >= 100 {
			return nil, errors.New("percentile must be 1-99")
		}
		payload["percentile"] = *req.Percentile
	}
	if req.PushCapPerDay != nil {
		if *req.PushCapPerDay <= 0 {
			return nil, errors.New("push_cap_per_day must be positive")
		}
		payload["push_cap_per_day"] = *req.PushCapPerDay
	}
	if req.Enable {
		payload["disabled"] = false
	}
	eventID, err := d.SimpleEvent("subscription.update", "subscription", req.SubscriptionID, payload, PublishRequest{Actor: req.Actor})
	if err != nil {
		return nil, err
	}
	return &SubscribeResult{SubscriptionID: req.SubscriptionID, EventID: eventID}, nil
}

// SubscriptionDisable stops delivery; the subscription and its history stay.
func (d *Daemon) SubscriptionDisable(id, actor string) (string, error) {
	if _, err := d.proj.SubscriptionByID(id); err != nil {
		return "", fmt.Errorf("rejected before ack: %w", err)
	}
	return d.SimpleEvent("subscription.disable", "subscription", id,
		map[string]any{"subscription_id": id}, PublishRequest{Actor: actor})
}

// SubscriptionDelete removes the subscription from the projection (the
// events remain in the log, as always).
func (d *Daemon) SubscriptionDelete(id, actor string) (string, error) {
	if _, err := d.proj.SubscriptionByID(id); err != nil {
		return "", fmt.Errorf("rejected before ack: %w", err)
	}
	return d.SimpleEvent("subscription.delete", "subscription", id,
		map[string]any{"subscription_id": id}, PublishRequest{Actor: actor})
}

// subscriptionMatches computes, for one digest generation, which messages
// each active subscription of the view surfaces (R24), excluding messages
// already mandatory for this digest. Returns message → matching sub ids.
// Deliveries are NOT recorded here — only entries that actually make it
// into the budget-capped digest count against windows and caps.
func (d *Daemon) subscriptionMatches(view string, exclude map[string]string) (map[string][]string, error) {
	subs, err := d.proj.Subscriptions(view, true)
	if err != nil || len(subs) == 0 {
		return nil, err
	}
	if d.embedder == nil {
		return nil, nil // semantic matching requires an embedder; degrade silently
	}
	heads, err := d.proj.HeadVectors(d.embedder.ModelID(), false)
	if err != nil || len(heads) == 0 {
		return nil, err
	}

	out := map[string][]string{}
	now := d.now()
	for _, sub := range subs {
		// caps and windows first: they bound everything downstream
		dayCount, err := d.tel.SubDeliveredSince(sub.SubscriptionID, now.Add(-24*time.Hour))
		if err != nil {
			return nil, err
		}
		allowance := sub.PushCapPerDay - dayCount
		if sub.ThresholdMode == "top_n" {
			windowCount, err := d.tel.SubDeliveredSince(sub.SubscriptionID,
				now.Add(-time.Duration(sub.WindowHours)*time.Hour))
			if err != nil {
				return nil, err
			}
			if rem := sub.TopN - windowCount; rem < allowance {
				allowance = rem
			}
		}
		if allowance <= 0 {
			continue
		}

		// hard filters FIRST (R24), then delivery/mandatory exclusion
		inTopics, err := d.proj.MessagesInTopicIDs(sub.HardTopics)
		if err != nil {
			return nil, err
		}
		delivered, err := d.tel.SubDelivered(sub.SubscriptionID)
		if err != nil {
			return nil, err
		}
		qvecs, err := d.embedder.Embed([]string{sub.InterestQuery})
		if err != nil {
			continue // embedder outage degrades this sub, never the digest
		}
		type scored struct {
			id  string
			sim float64
		}
		var pool []scored
		for id, vec := range heads {
			if !inTopics[id] || delivered[id] || exclude[id] != "" {
				continue
			}
			pool = append(pool, scored{id: id, sim: embed.Cosine(qvecs[0], vec)})
		}
		sort.Slice(pool, func(i, j int) bool {
			if pool[i].sim != pool[j].sim {
				return pool[i].sim > pool[j].sim
			}
			return pool[i].id < pool[j].id
		})
		sims := make([]float64, len(pool))
		simsBP := make([]int, len(pool))
		for i, p := range pool {
			sims[i] = p.sim
			simsBP[i] = int(p.sim * 10000)
		}
		observed, err := d.tel.SubObservedSims(sub.SubscriptionID)
		if err != nil {
			return nil, err
		}
		k := rank.CalibrateSubscription(sims, observed, allowance, sub.ThresholdMode, sub.Percentile, config.SubMarginMin)
		for i := 0; i < k; i++ {
			out[pool[i].id] = append(out[pool[i].id], sub.SubscriptionID)
		}
		// every evaluated candidate extends the observed distribution (R24)
		if err := d.tel.RecordSubObservations(sub.SubscriptionID, simsBP, now); err != nil {
			return nil, err
		}
	}
	return out, nil
}
