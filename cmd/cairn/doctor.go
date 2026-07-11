package main

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

func newDoctorCmd(dirFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Walk the event log, verify frames, hashes, signatures, chains, and seal headers; report problems (never repairs)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			loaded, err := identity.Load(dir)
			if err != nil {
				return err
			}
			if err := loaded.StartupCheck(nil, cmd.ErrOrStderr()); err != nil {
				return err
			}
			report, err := cairnlog.Doctor(fsx.OS{}, dir, identity.NewChainVerifier().Verify)
			if err != nil {
				return err
			}
			report.Summary(cmd.OutOrStdout())
			if !report.Clean() {
				return errors.New("doctor found problems")
			}
			return nil
		},
	}
}
