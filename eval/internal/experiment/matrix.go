package experiment

import (
	"context"
	"fmt"

	"github.com/ggoosen/cairn/eval/internal/ablation"
	"github.com/ggoosen/cairn/eval/internal/backend"
	"github.com/ggoosen/cairn/eval/internal/claims"
	"github.com/ggoosen/cairn/eval/internal/score"
)

// Matrix is a set of conditions run over one body of material.
type Matrix struct {
	Name       string // e4-ablations | e9-growth | …
	Conditions []Condition
	Material   Material
	Register   *claims.Register
}

// BearsOn is the union of claims every arm in the matrix speaks to. The
// reporting gate reads it, so a matrix that named no claim could never be
// blocked — which is why this is derived from the arms rather than passed in.
func (m Matrix) BearsOn() []string {
	arms := make([]ablation.Arm, 0, len(m.Conditions))
	for _, c := range m.Conditions {
		arms = append(arms, c.Arm)
	}
	return ablation.BearsOn(arms)
}

// Failure records a condition that could not be run, and why.
//
// It is a first-class result, not an exception. "B3 is a stub", "vector-only
// needs an embedder and none was provisioned", "priority-undecayed has no
// black-box route" are all findings about what this evaluation can and cannot
// currently establish, and a matrix that dropped them would silently look more
// complete than it is.
type Failure struct {
	Condition Condition
	Err       error
}

// Run executes every condition and returns a scorecard plus the failures.
//
// The scorecard is COMPUTED regardless of signoff state — the plumbing has to
// be provable — but carries its own Evidence=false stamp and refuses to be
// rendered as a comparison until score.Reportable passes. See internal/score.
func (m Matrix) Run(ctx context.Context, opts Options) (*score.ScoreCard, []Failure, error) {
	if m.Register == nil {
		return nil, nil, fmt.Errorf("a matrix without a claims register cannot know whether its results may be reported")
	}
	card := score.New(m.Name, opts.Seed, m.Material.CorpusRef, m.Register, m.BearsOn()...)

	var failures []Failure
	for _, cond := range m.Conditions {
		out, err := Run(ctx, opts, cond, m.Material)
		if err != nil {
			failures = append(failures, Failure{Condition: cond, Err: err})
			card.Note("NOT RUN — %s: %v", cond, err)
			continue
		}
		sec := out.Score(m.Material, opts.Seed)
		if out.EmptyForEveryQuery {
			// Not a failure — the run happened and the zeros are real. But a
			// condition that returned nothing for every single query is the
			// one shape a reader must not skim past, so it is stamped on the
			// section as well as noted on the run record.
			sec.Notes = append(sec.Notes, "RETURNED NOTHING FOR EVERY QUERY — read the raw output before treating these zeros as a retrieval result.")
			card.Note("%s returned zero results for every query; the zeros are real but so is the possibility that the condition was never properly exercised.", cond)
		}
		card.Add(sec)
	}
	if len(card.Sections) == 0 {
		return card, failures, fmt.Errorf("no condition in the matrix ran; every one failed (see the scorecard's notes)")
	}
	return card, failures, nil
}

// AblationMatrix builds the E4 matrix BUILD-PLAN §3.4 specifies: every baseline
// under the as-shipped condition, plus every ablation arm against Cairn, on
// every surface each is meaningful on.
//
// Conditions that cannot be run honestly are INCLUDED, so that running the
// matrix surfaces them as failures rather than pretending they were never
// asked for. B3/B4 and the unavailable arm are the cases that matter: a
// baseline missing from the table is invisible, a baseline failing loudly in
// the output is a finding.
func AblationMatrix(backends []backend.ID, armIDs []string, surfaces []backend.Surface) ([]Condition, error) {
	var conds []Condition
	for _, id := range backends {
		for _, armID := range armIDs {
			arm, err := ablation.Get(armID)
			if err != nil {
				return nil, err
			}
			// A baseline has nothing to ablate; only the control arm applies.
			if arm.ID != ablation.AsShipped && id != backend.B5Cairn {
				continue
			}
			for _, s := range surfaces {
				if !arm.AppliesTo(s) {
					continue
				}
				conds = append(conds, Condition{Backend: id, Arm: arm, Surface: s})
			}
		}
	}
	if len(conds) == 0 {
		return nil, fmt.Errorf("the requested backends, arms and surfaces produce no meaningful condition")
	}
	return conds, nil
}
