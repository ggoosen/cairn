// Package ablation is the catalogue of E4's ablation arms: the conditions
// BUILD-PLAN §3.4 asks for — lexical-only, vector-only, RRF fusion,
// ±freshness, ±priority decay, ±mandatory inclusion, P0 vs P2 profile — each
// with an honest statement of HOW it is produced and WHAT it therefore cannot
// show.
//
// THE POINT OF THE ABLATIONS. "A component whose removal doesn't hurt should
// be deleted; that is a result, not a failure." The catalogue exists so that
// sentence can be acted on: every arm names the claim in eval/claims.yaml it
// bears on, so a result arrives already attached to the kill criterion that
// would make it consequential.
//
// FIDELITY IS THE FIELD TO READ FIRST. eval/ is a separate Go module and can
// only reach Cairn through the CLI, so not every ablation is equally
// obtainable, and pretending otherwise would be the quiet kind of dishonesty
// this whole framework exists to prevent:
//
//   - NATIVE arms configure the system under test and measure what it then
//     does. Best evidence. Produced by process environment (rank profile,
//     embedder presence) or by how the corpus is written (mandatory
//     addressing).
//   - RECOMPUTED arms re-rank the results retrieval already returned, using
//     the published `cairn why-ranked` arithmetic — the same route an external
//     auditor has. They are a LOWER BOUND on an ablation's effect and they
//     cannot move Recall@K at the requested K at all, because they never
//     change which documents were retrieved. Their Limits string says so, and
//     it travels into every scorecard section.
//   - UNAVAILABLE arms are declared gaps with a stated reason. They FAIL
//     LOUDLY, exactly as backends B3 and B4 do, because an ablation that
//     silently ran the default condition and got labelled with the arm's name
//     would be a fabricated result in whichever direction happened to suit.
package ablation

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ggoosen/cairn/eval/internal/backend"
	"github.com/ggoosen/cairn/eval/internal/explain"
)

// Fidelity is how faithfully an arm reproduces the condition it names.
type Fidelity string

const (
	Native      Fidelity = "native"
	Recomputed  Fidelity = "recomputed"
	Unavailable Fidelity = "unavailable"
)

// ErrUnavailable is returned when an arm has no black-box route. Callers must
// surface it, never substitute a default run for it.
var ErrUnavailable = errors.New("ablation arm has no black-box route")

// ErrArmNotTaken is returned when an arm was configured but the system did not
// actually enter the intended state — a P2 profile that stayed P0, a
// vector-only arm on a lexical-only daemon. A silently-untaken arm is a
// mislabelled result, which is worse than a missing one.
var ErrArmNotTaken = errors.New("ablation arm did not take effect")

// Ranked pairs one returned hit with its parsed ranking arithmetic. A
// recomputed arm re-sorts a slice of these.
type Ranked struct {
	Hit backend.Hit
	Exp *explain.Explanation
}

// Rerank produces a new ordering. It may DROP entries (vector-only drops
// anything the vector retriever never returned) but must never invent one.
type Rerank func([]Ranked) []Ranked

// Arm is one ablation condition.
type Arm struct {
	ID       string
	Title    string
	Fidelity Fidelity

	// Surfaces the arm is meaningful on. Mandatory inclusion, for instance,
	// only exists on the digest.
	Surfaces []backend.Surface

	// BearsOn lists the eval/claims.yaml ids this arm's result would speak to.
	// The reporting gate reads these; an arm that bears on nothing could never
	// be blocked and could therefore never be dark.
	BearsOn []string

	// Mechanism describes, in one sentence, exactly what was changed.
	Mechanism string

	// Limits is the honest ceiling: what this arm's numbers cannot show. It is
	// copied verbatim into every scorecard section the arm produces, so it can
	// never be separated from the number it qualifies.
	Limits string

	// Env is the extra process environment for a native arm.
	Env []string

	// AddressToView writes corpus items addressed to the evaluation view,
	// which is what gives them the "recipient" mandatory inclusion class.
	AddressToView bool

	// RequiresEmbedder marks an arm that is meaningless without semantic
	// retrieval. The runner asserts retrieval_mode=="full" and fails with
	// ErrArmNotTaken otherwise, rather than reporting a vacuous arm.
	RequiresEmbedder bool

	// ExpectProfile, when set, is the rank profile the arm's why-ranked traces
	// must report. This is how a native profile arm proves it took effect
	// rather than being assumed to have.
	ExpectProfile string

	// ExpectRetrievalMode, when set, is the retrieval_mode the arm must report.
	ExpectRetrievalMode string

	// Rerank is set on recomputed arms only.
	Rerank Rerank

	// Why explains an unavailable arm.
	Why string
}

// Native reports whether the arm changes the system under test rather than
// post-processing its output.
func (a Arm) IsNative() bool { return a.Fidelity == Native }

// AppliesTo reports whether the arm is meaningful on a surface.
func (a Arm) AppliesTo(s backend.Surface) bool {
	for _, x := range a.Surfaces {
		if x == s {
			return true
		}
	}
	return false
}

// BackendConfig renders the arm as backend configuration.
func (a Arm) BackendConfig() backend.ArmConfig {
	return backend.ArmConfig{ID: a.ID, Env: a.Env, AddressToView: a.AddressToView}
}

// Err returns the error an unavailable arm must fail with.
func (a Arm) Err() error {
	if a.Fidelity != Unavailable {
		return nil
	}
	return fmt.Errorf("%w: %s: %s", ErrUnavailable, a.ID, a.Why)
}

// ---------------------------------------------------------------------------
// The catalogue
// ---------------------------------------------------------------------------

// Arm ids. Stable strings: they appear in scorecards that outlive this code.
const (
	AsShipped         = "as-shipped"
	LexicalOnly       = "lexical-only"
	VectorOnly        = "vector-only"
	NoFreshness       = "no-freshness"
	NoPriority        = "no-priority"
	PriorityUndecayed = "priority-undecayed"
	MandatoryOn       = "mandatory-inclusion"
	ProfileP0         = "profile-p0"
	ProfileP2         = "profile-p2"
)

var bothSurfaces = []backend.Surface{backend.SurfaceSearch, backend.SurfaceDigest}

// recomputedLimit is the ceiling every recomputed arm inherits. Written once
// and shared so it cannot drift into a weaker phrasing on the arm somebody
// most wants to quote.
const recomputedLimit = "RECOMPUTED, NOT NATIVE: this arm re-ranks the result set retrieval already " +
	"returned, using the published why-ranked arithmetic. It cannot surface a document the fusion " +
	"stage never retrieved, so Recall@K at the requested K CANNOT MOVE and must not be read as " +
	"'the ablation did not hurt recall'. Ordering metrics (nDCG, MRR, precision at small k) move " +
	"honestly. Treat every effect size here as a LOWER BOUND on the real ablation."

// Catalogue returns every arm, in a stable order.
func Catalogue() []Arm {
	return []Arm{
		{
			ID:       AsShipped,
			Title:    "RRF fusion, P0 profile — the system as it ships",
			Fidelity: Native,
			Surfaces: bothSurfaces,
			BearsOn:  []string{"RET-success5", "RET-hybrid-earns-place", "RET-dumb-ranking-suffices"},
			Mechanism: "nothing changed. This is the control arm every other ablation is " +
				"differenced against, and the RRF-fusion condition of the lexical/vector/RRF triple.",
			Limits:        "Whether this arm is hybrid or lexical-only depends on whether an embedder was provisioned; the recorded retrieval_mode says which, and a lexical_only run cannot answer RET-hybrid-earns-place.",
			ExpectProfile: "P0",
		},
		{
			ID:       LexicalOnly,
			Title:    "lexical retrieval only (no embedder provisioned)",
			Fidelity: Native,
			Surfaces: bothSurfaces,
			BearsOn:  []string{"RET-hybrid-earns-place", "RET-graceful-degradation"},
			Mechanism: "the daemon is started with no embedder interpreter configured, so no vectors " +
				"exist and retrieval degrades to FTS alone. This is the real degraded system, not a " +
				"simulation of one — it is also exactly what a user without the optional venv runs.",
			Limits: "None beyond the corpus: this is a genuine system configuration. Note it is the " +
				"DEFAULT configuration, so as-shipped and lexical-only coincide unless an embedder is provisioned.",
			Env:                 []string{"CAIRN_EMBED_PYTHON="},
			ExpectRetrievalMode: "lexical_only",
		},
		{
			ID:       VectorOnly,
			Title:    "vector retrieval only",
			Fidelity: Recomputed,
			Surfaces: []backend.Surface{backend.SurfaceSearch},
			BearsOn:  []string{"RET-hybrid-earns-place"},
			Mechanism: "results are re-ordered by the vector retriever's own rank (from why-ranked's " +
				"`vec rank`), and anything the vector retriever never returned is dropped.",
			Limits: recomputedLimit + " For this arm specifically the ceiling bites hardest: a native " +
				"vector-only retriever would have its own candidate set, and this one is confined to " +
				"what hybrid fusion surfaced. Cairn exposes no CLI switch for vector-only retrieval, " +
				"so no native arm exists.",
			RequiresEmbedder: true,
			Rerank:           byVectorRank,
		},
		{
			ID:       NoFreshness,
			Title:    "freshness term removed",
			Fidelity: Recomputed,
			Surfaces: bothSurfaces,
			BearsOn:  []string{"RET-dumb-ranking-suffices", "LONG-old-material-survives"},
			Mechanism: "each result's score is recomputed as the published sum with the F term " +
				"dropped, and the results re-sorted.",
			Limits: recomputedLimit,
			Rerank: byScoreWithout(explain.TermF),
		},
		{
			ID:       NoPriority,
			Title:    "decayed-priority term removed",
			Fidelity: Recomputed,
			Surfaces: bothSurfaces,
			BearsOn:  []string{"RET-dumb-ranking-suffices"},
			Mechanism: "each result's score is recomputed with the P_eff term dropped, and the results " +
				"re-sorted. NOTE this removes priority ENTIRELY, not just its decay.",
			Limits: recomputedLimit,
			Rerank: byScoreWithout(explain.TermPeff),
		},
		{
			ID:       PriorityUndecayed,
			Title:    "priority without decay",
			Fidelity: Unavailable,
			Surfaces: bothSurfaces,
			BearsOn:  []string{"RET-dumb-ranking-suffices"},
			Mechanism: "would score with the UNDECAYED declared priority in place of P_eff, isolating " +
				"the decay from the priority term itself.",
			Why: "why-ranked publishes the decayed P_eff but not the undecayed normalized priority, " +
				"and the normalization from declared_priority (0-3) into the additive input is internal. " +
				"Reconstructing it from a freshly-written message's near-zero decay would be an " +
				"assumption dressed as a measurement. Distinguishing 'priority does not earn its place' " +
				"from 'the DECAY does not earn its place' therefore needs either a CLI knob or a " +
				"published undecayed term; " + NoPriority + " answers only the first question.",
		},
		{
			ID:       MandatoryOn,
			Title:    "mandatory inclusion enabled (items addressed to the view)",
			Fidelity: Native,
			Surfaces: []backend.Surface{backend.SurfaceDigest},
			BearsOn:  []string{"RET-dumb-ranking-suffices", "PROD-budget-preserves-value"},
			Mechanism: "corpus items are written with `--to <eval view>`, giving them the `recipient` " +
				"inclusion class, which sorts ahead of score entirely and consumes budget first. " +
				"The ∅ control is the " + AsShipped + " arm, where nothing is addressed.",
			Limits: "Mandatory inclusion is an INCLUSION CLASS, not an additive term, so it cannot be " +
				"ablated by zeroing a weight — the arm is the write side, and it is therefore a " +
				"different corpus write, not a different query. Digest surface only.",
			AddressToView: true,
		},
		{
			ID:            ProfileP0,
			Title:         "P0 ranking profile (0.90·R + 0.07·F + 0.03·P)",
			Fidelity:      Native,
			Surfaces:      bothSurfaces,
			BearsOn:       []string{"RET-dumb-ranking-suffices"},
			Mechanism:     "the daemon runs with the default P0 additive profile.",
			Limits:        "None beyond the corpus.",
			Env:           []string{"CAIRN_RANK_PROFILE=p0"},
			ExpectProfile: "P0",
		},
		{
			ID:       ProfileP2,
			Title:    "P2 ranking profile (adds salience, intent, novelty; drops the linear P term)",
			Fidelity: Native,
			Surfaces: bothSurfaces,
			BearsOn:  []string{"RET-dumb-ranking-suffices"},
			Mechanism: "the daemon runs with CAIRN_RANK_PROFILE=p2, so ranking uses the full additive " +
				"model. why-ranked's reported profile is asserted, so an arm that failed to take is an " +
				"error rather than a duplicate of P0.",
			Limits: "P2's salience and novelty terms are TELEMETRY-DERIVED and start at zero on a " +
				"freshly provisioned mesh. A single-shot corpus load gives them nothing to work with, " +
				"so this arm measures P2's WEIGHTS, not P2's learning. A fair P0-vs-P2 comparison needs " +
				"a corpus replayed with interaction history — which is E7's substrate, not E4's.",
			Env:           []string{"CAIRN_RANK_PROFILE=p2"},
			ExpectProfile: "P2",
		},
	}
}

// Get returns an arm by id.
func Get(id string) (Arm, error) {
	for _, a := range Catalogue() {
		if a.ID == id {
			return a, nil
		}
	}
	return Arm{}, fmt.Errorf("unknown ablation arm %q", id)
}

// IDs returns every arm id, sorted.
func IDs() []string {
	var out []string
	for _, a := range Catalogue() {
		out = append(out, a.ID)
	}
	sort.Strings(out)
	return out
}

// BearsOn returns the union of claim ids the given arms speak to.
func BearsOn(arms []Arm) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range arms {
		for _, c := range a.BearsOn {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Recompute functions
// ---------------------------------------------------------------------------

// byScoreWithout re-sorts by the published score with the named terms removed.
// Ties keep the original relative order (sort.SliceStable), which preserves
// Cairn's own deterministic tiebreak rather than substituting a new one: an
// ablation that also changed the tiebreak would be measuring two things.
func byScoreWithout(terms ...string) Rerank {
	return func(in []Ranked) []Ranked {
		out := append([]Ranked(nil), in...)
		sort.SliceStable(out, func(i, j int) bool {
			return scoreWithout(out[i], terms) > scoreWithout(out[j], terms)
		})
		for i := range out {
			out[i].Hit.Rank = i + 1
		}
		return out
	}
}

func scoreWithout(r Ranked, terms []string) float64 {
	if r.Exp == nil {
		// No explanation means the harness could not read this item's
		// arithmetic. Sorting it to the bottom rather than guessing keeps the
		// failure visible in the ranking instead of inventing a position.
		return -1
	}
	return r.Exp.ScoreWithout(terms...)
}

// byVectorRank keeps only results the vector retriever returned and orders
// them by its rank. Dropping (rather than demoting) the lexical-only hits is
// the whole point: a vector-only condition does not have them.
func byVectorRank(in []Ranked) []Ranked {
	var out []Ranked
	for _, r := range in {
		if r.Exp != nil && r.Exp.VecRank > 0 {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Exp.VecRank < out[j].Exp.VecRank })
	for i := range out {
		out[i].Hit.Rank = i + 1
	}
	return out
}
