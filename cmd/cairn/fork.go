package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
)

// newForkCmd is the N8 fork-repair ceremony (spec §6.4). Offline: run with the
// daemon stopped, root key restored from offline storage.
func newForkCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "fork", Short: "Fork (equivocation) repair — spec §6.4 (offline; daemon stopped)"}
	var canonical, rootKey string
	resolve := &cobra.Command{
		Use:   "resolve <origin-device-id>",
		Short: "Resolve a detected fork: choose the canonical branch; reissue the losing branch under a recovery origin (both preserved)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			id, err := daemon.ResolveFork(daemon.ResolveForkOptions{
				Dir: dir, OriginDevice: args[0], Canonical: canonical,
				RootKeyPath: rootKey, Out: cmd.OutOrStdout(),
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "fork_resolution_id: %s\n", id)
			return nil
		},
	}
	resolve.Flags().StringVar(&canonical, "canonical", "local", "which branch is authoritative: local|remote")
	resolve.Flags().StringVar(&rootKey, "root-key", "", "path to the restored offline root key (required)")
	resolve.MarkFlagRequired("root-key")
	cmd.AddCommand(resolve)
	groupGuard(cmd)
	return cmd
}
