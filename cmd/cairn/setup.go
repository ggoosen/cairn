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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
			// Escape hatch for the one-command flow: install.sh / make deploy set
			// CAIRN_ALLOW_UNENCRYPTED=1 so a fresh Mac with FileVault off (the
			// default) doesn't dead-end at the volume gate. It is consulted ONLY
			// when the gate actually fails (below) — never pre-forced — so leaving
			// it exported (as install.sh suggests) does NOT persist an override on
			// an encrypted volume (Fable re-review NIT).
			envAllowUnencrypted := envTruthy(os.Getenv("CAIRN_ALLOW_UNENCRYPTED"))
			// Hints must work BEFORE cairn is on PATH — use the real binary path
			// and preserve any --dir the caller passed.
			self, _ := os.Executable()
			if self == "" {
				self = "cairn"
			}
			dirArg := ""
			if *dirFlag != "" {
				dirArg = " --dir " + *dirFlag
			}

			// Step 1 — mesh identity (idempotent: never re-init an existing mesh).
			fmt.Fprintln(out, "[1/3] Mesh identity")
			_, lerr := identity.Load(dir)
			switch {
			case lerr == nil:
				fmt.Fprintf(out, "  • already initialized at %s — keeping it\n", dir)
			case errors.Is(lerr, identity.ErrRestoredCopy):
				// Multi-machine / restored copy: portable data present, no device
				// identity here. The fix is adopt — NOT --allow-unencrypted.
				return fmt.Errorf("this machine has restored/second-machine Cairn data (portable dir at %s, but no device identity here).\n"+
					"  To take it over as a NEW origin on this device, run:\n    %s init --adopt%s\n"+
					"  (archives the old history, mints a fresh device identity, then re-run `cairn setup`)", dir, self, dirArg)
			default:
				opts := identity.InitOptions{
					Dir:              dir,
					AllowUnencrypted: allowUnencrypted,
					SyncListen:       "auto",
					Role:             config.RoleFull,
					Out:              out,
				}
				_, ierr := identity.Initialize(opts)
				// Only NOW, having actually hit the encryption gate, may the env
				// hatch retry — so an encrypted volume never persists an override
				// just because CAIRN_ALLOW_UNENCRYPTED is exported.
				if ierr != nil && !allowUnencrypted && envAllowUnencrypted && isUnencryptedGate(ierr) {
					fmt.Fprintln(out, "  • volume not encrypted; CAIRN_ALLOW_UNENCRYPTED is set — proceeding with the override")
					opts.AllowUnencrypted = true
					_, ierr = identity.Initialize(opts)
				}
				if ierr != nil {
					if !allowUnencrypted && isUnencryptedGate(ierr) {
						return fmt.Errorf("this volume isn't detected as encrypted (FileVault may be off).\n"+
							"  Recommended: turn on FileVault, then re-run. To proceed anyway right now:\n    %s setup --allow-unencrypted%s\n  (or set CAIRN_ALLOW_UNENCRYPTED=1)\n  (details: %v)", self, dirArg, ierr)
					}
					if strings.Contains(ierr.Error(), "restored copy") || strings.Contains(ierr.Error(), "init --adopt") {
						return fmt.Errorf("this looks like restored Cairn data.\n  Run:  %s init --adopt%s   then re-run `cairn setup`.\n  (details: %v)", self, dirArg, ierr)
					}
					return fmt.Errorf("creating the mesh: %w", ierr)
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

			// Summary + the few human next-steps we can't do for them. Use the
			// real binary in the "try it" line when its dir isn't on PATH yet, so
			// a copy-paste never hits "command not found".
			cairnCmd := "cairn"
			if !dirOnPath(self) {
				cairnCmd = self
				fmt.Fprintf(out, "\n(note: %s is not on your PATH — add it:\n  echo 'export PATH=\"%s:$PATH\"' >> ~/.zshrc && exec zsh)\n",
					binDir(self), binDir(self))
			}
			fmt.Fprintln(out, "\n✓ Cairn is set up. Next:")
			if !skipMCP {
				fmt.Fprintln(out, "  • Restart Claude Desktop / Claude Code so they load the cairn MCP tools.")
			}
			fmt.Fprintf(out, "  • Try it:  %s digest --view operator --budget 1500\n", cairnCmd)
			fmt.Fprintln(out, "  • Optional — let fresh agent sessions self-configure their relevance (R56):")
			fmt.Fprintf(out, "      %s onboarding publish --view <view> --interest \"what this view works on\" --budget 1500\n", cairnCmd)
			return nil
		},
	}
	cmd.Flags().BoolVar(&allowUnencrypted, "allow-unencrypted", false, "proceed on an unencrypted/unknown volume (persisted device-local; warns on every start)")
	cmd.Flags().BoolVar(&skipDaemon, "skip-daemon", false, "do not install the daemon service (you'll run `cairn daemon` yourself)")
	cmd.Flags().BoolVar(&skipMCP, "skip-mcp", false, "do not wire up MCP client apps")
	return cmd
}

// binDir is the directory holding the running binary.
func binDir(self string) string { return filepath.Dir(self) }

// dirOnPath reports whether the running binary's directory is on $PATH (so a
// bare `cairn` in printed next-steps will resolve).
func dirOnPath(self string) bool {
	d := filepath.Clean(binDir(self))
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(p) == d {
			return true
		}
	}
	return false
}

// isUnencryptedGate reports whether an init error is the encrypted-volume gate
// (the only failure the CAIRN_ALLOW_UNENCRYPTED hatch may retry).
func isUnencryptedGate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "--allow-unencrypted")
}

// envTruthy reads a permissive boolean env var (1/true/yes/on).
func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
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
