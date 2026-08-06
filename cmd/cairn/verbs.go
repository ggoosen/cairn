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
// Under `cairn run` the launcher exports CAIRN_SESSION; attaching it here
// confines EVERY CLI verb in that process tree to the session's profile
// (N2). Without it the local CLI is operator tier-1, unchanged from P0.
func call(dirFlag *string, req daemon.Request) (*daemon.Response, error) {
	dir, err := config.PortableDir(*dirFlag)
	if err != nil {
		return nil, err
	}
	clientDir, err := daemon.ClientDir(dir) // handles read-only restores (R9)
	if err != nil {
		return nil, err
	}
	if req.Session == "" {
		req.Session = os.Getenv(config.SessionEnvVar)
	}
	return daemon.Call(clientDir, req)
}

// stageAttachments streams each --attach file to the daemon (G6): the size is
// checked client-side FIRST (clean error, no `broken pipe`), then the raw bytes
// are streamed over IPC and referenced by object hash in the publish request.
func stageAttachments(dirFlag *string, paths []string) ([]daemon.AttachmentIn, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	dir, err := config.PortableDir(*dirFlag)
	if err != nil {
		return nil, err
	}
	clientDir, err := daemon.ClientDir(dir)
	if err != nil {
		return nil, err
	}
	session := os.Getenv(config.SessionEnvVar)
	out := make([]daemon.AttachmentIn, 0, len(paths))
	for _, p := range paths {
		st, err := daemon.StageAttachment(clientDir, session, p)
		if err != nil {
			return nil, err
		}
		out = append(out, daemon.AttachmentIn{
			ObjectHash: st.ObjectHash, ByteLen: st.ByteLen, Mime: st.Mime, Filename: st.Filename,
		})
	}
	return out, nil
}

func printJSON(cmd *cobra.Command, v any) error {
	blob, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(blob))
	return nil
}

// groupGuard (FIX-F5 ruling 3): a parent command invoked with an unknown
// subcommand — or bare — exits NONZERO instead of printing help with exit 0.
func groupGuard(cmd *cobra.Command) {
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("unknown %s subcommand %q — see `cairn %s --help`", c.Name(), args[0], c.Name())
		}
		_ = c.Help()
		return fmt.Errorf("%s requires a subcommand", c.Name())
	}
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
	var install, uninstall bool
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the resident single-writer daemon (log, projection, outbox, housekeeping, IPC)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			// FIX-G4: manage the daemon as a user service instead of running it.
			if install && uninstall {
				return fmt.Errorf("--install and --uninstall are mutually exclusive")
			}
			if uninstall {
				return uninstallService(cmd.OutOrStdout())
			}
			if install {
				return installService(dir, cmd.OutOrStdout())
			}
			// FIX-H7: if a daemon is ALREADY running a different binary, warn
			// loudly before we try to start (and fail on its lock) — the stale
			// daemon is why recent code isn't in effect.
			warnIfStaleDaemon(*dirFlag, version, cmd.ErrOrStderr())
			d, err := daemon.Start(daemon.Options{Dir: dir, Warn: cmd.ErrOrStderr(), Version: version})
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
	cmd.Flags().BoolVar(&install, "install", false, "install + start the daemon as a user service (launchd on macOS, systemd --user on Linux)")
	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "stop + remove the installed daemon user service")
	return cmd
}

func newSendCmd(dirFlag *string) *cobra.Command {
	var req daemon.PublishRequest
	var emergency bool
	var attach []string
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
			// G6: stream each attachment over IPC (size pre-checked client-side;
			// bytes never inline the publish JSON) and reference it by hash.
			staged, err := stageAttachments(dirFlag, attach)
			if err != nil {
				return err
			}
			req.Attachments = staged
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
	cmd.Flags().StringSliceVar(&attach, "attach", nil, "attach file(s) up to 16 MiB each; streamed over IPC, content-addressed, made searchable via deterministic derivatives (N4)")
	cmd.Flags().StringVar(&req.SenderSummary, "summary", "", "sender summary (an UNTRUSTED claim; the receiver verifies it against the body)")
	cmd.Flags().StringVar(&req.Durability, "durability", "", "attachment blob durability: ephemeral|normal(default)|important|pinned (N7)")
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
	defer groupGuard(cmd)
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
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List every topic with its live message count (RETR-D5: browse the taxonomy)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "topic-list"})
			if err != nil {
				return err
			}
			if len(resp.Topics) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no topics yet")
				return nil
			}
			for _, t := range resp.Topics {
				fmt.Fprintf(cmd.OutOrStdout(), "%-40s %5d message(s)  %s\n", t.Name, t.Messages, t.TopicID)
			}
			return nil
		},
	})
	return cmd
}

func newThreadCmd(dirFlag *string) *cobra.Command {
	var budget int
	cmd := &cobra.Command{
		Use:   "thread <thread-id>",
		Short: "Read a whole conversation (every live message of a thread, budget-capped)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "thread", ThreadID: args[0], BudgetChars: budget})
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), resp.Thread.Payload)
			if resp.Thread.Omitted > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "omitted: %d (raise --budget to see more)\n", resp.Thread.Omitted)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&budget, "budget", 4000, "budget_chars over the COMPLETE payload")
	return cmd
}

func newUnlinkCmd(dirFlag *string) *cobra.Command {
	var actor string
	cmd := &cobra.Command{
		Use:   "unlink <link-id>",
		Short: "Remove a message↔topic link (link ids: `cairn peek <message-id>` / projection)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "unlink", LinkID: args[0], Actor: actor})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "unlinked:", resp.EventID)
			return nil
		},
	}
	cmd.Flags().StringVar(&actor, "actor", "operator", "acting principal")
	return cmd
}

func newUnpinCmd(dirFlag *string) *cobra.Command {
	var actor string
	cmd := &cobra.Command{
		Use:   "unpin <pin-id>",
		Short: "Release a pin (the object stays while ANY active pin remains)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "unpin", PinID: args[0], Actor: actor})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "unpinned:", resp.EventID)
			return nil
		},
	}
	cmd.Flags().StringVar(&actor, "actor", "operator", "acting principal")
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
	var topics []string
	var sender, thread string
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Hybrid search (FTS + vector RRF fusion, P0 search profile, budget-capped)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := call(dirFlag, daemon.Request{
				Op: "search", Search2: &daemon.SearchOptions{
					Query: args[0], K: k, BudgetChars: budget, IncludeRetracted: includeRetracted,
					Topics: topics, Sender: sender, ThreadID: thread,
				},
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
	cmd.Flags().StringSliceVar(&topics, "topic", nil, "scope: only messages in these topics (existing names; repeatable)")
	cmd.Flags().StringVar(&sender, "sender", "", "scope: only messages from this principal")
	cmd.Flags().StringVar(&thread, "thread", "", "scope: only messages in this thread")
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
	var allowUnencrypted bool
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
				AllowUnencrypted: allowUnencrypted,
			})
			if err != nil {
				return err
			}
			return printJSON(cmd, res)
		},
	}
	cmd.Flags().StringVar(&displayName, "display-name", "", "display name for the new device certificate")
	cmd.Flags().BoolVar(&allowUnencrypted, "allow-unencrypted", false, "proceed on an unencrypted volume (writes a device key; warns loudly)")
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
	defer groupGuard(cmd)
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

func newHousekeepCmd(dirFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "housekeep",
		Short: "Run one ephemeral-TTL housekeeping sweep now",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := call(dirFlag, daemon.Request{Op: "housekeep"})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %v expired ephemeral object(s)\n", resp.Status["deleted"])
			return nil
		},
	}
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
