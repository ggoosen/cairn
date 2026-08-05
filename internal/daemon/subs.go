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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/rank"
)

// --- R25 session (local) tier -----------------------------------------------
//
// The local tier is views/<view>/view.json: an optional natural-language
// interest query plus hard topic filters. Unlike SubscribeDurable it appends
// NO event (the verified log is untouched), exercises NO admin capability, and
// only ever concerns the named — caller's — view. This is what MCP/agent
// self-subscription reaches (R55); the durable, replicated tier below stays
// operator-CLI-only and is never exposed to MCP.

// LocalSubscription is one view's session-tier interest (its view.json).
type LocalSubscription struct {
	View          string   `json:"view"`
	InterestQuery string   `json:"interest_query,omitempty"`
	Topics        []string `json:"topics,omitempty"`
}

// LocalSubRequest is the subscribe-local IPC payload. It deliberately has NONE
// of the durable-only knobs (threshold_mode/top_n/percentile/push_cap): the
// session tier is view.json, which holds only an interest query + topics.
type LocalSubRequest struct {
	View          string   `json:"view"`
	InterestQuery string   `json:"interest_query"`
	Topics        []string `json:"topics,omitempty"`
}

// SubscribeLocal sets the caller's session-tier interest (R25). No event is
// appended and no admin capability is exercised. topics == nil PRESERVES the
// view's existing hard topics (so an agent tuning only its interest query never
// clobbers operator-set topic filters); a non-nil slice replaces them.
func (d *Daemon) SubscribeLocal(view, interestQuery string, topics []string) (*LocalSubscription, error) {
	if !validViewName(view) {
		return nil, fmt.Errorf("invalid view %q", view)
	}
	if strings.TrimSpace(interestQuery) == "" {
		return nil, errors.New("interest_query is required (raw natural language)")
	}
	cur := d.readViewConfig(view)
	cfg := ViewConfig{InterestQuery: interestQuery, Topics: cur.Topics}
	if topics != nil {
		cfg.Topics = topics
	}
	if err := d.writeLocalViewConfig(view, cfg); err != nil {
		return nil, err
	}
	return &LocalSubscription{View: view, InterestQuery: cfg.InterestQuery, Topics: cfg.Topics}, nil
}

// LocalSubscriptionFor returns the caller's session-tier interest (its
// view.json). It reads only the named view.
func (d *Daemon) LocalSubscriptionFor(view string) (*LocalSubscription, error) {
	if !validViewName(view) {
		return nil, fmt.Errorf("invalid view %q", view)
	}
	cfg := d.readViewConfig(view)
	return &LocalSubscription{View: view, InterestQuery: cfg.InterestQuery, Topics: cfg.Topics}, nil
}

// writeLocalViewConfig durably OVERWRITES views/<view>/view.json. fsx.Write-
// FileAtomic refuses to overwrite (it guards the immutable log/objects); the
// session-tier config is deliberately mutable — re-subscribing replaces it — so
// this does temp → fsync → rename(overwrite) → dir-fsync itself, torn-free.
func (d *Daemon) writeLocalViewConfig(view string, cfg ViewConfig) error {
	base, err := d.viewDir(view) // choke point: callers validate, this re-checks
	if err != nil {
		return err
	}
	if err := d.fs.MkdirAll(base, 0o700); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(base, "view.json")
	tmp := path + ".tmp"
	d.fs.Remove(tmp)
	f, err := d.fs.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(blob); err != nil {
		f.Close()
		d.fs.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		d.fs.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		d.fs.Remove(tmp)
		return err
	}
	if err := d.fs.Rename(tmp, path); err != nil {
		d.fs.Remove(tmp)
		return err
	}
	return d.fs.SyncDir(base)
}

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

// viewDir validates an agent view name and returns views/<view> under the
// portable dir. EVERY path formed from a caller-supplied view name must go
// through this choke point: view names become filesystem paths, and "/",
// "\\", ".." would escape the views tree.
func (d *Daemon) viewDir(view string) (string, error) {
	if !validViewName(view) {
		return "", fmt.Errorf("invalid view %q (plain names only: no separators, no ..)", view)
	}
	return filepath.Join(d.dir, config.ViewsDirName, view), nil
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
