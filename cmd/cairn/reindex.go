package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/projection"
)

func newReindexCmd(dirFlag *string) *cobra.Command {
	var lexical, semantic bool
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Rebuild the derived SQLite projection from the event log (side-build + atomic swap)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !lexical && !semantic {
				return errors.New("pass --lexical (and/or --semantic)")
			}
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
			if semantic {
				resp, err := call(dirFlag, daemon.Request{Op: "reindex-semantic"})
				if err != nil {
					return fmt.Errorf("reindex --semantic runs through the daemon: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "semantic reindex embedded %v revisions\n", resp.Status["embedded"])
			}
			if lexical {
				fsys := fsx.OS{}
				store := object.NewStore(fsys, dir)
				// nil verifier → two-pass mesh-trust replay (FIX-F2)
				report, err := projection.ReindexLexical(fsys, dir, projection.DBPath(dir),
					nil, projection.StoreBodyFetch(store))
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "lexical projection rebuilt at %s (%d events)\n", projection.DBPath(dir), report.Events)
				if report.Parked > 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "\nATTENTION: %d event(s) PARKED (failed to project). Run `cairn doctor` for details.\n", report.Parked)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&lexical, "lexical", false, "rebuild the lexical projection (fast; product stays usable)")
	cmd.Flags().BoolVar(&semantic, "semantic", false, "rebuild embeddings (stub until M6)")
	return cmd
}
