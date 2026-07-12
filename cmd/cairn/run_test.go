package main

// N2 CLI level: the trusted launcher exports CAIRN_SESSION, the child's CLI
// verbs are confined, and the handle is revoked when the child exits.

import (
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
)

func TestN2RunExportsAndRevokesSession(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	startTestDaemon(t, dir)

	out, err := runCLI(t, "run", "--dir", dir, "--profile", "read-only", "--name", "probe",
		"--", "sh", "-c", "echo TOKEN=$CAIRN_SESSION")
	if err != nil {
		t.Fatalf("cairn run: %v\n%s", err, out)
	}
	var token string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "TOKEN=") {
			token = strings.TrimPrefix(line, "TOKEN=")
		}
	}
	if token == "" {
		t.Fatalf("child did not receive CAIRN_SESSION:\n%s", out)
	}

	// after exit the handle is revoked: using it is a capability error
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = daemon.Call(loaded.DeviceDir, daemon.Request{Op: "search", Session: token, Query: "x"})
	if err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("session survived child exit: %v", err)
	}

	// the child's exit code propagates
	if _, err := runCLI(t, "run", "--dir", dir, "--", "sh", "-c", "exit 3"); err == nil {
		t.Fatal("child failure did not propagate")
	}
}

func TestN2RunIsNonDelegable(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	startTestDaemon(t, dir)
	t.Setenv(config.SessionEnvVar, "pretend-handle")
	out, err := runCLI(t, "run", "--dir", dir, "--", "true")
	if err == nil || !strings.Contains(err.Error(), "non-delegable") {
		t.Fatalf("nested cairn run allowed: %v\n%s", err, out)
	}
}

func TestN2SessionListAndRevoke(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	startTestDaemon(t, dir)
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := daemon.Call(loaded.DeviceDir, daemon.Request{
		Op: "session-create", SessionProfile: "agent-standard", SessionName: "listed"})
	if err != nil {
		t.Fatal(err)
	}
	token := resp.Status["session"].(string)

	out, err := runCLI(t, "session", "list", "--dir", dir)
	// encoding/json HTML-escapes '>' as > in the hierarchy string
	if err != nil || !strings.Contains(out, "operator\\u003elisted") {
		t.Fatalf("session list: %v\n%s", err, out)
	}
	if strings.Contains(out, token) {
		t.Fatalf("session list leaked a full token:\n%s", out)
	}
	if out, err := runCLI(t, "session", "revoke", token, "--dir", dir); err != nil {
		t.Fatalf("session revoke: %v\n%s", err, out)
	}
	if _, err := daemon.Call(loaded.DeviceDir, daemon.Request{Op: "search", Session: token, Query: "x"}); err == nil {
		t.Fatal("revoked session still valid")
	}
}

func TestN2MCPNeverTier1(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	startTestDaemon(t, dir)

	// --profile full refused outright (R21)
	root := newRootCmd()
	root.SetIn(strings.NewReader(""))
	var sb strings.Builder
	root.SetOut(&sb)
	root.SetErr(&sb)
	root.SetArgs([]string{"mcp", "--dir", dir, "--profile", "full"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "never tier-1") {
		t.Fatalf("mcp --profile full allowed: %v", err)
	}

	// default profile round-trip works and the minted session dies with the server
	out, err := runMCP(t, dir,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"cairn_send","arguments":{"body":"from mcp under agent-standard"}}}`+"\n")
	if err != nil {
		t.Fatalf("mcp: %v\n%s", err, out)
	}
	if !strings.Contains(out, "send_receipt") {
		t.Fatalf("send under agent-standard failed:\n%s", out)
	}
	if lst, err := runCLI(t, "session", "list", "--dir", dir); err != nil || strings.Contains(lst, "\"mcp\"") {
		t.Fatalf("mcp session not revoked on exit: %v\n%s", err, lst)
	}

	// a read-only MCP server refuses sends with a capability tool error
	out, err = runMCPArgs(t, dir, []string{"--profile", "read-only"},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cairn_send","arguments":{"body":"blocked"}}}`+"\n")
	if err != nil {
		t.Fatalf("read-only mcp: %v\n%s", err, out)
	}
	if !strings.Contains(out, "isError") || !strings.Contains(out, "capability") {
		t.Fatalf("read-only mcp send not capability-refused:\n%s", out)
	}
}
