package main

// P2H3 (Codex P2 shakedown MAJOR-4): a one-shot operator health view. README
// and PROGRESS referenced `cairn status` (and the degradation ladder claims the
// level "shows in cairn status"), but the CLI command was absent — the IPC
// `status` op existed and was reachable only from the harness. This wires the
// documented command to that op and renders a compact operator summary: build
// version, log head, projection health (parked events), membership/peers, sync
// listener state, and the degradation level.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/daemon"
)

func newStatusCmd(dirFlag *string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "One-shot daemon health: version, log head, projection health, peers, sync, degradation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "status"})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Error)
			}
			if asJSON {
				return printJSON(cmd, resp.Status)
			}
			s := resp.Status
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "cairn status\n")
			fmt.Fprintf(out, "  version:       %v\n", s["version"])
			fmt.Fprintf(out, "  cairn_id:      %v\n", s["cairn_id"])
			fmt.Fprintf(out, "  device_id:     %v\n", s["device_id"])
			fmt.Fprintf(out, "  log next_seq:  %v\n", s["next_seq"])
			fmt.Fprintf(out, "  degradation:   %v\n", s["degradation"])
			if v, ok := s["pending_embeddings"]; ok {
				fmt.Fprintf(out, "  pending embed: %v\n", v)
			}
			// projection health: parked events (terminal = genuine corruption a
			// replay can never heal; retryable = a dependency not yet replicated).
			term, hasTerm := s["parked_terminal"]
			retry := s["parked_retryable"]
			if hasTerm {
				health := "clean"
				if n, ok := term.(float64); ok && n > 0 {
					health = "ATTENTION — run `cairn doctor`"
				}
				fmt.Fprintf(out, "  projection:    %s (parked: %v terminal, %v retryable)\n", health, term, retry)
			}
			fmt.Fprintf(out, "  members:       %v\n", s["members"])
			fmt.Fprintf(out, "  sync peers:    %v\n", s["peers"])
			fmt.Fprintf(out, "  sync listener: %v\n", s["sync_listener"])
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the raw status object as JSON")
	return cmd
}
