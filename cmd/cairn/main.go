// Command cairn is the single binary for Cairn P0: daemon + CLI subcommands.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var version = "0.0.1-m0"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var dir string

	root := &cobra.Command{
		Use:           "cairn",
		Short:         "Cairn — local-first, crash-safe message and knowledge daemon for AI agent sessions",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&dir, "dir", "", "path to the portable cairn directory (default ~/cairn, or $CAIRN_DIR)")

	root.AddCommand(newInitCmd(&dir))
	root.AddCommand(newIdentityCmd(&dir))
	root.AddCommand(newDoctorCmd(&dir))
	root.AddCommand(newReindexCmd(&dir))
	root.AddCommand(newDaemonCmd(&dir))
	root.AddCommand(newSendCmd(&dir))
	root.AddCommand(newReplyCmd(&dir))
	root.AddCommand(newRetractCmd(&dir))
	root.AddCommand(newTopicCmd(&dir))
	root.AddCommand(newLinkCmd(&dir))
	root.AddCommand(newPinCmd(&dir))
	root.AddCommand(newSignalCmd(&dir))
	root.AddCommand(newSearchCmd(&dir))
	root.AddCommand(newPeekCmd(&dir))
	root.AddCommand(newFetchCmd(&dir))
	root.AddCommand(newMigrateCmd(&dir))
	root.AddCommand(newExportCmd(&dir))
	root.AddCommand(newResolveCmd(&dir))
	// verbs whose semantics land in later milestones (honest stubs)
	root.AddCommand(stub("digest", "Ranked, budget-capped digest", "M6"))
	root.AddCommand(stub("why-ranked", "Explain a ranking decision", "M6"))
	root.AddCommand(stub("found", "Record a successful retrieval outcome", "M7"))
	root.AddCommand(stub("not-found", "Record a failed retrieval outcome", "M7"))
	root.AddCommand(stub("manual-workaround", "Record a manual workaround outcome", "M7"))
	return root
}
