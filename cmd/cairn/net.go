package main

// P3-4b CLI: `cairn net` — a one-shot network/connectivity diagnostic. It
// summarises the resolved transport, this node's role, the sync listener state,
// and configured peers. Relay diagnostics belong to the iroh transport, whose
// live wire is deferred (hardware-gated) — the line says so honestly rather
// than reporting a fake status. See P3-PLAN.md for the iroh/relay/patching story.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
)

func newNetCmd(dirFlag *string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "net",
		Short: "Network diagnostics: transport, role, sync listener, peers, relay status (P3)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "sync-status"})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Error)
			}
			if asJSON {
				return printJSON(cmd, resp.Status)
			}
			st := resp.Status
			out := cmd.OutOrStdout()
			transport := str(st["transport"], config.TransportTCPTailnet)
			fmt.Fprintf(out, "transport:  %s\n", transport)
			fmt.Fprintf(out, "role:       %s\n", str(st["role"], config.RoleFull))
			fmt.Fprintf(out, "listener:   %s\n", str(st["listener"], "unknown"))
			if peers, ok := st["peers"].([]any); ok {
				fmt.Fprintf(out, "peers:      %d configured\n", len(peers))
			} else {
				fmt.Fprintf(out, "peers:      0 configured\n")
			}
			if bootstrap, _ := st["bootstrap"].(bool); bootstrap {
				fmt.Fprintf(out, "trust:      grant-chain bootstrap (log not yet fully replicated; R37)\n")
			}
			// Relays are an iroh-transport concept; the tailnet transport has none,
			// and iroh's live relay diagnostics are deferred (hardware-gated).
			if transport == config.TransportIroh {
				fmt.Fprintf(out, "relays:     unavailable — the iroh wire is deferred (see P3-PLAN.md)\n")
			} else {
				fmt.Fprintf(out, "relays:     n/a for %s (relays are an iroh feature; deferred, P3-4)\n", transport)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the raw status JSON instead of the summary")
	return cmd
}

// str renders a status field as a string, falling back when absent.
func str(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}
