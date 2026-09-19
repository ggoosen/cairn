package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ggoosen/cairn/eval/internal/ablation"
	"github.com/ggoosen/cairn/eval/internal/backend"
	"github.com/ggoosen/cairn/eval/internal/claims"
	"github.com/ggoosen/cairn/eval/internal/corpus"
	"github.com/ggoosen/cairn/eval/internal/experiment"
)

// runMeasure is E4: the ablation × baseline matrix.
//
// It computes metrics and writes them, and it prints NO number. See
// internal/score for the gate and why it has two halves (signed criteria AND
// an independent corpus).
func runMeasure(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("measure", flag.ContinueOnError)
	corpusDir := fs.String("corpus", "", "corpus directory (required)")
	split := fs.String("split", corpus.SplitDev, "query split: dev | holdout | all — §3.7 calibrates on dev and answers the claim on holdout")
	backendsFlag := fs.String("backends", "B0,B1,B2,B5", "memory conditions to run")
	armsFlag := fs.String("arms", ablation.AsShipped, "ablation arms (Cairn only); \"all\" runs the whole catalogue")
	surfacesFlag := fs.String("surfaces", "search,digest", "retrieval surfaces")
	outDir := fs.String("out", "", "directory for run records and the scorecard (default: a temp dir, printed)")
	seed := fs.Int64("seed", 1, "seed for the bootstrap and any backend randomness")
	k := fs.Int("k", 0, "results requested per query (default: the CLI's own default)")
	budget := fs.Int("budget", 0, "budget_chars for budgeted surfaces")
	claimsPath := fs.String("claims", "", "path to claims.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *corpusDir == "" {
		return fmt.Errorf("-corpus is required: measurement needs material with declared label provenance, and there is no default")
	}
	reg, err := loadRegister(*claimsPath)
	if err != nil {
		return err
	}
	c, err := corpus.Load(*corpusDir)
	if err != nil {
		return err
	}
	mat, err := experiment.FromCorpus(c, *split)
	if err != nil {
		return err
	}

	armIDs := splitList(*armsFlag)
	if *armsFlag == "all" {
		armIDs = ablation.IDs()
	}
	if err := gateIndependentCorpus(reg, mat, armIDs); err != nil {
		return err
	}

	var backends []backend.ID
	for _, b := range splitList(*backendsFlag) {
		backends = append(backends, backend.ID(b))
	}
	var surfaces []backend.Surface
	for _, s := range splitList(*surfacesFlag) {
		surfaces = append(surfaces, backend.Surface(s))
	}

	conds, err := experiment.AblationMatrix(backends, armIDs, surfaces)
	if err != nil {
		return err
	}
	dest, err := resolveOutDir(*outDir)
	if err != nil {
		return err
	}

	m := experiment.Matrix{Name: "e4-ablations", Conditions: conds, Material: mat, Register: reg}
	card, failures, runErr := m.Run(ctx, experiment.Options{
		OutDir: dest, Seed: *seed, K: *k, BudgetChars: *budget, Label: "e4",
	})

	for _, f := range failures {
		// Failures are FINDINGS, not noise: "B3 is a stub", "vector-only needs
		// an embedder", "the P2 arm did not take effect" each say something
		// about what this evaluation can currently establish.
		fmt.Fprintf(os.Stderr, "NOT RUN  %s\n         %v\n", f.Condition, f.Err)
	}
	if runErr != nil {
		return runErr
	}

	cardPath := filepath.Join(dest, "scorecard-e4.json")
	if err := card.WriteFile(cardPath); err != nil {
		return err
	}
	fmt.Print(card.Summary())
	fmt.Printf("\nrun records and scorecard in %s\n", dest)
	fmt.Printf("scorecard: %s (evidence=%v)\n", cardPath, card.Evidence)
	return nil
}

// gateIndependentCorpus refuses to push mined human ground truth through the
// backends while the criteria that would interpret the result are unsigned.
//
// This mirrors the guard `cairn-eval smoke` already carries, and it is the
// stricter half of the standing rule. Running the SAMPLE corpus is fine — it
// is labelled not-evidence and its whole job is to prove the plumbing. Running
// an INDEPENDENT corpus produces the thing that would be evidence, and doing
// that before the kill criteria are fixed is exactly the ordering E1 exists to
// prevent: once the number exists, the threshold can no longer be chosen
// innocently.
func gateIndependentCorpus(reg *claims.Register, mat experiment.Material, armIDs []string) error {
	if !mat.CorpusRef.Independent {
		return nil
	}
	var arms []ablation.Arm
	for _, id := range armIDs {
		a, err := ablation.Get(id)
		if err != nil {
			return err
		}
		arms = append(arms, a)
	}
	bearsOn := ablation.BearsOn(arms)
	ok, blocked := reg.Signed(bearsOn...)
	if ok {
		return nil
	}
	return fmt.Errorf(`corpus %q carries INDEPENDENT human labels, and this matrix bears on kill criteria that are not signed off:

  %s

Running it would produce the first half of a measurement before its
falsification thresholds were fixed (BUILD-PLAN §5-E1). Either:
  - exercise the apparatus on a non-independent corpus (eval/corpora/sample-plumbing-v1), or
  - have the operator sign the criteria above in eval/claims.yaml.

There is no override flag, deliberately`,
		mat.CorpusRef.ID, strings.Join(blocked, "\n  "))
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func resolveOutDir(dir string) (string, error) {
	if dir != "" {
		return dir, os.MkdirAll(dir, 0o700)
	}
	return os.MkdirTemp("", "cairn-eval-results-")
}
