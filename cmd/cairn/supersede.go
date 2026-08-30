package main

// D16 (S19) — the operator surface for supersession.
//
// `cairn supersede A --by B` records that B replaced A. A is NOT deleted,
// hidden, or rewritten: it keeps its body, its sender, its topics, its
// signature and its place in search, and it gains an END DATE. Retrieval
// demotes it so B outranks it, `cairn fetch A` still returns it and says what
// replaced it, and `cairn supersession A` shows the whole history including
// assertions a retracted successor has since lifted.
//
// The verbs live in their own file rather than in verbs.go for a boring
// reason: this sprint runs alongside another one editing that file, and a new
// file cannot textually conflict with it.

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/daemon"
)

func newSupersedeCmd(dirFlag *string) *cobra.Command {
	var by, reason, actor string
	cmd := &cobra.Command{
		Use:   "supersede <message-id> --by <message-id>",
		Short: "Record that a later message replaced this one (demotes it in ranking; never deletes it)",
		Long: "Record that <message-id> has been superseded by a later message.\n\n" +
			"The superseded message is given an END DATE, not removed: it stays fetchable,\n" +
			"searchable and attributed, its history is intact, and retrieval simply stops\n" +
			"ranking it ahead of its successor. Retracting the successor lifts the\n" +
			"supersession automatically.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if by == "" {
				return fmt.Errorf("--by <message-id> is required: supersession names the message that REPLACED this one")
			}
			resp, err := call(dirFlag, daemon.Request{
				Op: "supersede", MessageID: args[0], SupersededBy: by, Reason: reason, Actor: actor,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "superseded: %s is now replaced by %s (event %s)\n", args[0], by, resp.EventID)
			fmt.Fprintln(cmd.OutOrStdout(), "the superseded message remains fetchable and searchable; it is demoted, not deleted")
			return nil
		},
	}
	cmd.Flags().StringVar(&by, "by", "", "message id of the message that REPLACES this one (required)")
	cmd.Flags().StringVar(&reason, "reason", "", "why it was superseded")
	cmd.Flags().StringVar(&actor, "actor", "operator", "acting principal")
	return cmd
}

func newSupersessionCmd(dirFlag *string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "supersession <message-id>",
		Short: "Show what replaced a message, when, who said so, and where the chain leads now",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "supersession", MessageID: args[0]})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				blob, err := json.MarshalIndent(map[string]any{
					"message_id":            args[0],
					"supersessions":         resp.Supersession,
					"current_version_chain": resp.Chain,
					"supersession_cycle":    resp.Cycled,
				}, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(out, string(blob))
				return nil
			}
			if len(resp.Supersession) == 0 {
				fmt.Fprintf(out, "%s is current — nothing supersedes it\n", args[0])
				return nil
			}
			for _, a := range resp.Supersession {
				state := "LIVE"
				if !a.Live {
					state = "lifted (successor retracted)"
				}
				fmt.Fprintf(out, "%s  superseded by %s at %s  [%s]\n", args[0], a.SupersededBy, a.ValidUntil, state)
				fmt.Fprintf(out, "    asserted by %s in event %s\n", orDash(a.Actor), a.EventID)
				if a.Reason != "" {
					// Mesh-authored text: DATA, never an instruction (R18).
					fmt.Fprintf(out, "    reason: %s\n", a.Reason)
				}
			}
			if resp.Cycled {
				fmt.Fprintln(out, "current version: UNDETERMINED — the supersession chain cycles or exceeds the depth bound")
				return nil
			}
			if len(resp.Chain) > 1 {
				fmt.Fprintf(out, "current version: %s\n", resp.Chain[len(resp.Chain)-1])
				fmt.Fprintf(out, "chain: %v\n", resp.Chain)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func newConsolidateCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consolidate",
		Short: "Run the supersession consolidation pass now and report the staleness census",
		Long: "Recompute the supersession graph and report how much of the mesh is current.\n\n" +
			"This pass computes RELATIONS and RANKINGS over immutable content. It never\n" +
			"rewrites, summarises, merges or deletes a stored message, and it never infers\n" +
			"a supersession nobody asserted.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "consolidate"})
			if err != nil {
				return err
			}
			c := resp.Census
			if c == nil {
				return fmt.Errorf("consolidate: daemon returned no census")
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "supersession assertions   %d (live %d, lifted by a retracted successor %d)\n",
				c.Assertions, c.LiveAssertions, c.LiftedAssertions)
			fmt.Fprintf(out, "live messages             %d current, %d superseded\n", c.CurrentMessages, c.SupersededMessages)
			fmt.Fprintf(out, "multiply superseded       %d\n", c.MultiplySuperseded)
			fmt.Fprintf(out, "longest chain             %d hop(s)\n", c.MaxChainDepth)
			fmt.Fprintf(out, "cycles                    %d\n", c.Cycles)
			if c.Cycles > 0 {
				fmt.Fprintln(out, "NOTE: a cycle means two messages each claim to supersede the other, so neither is")
				fmt.Fprintln(out, "      current. Resolve by retracting the wrong successor, or supersede both with one message.")
			}
			return nil
		},
	}
	return cmd
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
