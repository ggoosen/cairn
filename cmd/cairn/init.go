package main

import (
	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/identity"
)

func newInitCmd(dirFlag *string) *cobra.Command {
	var (
		allowUnencrypted bool
		displayName      string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a new cairn: portable directory, device identity, and the genesis event",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			_, err = identity.Initialize(identity.InitOptions{
				Dir:              dir,
				DisplayName:      displayName,
				AllowUnencrypted: allowUnencrypted,
				Out:              cmd.OutOrStdout(),
			})
			return err
		},
	}
	cmd.Flags().BoolVar(&allowUnencrypted, "allow-unencrypted", false,
		"operator override: proceed on an unencrypted/unknown volume (persisted device-local; warns on every start)")
	cmd.Flags().StringVar(&displayName, "display-name", "", "display name for this device's certificate")
	return cmd
}
