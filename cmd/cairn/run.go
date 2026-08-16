package main

// `cairn run` — the N2 trusted launcher: mints a short-TTL, non-delegable
// session handle bound to the launched process, exports it as CAIRN_SESSION,
// and revokes it when the process exits (the daemon's idle window is the
// backstop if the launcher dies without cleanup).
//
// Isolation honesty (RULINGS.md R22): the child runs as the SAME OS user.
// The profile prevents accidents — a confined agent can't retract or
// restructure the mesh — it does not defend against a malicious local
// process, which could talk to the daemon socket directly.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
)

func newRunCmd(dirFlag *string) *cobra.Command {
	var profile, name string
	cmd := &cobra.Command{
		Use:   "run --profile <name> -- <command> [args...]",
		Short: "Run a command under a capability-confined session (CAIRN_SESSION exported; auto-revoked on exit/idle)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv(config.SessionEnvVar) != "" {
				return errors.New("already inside a cairn session — handles are non-delegable (R23)")
			}
			sessName := name
			if sessName == "" {
				sessName = profile
			}
			resp, err := call(dirFlag, daemon.Request{
				Op: "session-create", SessionProfile: profile,
				SessionName: sessName, SessionPID: os.Getpid(),
			})
			if err != nil {
				return err
			}
			token := resp.Status["session"].(string)
			fmt.Fprintf(cmd.ErrOrStderr(), "cairn run: session %s… (%s as %q, expires %s)\n",
				token[:8], profile, resp.Status["principal"], resp.Status["expires_at"])

			child := exec.Command(args[0], args[1:]...)
			child.Stdin = cmd.InOrStdin()
			child.Stdout = cmd.OutOrStdout()
			child.Stderr = cmd.ErrOrStderr()
			child.Env = append(os.Environ(), config.SessionEnvVar+"="+token)
			runErr := child.Run()

			if _, err := call(dirFlag, daemon.Request{Op: "session-revoke", TargetSession: token}); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "cairn run: WARNING: session revoke failed (%v); the daemon's idle timeout is the backstop\n", err)
			}
			return runErr
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "agent-standard", "capability profile: full | agent-standard | read-only | a profiles.toml entry")
	cmd.Flags().StringVar(&name, "name", "", "leaf principal the session acts as (default: the profile name)")
	return cmd
}

// `cairn session list|prune|revoke` — operator visibility into live handles.
func newSessionCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "session", Short: "Inspect, prune and revoke capability sessions (operator tier only)"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List live sessions (token prefixes only — handles stay opaque)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "session-list"})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Status["sessions"])
		},
	})
	// D9: the daemon sweeps on load, on mint and on list, so a healthy node
	// never needs this. It exists for the backlog an OLDER daemon left behind
	// (2,673 records on the dev node) and to give the operator a way to see,
	// on demand, what the sweep considers dead.
	cmd.AddCommand(&cobra.Command{
		Use:   "prune",
		Short: "Reap expired and dead-process sessions now and compact the store",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "session-prune"})
			if err != nil {
				return err
			}
			removed, _ := resp.Status["removed"].(float64)
			remaining, _ := resp.Status["remaining"].(float64)
			fmt.Fprintf(cmd.OutOrStdout(), "pruned %d session(s); %d remain\n", int(removed), int(remaining))
			if by, ok := resp.Status["by_reason"].(map[string]any); ok && len(by) > 0 {
				for _, reason := range sortedKeys(by) {
					n, _ := by[reason].(float64)
					fmt.Fprintf(cmd.OutOrStdout(), "  %-18s %d\n", reason, int(n))
				}
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "revoke <token>",
		Short: "Revoke a session handle immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := call(dirFlag, daemon.Request{Op: "session-revoke", TargetSession: args[0]}); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "revoked")
			return nil
		},
	})
	groupGuard(cmd)
	return cmd
}

// sortedKeys keeps a map-derived report deterministic (a prune summary that
// reorders itself between runs is harder to diff than it needs to be).
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
