// Package rank implements the P0 retrieval math (spec §9.1, rulings §7):
// RRF fusion (k=60) over lexical and vector candidate lists, percentile
// normalization over the union, freshness as a bonus decaying to zero,
// executable priority decay with suspension, both P0 profiles, mandatory
// inclusion, deterministic ties, and budget_chars accounting over the
// COMPLETE payload. Pure functions — no I/O — so why_ranked can recompute
// every number exactly.
package rank

import (
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

// Profile selects the weight set.
type Profile string

const (
	ProfileSearch   Profile = "search-P0"
	ProfileDigest   Profile = "digest-P0"
	ProfileSearchP2 Profile = "search-P2"
	ProfileDigestP2 Profile = "digest-P2"
)

// weightSet holds every additive term's weight. P0 profiles set S/I/N to 0 and
// carry a P (priority) term; P2 profiles set P to 0 and carry S/I/N (§9.1).
//
// Dup/Sat are the S8 penalty weights and are NEGATIVE: the features they
// multiply are ordinary [0,1] features, and the weight IS the §9.1 cap, so
// "capped at 0.15" is a property of the weight rather than a clamp buried in
// the arithmetic. P0 profiles leave them at 0 — §9.1 gives P0 no penalty term.
type weightSet struct {
	R, S, F, P, I, N float64
	Dup, Sat         float64
	halfLife         time.Duration
}

// IsP2 reports whether the profile uses the full additive model.
func (p Profile) IsP2() bool { return p == ProfileSearchP2 || p == ProfileDigestP2 }

func (p Profile) weights() weightSet {
	switch p {
	case ProfileDigest:
		return weightSet{R: config.DigestWeightR, F: config.DigestWeightF, P: config.DigestWeightP, halfLife: config.DigestFreshnessHalfLife}
	case ProfileSearchP2:
		return weightSet{R: config.SearchP2WeightR, S: config.SearchP2WeightS, F: config.SearchP2WeightF, I: config.SearchP2WeightI, N: config.SearchP2WeightN,
			Dup: -config.PenaltyCap, Sat: -config.PenaltyCap, halfLife: config.SearchFreshnessHalfLife}
	case ProfileDigestP2:
		return weightSet{R: config.DigestP2WeightR, S: config.DigestP2WeightS, F: config.DigestP2WeightF, I: config.DigestP2WeightI, N: config.DigestP2WeightN,
			Dup: -config.PenaltyCap, Sat: -config.PenaltyCap, halfLife: config.DigestFreshnessHalfLife}
	default:
		return weightSet{R: config.SearchWeightR, F: config.SearchWeightF, P: config.SearchWeightP, halfLife: config.SearchFreshnessHalfLife}
	}
}

// PublicWeights exposes a profile's additive term weights for why_ranked
// persistence (§9.4 — every number must be recomputable). P0 profiles report
// S=I=N=0 and no penalty weights; P2 profiles report P=0.
type PublicWeights struct {
	R, S, F, P, I, N float64
	Dup, Sat         float64 // negative: the §9.1 penalty cap (S8)
}

// Weights returns the profile's term weights.
func (p Profile) Weights() PublicWeights {
	w := p.weights()
	return PublicWeights{R: w.R, S: w.S, F: w.F, P: w.P, I: w.I, N: w.N, Dup: w.Dup, Sat: w.Sat}
}

// score computes the additive score for one candidate's components under a
// weight set. The P (priority) term is decayed effective_P; in P2 wP=0 so
// priority does not add linearly (it caps elsewhere).
//
// R51: each product carries an explicit float64 conversion, which the Go spec
// defines as a rounding barrier — the compiler may never fuse a product into
// the following addition (FMA). The total is therefore exactly what a plain
// IEEE-754 recompute of the why-ranked trace produces (each printed value ×
// weight rounded, then summed in this term order), on every platform and in
// every language an auditor might verify with.
//
// S8: the two penalty products are LAST and carry negative weights, so the sum
// order the trace describes is R, S, F, P_eff, I, N, DUP, SAT. Adding them
// after the bonus terms (rather than folding them in anywhere else) is what
// makes the printed order the arithmetic order.
func (w weightSet) score(c Components) float64 {
	return float64(w.R*c.R) + float64(w.S*c.S) + float64(w.F*c.F) +
		float64(w.P*c.Peff) + float64(w.I*c.I) + float64(w.N*c.N) +
		float64(w.Dup*c.Dup) + float64(w.Sat*c.Sat)
}

// Candidate is one message entering ranking. LexRank/VecRank are 1-based
// positions in their retriever's list (0 = absent).
type Candidate struct {
	MessageID string
	EventID   string // created event id — the final deterministic tiebreak
	CreatedAt time.Time
	Priority  int  // declared_priority (immutable testimony)
	Suspended bool // active pin or priority_confirm suspends decay
	LexRank   int
	VecRank   int

	// P2 additive inputs (§9.1/§9.2), already normalized to [0,1]; P0 profiles
	// ignore them. Salience is P2-2's S; Intent is operator intent; Novelty is
	// the exploration/diversity term.
	Salience float64
	Intent   float64
	Novelty  float64

	// S8 penalty keys (spec §9.1), both reproducible from published values:
	// DupKey is the head revision's body content address — two candidates
	// sharing one ARE the same text — and ThreadKey is the projection thread id
	// (a root message keys on its own id, so a standalone message never
	// saturates against itself). Empty means "no key", which exempts the
	// candidate from that penalty. P0 profiles ignore both.
	DupKey    string
	ThreadKey string

	Mandatory string // "" | "recipient" | "pin" — inclusion class, not a score bonus
}

// Components are the stored scoring inputs for why_ranked (decimal STRINGS
// in persistence — floats never enter event payloads; these live in the
// projection's rank_explanations table).
type Components struct {
	R       float64
	S       float64 // P2 salience (§9.2)
	F       float64
	Peff    float64
	I       float64 // P2 operator intent
	N       float64 // P2 novelty
	// S8 penalties (P2 only), each a [0,1] feature multiplied by a NEGATIVE
	// weight equal to the §9.1 cap. DupAhead/SatAhead are the counts they were
	// derived from — printed in the trace so the feature itself is recomputable,
	// not merely asserted.
	Dup      float64
	Sat      float64
	DupAhead int
	SatAhead int
	RRF      float64
	LexRank  int
	VecRank  int
	Score    float64
	Profile  Profile
}

// Scored pairs a candidate with its components, ordered per rulings §7:
// mandatory class → score → wall time → event_id.
type Scored struct {
	Candidate
	Components
}

// RRFScore: sum over lists of 1/(k+rank) (rulings §7, k=60).
func RRFScore(lexRank, vecRank int) float64 {
	s := 0.0
	if lexRank > 0 {
		s += 1.0 / float64(config.RRFK+lexRank)
	}
	if vecRank > 0 {
		s += 1.0 / float64(config.RRFK+vecRank)
	}
	return s
}

// EffectiveP: (declared/3) × 2^(−age/60h), decay suspended by an active pin
// or priority_confirm (rulings §7).
func EffectiveP(priority int, age time.Duration, suspended bool) float64 {
	base := float64(priority) / float64(config.DeclaredPriorityMax)
	if suspended {
		return base
	}
	return base * math.Exp2(-age.Hours()/config.PriorityDecayHalfLife.Hours())
}

// Freshness: 2^(−age/half_life) — a bonus decaying to zero, never a penalty.
func Freshness(age time.Duration, halfLife time.Duration) float64 {
	return math.Exp2(-age.Hours() / halfLife.Hours())
}

// Rank fuses, normalizes, scores, and orders candidates. now anchors ages.
// Percentile R is over the union: R = (#candidates with strictly smaller
// RRF) / (n−1); single candidate ⇒ R=1. RRF ties share the same percentile.
func Rank(cands []Candidate, profile Profile, now time.Time) []Scored {
	w := profile.weights()
	n := len(cands)
	scored := make([]Scored, n)

	rrfs := make([]float64, n)
	for i, c := range cands {
		rrfs[i] = RRFScore(c.LexRank, c.VecRank)
	}
	for i, c := range cands {
		below := 0
		for j := range cands {
			if rrfs[j] < rrfs[i] {
				below++
			}
		}
		r := 1.0
		if n > 1 {
			r = float64(below) / float64(n-1)
		}
		age := now.Sub(c.CreatedAt)
		if age < 0 {
			age = 0 // clock skew: extreme-future timestamps never boost
		}
		comp := Components{
			R:       r,
			S:       c.Salience,
			F:       Freshness(age, w.halfLife),
			Peff:    EffectiveP(c.Priority, age, c.Suspended),
			I:       c.Intent,
			N:       c.Novelty,
			RRF:     rrfs[i],
			LexRank: c.LexRank,
			VecRank: c.VecRank,
			Profile: profile,
		}
		comp.Score = w.score(comp)
		scored[i] = Scored{Candidate: c, Components: comp}
	}

	return orderWithPenalties(scored, w)
}

// RankUniformR is Rank with R fixed at 1.0 for every candidate — the
// digest-without-interest-query rule (rulings §7: no query ⇒ R=1.0; the
// digest degrades to freshness+priority ordering).
func RankUniformR(cands []Candidate, profile Profile, now time.Time) []Scored {
	w := profile.weights()
	scored := make([]Scored, len(cands))
	for i, c := range cands {
		age := now.Sub(c.CreatedAt)
		if age < 0 {
			age = 0
		}
		comp := Components{
			R:       1.0,
			S:       c.Salience,
			F:       Freshness(age, w.halfLife),
			Peff:    EffectiveP(c.Priority, age, c.Suspended),
			I:       c.Intent,
			N:       c.Novelty,
			LexRank: c.LexRank,
			VecRank: c.VecRank,
			Profile: profile,
		}
		comp.Score = w.score(comp)
		scored[i] = Scored{Candidate: c, Components: comp}
	}
	return orderWithPenalties(scored, w)
}

// orderWithPenalties is the shared tail of both ranking entry points: sort into
// the BASE order (penalty terms still zero), apply the S8 positional penalties
// against that order, and sort again on the penalised scores. Under a profile
// with no penalty weights the middle step is a no-op and the second sort finds
// the slice already ordered, so P0 results are bit-identical to pre-S8.
func orderWithPenalties(scored []Scored, w weightSet) []Scored {
	sortScored(scored)
	applyPenalties(scored, w)
	sortScored(scored)
	return scored
}

// sortScored is the ruling §7 order: mandatory class → score → wall time →
// event_id. Total (event ids are unique), so the base ordering the penalty pass
// counts against is deterministic.
func sortScored(scored []Scored) {
	sort.SliceStable(scored, func(a, b int) bool {
		sa, sb := scored[a], scored[b]
		if ca, cb := mandatoryClass(sa.Mandatory), mandatoryClass(sb.Mandatory); ca != cb {
			return ca < cb
		}
		if sa.Score != sb.Score {
			return sa.Score > sb.Score
		}
		if !sa.CreatedAt.Equal(sb.CreatedAt) {
			return sa.CreatedAt.After(sb.CreatedAt)
		}
		return sa.EventID < sb.EventID
	})
}

// mandatoryClass orders inclusion classes: recipients first, then pins,
// then scored items (rulings §7).
func mandatoryClass(m string) int {
	switch m {
	case "recipient":
		return 0
	case "pin":
		return 1
	case "subscription": // N3 (R26): after mandatory, before interest ranking
		return 2
	default:
		return 3
	}
}

// CalibrateSubscription decides how many of the similarity-sorted candidates
// a durable subscription surfaces (N3, RULINGS.md R24). Calibration is
// RELATIVE only — no static cosine thresholds. The reference distribution is
// everything this subscription has OBSERVED: its recorded similarity history
// plus the current pool. A candidate qualifies when it stands out from that
// distribution:
//
//   - top_n mode: sim ≥ lower-quartile(observed) + margin — it must clear
//     the observed NOISE FLOOR by a clear gap ("margin over next-best"),
//     capped at the remaining window allowance;
//   - percentile mode: sim ≥ the Pth percentile of the observed
//     distribution, capped at the allowance.
//
// A single candidate with NO history has no relative signal at all and
// passes — hard filters, windows, and caps still govern it. A uniform pool
// (nothing stands out from what has been observed) surfaces nothing: a
// relative calibrator cannot certify a pool without structure.
func CalibrateSubscription(sims, observed []float64, allowance int, mode string, percentile int, margin float64) int {
	n := len(sims)
	if n == 0 || allowance <= 0 {
		return 0
	}
	if n == 1 && len(observed) == 0 {
		return 1
	}
	combined := make([]float64, 0, n+len(observed))
	combined = append(combined, sims...)
	combined = append(combined, observed...)
	sort.Float64s(combined) // ascending

	var ref float64
	if mode == "percentile" {
		idx := len(combined) * percentile / 100
		if idx >= len(combined) {
			idx = len(combined) - 1
		}
		ref = combined[idx]
	} else {
		ref = combined[len(combined)/4] + margin // noise floor (q25) + margin
	}
	included := 0
	for included < n && sims[included] >= ref {
		included++
	}
	if included > allowance {
		included = allowance
	}
	return included
}

// Dec renders a float as the decimal string stored in rank_explanations —
// shortest round-trip form, so ParseDec(Dec(x)) == x EXACTLY and why_ranked
// arithmetic recomputes to the identical value.
func Dec(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// ParseDec parses a stored decimal string.
func ParseDec(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
