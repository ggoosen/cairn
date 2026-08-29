package main

// `cairn skills-install` / `cairn skills-uninstall` — D18: install Cairn's four
// agent skills (recall, remember, recap, handoff) into the harnesses that read
// skills from disk, so a fresh agent DISCOVERS the verbs instead of needing a
// human to paste prose into CLAUDE.md.
//
// The shape deliberately mirrors `cairn mcp-install`: a registry of targets, a
// per-view install, idempotent, backed up before any overwrite, and reversible.
// The logic lives in internal/skillsinstall; this file is only the CLI.
//
// A skill carries no capability (RULINGS.md R21) — it is instructions naming a
// verb, run by the agent's own process under whatever CAIRN_SESSION that
// process already holds. Installing skills into a read-only session's harness
// does not make it able to write; the daemon refuses the send exactly as it
// would have if the agent had typed the command itself.

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/skillsinstall"
)

func skillsEnv() (skillsinstall.Env, error) {
	self, err := os.Executable()
	if err != nil {
		return skillsinstall.Env{}, fmt.Errorf("cannot resolve the running cairn binary: %w", err)
	}
	return skillsinstall.DefaultEnv(self)
}

// resolveTargets turns --target/--all into the harness set, defaulting to
// every harness actually detected on this machine (never creating a skills
// directory for a harness that is not installed — the DEPLOY-E4 lesson).
func resolveTargets(env skillsinstall.Env, names []string, all bool) ([]skillsinstall.Target, error) {
	if all && len(names) > 0 {
		return nil, fmt.Errorf("use either --all or --target, not both")
	}
	if len(names) > 0 {
		out := make([]skillsinstall.Target, 0, len(names))
		for _, n := range names {
			t, ok := skillsinstall.Lookup(n)
			if !ok {
				return nil, fmt.Errorf("unknown target %q (supported: %s)", n, skillsinstall.TargetNames())
			}
			out = append(out, t)
		}
		return out, nil
	}
	var out []skillsinstall.Target
	for _, t := range skillsinstall.Registry() {
		if t.Inspect(env, "").Installed {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no supported harness detected; pass --target %s explicitly", skillsinstall.TargetNames())
	}
	return out, nil
}

func newSkillsInstallCmd(_ *string) *cobra.Command {
	var targets []string
	var view string
	var all, status bool
	cmd := &cobra.Command{
		Use:   "skills-install",
		Short: "Install the cairn skills (recall, remember, recap, handoff) into Claude Code / Codex. No args → --status.",
		Long: "Install Cairn's four agent skills into the harnesses that read skills from disk.\n\n" +
			"A skill is a wrapper over a verb that already exists — it grants NOTHING (R21). The\n" +
			"commands inside run under whatever capability session the agent's process carries, so\n" +
			"a read-only session gets the daemon's refusal when it tries to write through a skill.\n\n" +
			"Installation is per-view, idempotent, backed up before any overwrite, and removable\n" +
			"with `cairn skills-uninstall`. A file cairn did not write is never overwritten.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := skillsEnv()
			if err != nil {
				return err
			}
			if status || (!all && len(targets) == 0) {
				return runSkillsStatus(cmd, env, view)
			}
			sel, err := resolveTargets(env, targets, all)
			if err != nil {
				return err
			}
			if view != "" && len(sel) > 1 {
				return fmt.Errorf("--view names ONE view; %d targets are selected (each target defaults to its own view, which is what keeps digests and telemetry attributable)", len(sel))
			}
			out := cmd.OutOrStdout()
			var failures int
			for _, t := range sel {
				tr, terr := t.Install(env, view)
				fmt.Fprintf(out, "%s (%s)\n", tr.Target, tr.Dir)
				for _, r := range tr.Results {
					mark := "•"
					if r.Changed {
						mark = "✓"
					}
					fmt.Fprintf(out, "  %s %-16s %s\n", mark, r.Unit, r.Message)
					if r.BackupPath != "" {
						fmt.Fprintf(out, "      backup: %s\n", r.BackupPath)
					}
				}
				if terr != nil {
					failures++
					fmt.Fprintf(out, "  ✗ %v\n", terr)
					continue
				}
				fmt.Fprintf(out, "  next:   %s\n", tr.NextStep)
			}
			if failures > 0 {
				return fmt.Errorf("%d target(s) had skills that could not be written (see above)", failures)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&targets, "target", nil, "harness(es) to install into (repeatable)")
	cmd.Flags().BoolVar(&all, "all", false, "install into every detected harness")
	cmd.Flags().StringVar(&view, "view", "", "the cairn view the skills name (default: one view per target, named after it)")
	cmd.Flags().BoolVar(&status, "status", false, "report what is installed and whether it is current; change nothing (default with no flags)")
	return cmd
}

func newSkillsUninstallCmd(_ *string) *cobra.Command {
	var targets []string
	var view string
	var all bool
	cmd := &cobra.Command{
		Use:   "skills-uninstall",
		Short: "Remove the cairn skills (only files cairn wrote; anything else is left untouched)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := skillsEnv()
			if err != nil {
				return err
			}
			sel, err := resolveTargets(env, targets, all)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var failures int
			for _, t := range sel {
				tr, terr := t.Uninstall(env, view)
				fmt.Fprintf(out, "%s (%s)\n", tr.Target, tr.Dir)
				for _, r := range tr.Results {
					mark := "•"
					if r.Changed {
						mark = "✓"
					}
					fmt.Fprintf(out, "  %s %-16s %s\n", mark, r.Unit, r.Message)
				}
				if terr != nil {
					failures++
					fmt.Fprintf(out, "  ✗ %v\n", terr)
				}
			}
			if failures > 0 {
				return fmt.Errorf("%d target(s) had skills that could not be removed (see above)", failures)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&targets, "target", nil, "harness(es) to remove from (repeatable)")
	cmd.Flags().BoolVar(&all, "all", false, "remove from every detected harness")
	cmd.Flags().StringVar(&view, "view", "", "the view the skills were installed for (affects only which files are compared)")
	return cmd
}

func runSkillsStatus(cmd *cobra.Command, env skillsinstall.Env, view string) error {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "cairn skills — recall, remember, recap, handoff")
	fmt.Fprintln(out, "(a skill grants nothing: it names a verb your session may or may not be permitted to run)")
	fmt.Fprintln(out)
	for _, t := range skillsinstall.Registry() {
		s := t.Inspect(env, view)
		if !s.Installed {
			fmt.Fprintf(out, "  %-14s not installed\n", s.Target)
			continue
		}
		fmt.Fprintf(out, "  %-14s installed — %s\n", s.Target, s.Detail)
		fmt.Fprintf(out, "  %-14s   dir: %s\n", "", s.Dir)
		for _, sk := range s.Skills {
			state := "not installed"
			switch {
			case sk.Exists && !sk.Managed:
				state = "present but NOT cairn-managed — will not be overwritten or removed"
			case sk.Exists && sk.UpToDate:
				state = fmt.Sprintf("installed, current (view %s)", sk.View)
			case sk.Exists:
				state = fmt.Sprintf("installed but STALE (view %s) — re-run skills-install to update", sk.View)
			}
			fmt.Fprintf(out, "  %-14s   %-16s %s\n", "", sk.Unit, state)
		}
	}
	fmt.Fprintln(out, "\nrun `cairn skills-install --all` to install into every detected harness, or --target <name> for one.")
	return nil
}
