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
