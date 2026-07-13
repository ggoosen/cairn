package rank

import (
	"math"

	"github.com/ggoosen/cairn/internal/config"
)

// Salience (spec §9.2) is the P2 bounded salience feature S ∈ [0,1], computed
// LOCALLY from three telemetry-derived inputs. Raw impressions never leave the
// node; only S is used by ranking. This file is the pure math — the daemon
// supplies the live counts.

// DemandPosterior is the exposure-normalised smoothed fetch rate
// (fetches+α)/(impressions+α+β) with α=1, β=4 (prior mean 0.20). Below
// config.SalienceMinImpressions qualified impressions, no NEGATIVE judgment is
// made: the value floors at the prior mean so a handful of no-fetch impressions
// cannot suppress an item (agents fetch wrong results while investigating).
func DemandPosterior(fetches, impressions int) float64 {
	if fetches < 0 {
		fetches = 0
	}
	if impressions < 0 {
		impressions = 0
	}
	p := (float64(fetches) + config.SalienceDemandAlpha) /
		(float64(impressions) + config.SalienceDemandAlpha + config.SalienceDemandBeta)
	if impressions < config.SalienceMinImpressions && p < config.SaliencePriorMean {
		return config.SaliencePriorMean
	}
	return clamp01(p)
}

// saturate maps a non-negative count to [0,1) with diminishing returns:
// 1 − 2^(−count/half). half is the count at which it reaches 0.5.
func saturate(count int, half float64) float64 {
	if count <= 0 || half <= 0 {
		return 0
	}
	return 1 - math.Pow(2, -float64(count)/half)
}

// Salience blends the three §9.2 inputs into a bounded S ∈ [0,1]:
// demand (already [0,1]), reference-graph in-degree (saturated), and summed
// operator-signal weight (saturated). Weights sum to 1 (config).
func Salience(fetches, impressions, refInDegree, signalWeight int) float64 {
	demand := DemandPosterior(fetches, impressions)
	ref := saturate(refInDegree, config.SalienceRefHalf)
	sig := saturate(signalWeight, config.SalienceSignalHalf)
	s := config.SalienceWeightDemand*demand +
		config.SalienceWeightRef*ref +
		config.SalienceWeightSignal*sig
	return clamp01(s)
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
