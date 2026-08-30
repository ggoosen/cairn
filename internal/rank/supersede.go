package rank

// D16 (S19) — the supersession demotion.
//
// A freshness half-life handles AGE. It does not handle WRONGNESS: a fact
// written last week that a later fact replaced still ranks high, because
// nothing in the scoring function knows the replacement happened. The
// projection now knows — `supersessions` records the relation with an end date
// — and this is the term that spends it.
//
// WHAT IT IS NOT. It is not deletion, and the distinction is the whole design.
// The superseded message keeps its body, its attribution, its topics, its
// signature and its place in search; `cairn fetch` still returns it and says
// what ended it and when. Demotion means the successor outranks it, not that
// the history stops being answerable. The pruning effect arrives without the
// audit loss that delete-and-rewrite causes.
//
// WHAT THE FEATURE IS. Binary: 1.0 when a LIVE message supersedes this one,
// 0 otherwise. The same argument that made S8's duplicate feature binary
// applies with more force here — supersession is a RELATION an auditor looks
// up in a table, not a similarity with a cutoff to argue about. There is no
// model in the loop, no threshold, and no parameter that would itself need
// recording and reconciling under R51. An external verifier reproduces the
// feature by reading the two ids the trace prints.
//
// WHY IT IS NOT POSITIONAL. S8's penalties ask "how much of this has the agent
// already been shown?", so they need a base order to count against. This one
// asks "has this fact been replaced?", which is a property of the candidate
// alone. It is therefore computed once, before any ordering exists, and is
// order-independent by construction rather than by a pass that restores it.
//
// RULING-NEEDED — the weight, and whether P0 carries the term at all.
// Spec §9.1 enumerates exactly two penalties (duplicate, thread saturation),
// caps each at 0.15, and gives P0 profiles none. It says NOTHING about
// supersession, because §9.1 predates the idea; BUILD-PLAN D16 requires the
// demotion but names no magnitude and no profile. Two decisions therefore have
// no authority behind them, and both are taken at their most conservative
// reading pending an author ruling:
//
//  1. MAGNITUDE. The cap is config.SupersessionPenaltyCap = 0.15, the only
//     penalty bound §9.1 states. Introducing a new, larger magnitude class
//     would be redesigning §9.1 rather than extending it, and a demotion large
//     enough to bury the old fact is a deletion in all but name.
//  2. PROFILE. P2 only, because §9.1 gives P0 no penalty term at all and
//     changing the DEFAULT ranking function on no spec authority is exactly
//     the redesign the precedence rule forbids. The consequence is stated
//     plainly rather than hidden: under the default P0 profile a superseded
//     fact is MARKED but not demoted, so an operator who wants the demotion
//     opts into P2. The staleness signal itself (which message ended this one,
//     and when) is profile-INDEPENDENT and rides every result under both
//     profiles, so the E9 metrics this exists to make measurable can be
//     computed without opting in.
//
// Recorded in PROGRESS.md under "Author rulings needed".

// MEASURED 2026-08-19 (PROGRESS.md "D16 verification"): the conservative
// reading above does NOT satisfy D16's acceptance criterion. End to end on a
// real daemon, superseding A by B moved A from 0.7900 to 0.6755 and B from
// 0.0400 to 0.0755 — the demotion fires, but the stale fact still outranks its
// replacement. The cause is structural, not a bug: percentile normalisation
// runs over the CANDIDATE SET, so two matching documents normalise to R=1.0
// and R=0.0, a 0.75 gap under P2 that no 0.15-bounded additive term can close.
// Re-running with 25 extra unrelated messages gave identical numbers, so this
// is not a small-corpus artifact — it is what percentile does whenever few
// documents match, which is the normal shape of a specific query.
//
// So the ruling above is not merely about magnitude. A bounded additive term
// cannot deliver the criterion in principle; only a hard ordering constraint
// (a superseded message never ranks above the one superseding it, recorded as
// a why-ranked component so R47/R51 still reconciles) or digest-only exclusion
// can. The comment above is right that burying a fact is deletion in all but
// name — which is an argument for the ORDERING constraint over a bigger
// number, since ordering demotes without hiding.

import "github.com/ggoosen/cairn/internal/config"

// hasSupersession reports whether this profile scores the supersession term.
// Mirrors S8's hasPenalties: under a profile that does not, the feature is not
// merely zero-weighted but never computed, so the trace's "SUP 0 × 0 = 0" is
// true of the score AND of the arithmetic that produced it.
//
// The EVIDENCE is recorded regardless (see fillSupersessionEvidence in the
// daemon): a trace that read "not superseded" for a message a live successor
// had replaced would be false about the item even while being true about the
// score, and the audit surface must not say a false thing.
func (w weightSet) hasSupersession() bool { return w.Sup != 0 }

// supersededFeature is the [0,1] feature for a candidate: the full value when a
// live message supersedes it, 0 when none does. Supersession admits no
// gradations for the same reason content identity does not — it is a recorded
// relation, not a measurement.
func supersededFeature(supersededBy string) float64 {
	if supersededBy == "" {
		return 0
	}
	return config.SupersededPenaltyValue
}

// applySupersession fills the SUP feature and its evidence on one candidate's
// components. The evidence (which message, and the end date) is carried under
// EVERY profile so the explanation can say what is true of the item; the
// FEATURE is filled only where it is scored, so a P0 trace's zero is honest
// arithmetic rather than a suppressed term.
func (w weightSet) applySupersession(comp *Components, c Candidate) {
	comp.SupBy, comp.SupAt = c.SupersededBy, c.SupersededAt
	if !w.hasSupersession() {
		return
	}
	comp.Sup = supersededFeature(c.SupersededBy)
}
