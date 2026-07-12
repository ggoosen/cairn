package main

// `cairn mcp` — the N1 MCP server: bridges stdio JSON-RPC to the running
// daemon's unix socket. Configure Claude Desktop / Claude Code with
// command "cairn", args ["mcp", "--view", "<agent-view>"].
//
// N2 (RULINGS.md R21): MCP is NEVER tier-1. Every request runs under a
// capability session — either the CAIRN_SESSION handle it was launched
// with (`cairn run ... -- cairn mcp`), or one the server mints at startup
// from --profile (default agent-standard; "full" is refused) and revokes
// on exit.

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/mcp"
)

func newMCPCmd(dirFlag *string) *cobra.Command {
	var view, actor, profile string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the nine §5.5 tools to an MCP client over stdio (requires a running daemon; never tier-1)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if profile == "full" {
				return errors.New("MCP is never tier-1 (RULINGS.md R21): --profile full is refused")
			}
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			clientDir, err := daemon.ClientDir(dir)
			if err != nil {
				return err
			}
			// fail fast with a readable error if no daemon is up, instead of
			// erroring per tool call after the MCP handshake succeeded
			if _, err := daemon.Call(clientDir, daemon.Request{Op: "status"}); err != nil {
				return err
			}

			token := os.Getenv(config.SessionEnvVar)
			if token == "" {
				// mint our own session from the configured profile (R21:
				// "a handle or a profile named in server config")
				resp, err := daemon.Call(clientDir, daemon.Request{
					Op: "session-create", SessionProfile: profile,
					SessionName: actor, SessionPID: os.Getpid(),
				})
				if err != nil {
					return err
				}
				token = resp.Status["session"].(string)
				defer daemon.Call(clientDir, daemon.Request{Op: "session-revoke", TargetSession: token})
			}

			caller := func(req daemon.Request) (*daemon.Response, error) {
				req.Session = token
				return daemon.Call(clientDir, req)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "cairn mcp: serving view %q as actor %q (profile %s, session %s…)\n",
				view, actor, profile, token[:8])
			return mcp.New(caller, actor, view, version).ServeStdio(cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&view, "view", "mcp", "agent view this MCP surface reads and fetches as")
	cmd.Flags().StringVar(&actor, "actor", "mcp", "principal recorded on writes and telemetry")
	cmd.Flags().StringVar(&profile, "profile", "agent-standard", "capability profile when not launched with CAIRN_SESSION (never \"full\", R21)")
	return cmd
}
