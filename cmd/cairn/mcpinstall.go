package main

// `cairn mcp-install` / `cairn mcp-uninstall` — P3 onboarding: wire Cairn's MCP
// server (`cairn mcp`) into MCP client apps (Claude Desktop, Claude Code)
// without hand-editing a config file. The command line binary is resolved via
// os.Executable() so the entry always points at the binary the operator ran.
//
// The logic lives in internal/mcpinstall (a registry of supported apps, with a
// regression-tested config-merge core). This file is only the CLI surface.
// RULINGS.md R54 governs the merge/refuse/backup invariants.

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/mcpinstall"
)

// resolveApps turns --app/--all flags into the target app set. With neither, it
// defaults to every app the registry reports as installed on this machine.
func resolveApps(env mcpinstall.Env, apps []string, all bool) ([]mcpinstall.App, error) {
	if all && len(apps) > 0 {
		return nil, fmt.Errorf("use either --all or --app, not both")
	}
	if len(apps) > 0 {
		out := make([]mcpinstall.App, 0, len(apps))
		for _, name := range apps {
			a, ok := mcpinstall.Lookup(name)
			if !ok {
				return nil, fmt.Errorf("unknown app %q (supported: claude-desktop, claude-code, codex)", name)
			}
			out = append(out, a)
		}
		return out, nil
	}
	// --all or default: every INSTALLED app (DEPLOY-E4: --all used to
	// bypass detection and create configs for apps that are not present,
	// e.g. a ~/.codex/config.toml with no Codex installed)
	_ = all // selection is identical either way; --all just skips the list default
	var out []mcpinstall.App
	for _, a := range mcpinstall.Registry() {
		if a.Inspect(env).Installed {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no supported MCP client apps detected; pass --app claude-desktop|claude-code|codex explicitly")
	}
	return out, nil
}

func mcpEnv() (mcpinstall.Env, error) {
	self, err := os.Executable()
	if err != nil {
		return mcpinstall.Env{}, fmt.Errorf("cannot resolve the running cairn binary: %w", err)
	}
	return mcpinstall.DefaultEnv(self)
}

func newMCPInstallCmd(_ *string) *cobra.Command {
	var apps []string
	var view string
	var all, list, status bool
	cmd := &cobra.Command{
		Use:   "mcp-install",
		Short: "Wire `cairn mcp` into Claude Desktop / Claude Code / Codex (merge-only; backs up first). No args → --list.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := mcpEnv()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			// --status: compact per-app table (detected / configured / path state)
			if status {
				return runMCPStatus(cmd, env)
			}
			// default (no --all/--app) with no explicit install intent → list
			if list || (!all && len(apps) == 0) {
				return runMCPList(cmd, env)
			}

			targets, err := resolveApps(env, apps, all)
			if err != nil {
				return err
			}
			if view != "" && len(targets) > 1 {
				return fmt.Errorf("--view names ONE view; %d apps are targeted (each app defaults to its own view — that is what keeps digests and telemetry attributable)", len(targets))
			}
			fmt.Fprintf(out, "cairn binary: %s\n\n", env.Self)
			var failures int
			for _, a := range targets {
				r, err := a.Install(env, view)
				if err != nil {
					failures++
					fmt.Fprintf(out, "✗ %s: %v\n", a.Name, err)
					continue
				}
				if !r.Changed {
					fmt.Fprintf(out, "• %s: %s\n", r.App, r.Message)
					continue
				}
				via := r.Method
				if r.Method == "cli" {
					via = "via CLI"
				}
				fmt.Fprintf(out, "✓ %s: %s (%s)\n", r.App, r.Message, via)
				if r.BackupPath != "" {
					fmt.Fprintf(out, "    backup: %s\n", r.BackupPath)
				}
				fmt.Fprintf(out, "    next:   %s\n", r.NextStep)
			}
			if failures > 0 {
				return fmt.Errorf("%d app(s) could not be configured (see above)", failures)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&apps, "app", nil, "target app(s): claude-desktop, claude-code, codex (repeatable)")
	cmd.Flags().BoolVar(&all, "all", false, "configure every installed supported app")
	cmd.Flags().StringVar(&view, "view", "", "override the agent view for a SINGLE targeted app (default: one view per app, named after it)")
	cmd.Flags().BoolVar(&list, "list", false, "detect apps and report state; change nothing (default with no flags)")
	cmd.Flags().BoolVar(&status, "status", false, "compact table: for every registry app — detected? configured? command current or stale?")
	return cmd
}

func newMCPUninstallCmd(_ *string) *cobra.Command {
	var apps []string
	var all bool
	cmd := &cobra.Command{
		Use:   "mcp-uninstall",
		Short: "Remove the cairn MCP server entry from Claude Desktop / Claude Code (backs up first; leaves everything else)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := mcpEnv()
			if err != nil {
				return err
			}
			targets, err := resolveApps(env, apps, all)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var failures int
			for _, a := range targets {
				r, err := a.Uninstall(env)
				if err != nil {
					failures++
					fmt.Fprintf(out, "✗ %s: %v\n", a.Name, err)
					continue
				}
				mark := "•"
				if r.Changed {
					mark = "✓"
				}
				fmt.Fprintf(out, "%s %s: %s\n", mark, r.App, r.Message)
				if r.BackupPath != "" {
					fmt.Fprintf(out, "    backup: %s\n", r.BackupPath)
				}
			}
			if failures > 0 {
				return fmt.Errorf("%d app(s) could not be updated (see above)", failures)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&apps, "app", nil, "target app(s): claude-desktop, claude-code, codex (repeatable)")
	cmd.Flags().BoolVar(&all, "all", false, "remove from every installed supported app")
	return cmd
}

func runMCPList(cmd *cobra.Command, env mcpinstall.Env) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "cairn binary: %s\n\n", env.Self)
	fmt.Fprintln(out, "supported MCP clients:")
	for _, a := range mcpinstall.Registry() {
		s := a.Inspect(env)
		if !s.Installed {
			fmt.Fprintf(out, "  %-14s not installed\n", s.App)
			continue
		}
		state := "not configured"
		switch {
		case s.Malformed:
			state = "MALFORMED config — `mcp-install` will back up and skip it"
		case s.Configured && s.Stale:
			state = fmt.Sprintf("configured but STALE (points at %s; run `cairn mcp-install --app %s` to fix)", s.CurrentCmd, s.App)
		case s.Configured:
			state = "configured (up to date)"
		}
		fmt.Fprintf(out, "  %-14s installed — %s\n", s.App, s.Detail)
		fmt.Fprintf(out, "  %-14s   config: %s\n", "", s.ConfigPath)
		fmt.Fprintf(out, "  %-14s   cairn:  %s\n", "", state)
	}
	fmt.Fprintln(out, "\nrun `cairn mcp-install --all` to configure every installed app, or --app <name> for one.")
	return nil
}

// runMCPStatus prints a compact one-line-per-app table answering, for every
// registry app: detected? configured? is the configured command current or
// stale? It mutates nothing (Inspect is read-only).
func runMCPStatus(cmd *cobra.Command, env mcpinstall.Env) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "cairn binary: %s\n\n", env.Self)
	fmt.Fprintf(out, "  %-14s %-9s %-12s %s\n", "APP", "DETECTED", "CONFIGURED", "COMMAND")
	for _, a := range mcpinstall.Registry() {
		s := a.Inspect(env)
		detected := "no"
		if s.Installed {
			detected = "yes"
		}
		configured := "no"
		command := "—"
		switch {
		case s.Malformed:
			configured = "malformed"
			command = "config not parseable; will back up & skip"
		case s.Configured && s.Stale:
			configured = "yes"
			command = fmt.Sprintf("STALE → %s (run `cairn mcp-install --app %s`)", s.CurrentCmd, s.App)
		case s.Configured:
			configured = "yes"
			command = "current"
		}
		fmt.Fprintf(out, "  %-14s %-9s %-12s %s\n", s.App, detected, configured, command)
	}
	return nil
}
