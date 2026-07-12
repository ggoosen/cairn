package main

// N1 CLI level: `cairn mcp` serves the MCP handshake over stdio against a
// running daemon, and fails fast (nonzero, R8) when no daemon is up.

import (
	"strings"
	"testing"
)

func runMCP(t *testing.T, dir, stdin string) (string, error) {
	return runMCPArgs(t, dir, nil, stdin)
}

func runMCPArgs(t *testing.T, dir string, extra []string, stdin string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out, errOut strings.Builder
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"mcp", "--dir", dir}, extra...))
	err := root.Execute()
	return out.String(), err
}

func TestMCPCommandServesStdio(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	startTestDaemon(t, dir)

	out, err := runMCP(t, dir,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n")
	if err != nil {
		t.Fatalf("cairn mcp: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"cairn"`) || !strings.Contains(lines[1], "cairn_digest") {
		t.Fatalf("unexpected stdio output:\n%s", out)
	}
	// stdout carries protocol messages ONLY — every line must be JSON-RPC
	for _, l := range lines {
		if !strings.HasPrefix(l, `{"jsonrpc":"2.0"`) {
			t.Fatalf("non-protocol bytes on stdout: %s", l)
		}
	}
}

func TestMCPCommandFailsWithoutDaemon(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if out, err := runMCP(t, dir, ""); err == nil {
		t.Fatalf("mcp succeeded with no daemon:\n%s", out)
	}
}
