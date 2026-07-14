package rank

// FIX-H4 (§9.2): operator-signal anti-gaming. The raw sum of signal weights is
// forgeable; EffectiveSignalWeights must dedup per (principal, message, kind),
// slow-decay by age, discount agent trust, and cap per principal per day.

import (
	"testing"

	"github.com/ggoosen/cairn/internal/config"
)

func obs(msg, principal, kind string, weight int, ageHours float64, day string) SignalObs {
	return SignalObs{Message: msg, Principal: principal, Kind: kind, Weight: weight, AgeHours: ageHours, DayBucket: day}
}

// dedup: N identical signals from one principal on one item == exactly one.
func TestSignalDedupPerPrincipalItemKind(t *testing.T) {
	var flood []SignalObs
	for i := 0; i < 25; i++ {
		flood = append(flood, obs("m", "agentA", "priority_confirm", 3, 0, "2026-07-14"))
	}
	one := []SignalObs{obs("m", "agentA", "priority_confirm", 3, 0, "2026-07-14")}
	if EffectiveSignalWeights(flood)["m"] != EffectiveSignalWeights(one)["m"] {
		t.Fatalf("flood %v != single %v — dedup failed",
			EffectiveSignalWeights(flood)["m"], EffectiveSignalWeights(one)["m"])
	}
}

// slow decay: an older signal contributes strictly less; one half-life ≈ half.
func TestSignalSlowDecay(t *testing.T) {
	fresh := EffectiveSignalWeights([]SignalObs{obs("m", "operator", "k", 3, 0, "d")})["m"]
	old := EffectiveSignalWeights([]SignalObs{obs("m", "operator", "k", 3, config.SignalDecayHalfLifeHours, "d")})["m"]
	if old >= fresh {
		t.Fatalf("decayed signal %v should be < fresh %v", old, fresh)
	}
	if ratio := old / fresh; ratio < 0.45 || ratio > 0.55 {
		t.Fatalf("one half-life should ~halve the weight, got ratio %v", ratio)
	}
}

// trust: an agent principal's signal is worth less than the operator's.
func TestSignalAgentTrustDiscount(t *testing.T) {
	op := EffectiveSignalWeights([]SignalObs{obs("m", "operator", "k", 3, 0, "d")})["m"]
	agent := EffectiveSignalWeights([]SignalObs{obs("m", "agentA", "k", 3, 0, "d")})["m"]
	if agent >= op {
		t.Fatalf("agent signal %v should be < operator %v (trust)", agent, op)
	}
}

// max-weight clamp: a single very-high-weight signal is bounded.
func TestSignalMaxWeightClamp(t *testing.T) {
	huge := EffectiveSignalWeights([]SignalObs{obs("m", "operator", "k", 1000, 0, "d")})["m"]
	capped := EffectiveSignalWeights([]SignalObs{obs("m", "operator", "k", int(config.SignalMaxWeight), 0, "d")})["m"]
	if huge != capped {
		t.Fatalf("weight 1000 %v should clamp to max-weight %v", huge, capped)
	}
}

// per-principal daily cap: one principal blanketing many items in a day has its
// TOTAL contribution bounded by the cap.
func TestSignalPerPrincipalDailyCap(t *testing.T) {
	var many []SignalObs
	for i := 0; i < 30; i++ {
		many = append(many, obs(string(rune('a'+i)), "agentA", "k", 3, 0, "2026-07-14"))
	}
	out := EffectiveSignalWeights(many)
	total := 0.0
	for _, v := range out {
		total += v
	}
	if total > config.SignalPrincipalDailyCap+1e-9 {
		t.Fatalf("principal-day total %v exceeds cap %v", total, config.SignalPrincipalDailyCap)
	}
	// a different day is a separate window (not folded into the same cap).
	next := append(append([]SignalObs{}, many...), obs("z", "agentA", "k", 3, 0, "2026-07-15"))
	if EffectiveSignalWeights(next)["z"] == 0 {
		t.Fatal("a fresh day's signal should contribute (separate cap window)")
	}
}
