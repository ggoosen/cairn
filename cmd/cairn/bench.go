package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/bench"
	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/embed"
)

func newBenchCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "bench", Short: "Reproducible benchmarks"}
	var embedderKind string
	golden := &cobra.Command{
		Use:   "golden",
		Short: "Run the golden retrieval corpus in a throwaway mesh; report Success@5 and lexical-only top-10 vs the P0 gates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var embedder embed.Embedder
			switch embedderKind {
			case "dev", "":
				// nil → deterministic BagOfWords
			case "real":
				// F9: the pinned sentence-transformers model. Unlike the
				// daemon's silent fallback, --embedder real REFUSES to run
				// with the dev embedder — a fake "real" score is worse than
				// no score.
				dir, err := config.PortableDir(*dirFlag)
				if err != nil {
					return err
				}
				py := embed.PythonInterpreter(dir)
				if py == "" {
					return fmt.Errorf("--embedder real needs the embed venv (DOGFOOD.md §2: python venv at <cairn>/.cairn/embed-venv with sentence-transformers, or $CAIRN_EMBED_PYTHON)")
				}
				p, err := embed.NewPython(py)
				if err != nil {
					return fmt.Errorf("real embedder unavailable: %w", err)
				}
				defer func() { _ = p.Close() }()
				embedder = p
			default:
				return fmt.Errorf("--embedder must be dev or real, not %q", embedderKind)
			}
			res, err := bench.RunGolden(cmd.OutOrStdout(), embedder)
			if err != nil {
				return err
			}
			if !res.PassSuccess || !res.PassLexicalOnly {
				return fmt.Errorf("golden corpus below gates (Success@5 %.2f, lexical %.2f)", res.SuccessAt5, res.LexicalTop10)
			}
			return nil
		},
	}
	golden.Flags().StringVar(&embedderKind, "embedder", "dev", "dev (deterministic BagOfWords) | real (pinned sentence-transformers via the embed venv) — F9")
	cmd.AddCommand(golden)
	groupGuard(cmd)
	return cmd
}
