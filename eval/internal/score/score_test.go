package score

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/eval/internal/claims"
	"github.com/ggoosen/cairn/eval/internal/metric"
)

func realRegister(t *testing.T) *claims.Register {
	t.Helper()
	reg, err := claims.Load(filepath.Join("..", "..", claims.DefaultPath))
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func signedRegister(t *testing.T, ids ...string) *claims.Register {
	t.Helper()
	var b strings.Builder
	b.WriteString("version: 1\nclaims:\n")
	for _, id := range ids {
		b.WriteString("  - id: " + id + "\n    kill_criterion: \"kill\"\n    signoff: 2026-08-16\n")
	}
	reg, err := claims.Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// The gate: while a bearing claim is unsigned, nothing is reportable. This is
// the single property the whole sprint rests on.
func TestUnsignedCriteriaBlockReporting(t *testing.T) {
	card := New("e4-ablations", 1,
		CorpusRef{ID: "sample-plumbing", LabelSource: "SYNTHETIC", Independent: false},
		realRegister(t), "RET-hybrid-earns-place", "PROD-beats-alternatives")
	if card.Reportable() == nil {
		t.Fatal("a card bearing on unsigned criteria reported as evidence")
	}
	if card.Evidence {
		t.Fatal("Evidence stamped true on a gated card — the file would not argue against itself")
	}
	if !strings.Contains(card.NotEvidenceReason, "PROD-beats-alternatives") {
		t.Fatalf("the blocking claim is not named in the artifact: %q", card.NotEvidenceReason)
	}
}

// Signing every criterion is NOT sufficient. A number over a corpus this
// project authored is a statement about the harness.
func TestSignedCriteriaStillBlockedByANonIndependentCorpus(t *testing.T) {
	reg := signedRegister(t, "RET-success5")
	card := New("e4-ablations", 1,
		CorpusRef{ID: "sample-plumbing", LabelSource: "SYNTHETIC — authored by this project", Independent: false},
		reg, "RET-success5")
	err := card.Reportable()
	if err == nil {
		t.Fatal("a synthetic corpus passed the evidence gate")
	}
	if !strings.Contains(err.Error(), "independent") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
}

func TestBothConditionsMetIsReportable(t *testing.T) {
	reg := signedRegister(t, "RET-success5")
	card := New("e4-ablations", 1,
		CorpusRef{ID: "gh-dupes", Checksum: "abcdef012345", LabelSource: "maintainer duplicate markers", Independent: true},
		reg, "RET-success5")
	if err := card.Reportable(); err != nil {
		t.Fatalf("signed criteria over an independent corpus should be reportable: %v", err)
	}
	if !card.Evidence {
		t.Fatal("Evidence not stamped true")
	}
}

// A claim id the register never heard of must block, not vanish.
func TestUnknownBearingClaimBlocks(t *testing.T) {
	reg := signedRegister(t, "RET-success5")
	card := New("x", 1, CorpusRef{ID: "c", Independent: true}, reg, "TYPO-CLAIM")
	if card.Reportable() == nil {
		t.Fatal("a bearing claim absent from the register did not block")
	}
}

// The one thing a gated card may print must contain no metric value.
func TestSummaryLeaksNoNumbers(t *testing.T) {
	card := New("e4-ablations", 7,
		CorpusRef{ID: "sample-plumbing", LabelSource: "SYNTHETIC", Independent: false},
		realRegister(t), "RET-success5")
	card.Add(Compute(
		Arm{Backend: "B5", Ablation: "rrf", Surface: "search", Fidelity: "native"},
		[]string{"run1"},
		map[int][]metric.Sample{1: {{QueryID: "q1", Value: 1}}, 5: {{QueryID: "q1", Value: 1}}},
		7))

	summary := card.Summary()
	if !strings.Contains(summary, "NO NUMBERS ARE SHOWN") {
		t.Fatalf("a gated summary must say so:\n%s", summary)
	}
	for _, forbidden := range []string{"ndcg", "nDCG", "mrr", "MRR", "0.", "1.0", "PASS", "FAIL", "beats", "wins"} {
		if strings.Contains(summary, forbidden) {
			t.Fatalf("summary of a gated card contains %q — that is a number reported as evidence:\n%s", forbidden, summary)
		}
	}
}

// Metrics are still COMPUTED and WRITTEN while gated: the plumbing has to be
// provable. Only reporting is blocked.
func TestGatedCardsStillComputeAndPersist(t *testing.T) {
	card := New("e4-ablations", 3,
		CorpusRef{ID: "sample-plumbing", LabelSource: "SYNTHETIC", Independent: false},
		realRegister(t), "RET-success5")
	card.Add(Compute(
		Arm{Backend: "B1", Surface: "search"},
		[]string{"r"},
		map[int][]metric.Sample{
			1:  {{QueryID: "q1", Value: 0}, {QueryID: "q2", Value: 1}},
			5:  {{QueryID: "q1", Value: 1}, {QueryID: "q2", Value: 1}},
			10: {{QueryID: "q1", Value: 1}, {QueryID: "q2", Value: 1}},
		}, 3))

	path := filepath.Join(t.TempDir(), "card.json")
	if err := card.WriteFile(path); err != nil {
		t.Fatal(err)
	}
	back, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Evidence {
		t.Fatal("the persisted card lost its not-evidence stamp")
	}
	sec := back.Sections[0]
	if agg, ok := sec.Get(metric.SuccessAt, 1); !ok || agg.Mean != 0.5 {
		t.Fatalf("metrics were not computed: %+v", sec.Metrics)
	}
}

// An unscoreable outcome must show up as an error count, not disappear.
func TestErrorsAreCounted(t *testing.T) {
	sec := Compute(Arm{Backend: "B1", Surface: "digest"}, nil,
		map[int][]metric.Sample{
			1: {{QueryID: "q1", Excluded: true, Reason: "backend does not implement this surface"}},
		}, 1)
	if sec.Errors != 1 {
		t.Fatalf("errors=%d, want 1", sec.Errors)
	}
}
