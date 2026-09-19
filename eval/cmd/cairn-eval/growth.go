package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ggoosen/cairn/eval/internal/ablation"
	"github.com/ggoosen/cairn/eval/internal/backend"
	"github.com/ggoosen/cairn/eval/internal/corpus"
	"github.com/ggoosen/cairn/eval/internal/experiment"
	"github.com/ggoosen/cairn/eval/internal/growth"
	"github.com/ggoosen/cairn/eval/internal/score"
)

// runGrowth is E9's RECALL-UNDER-GROWTH curve, and only that curve. The rest
// of E9 — recall-over-age, supersession accuracy, stale-confidence, duplicate
// dilution, temporal competence, and every mesh-specific metric — is not built
// here; see BUILD-PLAN §3.4.
//
// The budget and k are held FIXED across every scale. That is the whole
// experiment: selectivity demand rises with N while the agent's context does
// not, and anything that scaled the budget with the corpus would be measuring
// a different system than the one people run.
func runGrowth(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("growth", flag.ContinueOnError)
	corpusDir := fs.String("corpus", "", "corpus directory (required)")
	split := fs.String("split", corpus.SplitDev, "query split: dev | holdout | all")
	backendFlag := fs.String("backend", string(backend.B5Cairn), "memory condition to grow around")
	surfacesFlag := fs.String("surfaces", "search,digest", "retrieval surfaces")
	scalesFlag := fs.String("scales", "1,10,100", "corpus multipliers; the full curve §3.4 asks for is 1,10,100,1000")
	generatorFlag := fs.String("generator", "both", "filler model: neutral | contending | both")
	outDir := fs.String("out", "", "directory for run records and the scorecard")
	seed := fs.Int64("seed", 1, "seed for filler generation and the bootstrap")
	k := fs.Int("k", 0, "results per query, HELD FIXED across scales")
	budget := fs.Int("budget", 0, "budget_chars, HELD FIXED across scales")
	claimsPath := fs.String("claims", "", "path to claims.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *corpusDir == "" {
		return fmt.Errorf("-corpus is required")
	}
	reg, err := loadRegister(*claimsPath)
	if err != nil {
		return err
	}
	c, err := corpus.Load(*corpusDir)
	if err != nil {
		return err
	}
	base, err := experiment.FromCorpus(c, *split)
	if err != nil {
		return err
	}
	if err := gateIndependentCorpus(reg, base, []string{ablation.AsShipped}); err != nil {
		return err
	}

	var scales []int
	for _, s := range splitList(*scalesFlag) {
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("bad scale %q: %w", s, err)
		}
		scales = append(scales, n)
	}
	gens := growth.Generators()
	switch *generatorFlag {
	case "neutral":
		gens = []growth.Generator{growth.Neutral}
	case "contending":
		gens = []growth.Generator{growth.Contending}
	case "both":
	default:
		return fmt.Errorf("generator must be neutral, contending or both; got %q", *generatorFlag)
	}

	var surfaces []backend.Surface
	for _, s := range splitList(*surfacesFlag) {
		surfaces = append(surfaces, backend.Surface(s))
	}
	dest, err := resolveOutDir(*outDir)
	if err != nil {
		return err
	}

	asShipped, err := ablation.Get(ablation.AsShipped)
	if err != nil {
		return err
	}
	bid := backend.ID(*backendFlag)

	card := score.New("e9-recall-under-growth", *seed, base.CorpusRef, reg, "LONG-scales-with-corpus")
	card.Note("%s", growth.Limits)
	card.Note("budget_chars and k are held fixed across every scale; that constancy IS the experiment.")
	card.Note("This is E9's recall-under-growth curve ONLY. Recall-over-age, supersession accuracy, stale-confidence, duplicate dilution, temporal competence and every mesh metric are NOT measured here.")

	for _, gen := range gens {
		for _, scale := range scales {
			mat, err := growth.Grow(base, scale, gen, *seed)
			if err != nil {
				return err
			}
			for _, surface := range surfaces {
				cond := experiment.Condition{Backend: bid, Arm: asShipped, Surface: surface}
				if err := experiment.Applicable(cond); err != nil {
					fmt.Fprintf(os.Stderr, "NOT RUN  %s: %v\n", cond, err)
					card.Note("NOT RUN — %s at x%d/%s: %v", cond, scale, gen, err)
					continue
				}
				fmt.Fprintf(os.Stderr, "running  %s at x%d (%s), %d items…\n", cond, scale, gen, len(mat.Items))
				out, err := experiment.Run(ctx, experiment.Options{
					OutDir: dest, Seed: *seed, K: *k, BudgetChars: *budget,
					Label: fmt.Sprintf("e9-x%d-%s", scale, gen),
				}, cond, mat)
				if err != nil {
					fmt.Fprintf(os.Stderr, "NOT RUN  %s at x%d/%s: %v\n", cond, scale, gen, err)
					card.Note("NOT RUN — %s at x%d/%s: %v", cond, scale, gen, err)
					continue
				}
				sec := out.Score(mat, *seed)
				sec.Arm = growth.ArmFor(bid, surface, growth.Point{Scale: scale, Generator: gen, Items: len(mat.Items)}, growth.Limits)
				sec.Arm.RetrievalMode = out.RetrievalMode
				card.Add(sec)
			}
		}
	}
	if len(card.Sections) == 0 {
		return fmt.Errorf("no point of the curve ran; see the notes in the scorecard")
	}

	cardPath := filepath.Join(dest, "scorecard-e9-growth.json")
	if err := card.WriteFile(cardPath); err != nil {
		return err
	}
	fmt.Print(card.Summary())
	fmt.Printf("\nrun records and scorecard in %s\n", dest)
	fmt.Printf("scorecard: %s (evidence=%v)\n", cardPath, card.Evidence)
	return nil
}
