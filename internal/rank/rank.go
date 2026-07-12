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
	ProfileSearch Profile = "search-P0"
	ProfileDigest Profile = "digest-P0"
)

func (p Profile) weights() (wR, wF, wP float64, halfLife time.Duration) {
	switch p {
	case ProfileDigest:
		return config.DigestWeightR, config.DigestWeightF, config.DigestWeightP, config.DigestFreshnessHalfLife
	default:
		return config.SearchWeightR, config.SearchWeightF, config.SearchWeightP, config.SearchFreshnessHalfLife
	}
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

	Mandatory string // "" | "recipient" | "pin" — inclusion class, not a score bonus
}

// Components are the stored scoring inputs for why_ranked (decimal STRINGS
// in persistence — floats never enter event payloads; these live in the
// projection's rank_explanations table).
type Components struct {
	R       float64
	F       float64
	Peff    float64
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
	wR, wF, wP, halfLife := profile.weights()
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
			F:       Freshness(age, halfLife),
			Peff:    EffectiveP(c.Priority, age, c.Suspended),
			RRF:     rrfs[i],
			LexRank: c.LexRank,
			VecRank: c.VecRank,
			Profile: profile,
		}
		comp.Score = wR*comp.R + wF*comp.F + wP*comp.Peff
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
	wR, wF, wP, halfLife := profile.weights()
	scored := make([]Scored, len(cands))
	for i, c := range cands {
		age := now.Sub(c.CreatedAt)
		if age < 0 {
			age = 0
		}
		comp := Components{
			R:       1.0,
			F:       Freshness(age, halfLife),
			Peff:    EffectiveP(c.Priority, age, c.Suspended),
			LexRank: c.LexRank,
			VecRank: c.VecRank,
			Profile: profile,
		}
		comp.Score = wR*comp.R + wF*comp.F + wP*comp.Peff
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
