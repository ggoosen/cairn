package experiment

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/eval/internal/ablation"
	"github.com/ggoosen/cairn/eval/internal/backend"
	"github.com/ggoosen/cairn/eval/internal/claims"
	"github.com/ggoosen/cairn/eval/internal/corpus"
	"github.com/ggoosen/cairn/eval/internal/metric"
	"github.com/ggoosen/cairn/eval/internal/result"
)

func sample(t *testing.T) Material {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "corpora", "sample-plumbing-v1"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := corpus.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	mat, err := FromCorpus(c, corpus.SplitDev)
	if err != nil {
		t.Fatal(err)
	}
	return mat
}

func register(t *testing.T) *claims.Register {
	t.Helper()
	reg, err := claims.Load(filepath.Join("..", "..", claims.DefaultPath))
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// A split must be named explicitly. §3.7 forbids tuning on the evaluation set,
// and a default split is a decision nobody made.
func TestSplitMustBeNamed(t *testing.T) {
	dir, _ := filepath.Abs(filepath.Join("..", "..", "corpora", "sample-plumbing-v1"))
	c, err := corpus.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FromCorpus(c, ""); err == nil {
		t.Fatal("an unnamed split was accepted")
	}
	if _, err := FromCorpus(c, "everything"); err == nil {
		t.Fatal("an unknown split was accepted")
	}
}

// The plumbing, end to end, over the file-backed baselines: no daemon, no
// build, offline. It proves the harness can drive a condition, record it, and
// derive a section — which is exactly the S4 exit criterion, and nothing more.
func TestBaselineConditionsRunEndToEnd(t *testing.T) {
	mat := sample(t)
	out := t.TempDir()
	asShipped, _ := ablation.Get(ablation.AsShipped)

	conds, err := AblationMatrix(
		[]backend.ID{backend.B0NoMemory, backend.B1GrepTranscript, backend.B2FlatNotes},
		[]string{ablation.AsShipped},
		[]backend.Surface{backend.SurfaceSearch, backend.SurfaceDigest},
	)
	if err != nil {
		t.Fatal(err)
	}
	m := Matrix{Name: "e4-ablations", Conditions: conds, Material: mat, Register: register(t)}
	card, failures, err := m.Run(t.Context(), Options{OutDir: out, Seed: 5, Label: "test"})
	if err != nil {
		t.Fatal(err)
	}
	// B1 has no digest surface; that must be a failure, not a zero.
	for _, f := range failures {
		if !errors.Is(f.Err, ErrConditionUnrunnable) {
			t.Fatalf("unexpected failure for %s: %v", f.Condition, f.Err)
		}
	}
	if len(card.Sections) == 0 {
		t.Fatal("no section produced")
	}
	if card.Evidence {
		t.Fatal("a card over the synthetic sample stamped itself as evidence")
	}

	// Every run record must be readable and must carry ground truth, so a
	// third party can recompute our numbers or disagree with them.
	for _, sec := range card.Sections {
		if len(sec.Metrics) == 0 {
			t.Fatalf("section %s produced no metrics", sec.Arm.String())
		}
	}
	files, err := filepath.Glob(filepath.Join(out, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no run records written: %v", err)
	}
	for _, f := range files {
		run, err := result.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if run.Kind != result.KindMeasurement {
			t.Fatalf("%s: kind %q", f, run.Kind)
		}
		if run.Corpus.Checksum == "" {
			t.Fatalf("%s: corpus not pinned by checksum", f)
		}
		for _, o := range run.Outcomes {
			if o.Error == "" && len(o.Relevant) == 0 {
				t.Fatalf("%s: outcome %s recorded no ground truth", f, o.QueryID)
			}
		}
	}

	// Sanity on the metric bridge: B0 stores nothing, so it must score zero
	// everywhere — the control arm working is what makes the others legible.
	for _, sec := range card.Sections {
		if sec.Arm.Backend != string(backend.B0NoMemory) {
			continue
		}
		agg, ok := sec.Get(metric.SuccessAt, 5)
		if !ok || agg.Mean != 0 {
			t.Fatalf("B0 (no memory) scored %v at Success@5 — the control condition is not a control", agg.Mean)
		}
	}
	_ = asShipped
}

// A declared stub must fail loudly inside the matrix, not vanish from it. An
// unimplemented baseline that produced no row would silently become an absence
// nobody notices; one that produced a zero row would be a fabricated result.
func TestStubBaselinesFailLoudlyInAMatrix(t *testing.T) {
	mat := sample(t)
	conds, err := AblationMatrix(
		[]backend.ID{backend.B3VectorRAG, backend.B4FullContext},
		[]string{ablation.AsShipped},
		[]backend.Surface{backend.SurfaceSearch},
	)
	if err != nil {
		t.Fatal(err)
	}
	m := Matrix{Name: "e4-ablations", Conditions: conds, Material: mat, Register: register(t)}
	card, failures, err := m.Run(t.Context(), Options{OutDir: t.TempDir(), Seed: 1, Label: "test"})
	if err == nil {
		t.Fatal("a matrix of nothing but stubs reported success")
	}
	if len(failures) != 2 {
		t.Fatalf("expected two loud failures, got %d", len(failures))
	}
	for _, f := range failures {
		if !strings.Contains(f.Err.Error(), "stub") {
			t.Fatalf("%s failed for the wrong reason: %v", f.Condition, f.Err)
		}
	}
	// The refusal must be recorded in the artifact, not only in the terminal.
	if len(card.Notes) != 2 {
		t.Fatalf("the scorecard did not record the unrun conditions: %v", card.Notes)
	}
}

// Ablation arms are Cairn-only, and asking for one against a baseline must be
// refused rather than answered with the baseline's default behaviour.
func TestAblationsAreRefusedOnBaselines(t *testing.T) {
	p2, _ := ablation.Get(ablation.ProfileP2)
	err := Applicable(Condition{Backend: backend.B1GrepTranscript, Arm: p2, Surface: backend.SurfaceSearch})
	if !errors.Is(err, ErrConditionUnrunnable) {
		t.Fatalf("a Cairn ablation was accepted against B1: %v", err)
	}
}

// An unavailable arm must be refused before anything is provisioned.
func TestUnavailableArmIsRefusedUpFront(t *testing.T) {
	arm, _ := ablation.Get(ablation.PriorityUndecayed)
	err := Applicable(Condition{Backend: backend.B5Cairn, Arm: arm, Surface: backend.SurfaceSearch})
	if !errors.Is(err, ablation.ErrUnavailable) {
		t.Fatalf("the unavailable arm was accepted: %v", err)
	}
}
