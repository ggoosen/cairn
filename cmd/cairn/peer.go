package main

// SYNC-C2 CLI: `cairn peer add|rm|list` — sync peer management. Until this
// existed, sync_peers was a hand-edited TOML field plus a daemon restart;
// now peers apply live (the anti-entropy sweep re-reads the list) and
// persist.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/daemon"
)

func newPeerCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "peer", Short: "Manage sync peers (live + persisted; no daemon restart needed)"}

	printPeers := func(cmd *cobra.Command, peers []string) {
		if len(peers) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no sync peers configured — nothing will replicate; add one with `cairn peer add <host:port>`")
			return
		}
		for _, p := range peers {
			fmt.Fprintln(cmd.OutOrStdout(), p)
		}
	}

	add := &cobra.Command{
		Use:   "add <host:port>",
		Short: "Add a sync peer (tailnet address); the next sweep dials it immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "peer-add", Peer: args[0]})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "peer %s added (live + persisted)\n", args[0])
			printPeers(cmd, resp.Peers)
			return nil
		},
	}
	cmd.AddCommand(add)

	rm := &cobra.Command{
		Use:   "rm <host:port>",
		Short: "Remove a sync peer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "peer-remove", Peer: args[0]})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "peer %s removed\n", args[0])
			printPeers(cmd, resp.Peers)
			return nil
		},
	}
	cmd.AddCommand(rm)

	list := &cobra.Command{
		Use:   "list",
		Short: "List configured sync peers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "peer-list"})
			if err != nil {
				return err
			}
			printPeers(cmd, resp.Peers)
			return nil
		},
	}
	cmd.AddCommand(list)

	groupGuard(cmd)
	return cmd
}
