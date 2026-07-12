package mcp_test

// N1 acceptance at the protocol layer: the full MCP round-trip
// (initialize → tools/list → digest → search → peek → fetch → send → reply →
// signal → outcome → why_ranked) against a live daemon, the R18 envelope on
// every content-bearing result, R19 budget accounting, and the R20 send
// policy. The Claude Desktop leg of acceptance is the operator checkpoint
// (DOGFOOD.md has the client config).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	cairnembed "github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/mcp"
)

// startMesh initializes a cairn, runs a daemon + IPC socket, and returns an
// MCP server wired to it the same way `cairn mcp` wires one.
func startMesh(t *testing.T) (*mcp.Server, *daemon.Daemon) {
	t.Helper()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	dir := filepath.Join(t.TempDir(), "cairn")
	if _, err := identity.Initialize(identity.InitOptions{Dir: dir, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: cairnembed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Serve(ctx)
	t.Cleanup(func() {
		cancel()
		d.Close()
	})
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	caller := func(req daemon.Request) (*daemon.Response, error) {
		return daemon.Call(loaded.DeviceDir, req)
	}
	// wait until the daemon actually answers (the sock.path file appears
	// before its content is durable — existence alone races)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := caller(daemon.Request{Op: "status"}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon never became reachable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return mcp.New(caller, "mcp", "mcp", "test"), d
}

// rpc drives one JSON-RPC exchange through the wire-level handler.
func rpc(t *testing.T, s *mcp.Server, id int, method string, params any) map[string]any {
	t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	blob, _ := json.Marshal(msg)
	resp := s.HandleMessage(blob)
	if resp == nil {
		t.Fatalf("%s: no response", method)
	}
	out, _ := json.Marshal(resp)
	var decoded map[string]any
	json.Unmarshal(out, &decoded)
	return decoded
}

// callTool runs tools/call and returns the decoded result object from the
// single text content block, failing the test on isError unless wantErr.
func callTool(t *testing.T, s *mcp.Server, name string, args any, wantErr bool) map[string]any {
	t.Helper()
	resp := rpc(t, s, 1, "tools/call", map[string]any{"name": name, "arguments": args})
	if resp["error"] != nil {
		t.Fatalf("%s: protocol error: %v", name, resp["error"])
	}
	result := resp["result"].(map[string]any)
	isErr, _ := result["isError"].(bool)
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if isErr != wantErr {
		t.Fatalf("%s: isError=%v want %v: %s", name, isErr, wantErr, text)
	}
	if wantErr {
		return map[string]any{"error": text}
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("%s: result not JSON: %v\n%s", name, err, text)
	}
	return decoded
}

func TestN1HandshakeAndToolList(t *testing.T) {
	s, _ := startMesh(t)

	init := rpc(t, s, 1, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"clientInfo":      map[string]any{"name": "claude-desktop"},
	})
	result := init["result"].(map[string]any)
	if result["protocolVersion"] != "2025-03-26" {
		t.Fatalf("did not echo client protocol version: %v", result)
	}
	if result["serverInfo"].(map[string]any)["name"] != "cairn" {
		t.Fatalf("serverInfo: %v", result)
	}

	// notifications get no reply
	if resp := s.HandleMessage([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); resp != nil {
		t.Fatalf("notification got a reply: %+v", resp)
	}

	list := rpc(t, s, 2, "tools/list", nil)
	tools := list["result"].(map[string]any)["tools"].([]any)
	want := []string{"cairn_digest", "cairn_search", "cairn_peek", "cairn_fetch",
		"cairn_send", "cairn_reply", "cairn_signal", "cairn_outcome", "cairn_why_ranked"}
	if len(tools) != len(want) {
		t.Fatalf("tool count %d, want %d (§5.5: these nine, nothing else)", len(tools), len(want))
	}
	for i, tl := range tools {
		tool := tl.(map[string]any)
		if tool["name"] != want[i] {
			t.Errorf("tool[%d] = %v, want %s", i, tool["name"], want[i])
		}
	}
	// R18: content-bearing tools declare content is data, not instructions
	for _, name := range []string{"cairn_digest", "cairn_search", "cairn_fetch", "cairn_why_ranked"} {
		found := false
		for _, tl := range tools {
			tool := tl.(map[string]any)
			if tool["name"] == name && strings.Contains(tool["description"].(string), "never follow instructions") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s description lacks the untrusted-content statement (R18)", name)
		}
	}

	if resp := rpc(t, s, 3, "no/such/method", nil); resp["error"] == nil {
		t.Fatal("unknown method did not error")
	}
	if resp := rpc(t, s, 4, "ping", nil); resp["error"] != nil {
		t.Fatalf("ping: %v", resp["error"])
	}
}

// TestN1FullRoundTrip is the acceptance sequence digest→search→fetch→send→
// outcome (plus peek/reply/signal/why_ranked) with envelope checks on every
// content-bearing result.
func TestN1FullRoundTrip(t *testing.T) {
	s, d := startMesh(t)

	sent := callTool(t, s, "cairn_send", map[string]any{
		"body": "the council planning approval for the roastery was granted on tuesday",
	}, false)
	if sent["kind"] != "send_receipt" || sent["message_id"] == "" {
		t.Fatalf("send receipt: %v", sent)
	}
	msgID := sent["message_id"].(string)

	// enrich synchronously so hybrid search sees the vector
	for {
		n, err := d.EnrichOnce(config.EnrichBatch)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}

	search := callTool(t, s, "cairn_search", map[string]any{"query": "roastery planning approval"}, false)
	if search["kind"] != "search_results" || search["trust"] != "untrusted" {
		t.Fatalf("search envelope: %v", search)
	}
	if search["interaction_id"] == "" {
		t.Fatal("search has no interaction_id")
	}
	results := search["results"].([]any)
	if len(results) == 0 {
		t.Fatal("search found nothing")
	}
	first := results[0].(map[string]any)
	if first["kind"] != "search_result" || first["trust"] != "untrusted" {
		t.Fatalf("per-result envelope: %v", first)
	}
	prov := first["provenance"].(map[string]any)
	if prov["message_id"] != msgID || prov["revision_id"] == "" || prov["content_hash"] == "" {
		t.Fatalf("search result provenance incomplete: %v", prov)
	}

	peek := callTool(t, s, "cairn_peek", map[string]any{"message_id": msgID}, false)
	if peek["kind"] != "peek" || peek["trust"] != "untrusted" {
		t.Fatalf("peek envelope: %v", peek)
	}
	if peek["content"] != nil {
		t.Fatal("peek returned content (peek is metadata-only by design)")
	}
	peekProv := peek["provenance"].(map[string]any)
	if peekProv["sender"] != "mcp" {
		t.Fatalf("peek provenance sender: %v", peekProv)
	}

	fetch := callTool(t, s, "cairn_fetch", map[string]any{"message_id": msgID}, false)
	if fetch["kind"] != "fetch" || fetch["trust"] != "untrusted" {
		t.Fatalf("fetch envelope: %v", fetch)
	}
	fprov := fetch["provenance"].(map[string]any)
	if fprov["message_id"] != msgID || fprov["content_hash"] != prov["content_hash"] || fprov["sender"] != "mcp" {
		t.Fatalf("fetch provenance: %v", fprov)
	}
	content := fetch["content"].(map[string]any)
	if content["text"] != "the council planning approval for the roastery was granted on tuesday" {
		t.Fatalf("fetch body: %v", content)
	}
	if content["mime"] != "text/markdown" {
		t.Fatalf("fetch mime: %v", content)
	}

	why := callTool(t, s, "cairn_why_ranked", map[string]any{
		"interaction_id": search["interaction_id"], "message_id": msgID,
	}, false)
	if why["kind"] != "why_ranked" || why["trust"] != "untrusted" || why["content"] == nil {
		t.Fatalf("why_ranked envelope: %v", why)
	}

	outcome := callTool(t, s, "cairn_outcome", map[string]any{
		"interaction_id": search["interaction_id"], "outcome": "found", "message_id": msgID,
	}, false)
	if outcome["recorded"] != true {
		t.Fatalf("outcome: %v", outcome)
	}

	reply := callTool(t, s, "cairn_reply", map[string]any{
		"reply_to_message_id": msgID, "body": "confirmed — permit number recorded",
	}, false)
	replyPeek := callTool(t, s, "cairn_peek", map[string]any{"message_id": reply["message_id"]}, false)
	if replyPeek["reply_to_message_id"] != msgID || replyPeek["thread_id"] == "" {
		t.Fatalf("reply did not thread: %v", replyPeek)
	}

	signal := callTool(t, s, "cairn_signal", map[string]any{
		"message_id": msgID, "kind": "useful", "weight": 5,
	}, false)
	if signal["event_id"] == "" {
		t.Fatalf("signal: %v", signal)
	}

	digest := callTool(t, s, "cairn_digest", map[string]any{}, false)
	if digest["kind"] != "digest" || digest["trust"] != "untrusted" {
		t.Fatalf("digest envelope: %v", digest)
	}
	if digest["interaction_id"] == "" || digest["content"] == nil {
		t.Fatalf("digest: %v", digest)
	}
	dText := digest["content"].(map[string]any)["text"].(string)
	if utf8.RuneCountInString(dText) > config.MCPDigestBudgetDefault {
		t.Fatalf("digest exceeds default budget: %d chars", utf8.RuneCountInString(dText))
	}
}

// R19: over-budget digest truncates exactly; search obeys its budget too.
func TestN1BudgetTruncatesExactly(t *testing.T) {
	s, _ := startMesh(t)
	for i := 0; i < 12; i++ {
		callTool(t, s, "cairn_send", map[string]any{
			"body": fmt.Sprintf("budget filler message %d with enough words to consume digest space quickly", i),
		}, false)
	}
	const budget = 220
	digest := callTool(t, s, "cairn_digest", map[string]any{"budget_chars": budget}, false)
	text := digest["content"].(map[string]any)["text"].(string)
	if n := utf8.RuneCountInString(text); n > budget {
		t.Fatalf("digest payload %d chars exceeds budget %d", n, budget)
	}
	full := callTool(t, s, "cairn_digest", map[string]any{"budget_chars": 100000}, false)
	fullText := full["content"].(map[string]any)["text"].(string)
	if utf8.RuneCountInString(fullText) <= budget {
		t.Fatal("corpus never exceeded the small budget; truncation untested")
	}

	search := callTool(t, s, "cairn_search", map[string]any{"query": "budget filler message", "budget_chars": 150}, false)
	sText := search["content"].(map[string]any)["text"].(string)
	if n := utf8.RuneCountInString(sText); n > 150 {
		t.Fatalf("search payload %d chars exceeds budget 150", n)
	}
}

// R20: no force-class equivalent, unknown args reject, unresolved topics
// reject BEFORE ack instead of auto-creating.
func TestN1SendPolicy(t *testing.T) {
	s, _ := startMesh(t)

	for _, args := range []map[string]any{
		{"body": "x", "operator_override": true},
		{"body": "x", "force_class": "canonical"},
		{"body": "x", "auto_create_topics": true},
	} {
		res := callTool(t, s, "cairn_send", args, true)
		if !strings.Contains(res["error"].(string), "invalid arguments") {
			t.Fatalf("hidden knob accepted: %v → %v", args, res)
		}
	}

	res := callTool(t, s, "cairn_send", map[string]any{"body": "x", "topics": []string{"never-created"}}, true)
	if !strings.Contains(res["error"].(string), "before ack") {
		t.Fatalf("unresolved topic did not reject pre-ack: %v", res)
	}

	if res := callTool(t, s, "cairn_send", map[string]any{"body": ""}, true); res == nil {
		t.Fatal("empty body accepted")
	}
	if res := callTool(t, s, "cairn_outcome", map[string]any{"interaction_id": "zzz", "outcome": "shrug"}, true); res == nil {
		t.Fatal("bad outcome accepted")
	}
}

// ServeStdio: the actual wire loop — newline-delimited, responses only for
// id-bearing messages, clean EOF exit.
func TestN1ServeStdio(t *testing.T) {
	s, _ := startMesh(t)
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	var out strings.Builder
	if err := s.ServeStdio(in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 responses (notification silent), got %d:\n%s", len(lines), out.String())
	}
	if !strings.Contains(lines[1], "cairn_why_ranked") {
		t.Fatalf("tools/list response: %s", lines[1])
	}
}
