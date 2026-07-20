package main

// `cairn setup` — the one-command onboarding for a new machine. It collapses the
// five-step dance (init → daemon --install → mcp-install per client → verify)
// into a single guided, IDEMPOTENT command so a non-expert can go from a fresh
// binary to a running, MCP-wired mesh without knowing about genesis, launchd, or
// per-client config files.
//
// It only ORCHESTRATES existing, individually-tested commands — it adds no new
// mesh behaviour:
//   - identity.Initialize (skipped if a mesh already exists here)
//   - installService       (launchd/systemd; points the service at THIS binary)
//   - mcpinstall per detected client (merge-only; missing clients are fine)
//
// Because installService registers os.Executable(), running `cairn setup` from
// ~/.local/bin/cairn puts the daemon on the user path with no sudo — the
// simplest correct deployment.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/mcpinstall"
)

func newSetupCmd(dirFlag *string) *cobra.Command {
	var allowUnencrypted, skipDaemon, skipMCP bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "One command: create the mesh (if needed), install the daemon service, and wire up MCP clients",
		Long: "Guided, idempotent onboarding. Creates the mesh if this machine has none, installs the\n" +
			"resident daemon as a user service (launchd/systemd), and registers `cairn mcp` with every\n" +
			"detected MCP client (Claude Desktop / Claude Code / Codex). Safe to re-run after upgrades —\n" +
			"it reinstalls the service pointing at the current binary and re-merges client configs.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}

			// Step 1 — mesh identity (idempotent: never re-init an existing mesh).
			fmt.Fprintln(out, "[1/3] Mesh identity")
			if _, lerr := identity.Load(dir); lerr == nil {
				fmt.Fprintf(out, "  • already initialized at %s — keeping it\n", dir)
			} else {
				opts := identity.InitOptions{
					Dir:              dir,
					AllowUnencrypted: allowUnencrypted,
					SyncListen:       "auto",
					Role:             config.RoleFull,
					Out:              out,
				}
				if _, err := identity.Initialize(opts); err != nil {
					return fmt.Errorf("creating the mesh: %w\n(if this is an unencrypted/unknown volume, re-run: cairn setup --allow-unencrypted)", err)
				}
				fmt.Fprintf(out, "  ✓ created at %s\n", dir)
			}

			// Step 2 — daemon service (points at THIS binary; reload is idempotent).
			if skipDaemon {
				fmt.Fprintln(out, "[2/3] Daemon service — skipped (--skip-daemon)")
			} else {
				fmt.Fprintln(out, "[2/3] Daemon service")
				if err := installService(dir, out); err != nil {
					return fmt.Errorf("installing the daemon service: %w", err)
				}
			}

			// Step 3 — MCP clients (best-effort: absent clients are not an error).
			if skipMCP {
				fmt.Fprintln(out, "[3/3] MCP clients — skipped (--skip-mcp)")
			} else {
				fmt.Fprintln(out, "[3/3] MCP clients")
				setupWireMCP(cmd)
			}

			// Summary + the few human next-steps we can't do for them.
			fmt.Fprintln(out, "\n✓ Cairn is set up. Next:")
			if !skipMCP {
				fmt.Fprintln(out, "  • Restart Claude Desktop / Claude Code so they load the cairn MCP tools.")
			}
			fmt.Fprintln(out, "  • Try it:  cairn digest --view operator --budget 1500")
			fmt.Fprintln(out, "  • Optional — let fresh agent sessions self-configure their relevance (R56):")
			fmt.Fprintln(out, "      cairn onboarding publish --view <view> --interest \"what this view works on\" --budget 1500")
			return nil
		},
	}
	cmd.Flags().BoolVar(&allowUnencrypted, "allow-unencrypted", false, "proceed on an unencrypted/unknown volume (persisted device-local; warns on every start)")
	cmd.Flags().BoolVar(&skipDaemon, "skip-daemon", false, "do not install the daemon service (you'll run `cairn daemon` yourself)")
	cmd.Flags().BoolVar(&skipMCP, "skip-mcp", false, "do not wire up MCP client apps")
	return cmd
}

// setupWireMCP registers `cairn mcp` with every detected client, printing a line
// per app. A missing client is reported, not fatal — a layman may not have one
// installed yet, and can re-run `cairn setup` after installing it.
func setupWireMCP(cmd *cobra.Command) {
	out := cmd.OutOrStdout()
	env, err := mcpEnv()
	if err != nil {
		fmt.Fprintf(out, "  • skipped: %v\n", err)
		return
	}
	any := false
	for _, a := range mcpinstall.Registry() {
		if !a.Inspect(env).Installed {
			continue
		}
		any = true
		r, err := a.Install(env)
		if err != nil {
			fmt.Fprintf(out, "  ✗ %s: %v\n", a.Name, err)
			continue
		}
		if r.Changed {
			fmt.Fprintf(out, "  ✓ %s: %s\n", r.App, r.Message)
		} else {
			fmt.Fprintf(out, "  • %s: %s\n", r.App, r.Message)
		}
	}
	if !any {
		fmt.Fprintln(out, "  • no MCP client apps detected (Claude Desktop / Claude Code / Codex).")
		fmt.Fprintln(out, "    Install one, then re-run `cairn setup` to wire it up.")
	}
}
