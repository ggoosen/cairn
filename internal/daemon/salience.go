package daemon

// P2-2: local salience (spec §9.2). Combines the telemetry-derived demand
// signal (impressions + distinct-task fetches) with the projection's reference
// in-degree and operator-signal weight into a bounded S ∈ [0,1] per message,
// via the pure rank.Salience math. Nothing here leaves the node.

import "github.com/ggoosen/cairn/internal/rank"

// SalienceScores returns message_id → salience S ∈ [0,1] for every message that
// has ANY salience signal (impression, fetch, reply, or operator signal). A
// message with no signals is absent (its salience is the neutral 0 for
// ranking's additive S term). Reads are lock-free (SQLite serializes).
func (d *Daemon) SalienceScores() (map[string]float64, error) {
	demand, err := d.tel.DemandByMessage()
	if err != nil {
		return nil, err
	}
	refIn, err := d.proj.ReferenceInDegree()
	if err != nil {
		return nil, err
	}
	sig, err := d.proj.OperatorSignalWeight()
	if err != nil {
		return nil, err
	}
	// union of every message that carries a signal
	ids := map[string]struct{}{}
	for id := range demand.Impressions {
		ids[id] = struct{}{}
	}
	for id := range demand.Fetches {
		ids[id] = struct{}{}
	}
	for id := range refIn {
		ids[id] = struct{}{}
	}
	for id := range sig {
		ids[id] = struct{}{}
	}
	out := make(map[string]float64, len(ids))
	for id := range ids {
		out[id] = rank.Salience(demand.Fetches[id], demand.Impressions[id], refIn[id], sig[id])
	}
	return out, nil
}

// SalienceFor returns the salience of one message (0 if it has no signals).
func (d *Daemon) SalienceFor(messageID string) (float64, error) {
	all, err := d.SalienceScores()
	if err != nil {
		return 0, err
	}
	return all[messageID], nil
}

// P2Input carries the P2 ranking features a message contributes beyond
// relevance/freshness/priority: salience S and novelty N (spec §9.1/§9.2).
// Intent I is per-query (pin/priority-confirm) and computed at rank time.
type P2Input struct {
	Salience float64
	Novelty  float64
}

// P2Inputs computes S and N for every message carrying any demand/reference/
// signal — one pass over telemetry + projection. Novelty derives from prior
// exposure (impressions); an unseen item is maximally novel.
func (d *Daemon) P2Inputs() (map[string]P2Input, error) {
	demand, err := d.tel.DemandByMessage()
	if err != nil {
		return nil, err
	}
	refIn, err := d.proj.ReferenceInDegree()
	if err != nil {
		return nil, err
	}
	sig, err := d.proj.OperatorSignalWeight()
	if err != nil {
		return nil, err
	}
	ids := map[string]struct{}{}
	for id := range demand.Impressions {
		ids[id] = struct{}{}
	}
	for id := range demand.Fetches {
		ids[id] = struct{}{}
	}
	for id := range refIn {
		ids[id] = struct{}{}
	}
	for id := range sig {
		ids[id] = struct{}{}
	}
	out := make(map[string]P2Input, len(ids))
	for id := range ids {
		out[id] = P2Input{
			Salience: rank.Salience(demand.Fetches[id], demand.Impressions[id], refIn[id], sig[id]),
			Novelty:  rank.NoveltyFromExposure(demand.Impressions[id]),
		}
	}
	return out, nil
}

// rankProfileSearch / rankProfileDigest pick P2 vs P0 per the daemon's opt-in
// (env CAIRN_RANK_PROFILE=p2 or the test setter). P2 stays opt-in until the
// §9.3 calibration validates its weights.
func (d *Daemon) rankProfileSearch() rank.Profile {
	if d.useP2Rank() {
		return rank.ProfileSearchP2
	}
	return rank.ProfileSearch
}

func (d *Daemon) rankProfileDigest() rank.Profile {
	if d.useP2Rank() {
		return rank.ProfileDigestP2
	}
	return rank.ProfileDigest
}

func (d *Daemon) useP2Rank() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rankP2
}

// intentFromRow maps operator intent signals present on a message to I ∈ [0,1]:
// an active pin is strong intent; a priority-confirm is moderate.
func intentFromRow(pinActive, priorityConf bool) float64 {
	switch {
	case pinActive:
		return 1.0
	case priorityConf:
		return 0.6
	default:
		return 0.0
	}
}

// SetRankProfileP2ForTest toggles the P2 ranking profile (opt-in gate).
func (d *Daemon) SetRankProfileP2ForTest(on bool) {
	d.mu.Lock()
	d.rankP2 = on
	d.mu.Unlock()
}
