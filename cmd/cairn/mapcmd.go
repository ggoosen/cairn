package main

// P2-5: `cairn map` regenerates the local navigation view (views/<agent>/map.md).

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/daemon"
)

func newMapCmd(dirFlag *string) *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "map",
		Short: "Regenerate the local navigation view (topics/threads/pins rollup → views/<agent>/map.md)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "map", AgentView: agent})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Error)
			}
			return printJSON(cmd, resp.Status)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "operator", "agent view to render the map for")
	return cmd
}
