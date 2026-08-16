package main

// P2-4: `cairn saved` — named, re-runnable queries (device-local).

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/daemon"
)

func newSavedCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "saved", Short: "Saved searches: named, re-runnable queries (P2, device-local)"}

	cmd.AddCommand(&cobra.Command{
		Use:   "add <name> <query>",
		Short: "Create or replace a named query",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "saved-add", SavedName: args[0], Query: args[1]})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Error)
			}
			cmd.Printf("saved search %q added\n", args[0])
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List saved searches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "saved-list"})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Saved)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "rm <name>",
		Short: "Delete a saved search",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "saved-remove", SavedName: args[0]})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Error)
			}
			cmd.Printf("saved search %q removed\n", args[0])
			return nil
		},
	})

	var k, budget, budgetTokens int
	run := &cobra.Command{
		Use:   "run <name>",
		Short: "Run a saved search through the normal search path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "saved-run", SavedName: args[0], K: k,
				BudgetChars: budgetFlag(cmd, budget, budgetTokens), BudgetTokens: budgetTokens})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Search)
		},
	}
	run.Flags().IntVar(&k, "k", 10, "max results")
	run.Flags().IntVar(&budget, "budget", 0, "budget_chars over the COMPLETE payload (0 = unbudgeted)")
	addBudgetTokensFlag(run, &budgetTokens)
	cmd.AddCommand(run)

	groupGuard(cmd)
	return cmd
}
