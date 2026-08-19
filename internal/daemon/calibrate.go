package daemon

// P2-3b: the calibration harness (spec §9.3). Offline, inspectable, NOT learned
// online. Assembles genuine retrieval episodes (a `found` outcome + its logged
// why_ranked candidate set) and replays them under a weight grid, holding out
// entire tasks. It only REPORTS a recommendation — adopting a weight change is
// a human decision (the constants live in one commented config).

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ggoosen/cairn/internal/rank"
)

// CalibrationEpisodes joins telemetry `found` outcomes with the stored
// why_ranked component values into replayable episodes.
func (d *Daemon) CalibrationEpisodes() ([]rank.Episode, error) {
	found, err := d.tel.FoundEpisodes()
	if err != nil {
		return nil, err
	}
	var eps []rank.Episode
	for _, fe := range found {
		rows, err := d.proj.ExplanationsForInteraction(fe.InteractionID)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			continue // explanations pruned/absent — skip this episode
		}
		ep := rank.Episode{TaskID: fe.TaskID, Found: fe.FoundMessageID}
		for _, r := range rows {
			var rec componentsRecord
			if json.Unmarshal([]byte(r.ComponentsJSON), &rec) != nil {
				continue
			}
			ep.Cands = append(ep.Cands, rank.CalibCandidate{
				MessageID: r.MessageID,
				R:         rank.ParseDec(rec.R),
				S:         rank.ParseDec(rec.S),
				F:         rank.ParseDec(rec.F),
				P:         rank.ParseDec(rec.Peff),
				I:         rank.ParseDec(rec.I),
				N:         rank.ParseDec(rec.N),
				Penalty:   rec.penaltyProduct(),
			})
		}
		if ep.Found != "" && len(ep.Cands) > 0 {
			eps = append(eps, ep)
		}
	}
	return eps, nil
}

// TermStat is the contribution distribution of one weighted ranking term across
// all logged results (spec §9.3: `rank-stats` shows per-term contributions).
type TermStat struct {
	Term             string  `json:"term"`
	Count            int     `json:"count"`
	Min, Median, Max float64 `json:"-"`
	MinS             string  `json:"min"`
	MedianS          string  `json:"median"`
	MaxS             string  `json:"max"`
	MeanS            string  `json:"mean"`
}

// RankStats aggregates the per-term weighted contribution (weight·feature) over
// every stored explanation — the inspectable distribution operators read before
// touching weights.
func (d *Daemon) RankStats() ([]TermStat, error) {
	// walk every explanation via the interactions that produced them, summing
	// the WEIGHTED contribution (weight·feature) per term.
	ints, err := d.tel.AllInteractionIDs()
	if err != nil {
		return nil, err
	}
	// DUP/SAT are here for the same reason they are in the trace: an operator
	// reading the distribution before touching weights must be able to see how
	// much the S8 penalties are actually subtracting, not infer it.
	terms := []string{"R", "S", "F", "P", "I", "N", "DUP", "SAT"}
	vals := map[string][]float64{}
	for _, iid := range ints {
		rows, err := d.proj.ExplanationsForInteraction(iid)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			var rec componentsRecord
			if json.Unmarshal([]byte(r.ComponentsJSON), &rec) != nil {
				continue
			}
			contrib := map[string]float64{
				"R":   rank.ParseDec(rec.R) * rank.ParseDec(rec.Weights.R),
				"S":   rank.ParseDec(rec.S) * rank.ParseDec(rec.Weights.S),
				"F":   rank.ParseDec(rec.F) * rank.ParseDec(rec.Weights.F),
				"P":   rank.ParseDec(rec.Peff) * rank.ParseDec(rec.Weights.P),
				"I":   rank.ParseDec(rec.I) * rank.ParseDec(rec.Weights.I),
				"N":   rank.ParseDec(rec.N) * rank.ParseDec(rec.Weights.N),
				"DUP": rank.ParseDec(rec.Dup) * rank.ParseDec(rec.Weights.Dup),
				"SAT": rank.ParseDec(rec.Sat) * rank.ParseDec(rec.Weights.Sat),
			}
			for _, t := range terms {
				vals[t] = append(vals[t], contrib[t])
			}
		}
	}
	var out []TermStat
	for _, t := range terms {
		v := vals[t]
		if len(v) == 0 {
			continue
		}
		sort.Float64s(v)
		sum := 0.0
		for _, x := range v {
			sum += x
		}
		st := TermStat{
			Term: t, Count: len(v),
			Min: v[0], Median: v[len(v)/2], Max: v[len(v)-1],
		}
		st.MinS, st.MedianS, st.MaxS = rank.Dec(st.Min), rank.Dec(st.Median), rank.Dec(st.Max)
		st.MeanS = rank.Dec(sum / float64(len(v)))
		out = append(out, st)
	}
	return out, nil
}

// Calibrate runs the offline grid search over the current episodes and returns
// a recommendation (never auto-applied). baselineProfile names which profile's
// weights to beat; step is the grid granularity (spec §9.3: 0.05 increments).
func (d *Daemon) Calibrate(baselineProfile rank.Profile, step float64, holdoutEveryN int) (*rank.Recommendation, int, error) {
	eps, err := d.CalibrationEpisodes()
	if err != nil {
		return nil, 0, err
	}
	if len(eps) < 8 {
		return nil, len(eps), fmt.Errorf("only %d genuine episodes logged; calibration needs ≥30 for a trustworthy result (spec §9.3) — this is a preview", len(eps))
	}
	train, holdout := rank.HoldOutByTask(eps, holdoutEveryN)
	pw := baselineProfile.Weights()
	// Only the additive bonus weights are searched; the S8 penalty weights are
	// the §9.1 cap and ride along inside each candidate's logged Penalty offset.
	baseline := rank.WeightVector{R: pw.R, S: pw.S, F: pw.F, P: pw.P, I: pw.I, N: pw.N}
	active := activeTerms(baseline)
	grid := rank.SimplexGrid(step, active)
	rec := rank.Calibrate(train, holdout, grid, baseline)
	return &rec, len(eps), nil
}

func activeTerms(w rank.WeightVector) []string {
	var a []string
	if w.R > 0 {
		a = append(a, "R")
	}
	if w.S > 0 {
		a = append(a, "S")
	}
	if w.F > 0 {
		a = append(a, "F")
	}
	if w.P > 0 {
		a = append(a, "P")
	}
	if w.I > 0 {
		a = append(a, "I")
	}
	if w.N > 0 {
		a = append(a, "N")
	}
	return a
}
