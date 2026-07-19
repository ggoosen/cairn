package main

// R56 self-bootstrapping onboarding record — operator + agent CLI.
//
//	cairn onboarding publish --view <v> --interest "…" [--topic t]… [--budget N] [--note "…"]
//	    (operator) publish/update the record on cairn/onboarding/<v>.
//	cairn onboarding show --view <v>
//	    fetch + verify; print the whitelisted config or the refusal reason.
//	cairn onboarding apply --view <v> [--instructions CLAUDE.md]
//	    fetch + verify; if operator-authored, apply the WHITELISTED config only:
//	    set the local (R25) interest/topics and rewrite the DELIMITED block in the
//	    instructions file. Idempotent. Never runs commands, never writes outside
//	    the markers, never treats prose as a directive.

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/onboarding"
)

func newOnboardingCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "onboarding", Short: "Self-bootstrapping onboarding record (R56): publish / show / apply"}

	// --- publish (operator) --------------------------------------------------
	var pView, pInterest, pNote string
	var pTopics []string
	var pBudget int
	pub := &cobra.Command{
		Use:   "publish --view <view>",
		Short: "Publish/update the operator onboarding record for a view (operator only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if pView == "" {
				return fmt.Errorf("--view is required")
			}
			cfg := onboarding.Config{View: pView, InterestQuery: pInterest, Topics: pTopics, DigestBudget: pBudget}
			body := onboarding.RenderRecordBody(cfg, pNote)
			resp, err := call(dirFlag, daemon.Request{Op: "publish", Publish: &daemon.PublishRequest{
				Actor:            "operator", // authorship gate: only this principal is appliable
				Body:             body,
				TextClass:        "canonical",
				Topics:           []string{"cairn/onboarding/" + pView},
				AutoCreateTopics: true, // operator CLI path (FIX-F1 ruling 1)
			}})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Publish)
		},
	}
	pub.Flags().StringVar(&pView, "view", "", "view this record onboards (required)")
	pub.Flags().StringVar(&pInterest, "interest", "", "standing interest query for the view")
	pub.Flags().StringSliceVar(&pTopics, "topic", nil, "topic taxonomy this view cares about (repeatable)")
	pub.Flags().IntVar(&pBudget, "budget", 0, "recommended digest budget (chars)")
	pub.Flags().StringVar(&pNote, "note", "", "human prose (NOT machine-applied)")
	cmd.AddCommand(pub)

	// --- show ----------------------------------------------------------------
	var sView string
	show := &cobra.Command{
		Use:   "show --view <view>",
		Short: "Fetch + verify the onboarding record; print the whitelisted config or the refusal",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if sView == "" {
				return fmt.Errorf("--view is required")
			}
			res, err := call(dirFlag, daemon.Request{Op: "onboarding-get", AgentView: sView})
			if err != nil {
				return err
			}
			return printJSON(cmd, res.Onboarding)
		},
	}
	show.Flags().StringVar(&sView, "view", "", "view to look up (required)")
	cmd.AddCommand(show)

	// --- apply ---------------------------------------------------------------
	var aView, aInstructions string
	apply := &cobra.Command{
		Use:   "apply --view <view>",
		Short: "Apply the operator onboarding record: set local interest/topics + rewrite the delimited instructions block",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if aView == "" {
				return fmt.Errorf("--view is required")
			}
			res, err := call(dirFlag, daemon.Request{Op: "onboarding-get", AgentView: aView})
			if err != nil {
				return err
			}
			ob := res.Onboarding
			out := cmd.OutOrStdout()
			if ob == nil || !ob.Found {
				fmt.Fprintf(out, "no onboarding record for view %q — nothing to apply\n", aView)
				return nil
			}
			if !ob.Verified {
				// Found but not appliable: report the refusal and STOP. The body
				// is never applied as config (bounds 1/2).
				fmt.Fprintf(out, "onboarding record %s NOT applied: %s\n", ob.MessageID, ob.Refusal)
				return nil
			}
			cfg := *ob.Config

			// Bound 3a: set ONLY the caller's local (R25) interest/topics. Reuse
			// the local-tier path — no events, own view. Topics are replaced from
			// the record (non-nil, possibly empty).
			if cfg.InterestQuery != "" {
				topics := cfg.Topics
				if topics == nil {
					topics = []string{}
				}
				if _, err := call(dirFlag, daemon.Request{Op: "subscribe-local", LocalSub: &daemon.LocalSubRequest{
					View: cfg.View, InterestQuery: cfg.InterestQuery, Topics: topics,
				}}); err != nil {
					return fmt.Errorf("applying local interest: %w", err)
				}
			}

			// Bound 3b: rewrite ONLY the delimited block in the instructions file.
			path := aInstructions
			if path == "" {
				path = "CLAUDE.md"
			}
			existing := ""
			if blob, rerr := os.ReadFile(path); rerr == nil {
				existing = string(blob)
			} else if !os.IsNotExist(rerr) {
				return fmt.Errorf("reading %s: %w", path, rerr)
			}
			updated, changed := onboarding.ApplyToInstructions(existing, onboarding.RenderClaudeBlock(cfg))
			if changed {
				if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
					return fmt.Errorf("writing %s: %w", path, err)
				}
			}

			fmt.Fprintf(out, "applied onboarding revision %s for view %q\n", cfg.Revision, cfg.View)
			if cfg.InterestQuery != "" {
				fmt.Fprintf(out, "  local interest set: %s\n", cfg.InterestQuery)
			}
			if len(cfg.Topics) > 0 {
				fmt.Fprintf(out, "  topics: %v\n", cfg.Topics)
			}
			if changed {
				fmt.Fprintf(out, "  %s block updated\n", path)
			} else {
				fmt.Fprintf(out, "  %s block already current (no change)\n", path)
			}
			return nil
		},
	}
	apply.Flags().StringVar(&aView, "view", "", "view to onboard (required)")
	apply.Flags().StringVar(&aInstructions, "instructions", "", "instructions file to rewrite the delimited block in (default CLAUDE.md)")
	cmd.AddCommand(apply)

	groupGuard(cmd)
	return cmd
}
