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
	return root
}
