package main

// N5 CLI: the enrolment ceremony (offline; daemon stopped) and the
// membership probe. See DOGFOOD.md §11 for the operator runbook.

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/peer"
)

func newDeviceCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "device", Short: "Device membership: enrolment ceremony, revocation, listing"}

	var name, out string
	enroll := &cobra.Command{
		Use:   "enroll",
		Short: "NEW NODE: generate a device key and write the enrolment request (private key never leaves this machine)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req, err := identity.CreateEnrollRequest(name, out, time.Now())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"enrolment request %s written to %s (expires %s — R28: 1h, single-use)\n"+
					"carry it to the signing machine and run:  cairn device approve %s --root-key <restored-root-key> --grant grant.json\n",
				req.RequestID, out, req.ExpiresAt, out)
			return nil
		},
	}
	enroll.Flags().StringVar(&name, "name", "", "display name for the new device")
	enroll.Flags().StringVar(&out, "out", "enroll-request.json", "request file to write")
	cmd.AddCommand(enroll)

	var rootKey, grantOut string
	approve := &cobra.Command{
		Use:   "approve <request-file>",
		Short: "SIGNING MACHINE: root-sign the device.add and write the join grant (daemon must be stopped; restore the root key first, remove it after)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			_, err = identity.Approve(identity.ApproveOptions{
				Dir: dir, RequestPath: args[0], RootKeyPath: rootKey,
				GrantPath: grantOut, Out: cmd.OutOrStdout(),
			})
			return err
		},
	}
	approve.Flags().StringVar(&rootKey, "root-key", "", "path to the RESTORED root key (required)")
	approve.MarkFlagRequired("root-key")
	approve.Flags().StringVar(&grantOut, "grant", "join-grant.json", "grant file to write (carry back to the new node)")
	cmd.AddCommand(approve)

	join := &cobra.Command{
		Use:   "join <grant-file>",
		Short: "NEW NODE: verify the grant chain from genesis and install the approved identity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			return identity.Join(args[0], dir, cmd.OutOrStdout())
		},
	}
	cmd.AddCommand(join)

	var revokeKey, reason string
	revoke := &cobra.Command{
		Use:   "revoke <device-id>",
		Short: "Root-sign a device.revoke (offline; daemon stopped). Restart the daemon so the listener refuses the device.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			_, err = identity.RevokeDevice(dir, args[0], revokeKey, reason, nil, nil, cmd.OutOrStdout())
			return err
		},
	}
	revoke.Flags().StringVar(&revokeKey, "root-key", "", "path to the RESTORED root key (required)")
	revoke.MarkFlagRequired("root-key")
	revoke.Flags().StringVar(&reason, "reason", "", "recorded in the event")
	cmd.AddCommand(revoke)

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List mesh devices from the verified chain (member/revoked)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			trust, err := identity.MeshTrust(fsx.OS{}, dir)
			if err != nil {
				return err
			}
			for _, id := range trust.Devices() {
				state := "member"
				if trust.Revoked(id) {
					state = "REVOKED"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", id, state)
			}
			return nil
		},
	})

	groupGuard(cmd)
	return cmd
}

func newSyncCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "sync", Short: "Peer sync (N5: membership probe; N6 adds reconciliation)"}
	cmd.AddCommand(&cobra.Command{
		Use:   "ping <host:port>",
		Short: "Mutually authenticate with a peer's sync listener (proves BOTH memberships; no data flows)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			loaded, err := identity.Load(dir)
			if err != nil {
				return err
			}
			priv, err := identity.LoadKey(loaded.DeviceDir + "/" + config.DeviceKeyName)
			if err != nil {
				return err
			}
			// full members verify peers from their own log; a freshly joined
			// node (no replicated log until N6) uses the verified grant chain
			var trust peer.Trust
			if t, err := identity.MeshTrust(fsx.OS{}, dir); err == nil {
				trust = t
			} else if t, berr := identity.BootstrapTrust(loaded.DeviceDir); berr == nil {
				trust = t
			} else {
				return fmt.Errorf("no trust source: mesh logs unreadable (%v) and no join grant chain", err)
			}
			peerDevice, err := peer.Ping(args[0], peer.Identity{
				CairnID:  loaded.Portable.CairnID,
				DeviceID: loaded.Device.DeviceID,
				Priv:     priv,
			}, trust)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "authenticated: peer %s accepted this device, and proved it is enrolled device %s\n", args[0], peerDevice)
			return nil
		},
	})
	groupGuard(cmd)
	return cmd
}

// validateSyncListen is peer.ValidateListenAddr re-exported for tests.
func validateSyncListen(addr string) error { return peer.ValidateListenAddr(addr) }
