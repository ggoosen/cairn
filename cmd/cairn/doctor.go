package main

import (
	"errors"
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
	cmd.AddCommand(&cobra.Command{
		Use:   "fork [origin-device-id]",
		Short: "Show detected equivocations (N8): common ancestor, per-branch events, advertising peer",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			forks := daemon.ReadForkRecords(dir)
			if len(args) == 1 {
				for _, f := range forks {
					if f.OriginDevice == args[0] {
						fmt.Fprint(cmd.OutOrStdout(), daemon.FormatForkDetail(f))
						if !f.Resolved {
							return fmt.Errorf("origin %s is forked and frozen", args[0])
						}
						return nil
					}
				}
				return fmt.Errorf("no fork recorded for origin %s", args[0])
			}
			if len(forks) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no forks detected")
				return nil
			}
			unresolved := 0
			for _, f := range forks {
				fmt.Fprint(cmd.OutOrStdout(), daemon.FormatForkDetail(f))
				if !f.Resolved {
					unresolved++
				}
			}
			if unresolved > 0 {
				return fmt.Errorf("%d unresolved fork(s)", unresolved)
			}
			return nil
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
			switch {
			case errors.Is(err, identity.ErrRestoredCopy):
				// R9: doctor is a read-only operation — permitted on restores
				if err := identity.EnsureEncrypted(dir, nil, cmd.ErrOrStderr()); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "note: restored copy (read-only); writes refused")
			case err != nil:
				return err
			default:
				if err := loaded.StartupCheck(nil, cmd.ErrOrStderr()); err != nil {
					return err
				}
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
