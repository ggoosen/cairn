package main

// P3-2e CLI: `cairn pair invite` — the inviting node's half of the one-time
// pairing ceremony (operator RULING 2026-07-16, Option 1). Mint is an OFFLINE
// root ceremony (restore the root key, mint, remove it); it produces a single
// paste-able token the operator carries to the new node. `cairn pair join`
// (P3-2f) consumes it. See DOGFOOD.md for the operator runbook.

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/peer"
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

	var joinAllowUnenc bool
	join := &cobra.Command{
		Use:   "join <token-or-file> <peer-host:port>",
		Short: "NEW NODE: verify a pairing invitation, install this device's identity, and pair over the network",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// arg0 may be the token string OR a file containing it
			token := args[0]
			if blob, err := os.ReadFile(args[0]); err == nil {
				token = string(blob)
			}
			inv, err := identity.DecodeInvitation(token)
			if err != nil {
				return err
			}
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			priv, err := identity.PairJoinInstall(identity.PairJoinOptions{
				Invitation: inv, Dir: dir, AllowUnencrypted: joinAllowUnenc,
				Now: time.Now, Out: cmd.OutOrStdout(),
			})
			if err != nil {
				return err
			}
			// pair over the wire: the inviting node appends our device.add. The
			// payload carries only {cert, invite_id} — the private key stays here.
			payload, err := json.Marshal(map[string]any{"cert": inv.Cert, "invite_id": inv.InviteID})
			if err != nil {
				return err
			}
			evID, err := peer.PairDial(args[1], inv.CairnID, payload, priv)
			if err != nil {
				return fmt.Errorf("identity installed, but the pairing handshake failed: %w\n"+
					"(the invitation may have expired or already been used; if it succeeded once, just start the daemon to sync — re-running join is safe)", err)
			}
			// SYNC-C3: persist the counterparty as a sync peer — we just proved
			// it answers at args[1]. Without this, pairing succeeded but NOTHING
			// replicated until the operator hand-edited sync_peers. Reconciles
			// are bidirectional per connection, so this one entry converges
			// both nodes.
			if err := identity.AddSyncPeer(dir, args[1]); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"WARNING: could not persist %s as a sync peer (%v) — add it with `cairn peer add %s` after starting the daemon\n",
					args[1], err, args[1])
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"paired! admitted by device.add %s.\npeer %s registered for sync.\nstart the daemon to sync the mesh log:  cairn daemon\n", evID, args[1])
			return nil
		},
	}
	join.Flags().BoolVar(&joinAllowUnenc, "allow-unencrypted", false,
		"operator override: install the device key on an unencrypted/unknown volume (persisted device-local; warns on every start)")
	cmd.AddCommand(join)

	groupGuard(cmd)
	return cmd
}
