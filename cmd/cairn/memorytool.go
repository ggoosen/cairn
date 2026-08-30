package main

// `cairn memory-tool` — D19: serve Anthropic's client-side `memory_20250818`
// tool out of Cairn instead of out of a filesystem stub.
//
// The tool is six file commands executed by the DEVELOPER's own handler, so
// this command is that handler as a subprocess: newline-delimited JSON in
// (each line the `input` object of one memory tool_use block), newline-delimited
// JSON out (`{"content":…,"is_error":…}` — the two fields of a tool_result).
// A developer's loop in any language pipes to it; `--command` runs one command
// and exits, which is what a shell test drives.
//
// It is an AGENT SURFACE, so it is never tier-1 (RULINGS.md R21): every request
// runs under the CAIRN_SESSION handle it was launched with, or one it mints
// from --profile (default agent-standard, "full" refused at the flag) and
// revokes on exit — the same lifecycle, and the same D9 signal handling, as
// `cairn mcp`. Nothing in the facade can exceed that tier, which is why delete
// and str_replace are REFUSED under the default profile: they map onto
// retraction and revision, which are admin capability in this mesh.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/memorytool"
)

func newMemoryToolCmd(dirFlag *string) *cobra.Command {
	var view, actor, profile, class, one string
	var printTool bool

	cmd := &cobra.Command{
		Use:   "memory-tool",
		Short: "Serve Anthropic's memory_20250818 tool (view/create/str_replace/insert/delete/rename) out of Cairn over stdio",
		Long: "Back Anthropic's client-side memory tool with Cairn.\n\n" +
			"Reads one JSON memory command per line on stdin and writes one JSON tool_result\n" +
			"payload per line on stdout, so a Messages API tool-use loop in any language can\n" +
			"pipe to it. `--command '<json>'` runs a single command and exits.\n\n" +
			"Cairn's log is append-only, so two of the six commands are MAPPED, not emulated:\n" +
			"`delete` retracts (the content stops being served; the events stay in the log)\n" +
			"and `str_replace`/`insert` add a revision (the previous one stays fetchable).\n" +
			"Every reply says which happened — a caller expecting erasure is never told\n" +
			"erasure happened. For content that must genuinely disappear, use\n" +
			"`--class ephemeral`, whose bodies are removed when their TTL expires.\n\n" +
			"See docs/memory-tool.md.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if printTool {
				blob, err := json.Marshal(memorytool.ToolEntry())
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(blob))
				return nil
			}
			if profile == "full" {
				return errors.New("the memory-tool facade is an agent surface and is never tier-1 (RULINGS.md R21): --profile full is refused")
			}
			if actor == "" {
				actor = view
			}
			clientDir, token, cleanup, err := agentSession(dirFlag, profile, actor)
			if err != nil {
				return err
			}
			defer cleanup()

			caller := func(req daemon.Request) (*daemon.Response, error) {
				req.Session = token
				return daemon.Call(clientDir, req)
			}
			srv, err := memorytool.New(caller, view, actor, class,
				func() string { return time.Now().UTC().Format(time.RFC3339) })
			if err != nil {
				return err
			}

			run := func(raw []byte) error {
				out := memorytool.Result{}
				c, derr := memorytool.Decode(raw)
				if derr != nil {
					out = memorytool.Result{Content: "Error: " + derr.Error(), IsError: true}
				} else {
					out = srv.Handle(c)
				}
				blob, merr := json.Marshal(out)
				if merr != nil {
					return merr
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(blob))
				return nil
			}

			if one != "" {
				return run([]byte(one))
			}
			fmt.Fprintf(cmd.ErrOrStderr(),
				"cairn memory-tool: serving %s for view %q as actor %q (profile %s, class %s, session %s…)\n"+
					"  delete → retraction, str_replace/insert → revision; nothing is erased. NO secret redaction is applied (see docs/memory-tool.md).\n",
				config.MemoryToolRoot, view, actor, profile, class, token[:8])
			sc := bufio.NewScanner(cmd.InOrStdin())
			sc.Buffer(make([]byte, 0, 64*1024), config.MemoryToolMaxFileChars*4+8192)
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if line == "" {
					continue
				}
				if err := run([]byte(line)); err != nil {
					return err
				}
			}
			return sc.Err()
		},
	}
	cmd.Flags().StringVar(&view, "view", "memory", "the cairn view whose topic namespace becomes this /memories directory")
	cmd.Flags().StringVar(&actor, "actor", "", "principal recorded on writes (default: the view name)")
	cmd.Flags().StringVar(&profile, "profile", "agent-standard", "capability profile when not launched with CAIRN_SESSION (never \"full\", R21)")
	cmd.Flags().StringVar(&class, "class", "canonical", "text class new memory files take: canonical | eager-searchable | ephemeral (ephemeral is the honest choice when content must actually disappear)")
	cmd.Flags().StringVar(&one, "command", "", "run ONE memory command given as JSON and exit (instead of serving stdin)")
	cmd.Flags().BoolVar(&printTool, "print-tool", false, "print the `tools` entry a Messages API request sends, and exit")

	cmd.AddCommand(newMemoryToolInitCmd(dirFlag))
	return cmd
}

// newMemoryToolInitCmd provisions the view's memory namespace. It is a separate,
// OPERATOR-tier step on purpose: an agent surface never creates topics
// (FIX-F1), so the directories a memory tool may write into are decided by a
// person, once, and not invented by whatever the model happened to type.
func newMemoryToolInitCmd(dirFlag *string) *cobra.Command {
	var view string
	var dirs []string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Provision a view's memory namespace (the /memories root topic, and optionally its directories)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			names := []string{config.MemoryToolTopicRoot + "/" + view}
			for _, d := range dirs {
				names = append(names, config.MemoryToolTopicRoot+"/"+view+"/"+d)
			}
			for _, n := range names {
				resp, err := call(dirFlag, daemon.Request{Op: "topic-ensure", TopicName: n})
				if err != nil {
					return fmt.Errorf("provisioning topic %s: %w", n, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "topic %s ready (%s)\n", n, resp.TopicID)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"\n%s for view %q is provisioned. Point a Messages API loop at:\n  cairn memory-tool --view %s\n",
				config.MemoryToolRoot, view, view)
			return nil
		},
	}
	cmd.Flags().StringVar(&view, "view", "memory", "the view whose memory namespace to provision")
	cmd.Flags().StringSliceVar(&dirs, "subdir", nil, "subdirectories to provision (repeatable); each becomes a topic under the root. NOT --dir, which is the global portable-directory flag.")
	return cmd
}

// agentSession is the R21 session lifecycle `cairn mcp` established, factored
// out so a second agent surface cannot drift from it: use the handle we were
// launched with, or mint one from the profile and revoke it on exit AND on
// SIGTERM/SIGINT (D9 — a defer alone does not run when a client kills a stdio
// server, which is how one dev node accumulated 2,673 leaked session records).
func agentSession(dirFlag *string, profile, name string) (clientDir, token string, cleanup func(), err error) {
	dir, err := config.PortableDir(*dirFlag)
	if err != nil {
		return "", "", nil, err
	}
	clientDir, err = daemon.ClientDir(dir)
	if err != nil {
		return "", "", nil, err
	}
	// fail fast with a readable error if no daemon is up, rather than erroring
	// once per command after the caller's loop has already started
	if _, err := daemon.Call(clientDir, daemon.Request{Op: "status"}); err != nil {
		return "", "", nil, err
	}
	if token = os.Getenv(config.SessionEnvVar); token != "" {
		return clientDir, token, func() {}, nil
	}
	resp, err := daemon.Call(clientDir, daemon.Request{
		Op: "session-create", SessionProfile: profile, SessionName: name, SessionPID: os.Getpid()})
	if err != nil {
		return "", "", nil, err
	}
	token, _ = resp.Status["session"].(string)
	if token == "" {
		return "", "", nil, errors.New("daemon minted no session handle")
	}
	var once sync.Once
	revoke := func() {
		once.Do(func() {
			_, _ = daemon.Call(clientDir, daemon.Request{Op: "session-revoke", TargetSession: token})
		})
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig, okSig := <-sigCh
		if !okSig {
			return
		}
		revoke()
		code := 128
		if s, isSig := sig.(syscall.Signal); isSig {
			code += int(s)
		}
		os.Exit(code)
	}()
	return clientDir, token, func() { signal.Stop(sigCh); revoke() }, nil
}
