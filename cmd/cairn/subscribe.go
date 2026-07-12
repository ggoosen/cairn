package main

// N3 CLI: durable semantic subscriptions.
//
// R25: `cairn subscribe --durable` is the ONLY path that creates
// subscription.* events. Without --durable the command edits the view's
// LOCAL view.json (the session tier) — no events, telemetry-class only.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
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
				// session tier (R25): local view.json, never events
				dir, err := config.PortableDir(*dirFlag)
				if err != nil {
					return err
				}
				base := filepath.Join(dir, config.ViewsDirName, view)
				if err := os.MkdirAll(base, 0o700); err != nil {
					return err
				}
				blob, _ := json.MarshalIndent(daemon.ViewConfig{Topics: topics, InterestQuery: args[0]}, "", "  ")
				if err := os.WriteFile(filepath.Join(base, "view.json"), blob, 0o644); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(),
					"local subscription: view %q now ranks digests against %q (no events; use --durable to replicate)\n",
					view, args[0])
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
