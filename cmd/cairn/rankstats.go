package main

// P2-3b: `cairn rank-stats` shows the per-term contribution distribution from
// logged why_ranked; `--calibrate` additionally replays the episodes under a
// weight grid (task-holdout) and prints a RECOMMENDATION (never auto-applied).

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/daemon"
)

func newRankStatsCmd(dirFlag *string) *cobra.Command {
	var calibrate bool
	cmd := &cobra.Command{
		Use:   "rank-stats",
		Short: "Per-term ranking contribution distribution; --calibrate replays episodes for a weight recommendation (§9.3)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := daemon.Request{Op: "rank-stats"}
			if calibrate {
				req.Query = "calibrate"
			}
			resp, err := call(dirFlag, req)
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Error)
			}
			return printJSON(cmd, resp.Status)
		},
	}
	cmd.Flags().BoolVar(&calibrate, "calibrate", false, "replay logged episodes under a weight grid (task-holdout) and print a recommendation")
	return cmd
}
