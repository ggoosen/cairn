package cairnctl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/ggoosen/cairn/eval/internal/tunables"
)

// MCPSession is a client for `cairn mcp`: newline-delimited JSON-RPC over the
// subprocess's stdio, which is exactly how Claude Desktop / Claude Code talk
// to it. The harness needs this surface, not just the CLI, because agents
// reach Cairn through MCP and its tool descriptions are part of what is
// being evaluated (E6 in particular: an injection travels through whichever
// surface the agent actually reads).
type MCPSession struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	errs *lockedBuffer

	mu sync.Mutex
	id int
}

// MCPTool is one entry of tools/list.
type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// MCPToolResult is one tools/call reply.
type MCPToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError,omitempty"`
}

// Text concatenates the result's text blocks — for cairn's server, one block
// holding the tool's JSON.
func (r MCPToolResult) Text() string {
	var s string
	for _, c := range r.Content {
		s += c.Text
	}
	return s
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// StartMCP launches `cairn mcp --view <view>` against this instance and
// performs the initialize handshake. The daemon must already be running:
// the MCP server refuses to start without one, by design.
//
// profile is the capability profile the server mints for itself; empty means
// the CLI default (agent-standard). "full" is refused by cairn (R21: MCP is
// never tier-1) and the harness does not try.
func (i *Instance) StartMCP(ctx context.Context, view, profile string) (*MCPSession, error) {
	args := []string{"--dir", i.Dir, "mcp", "--view", view}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	cmd := exec.Command(i.Bin.Path, args...)
	cmd.Env = i.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errBuf := &lockedBuffer{}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting cairn mcp: %w", err)
	}
	s := &MCPSession{cmd: cmd, in: stdin, out: bufio.NewReaderSize(stdout, 1<<20), errs: errBuf}

	if _, err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"clientInfo":      map[string]any{"name": "cairn-eval", "version": "0"},
		"capabilities":    map[string]any{},
	}); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("mcp initialize: %w (stderr: %s)", err, errBuf.String())
	}
	return s, nil
}

// Tools lists the server's advertised tools. Their descriptions are part of
// the evaluated surface: an agent's behaviour follows from what the tool
// says it does.
func (s *MCPSession) Tools(ctx context.Context) ([]MCPTool, error) {
	raw, err := s.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []MCPTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return res.Tools, nil
}

// Call invokes one tool. A tool-level failure comes back as a result with
// IsError set (MCP semantics), not as a Go error — the distinction matters
// for E6, where a refusal IS the desired outcome.
func (s *MCPSession) Call(ctx context.Context, tool string, args map[string]any) (*MCPToolResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := s.call(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return nil, err
	}
	var res MCPToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// MCPEnvelope is the untrusted-content envelope cairn's MCP server wraps every
// content-bearing result in. Only the fields the harness uses are declared;
// Raw keeps the rest.
//
// Trust and Content are the E6 surface: Trust must say "untrusted" and Content
// is the text an agent is actually handed. Both are recorded verbatim, because
// "the envelope protects agents" is a claim about these exact bytes.
type MCPEnvelope struct {
	Kind          string `json:"kind"`
	Trust         string `json:"trust"`
	InteractionID string `json:"interaction_id,omitempty"`
	RetrievalMode string `json:"retrieval_mode,omitempty"`
	Partial       bool   `json:"partial,omitempty"`
	PartialReason string `json:"partial_reason,omitempty"`
	Included      int    `json:"included,omitempty"`
	Omitted       int    `json:"omitted,omitempty"`
	Content       *struct {
		Mime string `json:"mime"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
	// Provenance is the daemon-authored attribution block. It is what lets an
	// agent tell an operator's record from a stranger's, so E6 checks it: a
	// content-level sender spoof is unanswerable without it.
	Provenance *struct {
		MessageID   string `json:"message_id"`
		RevisionID  string `json:"revision_id,omitempty"`
		Sender      string `json:"sender,omitempty"`
		ContentHash string `json:"content_hash,omitempty"`
	} `json:"provenance,omitempty"`
	Raw string `json:"-"`
}

// Text returns the envelope's content, or "" when it carries none.
func (e MCPEnvelope) Text() string {
	if e.Content == nil {
		return ""
	}
	return e.Content.Text
}

// DigestViaMCP generates the digest through the MCP tool rather than the CLI.
//
// WHY MCP AND NOT `cairn digest`. The CLI prints the payload and nothing else;
// the MCP envelope carries the same daemon-generated payload PLUS the
// interaction id, which is the key `cairn why-ranked` needs. Without it the
// digest surface has no published ranking arithmetic and E4's ±freshness /
// ±priority ablations could only ever run on search — which would be the wrong
// answer, because the digest is where freshness matters most (a 72-hour
// half-life against search's 90 days). It is also the surface an agent
// actually reads.
func (s *MCPSession) DigestViaMCP(ctx context.Context, budgetChars int) (*MCPEnvelope, error) {
	args := map[string]any{}
	if budgetChars > 0 {
		args["budget_chars"] = budgetChars
	}
	return s.envelope(ctx, "cairn_digest", args)
}

// SearchViaMCP runs the search tool. Used by E6, where the surface an agent
// reads is part of what is being evaluated.
func (s *MCPSession) SearchViaMCP(ctx context.Context, query string, k, budgetChars int) (*MCPEnvelope, error) {
	args := map[string]any{"query": query}
	if k > 0 {
		args["k"] = k
	}
	if budgetChars > 0 {
		args["budget_chars"] = budgetChars
	}
	return s.envelope(ctx, "cairn_search", args)
}

// FetchViaMCP retrieves one body with provenance through the agent surface.
func (s *MCPSession) FetchViaMCP(ctx context.Context, messageID string) (*MCPEnvelope, error) {
	return s.envelope(ctx, "cairn_fetch", map[string]any{"message_id": messageID})
}

func (s *MCPSession) envelope(ctx context.Context, tool string, args map[string]any) (*MCPEnvelope, error) {
	res, err := s.Call(ctx, tool, args)
	if err != nil {
		return nil, err
	}
	text := res.Text()
	if res.IsError {
		return nil, fmt.Errorf("mcp %s refused: %s", tool, text)
	}
	var env MCPEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		return nil, fmt.Errorf("parsing %s envelope: %w\n%s", tool, err, text)
	}
	env.Raw = text
	return &env, nil
}

func (s *MCPSession) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id++
	req := map[string]any{"jsonrpc": "2.0", "id": s.id, "method": method, "params": params}
	blob, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := s.in.Write(append(blob, '\n')); err != nil {
		return nil, fmt.Errorf("writing %s: %w (stderr: %s)", method, err, s.errs.String())
	}

	type readResult struct {
		line []byte
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		line, err := s.out.ReadBytes('\n')
		ch <- readResult{line, err}
	}()

	timeout := time.NewTimer(tunables.MCPCallTimeout)
	defer timeout.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timeout.C:
		return nil, fmt.Errorf("mcp %s: no response within %s (stderr: %s)", method, tunables.MCPCallTimeout, s.errs.String())
	case r := <-ch:
		if r.err != nil && len(r.line) == 0 {
			return nil, fmt.Errorf("mcp %s: %w (stderr: %s)", method, r.err, s.errs.String())
		}
		var resp rpcResponse
		if err := json.Unmarshal(r.line, &resp); err != nil {
			return nil, fmt.Errorf("mcp %s: unparseable reply %q: %w", method, r.line, err)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp %s: rpc error %d: %s", method, resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// Stderr returns whatever the MCP server has written to its diagnostic
// stream. Protocol messages never appear there — cairn keeps stdout clean.
func (s *MCPSession) Stderr() string { return s.errs.String() }

// Close ends the session: closing stdin is EOF to ServeStdio, which exits
// and revokes the capability session it minted.
func (s *MCPSession) Close() error {
	_ = s.in.Close()
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil
		}
		return err
	case <-time.After(tunables.DaemonStopTimeout):
		if s.cmd.Process != nil {
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
		return nil
	}
}
