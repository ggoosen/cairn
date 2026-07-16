// Command cairn is the single binary for Cairn P0: daemon + CLI subcommands.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version derives from build info (RULINGS.md R11): milestone status + VCS
// rev. G7.5: P0 complete + P1 through the pre-N9 fixes → "p1".
var version = func() string {
	v := "p1"
	if info, ok := debug.ReadBuildInfo(); ok {
		rev, dirty := "", false
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if len(rev) >= 12 {
			v += "-" + rev[:12]
		} else {
			v += "-dev"
		}
		if dirty {
			v += "-dirty"
		}
	} else {
		v += "-dev"
	}
	return v
}()

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var dir string
	var showVersion bool

	root := &cobra.Command{
		Use:   "cairn",
		Short: "Cairn — local-first, crash-safe message and knowledge daemon for AI agent sessions",
		// FIX-H7: handle --version ourselves (not cobra's built-in flag) so we can
		// warn when a DIFFERENT daemon is running — cobra short-circuits its
		// version flag before any hook could run.
		RunE: func(cmd *cobra.Command, _ []string) error {
			if showVersion {
				fmt.Fprintln(cmd.OutOrStdout(), version)
				warnIfStaleDaemon(dir, version, cmd.ErrOrStderr())
				return nil
			}
			return cmd.Help()
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&dir, "dir", "", "path to the portable cairn directory (default ~/cairn, or $CAIRN_DIR)")
	root.Flags().BoolVar(&showVersion, "version", false, "print the build version (warns if a different daemon is running)")

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
	root.AddCommand(newDigestCmd(&dir))
	root.AddCommand(newWhyRankedCmd(&dir))
	root.AddCommand(newOutcomeCmd(&dir, "found", "found", "Record a successful retrieval outcome"))
	root.AddCommand(newOutcomeCmd(&dir, "not-found", "not_found", "Record a failed retrieval outcome"))
	root.AddCommand(newOutcomeCmd(&dir, "manual-workaround", "manual_workaround", "Record that a manual workaround was used"))
	root.AddCommand(newGatesCmd(&dir))
	root.AddCommand(newReserveCmd(&dir))
	root.AddCommand(newSetupAgentCmd(&dir))
	root.AddCommand(newIngestCmd(&dir))
	root.AddCommand(newHousekeepCmd(&dir))
	root.AddCommand(newBenchCmd(&dir))
	root.AddCommand(newMCPCmd(&dir))
	root.AddCommand(newMCPInstallCmd(&dir))
	root.AddCommand(newMCPUninstallCmd(&dir))
	root.AddCommand(newRunCmd(&dir))
	root.AddCommand(newSessionCmd(&dir))
	root.AddCommand(newSubscribeCmd(&dir))
	root.AddCommand(newSubscriptionCmd(&dir))
	root.AddCommand(newDerivativeCmd(&dir))
	root.AddCommand(newDeviceCmd(&dir))
	root.AddCommand(newPairCmd(&dir))
	root.AddCommand(newSyncCmd(&dir))
	root.AddCommand(newNetCmd(&dir))
	root.AddCommand(newForkCmd(&dir))
	root.AddCommand(newSavedCmd(&dir))
	root.AddCommand(newRankStatsCmd(&dir))
	root.AddCommand(newMapCmd(&dir))
	root.AddCommand(newCompactCmd(&dir))
	root.AddCommand(newStatusCmd(&dir))
	return root
}
