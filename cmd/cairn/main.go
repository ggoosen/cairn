// Command cairn is the single binary for Cairn P0: daemon + CLI subcommands.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// releaseVersion is the release tag, STAMPED AT LINK TIME by the release
// workflow:
//
//	go build -ldflags "-X main.releaseVersion=v0.3.0" ./cmd/cairn
//
// (see the Makefile's CAIRN_VERSION). It is empty in every other build, and
// that emptiness is the point: D13 asks that a released artifact name its tag,
// and equally that a development build never claim one it does not have. The
// only way to set it is to link it in, so a binary that prints a tag was built
// by something that knew the tag.
var releaseVersion string

// version is what `cairn --version`, `cairn status` and the FIX-H7
// stale-daemon check all report. See buildVersion for the shape.
var version = func() string {
	rev, dirty, ok := vcsStamp()
	return buildVersion(releaseVersion, rev, dirty, ok)
}()

// vcsStamp reads the VCS facts Go embeds in the binary (RULINGS.md R11):
// the commit and whether the tree it was built from was modified. ok is false
// for a build with no build info at all (`go run` of a bare file, some test
// harnesses) — which is itself a fact worth reporting rather than papering
// over.
func vcsStamp() (rev string, dirty, ok bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false, false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	return rev, dirty, true
}

// buildVersion composes the version string from the linked-in release tag (if
// any) and the VCS stamp. Kept pure so the composition is testable without
// building four binaries.
//
// Milestone + commit is the DEVELOPMENT shape and is unchanged (RULINGS.md
// R11, FIX-G7.5): "p1-<12 hex>", plus "-dirty" when the tree was modified, or
// "p1-dev" when there is no stamp at all.
//
// D13 prefixes the release tag when one was linked in: "v0.3.0 (p1-<commit>)".
// The tag leads because that is the fact a user reporting a bug can read off
// their install, but the commit stays because it is the fact the author needs
// to triage it — and because keeping the whole development stamp inside the
// parentheses means "-dirty" and "-dev" remain visible to the release
// workflow's guard, which refuses to publish an artifact carrying either. A
// tag next to the word "dirty" is a self-refuting claim; a tag that had
// silently swallowed it would not be.
func buildVersion(release, rev string, dirty, haveInfo bool) string {
	v := "p1"
	switch {
	case !haveInfo, len(rev) < 12:
		v += "-dev"
	default:
		v += "-" + rev[:12]
	}
	if dirty {
		v += "-dirty"
	}
	if release != "" {
		return release + " (" + v + ")"
	}
	return v
}

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
	root.AddCommand(newUnlinkCmd(&dir))
	root.AddCommand(newPinCmd(&dir))
	root.AddCommand(newUnpinCmd(&dir))
	root.AddCommand(newThreadCmd(&dir))
	root.AddCommand(newInteractionsCmd(&dir))
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
	root.AddCommand(newSetupCmd(&dir))
	root.AddCommand(newSubscribeCmd(&dir))
	root.AddCommand(newOnboardingCmd(&dir))
	root.AddCommand(newSubscriptionCmd(&dir))
	root.AddCommand(newDerivativeCmd(&dir))
	root.AddCommand(newDeviceCmd(&dir))
	root.AddCommand(newPairCmd(&dir))
	root.AddCommand(newPeerCmd(&dir))
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
