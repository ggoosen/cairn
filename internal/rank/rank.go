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
type weightSet struct {
	R, S, F, P, I, N float64
	halfLife         time.Duration
}

// IsP2 reports whether the profile uses the full additive model.
func (p Profile) IsP2() bool { return p == ProfileSearchP2 || p == ProfileDigestP2 }

func (p Profile) weights() weightSet {
	switch p {
	case ProfileDigest:
		return weightSet{R: config.DigestWeightR, F: config.DigestWeightF, P: config.DigestWeightP, halfLife: config.DigestFreshnessHalfLife}
	case ProfileSearchP2:
		return weightSet{R: config.SearchP2WeightR, S: config.SearchP2WeightS, F: config.SearchP2WeightF, I: config.SearchP2WeightI, N: config.SearchP2WeightN, halfLife: config.SearchFreshnessHalfLife}
	case ProfileDigestP2:
		return weightSet{R: config.DigestP2WeightR, S: config.DigestP2WeightS, F: config.DigestP2WeightF, I: config.DigestP2WeightI, N: config.DigestP2WeightN, halfLife: config.DigestFreshnessHalfLife}
	default:
		return weightSet{R: config.SearchWeightR, F: config.SearchWeightF, P: config.SearchWeightP, halfLife: config.SearchFreshnessHalfLife}
	}
}

// PublicWeights exposes a profile's additive term weights for why_ranked
// persistence (§9.4 — every number must be recomputable). P0 profiles report
// S=I=N=0; P2 profiles report P=0.
type PublicWeights struct{ R, S, F, P, I, N float64 }

// Weights returns the profile's term weights.
func (p Profile) Weights() PublicWeights {
	w := p.weights()
	return PublicWeights{R: w.R, S: w.S, F: w.F, P: w.P, I: w.I, N: w.N}
}

// score computes the additive score for one candidate's components under a
// weight set. The P (priority) term is decayed effective_P; in P2 wP=0 so
// priority does not add linearly (it caps elsewhere).
func (w weightSet) score(c Components) float64 {
	return w.R*c.R + w.S*c.S + w.F*c.F + w.P*c.Peff + w.I*c.I + w.N*c.N
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
	RRF     float64
	LexRank int
	VecRank int
	Score   float64
	Profile Profile
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
	return scored
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
	return scored
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

// BudgetChars counts Unicode scalar values (rulings §7: budget_chars is
// scalars, not bytes).
func BudgetChars(s string) int { return len([]rune(s)) }

// TakeWithinBudget renders items one at a time via render(i) and returns
// the largest prefix whose TOTAL payload (header + items + the truncation
// marker when items were dropped) fits budget. Returns the included count
// and the final payload. render must be deterministic.
type BudgetRender struct {
	Header string // counted always
	Marker string // appended when truncated; counted when present
}

func TakeWithinBudget(n int, budget int, br BudgetRender, render func(i int) string) (included int, payload string) {
	if budget <= 0 {
		return 0, ""
	}
	parts := make([]string, 0, n)
	total := BudgetChars(br.Header)
	if total > budget {
		return 0, ""
	}
	for i := 0; i < n; i++ {
		piece := render(i)
		cost := BudgetChars(piece)
		markerCost := 0
		if i < n-1 {
			markerCost = BudgetChars(br.Marker)
		}
		if total+cost+markerCost > budget {
			// adding this item (plus a marker if more remain) would exceed
			break
		}
		parts = append(parts, piece)
		total += cost
	}
	included = len(parts)
	out := br.Header
	for _, p := range parts {
		out += p
	}
	if included < n {
		// the marker is part of the budgeted payload too: append it only if
		// it fits (a budget too small for even the marker returns the bare
		// header — never a single char over budget)
		if BudgetChars(out)+BudgetChars(br.Marker) <= budget {
			out += br.Marker
		}
	}
	return included, out
}
