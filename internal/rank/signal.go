package rank

import (
	"math"

	"github.com/ggoosen/cairn/internal/config"
)

// Operator-signal anti-gaming (spec §9.2, FIX-H4). Raw summed signal weight is
// trivially forgeable: one principal emits N identical signals, or one
// high-weight signal, or blankets many items. This layer makes the signal
// contribution additive-with-slow-decay and bounded, per §9.2:
//
//   - dedup per (principal, message, kind): repeated identical signals from one
//     principal collapse to their single LEAST-decayed (most recent) instance;
//   - each signal's weight is clamped, trust-weighted (operator full, agents
//     discounted), and slow-decayed by age (never a multiplier lock);
//   - a per-principal per-UTC-day cap scales down any principal whose total
//     effective contribution in a day exceeds the cap (a blanket-many-items
//     flood cannot dominate either).
//
// The result is a per-message effective signal weight (a float), which the
// caller saturates into S exactly as before. Pure — the daemon supplies rows
// and the reference clock.

// SignalObs is one operator signal, projected + age-stamped by the daemon.
type SignalObs struct {
	Message   string
	Principal string
	Kind      string
	Weight    int
	AgeHours  float64 // now − created_at, in hours (negative clamped to 0)
	DayBucket string  // UTC calendar day of created_at (dedup/cap window key)
}

// trust returns the principal's signal trust weight.
func signalTrust(principal string) float64 {
	if principal == config.SignalOperatorPrincipal {
		return config.SignalOperatorTrust
	}
	return config.SignalAgentTrust
}

// signalDecay is 2^(−ageHours/halfLife): slow, additive, never below 0.
func signalDecay(ageHours float64) float64 {
	if ageHours <= 0 {
		return 1.0
	}
	return math.Pow(2, -ageHours/config.SignalDecayHalfLifeHours)
}

// EffectiveSignalWeights collapses raw signal observations into a bounded,
// deduped, decayed, trust-weighted, per-principal-capped effective weight per
// message. See the package comment for the §9.2 rationale.
func EffectiveSignalWeights(obs []SignalObs) map[string]float64 {
	// 1. dedup per (principal, message, kind): keep the least-decayed instance
	//    (smallest age). A principal spamming the same kind on one item counts
	//    once, no matter how many events it emits.
	type dk struct{ principal, message, kind string }
	best := map[dk]SignalObs{}
	for _, o := range obs {
		k := dk{o.Principal, o.Message, o.Kind}
		if cur, ok := best[k]; !ok || o.AgeHours < cur.AgeHours {
			best[k] = o
		}
	}

	// 2. per-signal effective weight, grouped by (principal, day) for capping and
	//    by message for the final sum.
	type pd struct{ principal, day string }
	perPrincipalDay := map[pd]float64{}     // total effective in a principal-day
	perMsgByPD := map[pd]map[string]float64{} // principal-day → message → effective
	for _, o := range best {
		w := float64(o.Weight)
		if w <= 0 {
			w = 1 // NULL/zero declared weight counts as 1 (schema default)
		}
		if w > config.SignalMaxWeight {
			w = config.SignalMaxWeight
		}
		eff := w * signalTrust(o.Principal) * signalDecay(o.AgeHours)
		key := pd{o.Principal, o.DayBucket}
		perPrincipalDay[key] += eff
		if perMsgByPD[key] == nil {
			perMsgByPD[key] = map[string]float64{}
		}
		perMsgByPD[key][o.Message] += eff
	}

	// 3. per-principal per-day cap: if a principal's day total exceeds the cap,
	//    scale ALL its contributions that day proportionally so the flood is
	//    bounded without singling out any one item.
	out := map[string]float64{}
	for key, byMsg := range perMsgByPD {
		scale := 1.0
		if total := perPrincipalDay[key]; total > config.SignalPrincipalDailyCap {
			scale = config.SignalPrincipalDailyCap / total
		}
		for msg, eff := range byMsg {
			out[msg] += eff * scale
		}
	}
	return out
}
