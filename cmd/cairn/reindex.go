package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
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
				if err := projection.ReindexSemantic(); err != nil {
					return err
				}
			}
			if lexical {
				fsys := fsx.OS{}
				store := object.NewStore(fsys, dir)
				if err := projection.ReindexLexical(fsys, dir, projection.DBPath(dir),
					func() cairnlog.VerifyFunc { return identity.NewChainVerifier().Verify },
					projection.StoreBodyFetch(store)); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "lexical projection rebuilt at %s\n", projection.DBPath(dir))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&lexical, "lexical", false, "rebuild the lexical projection (fast; product stays usable)")
	cmd.Flags().BoolVar(&semantic, "semantic", false, "rebuild embeddings (stub until M6)")
	return cmd
}
