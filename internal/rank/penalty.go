package rank

// S8 — the P2 duplicate and thread-saturation penalties (spec §9.1).
//
// §9.1 says exactly one thing about them: "Each duplicate/thread-saturation
// penalty capped at 0.15." It defines neither term. Two definitions were
// available and only one of them can be audited:
//
//   - CONTENT-ADDRESS IDENTITY (taken). Two results are duplicates when their
//     head revisions resolve to the same body object — the same BLAKE3 content
//     address the store already keys on. An auditor recomputes the penalty from
//     the body hashes Cairn prints beside every result; there is no threshold to
//     argue about and no model in the loop.
//   - EMBEDDING NEAR-DUPLICATE (rejected). "Similar enough" needs a cutoff on a
//     cosine score produced by whichever embedder happened to be provisioned.
//     The trace could print the number, but re-deriving it needs the same model
//     at the same version, and Cairn's embedder is an opt-in subprocess that a
//     node may not even have (search degrades to lexical_only without one). A
//     penalty that changes an agent's results and cannot be reproduced offline
//     is precisely the black box §9 exists to forbid — and R51 is explicit that
//     reconciliation is defined against an EXTERNAL recompute.
//
// RULING-NEEDED: whether near-duplicate detection is wanted at all, and if so
// on what reproducible key (a normalised-text hash and a shingle/MinHash
// signature are both auditable options, unlike a live embedding call). Recorded
// in PROGRESS.md under "Author rulings needed"; until it is answered, only exact
// content identity is penalised, which under-penalises and never over-penalises.
//
// Thread saturation is the same shape: the thread key is the projection's own
// thread id (a root message keys on its own id), and the feature grades with the
// number of earlier results from that thread.
//
// ORDER. Both penalties are positional — they ask "how much of this has the
// agent already been shown?" — so they need an order to be defined against, and
// that order cannot be the final one they help produce. The pass therefore runs
// on the BASE ordering (every candidate scored with both penalty terms at zero,
// sorted by the ordinary comparator, which is total), assigns each candidate its
// penalty from what precedes it there, rescores, and re-sorts. Deterministic,
// and the "ahead" counts are printed in the trace so the arithmetic is
// reproducible without re-running retrieval.

import "github.com/ggoosen/cairn/internal/config"

// hasPenalties reports whether this profile applies penalties at all. P0
// profiles do not — spec §9.1 gives them no penalty term, and their scores must
// stay exactly what they were before S8, so the pass is skipped entirely rather
// than run with a zero weight (a skipped pass cannot round -0.0 into a P0
// score, and a P0 trace that reads "DUP 0 × 0 = 0" is then true of the item as
// well as of the score).
func (w weightSet) hasPenalties() bool { return w.Dup != 0 || w.Sat != 0 }

// duplicateFeature is the [0,1] feature for a result with `ahead` earlier
// results sharing its body object: 0 for the first occurrence, 1 for every copy
// after it. Content identity admits no gradations.
func duplicateFeature(ahead int) float64 {
	if ahead <= 0 {
		return 0
	}
	return config.DuplicatePenaltyValue
}

// saturationFeature is the [0,1] feature for a result with `ahead` earlier
// results from the same thread: linear to 1.0 at ThreadSaturationFullAt, flat
// after (the cap is a cap).
func saturationFeature(ahead int) float64 {
	if ahead <= 0 {
		return 0
	}
	if float64(ahead) >= config.ThreadSaturationFullAt {
		return 1.0
	}
	return float64(ahead) / config.ThreadSaturationFullAt
}

// applyPenalties fills the Dup/Sat components (and the counts that explain
// them) from the candidates that precede each item in `scored`, then rescores
// every item under w. `scored` MUST already be in base order. It does not
// re-sort — the caller does, so the two orderings stay visible at the call site.
func applyPenalties(scored []Scored, w weightSet) {
	if !w.hasPenalties() {
		return
	}
	dupSeen := make(map[string]int, len(scored))
	threadSeen := make(map[string]int, len(scored))
	for i := range scored {
		s := &scored[i]
		if s.DupKey != "" {
			ahead := dupSeen[s.DupKey]
			s.Components.DupAhead = ahead
			s.Components.Dup = duplicateFeature(ahead)
			dupSeen[s.DupKey] = ahead + 1
		}
		if s.ThreadKey != "" {
			ahead := threadSeen[s.ThreadKey]
			s.Components.SatAhead = ahead
			s.Components.Sat = saturationFeature(ahead)
			threadSeen[s.ThreadKey] = ahead + 1
		}
		s.Components.Score = w.score(s.Components)
	}
}
