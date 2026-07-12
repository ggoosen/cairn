package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/bench"
)

func newBenchCmd(_ *string) *cobra.Command {
	cmd := &cobra.Command{Use: "bench", Short: "Reproducible benchmarks"}
	cmd.AddCommand(&cobra.Command{
		Use:   "golden",
		Short: "Run the golden retrieval corpus in a throwaway mesh; report Success@5 and lexical-only top-10 vs the P0 gates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := bench.RunGolden(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if !res.PassSuccess || !res.PassLexicalOnly {
				return fmt.Errorf("golden corpus below gates (Success@5 %.2f, lexical %.2f)", res.SuccessAt5, res.LexicalTop10)
			}
			return nil
		},
	})
	groupGuard(cmd)
	return cmd
}
