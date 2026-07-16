package main

// P3-2e CLI: `cairn pair invite` — the inviting node's half of the one-time
// pairing ceremony (operator RULING 2026-07-16, Option 1). Mint is an OFFLINE
// root ceremony (restore the root key, mint, remove it); it produces a single
// paste-able token the operator carries to the new node. `cairn pair join`
// (P3-2f) consumes it. See DOGFOOD.md for the operator runbook.

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/identity"
)

func newPairCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "pair", Short: "One-time pairing invitations (P3): frictionless onboarding"}

	var name, rootKey, out string
	invite := &cobra.Command{
		Use:   "invite",
		Short: "INVITING NODE (offline): mint a one-time pairing invitation (restore the root key first; remove it after)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			inv, err := identity.MintPairingInvitation(identity.MintPairingInvitationOptions{
				Dir: dir, RootKeyPath: rootKey, DisplayName: name, Now: time.Now, Out: cmd.OutOrStdout(),
			})
			if err != nil {
				return err
			}
			token, err := identity.EncodeInvitation(inv)
			if err != nil {
				return err
			}
			if out == "" {
				fmt.Fprintln(cmd.OutOrStdout(), token)
				return nil
			}
			if err := os.WriteFile(out, []byte(token+"\n"), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"one-time pairing token written to %s (expires in %s; treat it as a SECRET)\n"+
					"on the new node run:  cairn pair join %s <this-node-tailnet-addr:%d>\n"+
					"then REMOVE the restored root key\n",
				out, config.PairingInviteTTL, out, config.SyncDefaultPort)
			return nil
		},
	}
	invite.Flags().StringVar(&name, "name", "", "display name for the new device")
	invite.Flags().StringVar(&rootKey, "root-key", "", "path to the RESTORED root key (required)")
	invite.MarkFlagRequired("root-key")
	invite.Flags().StringVar(&out, "out", "pair-invite.token", `write the token to this file ("" = stdout)`)
	cmd.AddCommand(invite)

	groupGuard(cmd)
	return cmd
}
