package rank

import "sort"

// Calibration (spec §9.3): offline, inspectable, NOT learned online. Replays
// the logged why_ranked component values under a grid of weight vectors,
// optimising Success@5 and reciprocal rank of the operator-chosen result, with
// entire TASKS held out. It only RECOMMENDS — adopting a weight change stays a
// human decision (constants live in one commented config).

// CalibCandidate is one candidate's stored component values (from why_ranked).
type CalibCandidate struct {
	MessageID        string
	R, S, F, P, I, N float64
	// Penalty is the S8 duplicate + thread-saturation contribution as it was
	// LOGGED (already weight-multiplied, so ≤ 0). It is a fixed offset rather
	// than a searchable weight: §9.1 pins the penalty cap, §9.3 calibrates the
	// additive weights, and a grid that silently dropped the penalties would
	// compare weight vectors against a baseline scored under different rules.
	Penalty float64
}

// Episode is one retrieval episode with a known-good result (a `found`
// outcome). Cands are every candidate shown, with their logged components.
type Episode struct {
	TaskID string
	Found  string // message_id the operator found useful
	Cands  []CalibCandidate
}

// WeightVector is a candidate weighting of the additive terms.
type WeightVector struct{ R, S, F, P, I, N float64 }

func (w WeightVector) score(c CalibCandidate) float64 {
	// R51: same per-term rounding as the live scorer (explicit conversions
	// forbid FMA contraction), so a calibration replay of the baseline weights
	// reproduces logged scores bit-exactly.
	return float64(w.R*c.R) + float64(w.S*c.S) + float64(w.F*c.F) +
		float64(w.P*c.P) + float64(w.I*c.I) + float64(w.N*c.N) + c.Penalty
}

// rankOfFound returns the 1-based rank of the found candidate under w (0 if the
// found id is not among the candidates). Ties break by message_id (stable,
// deterministic — mirrors the ranker's final tiebreak spirit).
func (ep Episode) rankOfFound(w WeightVector) int {
	type sc struct {
		id string
		v  float64
	}
	scored := make([]sc, len(ep.Cands))
	for i, c := range ep.Cands {
		scored[i] = sc{c.MessageID, w.score(c)}
	}
	sort.Slice(scored, func(a, b int) bool {
		if scored[a].v != scored[b].v {
			return scored[a].v > scored[b].v
		}
		return scored[a].id < scored[b].id
	})
	for i, s := range scored {
		if s.id == ep.Found {
			return i + 1
		}
	}
	return 0
}

// Metrics summarise a weight vector over a set of episodes.
type Metrics struct {
	Episodes   int
	SuccessAt5 float64 // fraction whose found result lands in the top 5
	MRR        float64 // mean reciprocal rank of the found result
}

// Evaluate scores a weight vector over episodes.
func Evaluate(eps []Episode, w WeightVector) Metrics {
	m := Metrics{Episodes: len(eps)}
	if len(eps) == 0 {
		return m
	}
	hits, rr := 0, 0.0
	for _, ep := range eps {
		r := ep.rankOfFound(w)
		if r == 0 {
			continue // found id not shown → contributes 0
		}
		if r <= 5 {
			hits++
		}
		rr += 1.0 / float64(r)
	}
	m.SuccessAt5 = float64(hits) / float64(len(eps))
	m.MRR = rr / float64(len(eps))
	return m
}

// better reports whether metrics a beat b (Success@5 first, MRR tiebreak).
func (a Metrics) better(b Metrics) bool {
	if a.SuccessAt5 != b.SuccessAt5 {
		return a.SuccessAt5 > b.SuccessAt5
	}
	return a.MRR > b.MRR
}

// SimplexGrid enumerates weight vectors whose ACTIVE terms are multiples of
// step and sum to 1.0 (inactive terms fixed at 0). active names a subset of
// {"R","S","F","P","I","N"}. step must divide 1 (e.g. 0.05, 0.1).
func SimplexGrid(step float64, active []string) []WeightVector {
	if step <= 0 || step > 1 {
		return nil
	}
	units := int(1.0/step + 0.5)
	var out []WeightVector
	var rec func(idx, remaining int, acc map[string]int)
	rec = func(idx, remaining int, acc map[string]int) {
		if idx == len(active)-1 {
			acc[active[idx]] = remaining
			out = append(out, vectorFrom(acc, step))
			return
		}
		for u := 0; u <= remaining; u++ {
			acc[active[idx]] = u
			rec(idx+1, remaining-u, acc)
		}
	}
	if len(active) == 0 {
		return nil
	}
	rec(0, units, map[string]int{})
	return out
}

func vectorFrom(units map[string]int, step float64) WeightVector {
	f := func(k string) float64 { return float64(units[k]) * step }
	return WeightVector{R: f("R"), S: f("S"), F: f("F"), P: f("P"), I: f("I"), N: f("N")}
}

// Recommendation is the calibration result — a suggestion, never auto-applied.
type Recommendation struct {
	Baseline        WeightVector
	BaselineHoldout Metrics
	Best            WeightVector
	BestTrain       Metrics
	BestHoldout     Metrics
	Improves        bool // Best beats Baseline on the HELD-OUT tasks
	GridSize        int
}

// Calibrate grid-searches weight vectors on the train episodes, then validates
// the train-winner on the held-out episodes. Improves is true only when the
// winner beats the baseline on the HOLDOUT (spec §9.3: hold out entire tasks;
// adopt only if it survives). It never mutates config.
func Calibrate(train, holdout []Episode, grid []WeightVector, baseline WeightVector) Recommendation {
	rec := Recommendation{
		Baseline:        baseline,
		BaselineHoldout: Evaluate(holdout, baseline),
		Best:            baseline,
		BestTrain:       Evaluate(train, baseline),
		GridSize:        len(grid),
	}
	for _, w := range grid {
		m := Evaluate(train, w)
		if m.better(rec.BestTrain) {
			rec.BestTrain = m
			rec.Best = w
		}
	}
	rec.BestHoldout = Evaluate(holdout, rec.Best)
	rec.Improves = rec.BestHoldout.better(rec.BaselineHoldout)
	return rec
}

// HoldOutByTask partitions episodes into train/holdout by TASK id (not random
// query): tasks whose id hashes into the holdout fraction go to holdout. A
// deterministic split (FNV over task id) keeps calibration reproducible.
func HoldOutByTask(eps []Episode, holdoutEveryN int) (train, holdout []Episode) {
	if holdoutEveryN < 2 {
		holdoutEveryN = 4 // default ~25% holdout
	}
	assign := map[string]bool{}
	order := []string{}
	for _, ep := range eps {
		if _, ok := assign[ep.TaskID]; !ok {
			assign[ep.TaskID] = false
			order = append(order, ep.TaskID)
		}
	}
	sort.Strings(order)
	for i, tid := range order {
		if i%holdoutEveryN == holdoutEveryN-1 {
			assign[tid] = true
		}
	}
	for _, ep := range eps {
		if assign[ep.TaskID] {
			holdout = append(holdout, ep)
		} else {
			train = append(train, ep)
		}
	}
	return train, holdout
}
