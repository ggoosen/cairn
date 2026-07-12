package main

// `cairn mcp` — the N1 MCP server: bridges stdio JSON-RPC to the running
// daemon's unix socket. Configure Claude Desktop / Claude Code with
// command "cairn", args ["mcp", "--view", "<agent-view>"].

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/mcp"
)

func newMCPCmd(dirFlag *string) *cobra.Command {
	var view, actor string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the nine §5.5 tools to an MCP client over stdio (requires a running daemon)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			caller := func(req daemon.Request) (*daemon.Response, error) {
				return daemon.Call(clientDir, req)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "cairn mcp: serving view %q as actor %q\n", view, actor)
			return mcp.New(caller, actor, view, version).ServeStdio(cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&view, "view", "mcp", "agent view this MCP surface reads and fetches as")
	cmd.Flags().StringVar(&actor, "actor", "mcp", "principal recorded on writes and telemetry")
	return cmd
}
