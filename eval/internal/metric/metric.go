// Package metric implements the information-retrieval metrics BUILD-PLAN
// §3.4 E4 requires: nDCG@k, MRR, Recall@k, Precision@k — and Success@k, kept
// only so the existing gate number stays comparable with its successors.
//
// WHY THESE AND NOT Success@5. Success@5 is a step function: it cannot tell
// "the answer was rank 1" from "the answer was rank 5", cannot see a second
// relevant document at all, and cannot distinguish a system that returned the
// answer plus nine distractors from one that returned the answer alone. Every
// one of those differences is the thing an agent actually pays for, in budget
// and in attention. E4 exists to replace a binary with a curve.
//
// THREE PROPERTIES ARE DELIBERATE.
//
// First, an ERROR IS NOT A ZERO. A backend that failed, or that does not have
// the surface being asked about, is excluded from the mean and counted
// separately — see Aggregate. Averaging a failure in as 0.0 quietly converts
// "we could not measure this" into "this scored badly", and averaging it in as
// a skip quietly flatters whichever system errored. Both are lies; the only
// honest move is to carry the count.
//
// Second, NOTHING HERE DECIDES ANYTHING. These functions return numbers. No
// threshold, no comparison, no pass/fail lives in this package; the gate that
// stops a number being reported as evidence is in internal/score, driven by
// the operator signoffs in eval/claims.yaml.
//
// Third, the confidence interval is a SEEDED BOOTSTRAP (stdlib math/rand),
// not a normal approximation. Query-level IR scores are bounded, skewed and
// often bimodal; a normal-approximation interval on 40 queries would be
// decoration. The bootstrap is reproducible from the recorded seed.
package metric

import (
	"math"
	"math/rand"
	"sort"
)

// RelevanceSet is a corpus's ground truth for one query: the set of item ids a
// human judged relevant. Binary, because mined human labels are binary — a
// maintainer either marked #B a duplicate of #A or did not.
type RelevanceSet map[string]bool

// NewRelevanceSet builds a set from the corpus's list.
func NewRelevanceSet(ids []string) RelevanceSet {
	s := make(RelevanceSet, len(ids))
	for _, id := range ids {
		if id != "" {
			s[id] = true
		}
	}
	return s
}

// Ranking is what a backend returned, in rank order. An empty string marks a
// hit the backend could not map back to a corpus item; it counts as a
// non-relevant result occupying a rank position, which is exactly what it is
// from the agent's point of view.
type Ranking []string

// truncate returns the first k of a ranking (all of it when k <= 0).
func (r Ranking) truncate(k int) Ranking {
	if k <= 0 || k >= len(r) {
		return r
	}
	return r[:k]
}

// DCG is the discounted cumulative gain of a ranking at cutoff k under binary
// relevance: sum over positions of rel(i) / log2(i+1).
func DCG(r Ranking, rel RelevanceSet, k int) float64 {
	var sum float64
	for i, id := range r.truncate(k) {
		if rel[id] {
			sum += 1 / math.Log2(float64(i)+2) // i is 0-based: position i+1
		}
	}
	return sum
}

// IdealDCG is the DCG of the best possible ranking: every relevant item first,
// capped at k and at the number of relevant items that exist.
func IdealDCG(rel RelevanceSet, k int) float64 {
	n := len(rel)
	if k > 0 && k < n {
		n = k
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += 1 / math.Log2(float64(i)+2)
	}
	return sum
}

// NDCG is normalized DCG at k, in [0,1]. A query with no relevant items is
// undefined rather than perfect; the loader refuses such queries
// (corpus.validate), so reaching the zero case means the corpus lied and NaN
// is a louder answer than 1.0.
func NDCG(r Ranking, rel RelevanceSet, k int) float64 {
	ideal := IdealDCG(rel, k)
	if ideal == 0 {
		return math.NaN()
	}
	return DCG(r, rel, k) / ideal
}

// ReciprocalRank is 1/rank of the FIRST relevant result, or 0 if none appears
// within k. Cutting MRR at k rather than leaving it unbounded matters: an
// agent reads what it is handed, and a relevant document at rank 400 is not a
// hundredth of a success, it is a miss.
func ReciprocalRank(r Ranking, rel RelevanceSet, k int) float64 {
	for i, id := range r.truncate(k) {
		if rel[id] {
			return 1 / (float64(i) + 1)
		}
	}
	return 0
}

// Recall is the fraction of relevant items that appear in the top k.
func Recall(r Ranking, rel RelevanceSet, k int) float64 {
	if len(rel) == 0 {
		return math.NaN()
	}
	return float64(hits(r, rel, k)) / float64(len(rel))
}

// Precision is relevant results per RANK SLOT, denominated by k rather than by
// the number of results actually returned. Denominating by the returned count
// would reward a system for returning less: a backend that returned one
// correct result would score 1.0 against a backend that returned the same
// result plus nine distractors at 0.1, when the agent's budget was spent on
// ten slots either way.
func Precision(r Ranking, rel RelevanceSet, k int) float64 {
	if k <= 0 {
		k = len(r)
	}
	if k == 0 {
		return math.NaN()
	}
	return float64(hits(r, rel, k)) / float64(k)
}

// Success is 1 when any relevant item appears in the top k, else 0. Retained
// because spec §11's gate is stated in these terms and a replacement metric
// that cannot be reconciled with the old number is a discontinuity nobody can
// audit — not because it is a good metric.
func Success(r Ranking, rel RelevanceSet, k int) float64 {
	if hits(r, rel, k) > 0 {
		return 1
	}
	return 0
}

func hits(r Ranking, rel RelevanceSet, k int) int {
	n := 0
	seen := map[string]bool{}
	for _, id := range r.truncate(k) {
		if rel[id] && !seen[id] {
			seen[id] = true
			n++
		}
	}
	return n
}

// Name identifies a metric in a scorecard. Strings rather than an enum so a
// recorded artifact stays readable years later without this package.
type Name string

const (
	NDCGAt    Name = "ndcg_at"
	MRRAt     Name = "mrr_at"
	RecallAt  Name = "recall_at"
	PrecAt    Name = "precision_at"
	SuccessAt Name = "success_at"
)

// Fn is one metric evaluated on one query.
type Fn func(Ranking, RelevanceSet, int) float64

// Fns maps each metric name to its implementation.
func Fns() map[Name]Fn {
	return map[Name]Fn{
		NDCGAt:    NDCG,
		MRRAt:     ReciprocalRank,
		RecallAt:  Recall,
		PrecAt:    Precision,
		SuccessAt: Success,
	}
}

// Sample is one query's contribution to an aggregate: the per-query value, or
// an error that excludes it. QueryID is kept so a scorecard can be traced back
// to the outcomes that produced it.
type Sample struct {
	QueryID string
	Value   float64
	// Excluded marks a query that could not be scored — the backend errored,
	// or does not implement the surface. Excluded samples never enter the mean.
	Excluded bool
	Reason   string
}

// Aggregate is a metric over a query set. It carries the counts that make the
// mean interpretable: how many queries were scored, and how many were dropped
// and why. A mean without its denominator is not a result.
type Aggregate struct {
	Metric Name    `json:"metric"`
	K      int     `json:"k"`
	Mean   float64 `json:"mean"`
	// N is the number of queries that entered the mean.
	N int `json:"n"`
	// Excluded is the number that did not, with the distinct reasons. A
	// nonzero value here changes what the mean means, so it is never omitted.
	Excluded        int      `json:"excluded"`
	ExcludedReasons []string `json:"excluded_reasons,omitempty"`
	// CI95Low/High are a seeded bootstrap percentile interval over the scored
	// queries. Zero-width when N < MinBootstrapN, in which case Bootstrapped
	// is false and no interval should be drawn.
	CI95Low      float64 `json:"ci95_low"`
	CI95High     float64 `json:"ci95_high"`
	Bootstrapped bool    `json:"bootstrapped"`
	BootstrapN   int     `json:"bootstrap_resamples,omitempty"`
	Seed         int64   `json:"bootstrap_seed,omitempty"`
}

// MinBootstrapN is the query count below which an interval is refused rather
// than drawn. Six queries resampled two thousand times still only knows about
// six queries; an interval computed from them looks like precision and is not.
const MinBootstrapN = 20

// BootstrapResamples is the resample count. Two thousand is enough for a 95%
// percentile interval to be stable to about a percentage point, and cheap.
const BootstrapResamples = 2000

// Aggregate computes the mean and a bootstrap interval over samples. Excluded
// samples are counted, never imputed: see the package comment.
func (n Name) Aggregate(k int, samples []Sample, seed int64) Aggregate {
	agg := Aggregate{Metric: n, K: k}
	var vals []float64
	reasons := map[string]bool{}
	for _, s := range samples {
		if s.Excluded || math.IsNaN(s.Value) {
			agg.Excluded++
			reason := s.Reason
			if reason == "" {
				reason = "undefined for this query"
			}
			reasons[reason] = true
			continue
		}
		vals = append(vals, s.Value)
	}
	for r := range reasons {
		agg.ExcludedReasons = append(agg.ExcludedReasons, r)
	}
	sort.Strings(agg.ExcludedReasons)

	agg.N = len(vals)
	if agg.N == 0 {
		agg.Mean = math.NaN()
		return agg
	}
	agg.Mean = mean(vals)
	if agg.N >= MinBootstrapN {
		agg.CI95Low, agg.CI95High = bootstrapCI(vals, seed)
		agg.Bootstrapped = true
		agg.BootstrapN = BootstrapResamples
		agg.Seed = seed
	}
	return agg
}

func mean(xs []float64) float64 {
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// bootstrapCI resamples with replacement and returns the 2.5th and 97.5th
// percentiles of the resampled means. Seeded so the interval is a property of
// the data plus the recorded seed, not of the day it was computed.
func bootstrapCI(vals []float64, seed int64) (float64, float64) {
	rng := rand.New(rand.NewSource(seed))
	means := make([]float64, BootstrapResamples)
	sample := make([]float64, len(vals))
	for r := 0; r < BootstrapResamples; r++ {
		for i := range sample {
			sample[i] = vals[rng.Intn(len(vals))]
		}
		means[r] = mean(sample)
	}
	sort.Float64s(means)
	lo := means[int(0.025*float64(BootstrapResamples))]
	hi := means[int(0.975*float64(BootstrapResamples))-1]
	return lo, hi
}
