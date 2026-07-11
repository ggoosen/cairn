package main

import (
	"encoding/base64"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/identity"
)

func newIdentityCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "identity",
		Short: "Inspect this device's cairn identity",
	}
	cmd.AddCommand(newIdentityShowCmd(dirFlag))
	return cmd
}

func newIdentityShowCmd(dirFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show cairn, device, and key identity; verifies the genesis event",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			loaded, err := identity.Load(dir)
			if err != nil {
				return err
			}
			// Every start against an initialized cairn re-checks encryption;
			// the persisted override warns each time (rulings §9).
			if err := loaded.StartupCheck(nil, cmd.ErrOrStderr()); err != nil {
				return err
			}

			env, pl, err := loaded.GenesisRecord()
			if err != nil {
				return err
			}
			devicePub, err := pl.InitialDeviceCert.DevicePublicKey()
			if err != nil {
				return err
			}
			rootPub, err := base64.StdEncoding.DecodeString(pl.RootPubkey)
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "cairn_id:        %s\n", loaded.Portable.CairnID)
			fmt.Fprintf(out, "portable dir:    %s\n", loaded.Dir)
			fmt.Fprintf(out, "device state:    %s\n", loaded.DeviceDir)
			fmt.Fprintf(out, "device_id:       %s (generation %d)\n", loaded.Device.DeviceID, loaded.Device.OriginGeneration)
			if pl.InitialDeviceCert.DisplayName != "" {
				fmt.Fprintf(out, "display name:    %s\n", pl.InitialDeviceCert.DisplayName)
			}
			fmt.Fprintf(out, "device key id:   %s\n", event.KeyID(devicePub))
			fmt.Fprintf(out, "root key id:     %s\n", event.KeyID(rootPub))
			fmt.Fprintf(out, "genesis event:   %s (verified: envelope signature, event_id, root-signed cert)\n", env.EventID)
			if loaded.Device.AllowUnencrypted {
				fmt.Fprintf(out, "encryption:      OVERRIDDEN (--allow-unencrypted persisted device-local)\n")
			}
			return nil
		},
	}
}
