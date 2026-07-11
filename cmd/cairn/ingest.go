package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/ingest"
	"github.com/ggoosen/cairn/internal/object"
)

func newIngestCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Import an existing knowledge base (docs tree / llm-wiki repo): scan → manifest → apply",
	}

	caller := func() (ingest.Caller, error) {
		dir, err := config.PortableDir(*dirFlag)
		if err != nil {
			return nil, err
		}
		loaded, err := identity.Load(dir)
		if err != nil {
			return nil, err
		}
		return func(req daemon.Request) (*daemon.Response, error) {
			return daemon.Call(loaded.DeviceDir, req)
		}, nil
	}

	var repo, manifestPath, class string
	scan := &cobra.Command{
		Use:   "scan <source-dir>",
		Short: "Scan a source tree against the live cairn and write the reviewable manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			call, err := caller()
			if err != nil {
				return err
			}
			root, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			m, err := ingest.Scan(call, root, repo, class, newUUID, timeNow())
			if err != nil {
				return err
			}
			if err := ingest.WriteManifest(manifestPath, m); err != nil {
				return err
			}
			counts := map[ingest.Action]int{}
			unresolved := 0
			for _, e := range m.Entries {
				counts[e.Action]++
				unresolved += len(e.Unresolved)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "manifest %s: %d publish, %d revise, %d skip (%d files, %d unresolved wiki links)\n",
				manifestPath, counts[ingest.ActionPublish], counts[ingest.ActionRevise], counts[ingest.ActionSkip], len(m.Entries), unresolved)
			fmt.Fprintln(cmd.OutOrStdout(), "review it, then run: cairn ingest apply --manifest", manifestPath)
			return nil
		},
	}
	scan.Flags().StringVar(&repo, "repo", "", "repo label (default: source dir base name)")
	scan.Flags().StringVar(&manifestPath, "manifest", "cairn-ingest-manifest.json", "manifest output path")
	scan.Flags().StringVar(&class, "class", object.ClassCanonical, "text class for imported messages (policy may downgrade)")

	var applyManifest string
	apply := &cobra.Command{
		Use:   "apply",
		Short: "Execute a scan manifest against the live cairn (idempotent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			call, err := caller()
			if err != nil {
				return err
			}
			m, err := ingest.ReadManifest(applyManifest)
			if err != nil {
				return err
			}
			rep, err := ingest.Apply(call, m, timeNow())
			if err != nil {
				return err
			}
			return printJSON(cmd, rep)
		},
	}
	apply.Flags().StringVar(&applyManifest, "manifest", "cairn-ingest-manifest.json", "manifest to apply")

	cmd.AddCommand(scan, apply)
	return cmd
}
