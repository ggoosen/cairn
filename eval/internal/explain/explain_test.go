package explain

import (
	"math"
	"strings"
	"testing"
)

// A why-ranked trace as cairn prints it (internal/daemon/retrieve.go
// WhyRanked, values formatted by rank.Dec).
const sample = `01J8ZQ  rank 3 (profile=search-P0)
  R     0.75 × 0.9 = 0.675   (lex rank 1, vec rank 4, RRF 0.0163934)
  S     0 × 0 = 0   (salience §9.2)
  F     0.5 × 0.07 = 0.035   (created 2026-08-16T00:00:00Z)
  P_eff 0.6667 × 0.03 = 0.020001   (executable priority, decayed)
  I     0 × 0 = 0   (operator intent)
  N     0 × 0 = 0   (novelty/exposure)
  mandatory: recipient (inclusion class — not an additive score term)
  total 0.730001
`

// A P2 trace with both S8 penalties live. The penalty weights are NEGATIVE (the
// §9.1 cap), so an external verifier that summed |product| — or that skipped the
// two lines it did not recognise — would disagree with the total.
const penalised = `01J8ZR  rank 2 (profile=search-P2)
  R     1 × 0.75 = 0.75   (lex rank 1, vec rank 2, RRF 0.0325)
  S     0.4 × 0.08 = 0.032   (salience §9.2)
  F     0.5 × 0.04 = 0.02   (created 2026-08-16T00:00:00Z)
  P_eff 0 × 0 = 0   (executable priority, decayed)
  I     0.6 × 0.1 = 0.06   (operator intent)
  N     0.25 × 0.03 = 0.0075   (novelty/exposure)
  DUP   1 × -0.15 = -0.15   (1 earlier result shares body b3:aaaa; cap 0.15)
  SAT   0.6666666666666666 × -0.15 = -0.09999999999999999   (2 earlier results share thread 01J8ZP; full at 3; cap 0.15)
  total 0.6194999999999999
`

// The penalties are terms like any other: parsed, summed in printed order, and
// reconciled against the total. Before S8 this trace would have reconciled to
// 0.8695 — i.e. the harness would have believed a score the agent never saw.
func TestParsePenalties(t *testing.T) {
	e, err := Parse(penalised)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Terms[TermDup]; got.Value != 1 || got.Weight != -0.15 || got.Product != -0.15 {
		t.Fatalf("DUP term parsed wrong: %+v", got)
	}
	if got := e.Terms[TermSat]; got.Weight != -0.15 {
		t.Fatalf("SAT term parsed wrong: %+v", got)
	}
	if err := e.Reconcile(); err != nil {
		t.Fatal(err)
	}
	// an ablation must carry the penalties through: dropping F changes the score
	// by exactly F's product and by nothing else.
	if diff := e.ScoreWithout() - e.ScoreWithout(TermF); math.Abs(diff-0.02) > 1e-12 {
		t.Fatalf("dropping F on a penalised trace moved the score by %v, want 0.02", diff)
	}
}

// Mutation guard: an external verifier that ignored the penalty lines would
// reconcile a trace whose total does not include them. Assert it does not.
func TestReconciliationCatchesAnIgnoredPenalty(t *testing.T) {
	e, err := Parse(strings.Replace(penalised,
		"  DUP   1 × -0.15 = -0.15   (1 earlier result shares body b3:aaaa; cap 0.15)\n", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(); err == nil {
		t.Fatal("a trace missing its duplicate penalty reconciled: the verifier is not summing penalties")
	}
}

func TestParse(t *testing.T) {
	e, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if e.MessageID != "01J8ZQ" || e.Rank != 3 || e.Profile != "search-P0" {
		t.Fatalf("header parsed wrong: %+v", e)
	}
	if e.LexRank != 1 || e.VecRank != 4 || math.Abs(e.RRF-0.0163934) > 1e-9 {
		t.Fatalf("R annotation parsed wrong: lex=%d vec=%d rrf=%v", e.LexRank, e.VecRank, e.RRF)
	}
	if e.Mandatory != "recipient" {
		t.Fatalf("mandatory class parsed wrong: %q", e.Mandatory)
	}
	if got := e.Terms[TermF]; got.Value != 0.5 || got.Weight != 0.07 || got.Product != 0.035 {
		t.Fatalf("F term parsed wrong: %+v", got)
	}
	if err := e.Reconcile(); err != nil {
		t.Fatal(err)
	}
}

// A trace whose printed terms do not sum to its printed total means the
// explanation does not describe the score. Every ablation built on it would be
// measuring fiction, so this must be loud.
func TestReconciliationCatchesADroppedTerm(t *testing.T) {
	e, err := Parse(strings.Replace(sample, "  F     0.5 × 0.07 = 0.035   (created 2026-08-16T00:00:00Z)\n", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(); err == nil {
		t.Fatal("a missing term reconciled; a parse failure would be indistinguishable from a successful ablation")
	}
}

func TestScoreWithoutRemovesExactlyOneTerm(t *testing.T) {
	e, _ := Parse(sample)
	full := e.ScoreWithout()
	if math.Abs(full-e.Total) > ReconcileTolerance {
		t.Fatalf("recompute with nothing dropped (%v) does not match the total (%v)", full, e.Total)
	}
	noF := e.ScoreWithout(TermF)
	if math.Abs((full-noF)-0.035) > 1e-9 {
		t.Fatalf("dropping F changed the score by %v, want 0.035", full-noF)
	}
	noP := e.ScoreWithout(TermPeff)
	if math.Abs((full-noP)-0.020001) > 1e-6 {
		t.Fatalf("dropping P_eff changed the score by %v, want ~0.020001", full-noP)
	}
}

func TestScoreOnly(t *testing.T) {
	e, _ := Parse(sample)
	if got := e.ScoreOnly(TermR); math.Abs(got-0.675) > 1e-9 {
		t.Fatalf("R-only = %v, want 0.675", got)
	}
}

func TestGarbageIsRefused(t *testing.T) {
	for _, bad := range []string{"", "nothing here", "01J8  rank x (profile=p)\n"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("parsed %q as an explanation", bad)
		}
	}
}
