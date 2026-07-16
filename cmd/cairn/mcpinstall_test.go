package main

// FIX-MCP1: CLI-surface tests for `cairn mcp-install` / `mcp-uninstall`. The
// merge/backup/refuse invariants are unit-tested in internal/mcpinstall; here we
// only exercise the cobra wiring end-to-end against a hermetic $HOME so os.Getenv
// resolution and os.Executable() reach the real code paths.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/mcpinstall"
)

func desktopConfigPath(home string) string {
	return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
}

// A hermetic HOME with the Claude Desktop app dir present so claude-desktop is
// "installed" but claude-code (no `claude` on a scrubbed PATH, no ~/.claude.json)
// is not — keeps the round-trip on the deterministic file-merge path.
func hermeticHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(home, "empty-bin")) // no `claude` binary
	if err := os.MkdirAll(filepath.Join(home, "Library", "Application Support", "Claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestMCPInstallListDefault(t *testing.T) {
	hermeticHome(t)
	out, err := runCLI(t, "mcp-install")
	if err != nil {
		t.Fatalf("mcp-install (default list): %v\n%s", err, out)
	}
	for _, want := range []string{"cairn binary:", "supported MCP clients:", "claude-desktop", "claude-code", "not configured"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestMCPInstallUninstallRoundTrip(t *testing.T) {
	home := hermeticHome(t)
	path := desktopConfigPath(home)

	// install into claude-desktop
	out, err := runCLI(t, "mcp-install", "--app", "claude-desktop")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if !strings.Contains(out, "✓ claude-desktop") {
		t.Errorf("install did not report success:\n%s", out)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	cfg, err := mcpinstall.ParseConfig(b)
	if err != nil {
		t.Fatalf("written config invalid: %v", err)
	}
	self, _ := os.Executable()
	if cmd, present := mcpinstall.CairnCommand(cfg); !present || cmd != self {
		t.Fatalf("cairn entry wrong: %q present=%v (want %q)", cmd, present, self)
	}

	// list now shows configured + up to date
	out, _ = runCLI(t, "mcp-install", "--list")
	if !strings.Contains(out, "up to date") {
		t.Errorf("list did not report up-to-date:\n%s", out)
	}

	// idempotent second install
	out, err = runCLI(t, "mcp-install", "--app", "claude-desktop")
	if err != nil {
		t.Fatalf("second install: %v\n%s", err, out)
	}
	if !strings.Contains(out, "already up to date") {
		t.Errorf("second install not idempotent:\n%s", out)
	}

	// uninstall
	out, err = runCLI(t, "mcp-uninstall", "--app", "claude-desktop")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if !strings.Contains(out, "✓ claude-desktop: removed") {
		t.Errorf("uninstall did not report removal:\n%s", out)
	}
	b, _ = os.ReadFile(path)
	cfg, _ = mcpinstall.ParseConfig(b)
	if _, present := mcpinstall.CairnCommand(cfg); present {
		t.Error("cairn entry still present after uninstall")
	}
}

func TestMCPInstallUnknownApp(t *testing.T) {
	hermeticHome(t)
	out, err := runCLI(t, "mcp-install", "--app", "emacs")
	if err == nil {
		t.Fatalf("expected error for unknown app:\n%s", out)
	}
	if !strings.Contains(err.Error(), "unknown app") {
		t.Errorf("wrong error: %v", err)
	}
}
