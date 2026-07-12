package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/projection"
)

func newDoctorCmd(dirFlag *string) *cobra.Command {
	cmd := doctorMain(dirFlag)
	cmd.AddCommand(&cobra.Command{
		Use:   "conflicts",
		Short: "List unresolved merge-conflict directories (conflict debt)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			dirs, err := daemon.UnresolvedConflicts(fsx.OS{}, dir)
			if err != nil {
				return err
			}
			if len(dirs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no unresolved conflicts")
				return nil
			}
			for _, d := range dirs {
				fmt.Fprintln(cmd.OutOrStdout(), d)
			}
			return fmt.Errorf("%d unresolved conflict(s)", len(dirs))
		},
	})
	return cmd
}

func doctorMain(dirFlag *string) *cobra.Command {
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
			// log summary first (frames/chains/seals per origin)
			trust, err := identity.MeshTrust(fsx.OS{}, dir)
			if err != nil {
				return fmt.Errorf("mesh trust unresolved: %w", err)
			}
			report, err := cairnlog.Doctor(fsx.OS{}, dir, trust.Verifier())
			if err != nil {
				return err
			}
			report.Summary(cmd.OutOrStdout())

			// FIX-F3: deepened checks — projectability (parked events,
			// checkpoint drift), object presence, cross-origin trust
			problems, infos, err := daemon.DeepDoctor(fsx.OS{}, dir, projection.DBPath(dir), time.Now())
			if err != nil {
				return err
			}
			for _, i := range infos {
				fmt.Fprintf(cmd.OutOrStdout(), "  note: %s\n", i)
			}
			for _, p := range problems {
				fmt.Fprintf(cmd.OutOrStdout(), "  PROBLEM %s\n", p)
			}
			if len(problems) > 0 {
				return fmt.Errorf("doctor found %d problem(s)", len(problems))
			}
			fmt.Fprintln(cmd.OutOrStdout(), "deep doctor: clean (log, projection, objects, trust)")
			return nil
		},
	}
}
