package main

// P2-6: `cairn compact` regenerates the current-state compaction view
// (views/<agent>/compaction.md).

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/daemon"
)

func newCompactCmd(dirFlag *string) *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "compact",
		Short: "Regenerate the current-state compaction view (events → live entities → views/<agent>/compaction.md)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "compact", AgentView: agent})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Error)
			}
			return printJSON(cmd, resp.Status)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "operator", "agent view to render the compaction for")
	return cmd
}
