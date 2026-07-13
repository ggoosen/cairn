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
