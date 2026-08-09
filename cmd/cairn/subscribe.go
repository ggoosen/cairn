package main

// N3 CLI: durable semantic subscriptions.
//
// R25: `cairn subscribe --durable` is the ONLY path that creates
// subscription.* events. Without --durable the command edits the view's
// LOCAL view.json (the session tier) — no events, telemetry-class only.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/daemon"
)

func newSubscribeCmd(dirFlag *string) *cobra.Command {
	var view, mode string
	var topics []string
	var durable bool
	var topN, windowHours, percentile, pushCap int
	cmd := &cobra.Command{
		Use:   "subscribe <interest-query>",
		Short: "Subscribe a view to semantically matching messages (--durable = replicated events; default = local view config)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !durable {
				// session tier (R25): local view.json, never events. Routed
				// through the daemon's subscribe-local so an unset --topic
				// PRESERVES operator-set hard topic filters (the old direct
				// write rebuilt view.json from scratch and erased them, and
				// raced the daemon's reader).
				var t []string
				if cmd.Flags().Changed("topic") {
					t = topics
				}
				resp, err := call(dirFlag, daemon.Request{Op: "subscribe-local", LocalSub: &daemon.LocalSubRequest{
					View: view, InterestQuery: args[0], Topics: t,
				}})
				if err != nil {
					return fmt.Errorf("%w\n(the session tier is daemon-managed; start the daemon and retry)", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(),
					"local subscription: view %q now ranks digests against %q (topics: %v; no events; use --durable to replicate)\n",
					view, resp.LocalSub.InterestQuery, resp.LocalSub.Topics)
				return nil
			}
			resp, err := call(dirFlag, daemon.Request{Op: "subscribe-durable", Subscribe: &daemon.SubscribeRequest{
				OwnerView: view, InterestQuery: args[0], Topics: topics,
				ThresholdMode: mode, TopN: topN, WindowHours: windowHours,
				Percentile: percentile, PushCapPerDay: pushCap,
			}})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Sub)
		},
	}
	cmd.Flags().StringVar(&view, "view", "operator", "owner view the matches surface in")
	cmd.Flags().StringSliceVar(&topics, "topic", nil, "hard topic filter(s) — existing topics only, applied FIRST")
	cmd.Flags().BoolVar(&durable, "durable", false, "create replicated subscription.* events (R25); default is the local view config")
	cmd.Flags().StringVar(&mode, "mode", "", "threshold mode: top_n (default) | percentile — calibration is relative, never a static cosine threshold")
	cmd.Flags().IntVar(&topN, "top-n", 0, "top_n mode: max matches per window (default 10)")
	cmd.Flags().IntVar(&windowHours, "window-hours", 0, "top_n window in hours (default 24)")
	cmd.Flags().IntVar(&percentile, "percentile", 0, "percentile mode: include above this pool percentile (default 90)")
	cmd.Flags().IntVar(&pushCap, "push-cap", 0, "max deliveries per day (default 20)")
	return cmd
}

func newSubscriptionCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "subscription", Short: "Manage durable subscriptions"}

	var listView string
	list := &cobra.Command{
		Use:   "list",
		Short: "List durable subscriptions (all views unless --view)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "subscription-list", AgentView: listView})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Subs)
		},
	}
	list.Flags().StringVar(&listView, "view", "", "filter by owner view")
	cmd.AddCommand(list)

	cmd.AddCommand(&cobra.Command{
		Use:   "disable <subscription-id>",
		Short: "Stop delivery (the subscription and its events remain)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "subscription-disable", SubscriptionID: args[0]})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "disabled:", resp.EventID)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "delete <subscription-id>",
		Short: "Remove the subscription from the projection (log history remains)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "subscription-delete", SubscriptionID: args[0]})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "deleted:", resp.EventID)
			return nil
		},
	})

	var baseRev, uTopN, uWindow, uPercentile, uPushCap int
	var uQuery, uMode string
	var uTopics []string
	var enable bool
	update := &cobra.Command{
		Use:   "update <subscription-id> --base-revision <n>",
		Short: "Update a subscription (optimistic: stale --base-revision is refused before ack)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &daemon.SubUpdateRequest{SubscriptionID: args[0], BaseRevision: baseRev, Enable: enable}
			if cmd.Flags().Changed("query") {
				req.InterestQuery = &uQuery
			}
			if cmd.Flags().Changed("topic") {
				req.Topics = uTopics
			}
			if cmd.Flags().Changed("mode") {
				req.ThresholdMode = &uMode
			}
			if cmd.Flags().Changed("top-n") {
				req.TopN = &uTopN
			}
			if cmd.Flags().Changed("window-hours") {
				req.WindowHours = &uWindow
			}
			if cmd.Flags().Changed("percentile") {
				req.Percentile = &uPercentile
			}
			if cmd.Flags().Changed("push-cap") {
				req.PushCapPerDay = &uPushCap
			}
			resp, err := call(dirFlag, daemon.Request{Op: "subscription-update", SubUpdate: req})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Sub)
		},
	}
	update.Flags().IntVar(&baseRev, "base-revision", 0, "current revision this update is based on (required)")
	update.MarkFlagRequired("base-revision")
	update.Flags().StringVar(&uQuery, "query", "", "new interest query")
	update.Flags().StringSliceVar(&uTopics, "topic", nil, "replace hard topic filters in full")
	update.Flags().StringVar(&uMode, "mode", "", "top_n | percentile")
	update.Flags().IntVar(&uTopN, "top-n", 0, "")
	update.Flags().IntVar(&uWindow, "window-hours", 0, "")
	update.Flags().IntVar(&uPercentile, "percentile", 0, "")
	update.Flags().IntVar(&uPushCap, "push-cap", 0, "")
	update.Flags().BoolVar(&enable, "enable", false, "re-enable a disabled subscription")
	cmd.AddCommand(update)

	groupGuard(cmd)
	return cmd
}

// N4: derivative inspection + invalidation.
func newDerivativeCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "derivative", Short: "Inspect and invalidate attachment derivatives (N4)"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list <message-id>",
		Short: "Show derivative records (extractor provenance) for a message's attachments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "derivative-list", MessageID: args[0]})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Derivs)
		},
	})
	var reason string
	inv := &cobra.Command{
		Use:   "invalidate <derivative-id>",
		Short: "Invalidate a derivative (drops it from search; regenerated on the next enrichment pass)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "derivative-invalidate", DerivativeID: args[0], Reason: reason})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "invalidated:", resp.EventID)
			return nil
		},
	}
	inv.Flags().StringVar(&reason, "reason", "", "why (recorded in the event)")
	cmd.AddCommand(inv)
	cmd.AddCommand(&cobra.Command{
		Use:   "summary <message-id>",
		Short: "Show the sender summary claim and the receiver's consistency verdict",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "summary-show", MessageID: args[0]})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Summary)
		},
	})
	groupGuard(cmd)
	return cmd
}
