package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/outbox"
)

// call resolves the cairn dir and sends one IPC request to the daemon.
func call(dirFlag *string, req daemon.Request) (*daemon.Response, error) {
	dir, err := config.PortableDir(*dirFlag)
	if err != nil {
		return nil, err
	}
	loaded, err := identity.Load(dir)
	if err != nil {
		return nil, err
	}
	return daemon.Call(loaded.DeviceDir, req)
}

func printJSON(cmd *cobra.Command, v any) error {
	blob, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(blob))
	return nil
}

func timeNow() time.Time { return time.Now() }

func newUUID() string {
	u, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return u.String()
}

func newDaemonCmd(dirFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the resident single-writer daemon (log, projection, outbox, housekeeping, IPC)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			d, err := daemon.Start(daemon.Options{Dir: dir, Warn: cmd.ErrOrStderr()})
			if err != nil {
				return err
			}
			defer d.Close()

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			// outbox watchers: every existing views/<agent>/outbox, plus any
			// created later (rescan each pass)
			processOutbox := func() error {
				entries, err := fsx.OS{}.ReadDir(dir + "/" + config.ViewsDirName)
				if err != nil {
					return nil
				}
				for _, e := range entries {
					if !e.IsDir() {
						continue
					}
					w, err := outbox.NewWatcher(fsx.OS{}, d, dir, e.Name())
					if err != nil {
						continue
					}
					if err := w.ProcessOnce(); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: outbox %s: %v\n", e.Name(), err)
					}
				}
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "cairn daemon running (dir %s); Ctrl-C to stop\n", dir)
			return d.Run(ctx, processOutbox)
		},
	}
}

func newSendCmd(dirFlag *string) *cobra.Command {
	var req daemon.PublishRequest
	var emergency bool
	cmd := &cobra.Command{
		Use:   "send <body|-> ",
		Short: "Publish a message (body as argument, or - for stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := args[0]
			if body == "-" {
				blob, err := readAllStdin(cmd)
				if err != nil {
					return err
				}
				body = string(blob)
			}
			req.Body = body
			req.AutoCreateTopics = true // operator CLI path (FIX-F1 ruling 1)
			op := "publish"
			if emergency {
				op = "emergency-publish"
			}
			resp, err := call(dirFlag, daemon.Request{Op: op, Publish: &req})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Publish)
		},
	}
	cmd.Flags().BoolVar(&emergency, "emergency", false, "consume the one-shot reserve release grant (requires `cairn reserve release`)")
	cmd.Flags().StringVar(&req.Actor, "actor", "operator", "acting principal")
	cmd.Flags().StringVar(&req.TaskID, "task-id", "", "task id for telemetry attribution")
	cmd.Flags().StringVar(&req.TextClass, "class", "canonical", "text class: canonical|eager-searchable|ephemeral")
	cmd.Flags().IntVar(&req.DeclaredPriority, "priority", 0, "declared priority 0-3 (immutable testimony)")
	cmd.Flags().BoolVar(&req.OperatorOverride, "force-class", false, "OPERATOR override: keep declared class despite size policy")
	cmd.Flags().StringVar(&req.ThreadID, "thread", "", "thread id")
	cmd.Flags().StringSliceVar(&req.Topics, "topic", nil, "initial topic link(s) by name or id (auto-creates unknown names)")
	cmd.Flags().StringSliceVar(&req.Recipients, "to", nil, "explicit recipient agent view(s)")
	return cmd
}

func newReplyCmd(dirFlag *string) *cobra.Command {
	var req daemon.PublishRequest
	cmd := &cobra.Command{
		Use:   "reply <message-id> <body|->",
		Short: "Reply to a message (atomic publish variant)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req.ReplyToMessageID = args[0]
			body := args[1]
			if body == "-" {
				blob, err := readAllStdin(cmd)
				if err != nil {
					return err
				}
				body = string(blob)
			}
			req.Body = body
			resp, err := call(dirFlag, daemon.Request{Op: "publish", Publish: &req})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Publish)
		},
	}
	cmd.Flags().StringVar(&req.Actor, "actor", "operator", "acting principal")
	cmd.Flags().StringVar(&req.TextClass, "class", "canonical", "text class")
	cmd.Flags().IntVar(&req.DeclaredPriority, "priority", 0, "declared priority 0-3")
	return cmd
}

func newRetractCmd(dirFlag *string) *cobra.Command {
	var reason, actor string
	cmd := &cobra.Command{
		Use:   "retract <message-id>",
		Short: "Retract a logical message (hides all revisions from default retrieval; history preserved)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "retract", MessageID: args[0], Reason: reason, Actor: actor})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "retracted:", resp.EventID)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "retraction reason")
	cmd.Flags().StringVar(&actor, "actor", "operator", "acting principal")
	return cmd
}

func newTopicCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "topic", Short: "Topic management"}
	cmd.AddCommand(&cobra.Command{
		Use:   "create <name>",
		Short: "Create a topic",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := newUUID()
			resp, err := call(dirFlag, daemon.Request{Op: "topic-create", TopicID: id, TopicName: args[0]})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "topic %s created (%s), event %s\n", args[0], id, resp.EventID)
			return nil
		},
	})
	return cmd
}

func newLinkCmd(dirFlag *string) *cobra.Command {
	var protected bool
	var actor string
	cmd := &cobra.Command{
		Use:   "link <message-id> <topic-name-or-id>",
		Short: "Link a message to an EXISTING topic (unknown topics are rejected before ack)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{
				Op: "link", MessageID: args[0], TopicID: args[1],
				Protected: protected, Actor: actor,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "linked:", resp.EventID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&protected, "protected", false, "human-added link, immune to automatic removal")
	cmd.Flags().StringVar(&actor, "actor", "operator", "acting principal")
	return cmd
}

func newPinCmd(dirFlag *string) *cobra.Command {
	var durability, actor string
	cmd := &cobra.Command{
		Use:   "pin <object-hash>",
		Short: "Pin an object (stays while any active pin intent exists)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{
				Op: "pin", PinID: newUUID(), ObjectRef: args[0], Durability: durability, Actor: actor,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "pinned:", resp.EventID)
			return nil
		},
	}
	cmd.Flags().StringVar(&durability, "durability", "pinned", "durability class")
	cmd.Flags().StringVar(&actor, "actor", "operator", "acting principal")
	return cmd
}

func newSignalCmd(dirFlag *string) *cobra.Command {
	var weight int
	var actor string
	cmd := &cobra.Command{
		Use:   "signal <message-id> <kind>",
		Short: "Emit an operator signal (important|ignore|priority_confirm|found|not_found)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{
				Op: "signal", MessageID: args[0], Kind: args[1], Weight: weight, Actor: actor,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "signal:", resp.EventID)
			return nil
		},
	}
	cmd.Flags().IntVar(&weight, "weight", 0, "signal weight 1-10")
	cmd.Flags().StringVar(&actor, "actor", "operator", "acting principal")
	return cmd
}

func newSearchCmd(dirFlag *string) *cobra.Command {
	var k, budget int
	var includeRetracted bool
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Hybrid search (FTS + vector RRF fusion, P0 search profile, budget-capped)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{
				Op: "search", Query: args[0], K: k, BudgetChars: budget, IncludeRetracted: includeRetracted,
			})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Search)
		},
	}
	cmd.Flags().IntVar(&k, "k", 10, "max results")
	cmd.Flags().IntVar(&budget, "budget", 0, "budget_chars over the COMPLETE payload (0 = unbudgeted)")
	cmd.Flags().BoolVar(&includeRetracted, "include-retracted", false, "include retracted messages (capability-gated in P1)")
	return cmd
}

func newDigestCmd(dirFlag *string) *cobra.Command {
	var budget int
	var view string
	cmd := &cobra.Command{
		Use:   "digest",
		Short: "Generate the ranked, budget-capped digest for an agent view (writes views/<view>/digest.md)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "digest", AgentView: view, BudgetChars: budget})
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), resp.Digest.Payload)
			if resp.Digest.OmittedMandatory > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "omitted_mandatory_count: %d\n", resp.Digest.OmittedMandatory)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&budget, "budget", 4000, "budget_chars over the COMPLETE digest")
	cmd.Flags().StringVar(&view, "view", "operator", "agent view")
	return cmd
}

func newWhyRankedCmd(dirFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "why-ranked <interaction-id> <message-id>",
		Short: "Print the exact stored ranking arithmetic for one result",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "why-ranked", InteractionID: args[0], MessageID: args[1]})
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), resp.Text)
			return nil
		},
	}
}

func newPeekCmd(dirFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "peek <message-id>",
		Short: "Show message metadata without fetching the body",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "peek", MessageID: args[0]})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Message)
		},
	}
}

func newFetchCmd(dirFlag *string) *cobra.Command {
	var view string
	cmd := &cobra.Command{
		Use:   "fetch <message-id>",
		Short: "Materialize a message body into views/<agent>/fetched/ (manifest + body, provenance-separated)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "fetch", MessageID: args[0], AgentView: view})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Fetched)
		},
	}
	cmd.Flags().StringVar(&view, "view", "operator", "agent view receiving the fetch")
	return cmd
}

func newMigrateCmd(dirFlag *string) *cobra.Command {
	var displayName string
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Offline device-migration ceremony: enrol new identity, revoke this device, old origin becomes read-only",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			res, err := identity.Migrate(identity.MigrateOptions{
				Dir: dir, DisplayName: displayName, Out: cmd.OutOrStdout(),
			})
			if err != nil {
				return err
			}
			return printJSON(cmd, res)
		},
	}
	cmd.Flags().StringVar(&displayName, "display-name", "", "display name for the new device certificate")
	return cmd
}

func newExportCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export <message-id>",
		Short: "Render exports/<message-id>.md (read-only front-matter; edit the body, then `cairn export ingest`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "export", MessageID: args[0]})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), resp.Path)
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "ingest <path>",
		Short: "Ingest an edited export: base==head → revision; clean diff3 → machine-merged; conflict → conflicts/",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			abs, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			resp, err := call(dirFlag, daemon.Request{Op: "export-ingest", Path: abs})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Ingest)
		},
	})
	return cmd
}

func newResolveCmd(dirFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "resolve <message-id>",
		Short: "Resolve a merge conflict: applies conflicts/<id>/RESOLVE.md against the current head",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "resolve", MessageID: args[0]})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Ingest)
		},
	}
}

func readAllStdin(cmd *cobra.Command) ([]byte, error) {
	return io.ReadAll(cmd.InOrStdin())
}

func newOutcomeCmd(dirFlag *string, use, outcome, short string) *cobra.Command {
	var messageID string
	cmd := &cobra.Command{
		Use:   use + " <interaction-id>",
		Short: short + " (bound to the interaction_id returned by search/digest)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := call(dirFlag, daemon.Request{Op: "outcome", InteractionID: args[0], Outcome: outcome, MessageID: messageID})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recorded %s for %s\n", outcome, args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&messageID, "message", "", "the message that satisfied (or failed) the retrieval")
	return cmd
}

func newGatesCmd(dirFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "gates",
		Short: "Report the P0 gates (engineering gates computed; product gates from recorded outcomes)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "gates"})
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), resp.Text)
			return nil
		},
	}
}

func newReserveCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "reserve", Short: "Emergency reserve (64 MiB preallocated; rulings §11)"}
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show reserve and release-grant state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "reserve-status"})
			if err != nil {
				return err
			}
			return printJSON(cmd, resp.Status)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "release",
		Short: "OPERATOR: release the reserve, granting ONE emergency send ≤ 64 KiB",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := call(dirFlag, daemon.Request{Op: "reserve-release"}); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "reserve released: ONE emergency send ≤ 64 KiB is granted (cairn send --emergency)")
			return nil
		},
	})
	return cmd
}

func newSetupAgentCmd(dirFlag *string) *cobra.Command {
	var topics []string
	var interest string
	cmd := &cobra.Command{
		Use:   "setup-agent <name>",
		Short: "Create an agent view: views/<name>/{outbox,fetched,conflicts} + view.json",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
				return fmt.Errorf("invalid agent view name %q (plain names only)", name)
			}
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			if _, err := identity.Load(dir); err != nil {
				return err
			}
			base := filepath.Join(dir, config.ViewsDirName, name)
			for _, sub := range []string{"outbox", "fetched", "conflicts"} {
				if err := os.MkdirAll(filepath.Join(base, sub), 0o700); err != nil {
					return err
				}
			}
			viewCfg := filepath.Join(base, "view.json")
			if _, err := os.Stat(viewCfg); err != nil {
				blob, _ := json.MarshalIndent(daemon.ViewConfig{Topics: topics, InterestQuery: interest}, "", "  ")
				if err := os.WriteFile(viewCfg, blob, 0o644); err != nil {
					return err
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "agent view %q ready:\n  drop .md files or bundles in %s\n  digests land at %s\n  edit %s for hard topic filters / interest query\n",
				name, filepath.Join(base, "outbox"), filepath.Join(base, "digest.md"), viewCfg)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&topics, "topic", nil, "hard topic filter(s) for this view's digest")
	cmd.Flags().StringVar(&interest, "interest", "", "natural-language interest query for digest relevance")
	return cmd
}
